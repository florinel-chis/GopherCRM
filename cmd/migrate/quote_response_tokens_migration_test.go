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

// #382: 20261121120000_add_quote_response_tokens creates
// quote_response_tokens only when it is missing, with the columns and indexes
// AutoMigrate produces (no foreign key); rollback drops that table and nothing
// else. No existing table is altered.

const (
	quoteResponseTokensVersion         = 20261121120000
	quoteResponseTokensPreviousVersion = webhooksVersion
	quoteResponseTokensMigrationName   = "20261121120000_add_quote_response_tokens"
	quoteResponseTokensTable           = "quote_response_tokens"
)

// TestQuoteResponseTokensMigrationFiles needs no database: the pair exists
// with its headers, creates the table only when missing with the model's
// columns and index names, and drops only it. It is no longer the newest
// migration (#400).
func TestQuoteResponseTokensMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, quoteResponseTokensMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, quoteResponseTokensMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Quote response tokens"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove quote response tokens"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "create table if not exists "+quoteResponseTokensTable+" (")
	assert.Contains(t, downSQL, "drop table if exists "+quoteResponseTokensTable+";")
	for _, fragment := range []string{
		"quote_id bigint unsigned not null",
		"token_hash varchar(255) not null",
		"expires_at datetime(3) null",
		"used_at datetime(3) null",
		"decision varchar(20) null",
		"unique key idx_quote_response_tokens_token_hash (token_hash)",
		"key idx_quote_response_tokens_quote_id (quote_id)",
	} {
		assert.Contains(t, upSQL, fragment)
	}
	assert.NotContains(t, upSQL, "foreign key")
	assert.NotContains(t, upSQL, "deleted_at")
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops exactly the one table")

	// The model declares the same index names and hides the hash.
	for field, fragment := range map[string]string{
		"QuoteID":   "index:idx_quote_response_tokens_quote_id",
		"TokenHash": "uniqueIndex:idx_quote_response_tokens_token_hash",
	} {
		tag, ok := reflectField(models.QuoteResponseToken{}, field)
		require.True(t, ok, field)
		assert.Contains(t, tag, fragment, field)
	}
	assert.Equal(t, quoteResponseTokensTable, models.QuoteResponseToken{}.TableName())

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestQuoteHistorySourceMigrationFiles (#400).
	version, err := strconv.Atoi(strings.SplitN(quoteResponseTokensMigrationName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, quoteResponseTokensVersion, version)
	assert.Less(t, quoteResponseTokensVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Greater(t, quoteResponseTokensVersion, quoteResponseTokensPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+quoteResponseTokensTable+`"`, "main_test.go's expected-table list names the table")
}

// insertMigrationQuoteResponseToken writes one unused row and proves the
// unique index on the hash and the nullable columns.
func insertMigrationQuoteResponseToken(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	insert := "INSERT INTO quote_response_tokens (created_at, updated_at, quote_id, token_hash) VALUES (NOW(3), NOW(3), 1, ?)"
	hash := fmt.Sprintf("hash-%d", n)
	_, err := db.Exec(insert, hash)
	require.NoError(t, err)
	_, err = db.Exec(insert, hash)
	require.Error(t, err, "one row per token hash")
	var expiresAt, usedAt sql.NullTime
	var decision sql.NullString
	require.NoError(t, db.QueryRow("SELECT expires_at, used_at, decision FROM quote_response_tokens WHERE token_hash = ?", hash).
		Scan(&expiresAt, &usedAt, &decision))
	assert.False(t, expiresAt.Valid)
	assert.False(t, usedAt.Valid)
	assert.False(t, decision.Valid)
}

func TestQuoteResponseTokensMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })
	tables := []string{quoteResponseTokensTable}

	// The parity reference: the table as MigrateDatabase creates it on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "qrt_ref")
	require.NoError(t, reference.m.Migrate(quoteResponseTokensPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), quoteResponseTokensTable)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, quoteResponseTokensTable)
	require.NotEmpty(t, want.Columns)
	assert.Contains(t, want.Indexes, "idx_quote_response_tokens_token_hash (token_hash) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_quote_response_tokens_quote_id (quote_id) non_unique=1")
	assert.Empty(t, want.ForeignKeys, "the table has no foreign key")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "qrt_fresh")
		require.NoError(t, tdb.m.Migrate(quoteResponseTokensVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, quoteResponseTokensTable), "a fresh SQL schema carries what MigrateDatabase creates")
		insertMigrationQuoteResponseToken(t, tdb.sql, 1)

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the table and nothing else")
		require.NoError(t, tdb.m.Migrate(quoteResponseTokensVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, quoteResponseTokensTable))

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), quoteResponseTokensTable)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "qrt_mig")
			require.NoError(t, tdb.m.Migrate(quoteResponseTokensPreviousVersion))
			if autoMigrateFirst {
				// A new binary started before the operator migrated: the
				// schema MigrateDatabase builds at the previous head.
				appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
				require.NoError(t, err)
				pool, err := appDB.DB()
				require.NoError(t, err)
				models.DB = appDB
				require.NoError(t, models.MigrateDatabase())
				_ = pool.Close()
				insertMigrationQuoteResponseToken(t, tdb.sql, 2)
			}

			require.NoError(t, tdb.m.Migrate(quoteResponseTokensVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteResponseTokensVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, quoteResponseTokensTable), "the SQL migration and AutoMigrate produce the same table")
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM quote_response_tokens").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, quoteResponseTokensTable), "MigrateDatabase does not change the table")
			insertMigrationQuoteResponseToken(t, tdb.sql, 3)
			require.ErrorIs(t, tdb.m.Migrate(quoteResponseTokensVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteResponseTokensPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the table and nothing else")

			require.NoError(t, tdb.m.Migrate(quoteResponseTokensVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, quoteResponseTokensTable))
			insertMigrationQuoteResponseToken(t, tdb.sql, 4)
		})
	}
}
