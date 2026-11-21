package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #525: 20261129120002_api_key_revoked_at adds api_keys.revoked_at
// (datetime(3) NULL) only when missing. Revoking a key stamps it; existing
// rows keep NULL and their is_active. No backfill, no index. The down half
// drops the column.

const (
	apiKeyRevokedAtVersion         = 20261129120002
	apiKeyRevokedAtPreviousVersion = adminOnlyPermissionsVersion
	apiKeyRevokedAtMigrationName   = "20261129120002_api_key_revoked_at"
	apiKeysTable                   = "api_keys"
)

// apiKeyRevokedAtColumns are the columns the migration adds.
var apiKeyRevokedAtColumns = []string{"revoked_at"}

// TestAPIKeyRevokedAtMigrationFiles needs no database: the pair exists with
// its headers, the up half adds exactly revoked_at behind an
// INFORMATION_SCHEMA probe and writes no row, the down half drops only it,
// the model declares the column without a type override, and a newer
// migration follows it.
func TestAPIKeyRevokedAtMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, apiKeyRevokedAtMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, apiKeyRevokedAtMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: API key revoked_at (#525)"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove API key revoked_at (#525)"), "the down half has its header")
	assert.Contains(t, string(up), "MySQL 8 and MariaDB 10.11 accept this as written.")
	assert.Contains(t, string(down), "MySQL 8 and MariaDB 10.11 accept this as written.")

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

	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'api_keys' and column_name = 'revoked_at'), 'select 1', 'alter table api_keys add column revoked_at datetime(3) null'",
		"revoked_at is added only when missing")
	assert.Equal(t, 1, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upStatements, "add column"), "exactly one column")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "index", "unique", "default"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, adds no index or key and changes nothing else")
	}

	assert.Contains(t, downStatements, "column_name = 'revoked_at'), 'alter table api_keys drop column revoked_at', 'select 1'")
	assert.Equal(t, 1, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downStatements, " drop "), "the down half drops revoked_at only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the column with no gorm type override, so
	// AutoMigrate builds datetime(3) NULL on MySQL like SCIMToken.RevokedAt.
	tag, ok := reflectField(models.APIKey{}, "RevokedAt")
	require.True(t, ok)
	assert.Equal(t, "", tag)

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestAPIKeyLifetimeMigrationFiles (#532).
	assert.Less(t, apiKeyRevokedAtVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Equal(t, apiKeyRevokedAtPreviousVersion+1, apiKeyRevokedAtVersion)
}

// insertRevokedAtAPIKeyRow writes one user and one API key as the previous
// release wrote them, without revoked_at, and returns the key id.
func insertRevokedAtAPIKeyRow(t *testing.T, tdb *migrationTestDB, label string, active bool) int64 {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
		VALUES (?, 'x', 'K', 'R', 'sales', 1, NOW(3), NOW(3))`, label+"@example.com")
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	result, err = tdb.sql.Exec(`INSERT INTO api_keys (created_at, updated_at, name, key_hash, prefix, user_id, is_active)
		VALUES (NOW(3), NOW(3), ?, ?, 'abcd1234', ?, ?)`, label, "hash-"+label, userID, active)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertRevokedAtAPIKeyRowKept checks that the key written before the
// migration is there with its name and is_active and a NULL revoked_at.
func assertRevokedAtAPIKeyRowKept(t *testing.T, tdb *migrationTestDB, id int64, label string, active bool) {
	t.Helper()
	var name string
	var isActive bool
	var revokedAtNull bool
	require.NoError(t, tdb.sql.QueryRow(`SELECT name, is_active, revoked_at IS NULL FROM api_keys WHERE id = ?`, id).
		Scan(&name, &isActive, &revokedAtNull))
	assert.Equal(t, label, name)
	assert.Equal(t, active, isActive, "is_active is unchanged")
	assert.True(t, revokedAtNull, "an existing row keeps revoked_at NULL")
}

// TestAPIKeyRevokedAtMigration compares only what the migration owns with
// what MigrateDatabase builds: AutoMigrate also changes api_keys.user_id
// (nullability and a second foreign key) on top of the SQL head, which is
// older than #525 and outside this migration, so each step asserts that
// revoked_at matches AutoMigrate's and that every other column, index and key
// is as it was before the step.
func TestAPIKeyRevokedAtMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: revoked_at as MigrateDatabase builds it on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "akr_ref")
	require.NoError(t, reference.m.Migrate(apiKeyRevokedAtPreviousVersion))
	before := snapshotTable(t, reference, apiKeysTable)
	require.Empty(t, ownedColumns(before, apiKeyRevokedAtColumns), "the previous head has no revoked_at")
	runMigrateDatabase(t, reference.dsn)
	wantOwned := ownedColumns(snapshotTable(t, reference, apiKeysTable), apiKeyRevokedAtColumns)
	require.Len(t, wantOwned, 1)
	// MySQL reports no default, MariaDB a NULL default: both are datetime(3) NULL.
	assert.True(t, strings.HasPrefix(wantOwned[0], "revoked_at datetime(3) nullable=YES default="), "AutoMigrate builds datetime(3) NULL, got %q", wantOwned[0])

	// migrated asserts that the table is base plus exactly revoked_at as
	// AutoMigrate builds it.
	migrated := func(t *testing.T, tdb *migrationTestDB, base tableSchema) tableSchema {
		t.Helper()
		got := snapshotTable(t, tdb, apiKeysTable)
		assert.Equal(t, wantOwned, ownedColumns(got, apiKeyRevokedAtColumns), "revoked_at matches AutoMigrate")
		assert.Equal(t, withoutColumns(base, apiKeyRevokedAtColumns), withoutColumns(got, apiKeyRevokedAtColumns), "nothing else changes")
		return got
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "akr_fresh")
		require.NoError(t, tdb.m.Migrate(apiKeyRevokedAtVersion), "the up applies on an empty database")
		migrated(t, tdb, before)
		id := insertRevokedAtAPIKeyRow(t, tdb, "fresh-key", true)
		assertRevokedAtAPIKeyRowKept(t, tdb, id, "fresh-key", true)

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, apiKeysTable), "rollback removes revoked_at only")
		require.NoError(t, tdb.m.Migrate(apiKeyRevokedAtVersion))
		migrated(t, tdb, before)
		require.ErrorIs(t, tdb.m.Migrate(apiKeyRevokedAtVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "akr_mig")
			require.NoError(t, tdb.m.Migrate(apiKeyRevokedAtPreviousVersion))
			liveID := insertRevokedAtAPIKeyRow(t, tdb, "live-key", true)
			inactiveID := insertRevokedAtAPIKeyRow(t, tdb, "inactive-key", false)
			base := snapshotTable(t, tdb, apiKeysTable)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				base = snapshotTable(t, tdb, apiKeysTable)
				assert.Equal(t, wantOwned, ownedColumns(base, apiKeyRevokedAtColumns))
			}

			require.NoError(t, tdb.m.Migrate(apiKeyRevokedAtVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(apiKeyRevokedAtVersion), version)
			after := migrated(t, tdb, base)
			assertRevokedAtAPIKeyRowKept(t, tdb, liveID, "live-key", true)
			assertRevokedAtAPIKeyRowKept(t, tdb, inactiveID, "inactive-key", false)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, wantOwned, ownedColumns(snapshotTable(t, tdb, apiKeysTable), apiKeyRevokedAtColumns),
				"MigrateDatabase does not change revoked_at")
			require.ErrorIs(t, tdb.m.Migrate(apiKeyRevokedAtVersion), migrate.ErrNoChange)

			// Rollback keeps the rows and removes only what #525 added.
			after = snapshotTable(t, tdb, apiKeysTable)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(apiKeyRevokedAtPreviousVersion), version)
			assert.Equal(t, withoutColumns(after, apiKeyRevokedAtColumns), snapshotTable(t, tdb, apiKeysTable), "rollback removes revoked_at only")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM api_keys WHERE id IN (?, ?)", liveID, inactiveID).Scan(&count))
			assert.Equal(t, 2, count, "rollback keeps the key rows")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(apiKeyRevokedAtVersion), "reapply")
			migrated(t, tdb, after)
			assertRevokedAtAPIKeyRowKept(t, tdb, liveID, "live-key", true)
			assertRevokedAtAPIKeyRowKept(t, tdb, inactiveID, "inactive-key", false)
			require.ErrorIs(t, tdb.m.Migrate(apiKeyRevokedAtVersion), migrate.ErrNoChange)
		})
	}
}
