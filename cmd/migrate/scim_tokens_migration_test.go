package main

import (
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

// #468: 20261126120000_add_scim_tokens creates scim_tokens (one row per SCIM
// provider token: a label, the HMAC of the token, created, last used and
// revoked times; no foreign key, no soft delete) only when missing, with the
// columns and index names AutoMigrate produces; rollback drops that table and
// nothing else.

const (
	scimTokensVersion         = 20261126120000
	scimTokensPreviousVersion = authAuditLogAttemptsVersion
	scimTokensMigrationName   = "20261126120000_add_scim_tokens"
	scimTokensTable           = "scim_tokens"
	scimTokensHashIndex       = "idx_scim_tokens_token_hash"
)

// TestSCIMTokensMigrationFiles needs no database: the pair exists with its
// headers, creates the table only when missing with the model's columns and
// index name, has no foreign key and alters nothing, drops only that table,
// and is the newest migration of the directory, which latestMigrationVersion
// names.
func TestSCIMTokensMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, scimTokensMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, scimTokensMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: SCIM provider tokens"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove SCIM provider tokens"), "the down half has its header")

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

	for _, fragment := range []string{
		"create table if not exists scim_tokens (",
		"id bigint unsigned not null auto_increment",
		"name varchar(100) not null",
		"token_hash varchar(128) not null",
		"created_at datetime(3) not null",
		"last_used_at datetime(3) null",
		"revoked_at datetime(3) null",
		"primary key (id)",
		"unique key idx_scim_tokens_token_hash (token_hash)",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		assert.Containsf(t, upStatements, fragment, "up declares %s", fragment)
	}
	assert.NotContains(t, upStatements, "foreign key", "no foreign key")
	assert.NotContains(t, upStatements, "references", "no foreign key")
	assert.NotContains(t, upStatements, "deleted_at", "no soft delete")
	assert.NotContains(t, upStatements, "updated_at", "no updated_at")
	assert.NotContains(t, upStatements, "alter table", "no existing table is altered")
	assert.Equal(t, 1, strings.Count(upStatements, "create table"), "one table")
	assert.Equal(t, "drop table if exists scim_tokens;", strings.TrimSpace(downStatements), "rollback drops the one table only")

	// The model declares the same widths and index name, and no default.
	for field, want := range map[string]string{
		"Name":      "type:varchar(100);not null",
		"TokenHash": "type:varchar(128);not null;uniqueIndex:" + scimTokensHashIndex,
		"CreatedAt": "not null",
	} {
		tag, ok := reflectField(models.SCIMToken{}, field)
		require.Truef(t, ok, "SCIMToken.%s exists", field)
		assert.Equalf(t, want, tag, "SCIMToken.%s", field)
	}
	for _, field := range []string{"UpdatedAt", "DeletedAt"} {
		_, ok := reflectField(models.SCIMToken{}, field)
		assert.Falsef(t, ok, "SCIMToken has no %s", field)
	}
	assert.Equal(t, scimTokensTable, models.SCIMToken{}.TableName())

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from
	// TestAuthAuditLogAttemptsMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, scimTokensMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, scimTokensVersion, version)
	assert.Equal(t, scimTokensVersion, latestMigrationVersion)
	assert.Greater(t, scimTokensVersion, scimTokensPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+scimTokensTable+`"`, "main_test.go's expected-table list names %s", scimTokensTable)
}

const scimTokensInsert = `INSERT INTO scim_tokens (name, token_hash, created_at) VALUES ('Entra', ?, UTC_TIMESTAMP(3))`

func TestSCIMTokensMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })
	tables := []string{scimTokensTable}

	// The parity reference: the table as MigrateDatabase creates it on top of
	// the previous SQL head, on the same server.
	reference := openMigrationTestDB(t, baseDSN, "scim_ref")
	require.NoError(t, reference.m.Migrate(scimTokensPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), scimTokensTable)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, scimTokensTable)
	require.Len(t, want.Columns, 6)
	assert.Contains(t, want.Indexes, scimTokensHashIndex+" (token_hash) non_unique=0")
	require.Len(t, want.Indexes, 2, "the primary key and the unique index")
	assert.Empty(t, want.ForeignKeys)

	assertShape := func(t *testing.T, tdb *migrationTestDB) {
		t.Helper()
		assert.Equal(t, want, snapshotTable(t, tdb, scimTokensTable), "scim_tokens as AutoMigrate creates it")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "scim_fresh")
		require.NoError(t, tdb.m.Migrate(scimTokensVersion))
		assertShape(t, tdb)
		_, err := tdb.sql.Exec(scimTokensInsert, "hmac$one")
		require.NoError(t, err)
		_, err = tdb.sql.Exec(scimTokensInsert, "hmac$one")
		require.Error(t, err, "a duplicate token hash is refused")

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the table and nothing else")
		require.NoError(t, tdb.m.Migrate(scimTokensVersion))
		assertShape(t, tdb)
		require.ErrorIs(t, tdb.m.Migrate(scimTokensVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err = tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		name := "Upgrade/AutoMigrateFirst=false"
		if autoMigrateFirst {
			name = "Upgrade/AutoMigrateFirst=true"
		}
		t.Run(name, func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "scim_mig")
			require.NoError(t, tdb.m.Migrate(scimTokensPreviousVersion))
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				_, err := tdb.sql.Exec(scimTokensInsert, "hmac$kept")
				require.NoError(t, err)
			}
			require.NoError(t, tdb.m.Migrate(scimTokensVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(scimTokensVersion), version)
			assertShape(t, tdb)
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM scim_tokens").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows (the up is a no-op)")
			}

			runMigrateDatabase(t, tdb.dsn)
			assertShape(t, tdb)
			require.ErrorIs(t, tdb.m.Migrate(scimTokensVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(scimTokensPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the table and nothing else")

			require.NoError(t, tdb.m.Migrate(scimTokensVersion), "reapply")
			assertShape(t, tdb)
		})
	}
}
