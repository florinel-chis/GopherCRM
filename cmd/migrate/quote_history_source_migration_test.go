package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #400: 20261122120000_quote_history_source adds quote_status_changes.source
// (varchar(20) NOT NULL DEFAULT 'staff') only when missing and makes
// changed_by_id nullable only when it is still NOT NULL; the foreign key to
// users stays. No backfill. The down half gives every row without a user the
// owner of its quote, restores NOT NULL and drops source.

const (
	quoteHistorySourceVersion         = 20261122120000
	quoteHistorySourcePreviousVersion = quoteResponseTokensVersion
	quoteHistorySourceMigrationName   = "20261122120000_quote_history_source"
	quoteHistoryTable                 = "quote_status_changes"
)

// quoteHistorySourceColumns are the columns the migration owns: it adds the
// one and relaxes the other.
var quoteHistorySourceColumns = []string{"source", "changed_by_id"}

// withoutQuoteHistorySourceColumns removes the columns of this migration from
// a snapshot of the quote tables, for the roots of older migrations: their
// AutoMigrate reference carries source and a nullable changed_by_id, and a
// schema migrated only up to their version carries neither.
func withoutQuoteHistorySourceColumns(tables map[string]tableSchema) map[string]tableSchema {
	out := make(map[string]tableSchema, len(tables))
	for table, schema := range tables {
		if table == quoteHistoryTable {
			out[table] = withoutColumns(schema, quoteHistorySourceColumns)
			continue
		}
		out[table] = schema
	}
	return out
}

// TestQuoteHistorySourceMigrationFiles needs no database: the pair exists
// with its headers, the ADD, both MODIFYs and the DROP are each guarded by an
// INFORMATION_SCHEMA probe, the up half writes no row and the down half
// exactly one UPDATE, the model declares the same columns, and a newer
// migration follows it (#435).
func TestQuoteHistorySourceMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, quoteHistorySourceMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, quoteHistorySourceMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Quote history source"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove quote history source"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "column_name = 'source'), 'select 1', 'alter table quote_status_changes add column source varchar(20) not null default ''staff'''",
		"source is added only when missing")
	assert.Contains(t, upSQL, "column_name = 'changed_by_id' and is_nullable = 'no'), 'alter table quote_status_changes modify column changed_by_id bigint unsigned null', 'select 1'",
		"changed_by_id is relaxed only when it is still NOT NULL")
	assert.Equal(t, 2, strings.Count(upSQL, "execute stmt;"))
	// The statements alone: the header comment names the foreign key it keeps.
	var statements []string
	for _, line := range strings.Split(upSQL, "\n") {
		if !strings.HasPrefix(line, "--") {
			statements = append(statements, line)
		}
	}
	upStatements := strings.Join(statements, "\n")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create index", "add index", "add key", "foreign key", "add constraint"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, drops nothing and adds no index or key")
	}

	assert.Equal(t, 1, strings.Count(downSQL, "update "), "exactly one UPDATE in the down half")
	assert.Contains(t, downSQL, "update quote_status_changes c join quotes q on q.id = c.quote_id set c.changed_by_id = q.owner_id where c.changed_by_id is null;")
	assert.NotContains(t, downSQL, "deleted_at", "the rows of soft-deleted quotes are rewritten too")
	assert.Contains(t, downSQL, "column_name = 'changed_by_id' and is_nullable = 'yes'), 'alter table quote_status_changes modify column changed_by_id bigint unsigned not null', 'select 1'",
		"NOT NULL is restored only when the column is nullable")
	assert.Contains(t, downSQL, "column_name = 'source'), 'alter table quote_status_changes drop column source', 'select 1'",
		"source is dropped only when present")
	assert.Equal(t, 2, strings.Count(downSQL, "execute stmt;"))
	assert.Less(t, strings.Index(downSQL, "update quote_status_changes"), strings.Index(downSQL, "modify column"), "the rows are rewritten before NOT NULL returns")
	assert.Less(t, strings.Index(downSQL, "modify column"), strings.Index(downSQL, "drop column"), "source is dropped last")

	// The model declares the same columns and the values the service writes.
	tag, ok := reflectField(models.QuoteStatusChange{}, "ChangedByID")
	require.True(t, ok)
	assert.NotContains(t, tag, "not null", "changed_by_id is nullable")
	tag, ok = reflectField(models.QuoteStatusChange{}, "Source")
	require.True(t, ok)
	assert.Contains(t, tag, "type:varchar(20);not null;default:staff")
	tag, ok = reflectField(models.QuoteStatusChange{}, "ChangedBy")
	require.True(t, ok)
	assert.Contains(t, tag, "foreignKey:ChangedByID", "the foreign key to users is kept")
	assert.Equal(t, "staff", models.QuoteChangeSourceStaff)
	assert.Equal(t, "client", models.QuoteChangeSourceClient)

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestUserIdentitiesMigrationFiles (#435). This one stays in sequence.
	version, err := strconv.Atoi(strings.SplitN(quoteHistorySourceMigrationName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, quoteHistorySourceVersion, version)
	assert.Less(t, quoteHistorySourceVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Greater(t, quoteHistorySourceVersion, quoteHistorySourcePreviousVersion)
}

// quoteHistorySeed names the rows seedQuoteHistoryRows wrote.
type quoteHistorySeed struct {
	ownerID, actorID      int64
	liveQuote, goneQuote  int64
	staffRow, staffRowOld int64
}

// seedQuoteHistoryRows writes, at the previous head, a quote owner and a
// second staff user, a live and a soft-deleted quote of that owner, and one
// history row per quote that the second user wrote: the acting user of a
// kept row therefore differs from the owner the rollback falls back to.
func seedQuoteHistoryRows(t *testing.T, tdb *migrationTestDB) quoteHistorySeed {
	t.Helper()
	appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	pool, err := appDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	owner := models.User{Email: "quote-history-owner@example.com", Password: "x", FirstName: "Q", LastName: "O", Role: models.RoleSales, IsActive: true}
	require.NoError(t, appDB.Create(&owner).Error)
	actor := models.User{Email: "quote-history-actor@example.com", Password: "x", FirstName: "Q", LastName: "A", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, appDB.Create(&actor).Error)
	require.NotEqual(t, owner.ID, actor.ID)

	seed := quoteHistorySeed{ownerID: int64(owner.ID), actorID: int64(actor.ID)}
	for i, deleted := range []string{"NULL", "'2026-11-01 09:00:00.000'"} {
		result, err := tdb.sql.Exec(fmt.Sprintf("INSERT INTO quotes (created_at, updated_at, deleted_at, title, status, currency, subtotal_cents, discount_cents, total_cents, owner_id) VALUES (NOW(3), NOW(3), %s, 'History', 'sent', 'EUR', 0, 0, 0, ?)", deleted), owner.ID)
		require.NoError(t, err)
		quoteID, err := result.LastInsertId()
		require.NoError(t, err)
		result, err = tdb.sql.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, changed_at) VALUES (?, 'draft', 'sent', ?, NOW(3))", quoteID, actor.ID)
		require.NoError(t, err)
		rowID, err := result.LastInsertId()
		require.NoError(t, err)
		if i == 0 {
			seed.liveQuote, seed.staffRow = quoteID, rowID
		} else {
			seed.goneQuote, seed.staffRowOld = quoteID, rowID
		}
	}
	return seed
}

// insertClientHistoryRow writes a client answer: no user, source client.
func insertClientHistoryRow(t *testing.T, db *sql.DB, quoteID int64) int64 {
	t.Helper()
	result, err := db.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, source, changed_at) VALUES (?, 'sent', 'accepted', NULL, 'client', NOW(3))", quoteID)
	require.NoError(t, err, "a client row without a user is accepted")
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// quoteHistoryRow reads the actor and, when the column exists, the source.
func quoteHistoryRow(t *testing.T, db *sql.DB, id int64, withSource bool) (sql.NullInt64, string) {
	t.Helper()
	var actor sql.NullInt64
	source := ""
	if withSource {
		require.NoError(t, db.QueryRow("SELECT changed_by_id, source FROM quote_status_changes WHERE id = ?", id).Scan(&actor, &source))
	} else {
		require.NoError(t, db.QueryRow("SELECT changed_by_id FROM quote_status_changes WHERE id = ?", id).Scan(&actor))
	}
	return actor, source
}

func assertQuoteHistoryStaffRows(t *testing.T, db *sql.DB, seed quoteHistorySeed) {
	t.Helper()
	for _, id := range []int64{seed.staffRow, seed.staffRowOld} {
		actor, source := quoteHistoryRow(t, db, id, true)
		assert.Equal(t, sql.NullInt64{Int64: seed.actorID, Valid: true}, actor, "row %d keeps its changed_by_id", id)
		assert.Equal(t, "staff", source, "row %d reads source staff", id)
	}
}

func countQuoteHistoryRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM quote_status_changes").Scan(&count))
	return count
}

// assertQuoteHistoryShape checks the two owned columns of a snapshot in a
// form MySQL 8 and MariaDB 10.11 both render.
func assertQuoteHistoryShape(t *testing.T, schema tableSchema, migrated bool) {
	t.Helper()
	owned := ownedColumns(schema, quoteHistorySourceColumns)
	actorNullable, source := "", ""
	for _, line := range owned {
		switch {
		case strings.HasPrefix(line, "changed_by_id bigint"):
			actorNullable = line
		case strings.HasPrefix(line, "source "):
			source = line
		}
	}
	require.NotEmpty(t, actorNullable, "changed_by_id exists")
	assert.Contains(t, actorNullable, "unsigned")
	if !migrated {
		assert.Contains(t, actorNullable, "nullable=NO")
		assert.Empty(t, source, "no source column before the migration")
		return
	}
	assert.Contains(t, actorNullable, "nullable=YES")
	require.NotEmpty(t, source, "source exists")
	assert.Contains(t, source, "source varchar(20) nullable=NO default=")
	assert.Contains(t, source, "staff")
	assert.Contains(t, schema.Indexes, "idx_quote_status_changes_quote_id (quote_id) non_unique=1")
	assert.Contains(t, schema.ForeignKeys, "fk_quote_status_changes_changed_by changed_by_id -> users.id")
	assert.Len(t, schema.ForeignKeys, 1, "no foreign key to the quote")
}

func TestQuoteHistorySourceMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as MigrateDatabase leaves it on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "qhs_ref")
	require.NoError(t, reference.m.Migrate(quoteHistorySourcePreviousVersion))
	before := snapshotTable(t, reference, quoteHistoryTable)
	assertQuoteHistoryShape(t, before, false)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, quoteHistoryTable)
	assertQuoteHistoryShape(t, want, true)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "qhs_fresh")
		require.NoError(t, tdb.m.Migrate(quoteHistorySourceVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable), "a fresh SQL schema carries what MigrateDatabase builds")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, quoteHistoryTable), "rollback restores the previous table")
		require.NoError(t, tdb.m.Migrate(quoteHistorySourceVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable))
		require.ErrorIs(t, tdb.m.Migrate(quoteHistorySourceVersion), migrate.ErrNoChange)

		// The down half is guarded: with source already gone and NOT NULL
		// already back it still succeeds.
		_, err := tdb.sql.Exec("ALTER TABLE quote_status_changes DROP COLUMN source")
		require.NoError(t, err)
		_, err = tdb.sql.Exec("ALTER TABLE quote_status_changes MODIFY COLUMN changed_by_id bigint unsigned NOT NULL")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "the down steps run only when needed")
		assert.Equal(t, before, snapshotTable(t, tdb, quoteHistoryTable))

		require.NoError(t, tdb.m.Migrate(quoteHistorySourceVersion))
		require.NoError(t, tdb.m.Down())
		_, _, err = tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "qhs_mig")
			require.NoError(t, tdb.m.Migrate(quoteHistorySourcePreviousVersion))
			seed := seedQuoteHistoryRows(t, tdb)
			_, err := tdb.sql.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, changed_at) VALUES (?, 'sent', 'accepted', NULL, NOW(3))", seed.liveQuote)
			require.Error(t, err, "before the migration a row needs a user")

			var early int64
			if autoMigrateFirst {
				// A new binary started before the operator migrated and
				// recorded a client answer.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable))
				early = insertClientHistoryRow(t, tdb.sql, seed.liveQuote)
			}
			rowsBefore := countQuoteHistoryRows(t, tdb.sql)

			require.NoError(t, tdb.m.Migrate(quoteHistorySourceVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteHistorySourceVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable), "the SQL migration and MigrateDatabase produce the same table")
			assert.Equal(t, rowsBefore, countQuoteHistoryRows(t, tdb.sql), "every row is kept")
			assertQuoteHistoryStaffRows(t, tdb.sql, seed)
			if autoMigrateFirst {
				actor, source := quoteHistoryRow(t, tdb.sql, early, true)
				assert.False(t, actor.Valid, "a client row written before the migration is kept as it is")
				assert.Equal(t, "client", source)
			}

			// A client answer has no user; a user that does not exist is
			// still refused by the foreign key.
			clientLive := insertClientHistoryRow(t, tdb.sql, seed.liveQuote)
			clientGone := insertClientHistoryRow(t, tdb.sql, seed.goneQuote)
			_, err = tdb.sql.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, source, changed_at) VALUES (?, 'draft', 'sent', 987654321, 'staff', NOW(3))", seed.liveQuote)
			require.Error(t, err, "an unknown user id is refused")
			assert.Contains(t, strings.ToLower(err.Error()), "foreign key")

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable), "MigrateDatabase does not change the table")
			assertQuoteHistoryStaffRows(t, tdb.sql, seed)
			require.ErrorIs(t, tdb.m.Migrate(quoteHistorySourceVersion), migrate.ErrNoChange)

			// Rollback: the rows without a user get the owner of their
			// quote, the soft-deleted quote's too; the others keep theirs.
			rowsMigrated := countQuoteHistoryRows(t, tdb.sql)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteHistorySourcePreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, quoteHistoryTable), "rollback restores NOT NULL and drops source")
			assert.Equal(t, rowsMigrated, countQuoteHistoryRows(t, tdb.sql), "rollback keeps every row")
			clientRows := []int64{clientLive, clientGone}
			if autoMigrateFirst {
				clientRows = append(clientRows, early)
			}
			for _, id := range clientRows {
				actor, _ := quoteHistoryRow(t, tdb.sql, id, false)
				assert.Equal(t, sql.NullInt64{Int64: seed.ownerID, Valid: true}, actor, "row %d names the quote owner after the rollback", id)
			}
			for _, id := range []int64{seed.staffRow, seed.staffRowOld} {
				actor, _ := quoteHistoryRow(t, tdb.sql, id, false)
				assert.Equal(t, sql.NullInt64{Int64: seed.actorID, Valid: true}, actor, "row %d keeps its user through the rollback", id)
			}

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(quoteHistorySourceVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, quoteHistoryTable))
			assert.Equal(t, rowsMigrated, countQuoteHistoryRows(t, tdb.sql))
			assertQuoteHistoryStaffRows(t, tdb.sql, seed)
			again := insertClientHistoryRow(t, tdb.sql, seed.liveQuote)
			actor, source := quoteHistoryRow(t, tdb.sql, again, true)
			assert.False(t, actor.Valid)
			assert.Equal(t, "client", source)
		})
	}
}
