package main

import (
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
)

// #804: 20261129120009_add_form_ticket_title_field adds
// forms.ticket_title_field only when missing. Existing rows keep ''; nothing
// is filled. The down half drops the column only when present.

const (
	formTicketTitleFieldVersion         = 20261129120009
	formTicketTitleFieldPreviousVersion = refreshTokenProviderSessionVersion
	formTicketTitleFieldMigrationName   = "20261129120009_add_form_ticket_title_field"
	formsTable                          = "forms"
)

// formTicketTitleFieldColumns is the column the migration adds.
var formTicketTitleFieldColumns = []string{"ticket_title_field"}

// TestFormTicketTitleFieldMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the column behind an
// INFORMATION_SCHEMA probe and fills nothing, the down half drops only that
// column, the model declares the column as the up half adds it, and it is the
// newest migration of the directory, which latestMigrationVersion names.
func TestFormTicketTitleFieldMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, formTicketTitleFieldMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, formTicketTitleFieldMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Form ticket title field (#804)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove form ticket title field (#804)\n-- Date: "), "the down half has its header")
	for _, half := range []string{string(up), string(down)} {
		assert.Contains(t, half, "\n-- Description: ")
		assert.Contains(t, half, "MySQL 8 and MariaDB 10.11 accept this as written.")
	}

	statementsOf := func(sql string) string {
		var lines []string
		for _, line := range strings.Split(strings.ToLower(sql), "\n") {
			if !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	upStatements, downStatements := statementsOf(string(up)), statementsOf(string(down))

	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'forms' and column_name = "+
		"'ticket_title_field'), 'select 1', 'alter table forms add column ticket_title_field varchar(50) not null default '''''")
	assert.Equal(t, 1, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upStatements, "add column"), "exactly one column")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "add index", "create index", "foreign key", "references", "modify ", "unique"} {
		assert.NotContains(t, upStatements, forbidden, "the up half adds the column and changes nothing else")
	}

	assert.Contains(t, downStatements, "table_name = 'forms' and column_name = 'ticket_title_field'), "+
		"'alter table forms drop column ticket_title_field', 'select 1'")
	assert.Equal(t, 1, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downStatements, " drop "), "the down half drops the column only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "add index", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	tag, ok := reflectField(models.Form{}, "TicketTitleField")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(50);not null;default:''", tag)

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from
	// TestRefreshTokenProviderSessionMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, formTicketTitleFieldMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, formTicketTitleFieldVersion, version)
	assert.Equal(t, formTicketTitleFieldVersion, latestMigrationVersion)
	assert.Equal(t, formTicketTitleFieldPreviousVersion+1, formTicketTitleFieldVersion)
}

// ticketTitleFieldRows renders every form as text, ordered by id, with
// ticket_title_field when withColumn.
func ticketTitleFieldRows(t *testing.T, tdb *migrationTestDB, withColumn bool) []string {
	t.Helper()
	column := "''"
	if withColumn {
		column = "ticket_title_field"
	}
	rows, err := tdb.sql.Query(`SELECT id, name, public_id, status, create_ticket, double_opt_in, deleted_at IS NULL,
		DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f'), ` + column + `
		FROM forms ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var name, publicID, status, created, updated, titleField string
		var createTicket, doubleOptIn, live bool
		require.NoError(t, rows.Scan(&id, &name, &publicID, &status, &createTicket, &doubleOptIn, &live, &created, &updated, &titleField))
		out = append(out, fmt.Sprintf("%d name=%s public=%s status=%s ticket=%t doi=%t live=%t created=%s updated=%s title_field=%q",
			id, name, publicID, status, createTicket, doubleOptIn, live, created, updated, titleField))
	}
	require.NoError(t, rows.Err())
	return out
}

// seedTicketTitleFieldRows writes a ticket form, a lead form and a
// soft-deleted form as the previous release wrote them.
func seedTicketTitleFieldRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for i, row := range []struct{ ticket, lead, deleted string }{
		{"1", "0", "NULL"}, {"0", "1", "NULL"}, {"1", "0", "NOW(3)"},
	} {
		_, err := tdb.sql.Exec(fmt.Sprintf(`INSERT INTO forms (created_at, updated_at, deleted_at, name, public_id, status, fields, submit_action,
			double_opt_in, create_lead, create_ticket)
			VALUES ('2026-11-20 09:30:00.123', '2026-11-20 09:31:00.456', %s, ?, ?, 'published', '[]', 'message', 1, %s, %s)`,
			row.deleted, row.lead, row.ticket), fmt.Sprintf("Title field form %d", i), fmt.Sprintf("title-field-public-%d", i))
		require.NoError(t, err)
	}
}

func TestFormTicketTitleFieldMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the column as MigrateDatabase builds it on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "fttf_ref")
	require.NoError(t, reference.m.Migrate(formTicketTitleFieldPreviousVersion))
	before := snapshotTable(t, reference, formsTable)
	require.Empty(t, ownedColumns(before, formTicketTitleFieldColumns), "the previous head has no ticket_title_field")
	runMigrateDatabase(t, reference.dsn)
	afterAutoMigrate := snapshotTable(t, reference, formsTable)
	wantOwned := ownedColumns(afterAutoMigrate, formTicketTitleFieldColumns)
	require.Len(t, wantOwned, 1)
	assert.True(t, strings.HasPrefix(wantOwned[0], "ticket_title_field varchar(50) nullable=NO default="), wantOwned[0])
	assert.Equal(t, before, withoutColumns(afterAutoMigrate, formTicketTitleFieldColumns), "the migration owns only the column")

	migrated := func(t *testing.T, tdb *migrationTestDB) tableSchema {
		t.Helper()
		got := snapshotTable(t, tdb, formsTable)
		assert.Equal(t, afterAutoMigrate, got, "the SQL migration and MigrateDatabase produce the same table")
		assert.Equal(t, wantOwned, ownedColumns(got, formTicketTitleFieldColumns))
		return got
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "fttf_fresh")
		require.NoError(t, tdb.m.Migrate(formTicketTitleFieldVersion), "the up applies on an empty database")
		migrated(t, tdb)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, formsTable), "rollback removes the column only")
		require.NoError(t, tdb.m.Migrate(formTicketTitleFieldVersion))
		migrated(t, tdb)
		require.ErrorIs(t, tdb.m.Migrate(formTicketTitleFieldVersion), migrate.ErrNoChange)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "fttf_mig")
			require.NoError(t, tdb.m.Migrate(formTicketTitleFieldPreviousVersion))
			seedTicketTitleFieldRows(t, tdb)
			legacy := ticketTitleFieldRows(t, tdb, false)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				migrated(t, tdb)
			}

			require.NoError(t, tdb.m.Migrate(formTicketTitleFieldVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formTicketTitleFieldVersion), version)
			schema := migrated(t, tdb)
			after := ticketTitleFieldRows(t, tdb, true)
			assert.Equal(t, legacy, after, "every existing form reads '' and nothing else changes")

			// Applying the up half again changes nothing.
			require.NoError(t, tdb.m.Force(formTicketTitleFieldPreviousVersion))
			require.NoError(t, tdb.m.Migrate(formTicketTitleFieldVersion), "the up half runs again")
			assert.Equal(t, schema, snapshotTable(t, tdb, formsTable), "a second run changes no schema")
			assert.Equal(t, after, ticketTitleFieldRows(t, tdb, true), "a second run changes no row")

			// Rollback removes exactly the column and keeps every row;
			// running it again changes nothing.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formTicketTitleFieldPreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, formsTable), "rollback removes the column only")
			assert.Equal(t, legacy, ticketTitleFieldRows(t, tdb, false), "rollback keeps every row")
			require.NoError(t, tdb.m.Force(formTicketTitleFieldVersion))
			require.NoError(t, tdb.m.Steps(-1), "the down half runs again")
			assert.Equal(t, before, snapshotTable(t, tdb, formsTable))

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(formTicketTitleFieldVersion), "reapply")
			migrated(t, tdb)
			assert.Equal(t, after, ticketTitleFieldRows(t, tdb, true))
			require.ErrorIs(t, tdb.m.Migrate(formTicketTitleFieldVersion), migrate.ErrNoChange)
		})
	}
}
