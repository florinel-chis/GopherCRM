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

// #551: 20261129120006_auth_audit_log_api_key_id adds
// auth_audit_log.api_key_id (bigint unsigned NULL) only when it is missing:
// the api_keys row an api_key_create, api_key_revoke or api_key_enable row
// concerns. No backfill, no index, no foreign key. The down half drops the
// column.

const (
	authAuditLogAPIKeyIDVersion         = 20261129120006
	authAuditLogAPIKeyIDPreviousVersion = webhookEndpointPauseVersion
	authAuditLogAPIKeyIDMigrationName   = "20261129120006_auth_audit_log_api_key_id"
)

// authAuditLogAPIKeyIDColumns is the column the migration adds.
var authAuditLogAPIKeyIDColumns = []string{"api_key_id"}

// withoutAuthAuditAPIKeyID drops what #551 added to auth_audit_log from a
// snapshot, so the roots of earlier auth_audit_log migrations check only
// their own columns.
func withoutAuthAuditAPIKeyID(schema tableSchema) tableSchema {
	return withoutColumns(schema, authAuditLogAPIKeyIDColumns)
}

// TestAuthAuditLogAPIKeyIDMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the one column behind an
// INFORMATION_SCHEMA probe, the down half drops only it, the model declares
// the same, and it is the newest migration of the directory, which
// latestMigrationVersion names.
func TestAuthAuditLogAPIKeyIDMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, authAuditLogAPIKeyIDMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, authAuditLogAPIKeyIDMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Auth audit log API key id (#551)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove auth audit log API key id (#551)\n-- Date: "), "the down half has its header")
	for _, half := range []string{string(up), string(down)} {
		assert.Contains(t, half, "\n-- Description: ")
		assert.Contains(t, half, "MySQL 8 and MariaDB 10.11 accept this as written.")
	}
	assert.Contains(t, string(up), "No backfill")

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

	probe := "information_schema.columns where table_schema = database() and table_name = 'auth_audit_log' and column_name = 'api_key_id'), "
	assert.Contains(t, upStatements, probe+"'select 1', 'alter table auth_audit_log add column api_key_id bigint unsigned null'",
		"api_key_id is added only when missing")
	assert.Equal(t, 1, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upStatements, "add column"), "exactly one column")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "index", "unique", "default"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, adds no index, key or default and changes nothing else")
	}

	assert.Contains(t, downStatements, probe+"'alter table auth_audit_log drop column api_key_id', 'select 1'")
	assert.Equal(t, 1, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downStatements, "drop column"), "the down half drops the one column only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "modify ", "create "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the same: a nullable id with no index and no default.
	tag, ok := reflectField(models.AuthAuditLog{}, "APIKeyID")
	require.True(t, ok)
	assert.Equal(t, "column:api_key_id", tag)

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestWebhookEndpointPauseMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, authAuditLogAPIKeyIDMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, authAuditLogAPIKeyIDVersion, version)
	assert.Equal(t, authAuditLogAPIKeyIDVersion, latestMigrationVersion)
	assert.Equal(t, authAuditLogAPIKeyIDPreviousVersion+1, authAuditLogAPIKeyIDVersion)
}

// insertAPIKeyIDAuditRow writes one audit row as #550 left the table, without
// the new column, and returns its id.
func insertAPIKeyIDAuditRow(t *testing.T, tdb *migrationTestDB, requestID string) int64 {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, user_id, request_id, client_network, user_agent_family, attempts)
		VALUES (UTC_TIMESTAMP(3), 'logout', 'success', '', 7, ?, '192.0.2.0/24', 'Firefox on Linux', 1)`, requestID)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertAPIKeyIDAuditRowKept checks that the row written before the
// migration is there unchanged and reads NULL in the new column.
func assertAPIKeyIDAuditRowKept(t *testing.T, tdb *migrationTestDB, id int64, requestID string) {
	t.Helper()
	var method, outcome, gotRequestID, network string
	var userID int
	var keyNull bool
	require.NoError(t, tdb.sql.QueryRow(`SELECT method, outcome, user_id, request_id, client_network, api_key_id IS NULL
		FROM auth_audit_log WHERE id = ?`, id).Scan(&method, &outcome, &userID, &gotRequestID, &network, &keyNull))
	assert.Equal(t, "logout", method)
	assert.Equal(t, "success", outcome)
	assert.Equal(t, 7, userID)
	assert.Equal(t, requestID, gotRequestID)
	assert.Equal(t, "192.0.2.0/24", network)
	assert.True(t, keyNull, "api_key_id reads NULL")
}

func TestAuthAuditLogAPIKeyIDMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table at the previous SQL head (before), and
	// as MigrateDatabase builds it on top of that head (want).
	reference := openMigrationTestDB(t, baseDSN, "aak_ref")
	require.NoError(t, reference.m.Migrate(authAuditLogAPIKeyIDPreviousVersion))
	before := snapshotTable(t, reference, authAuditLogTable)
	require.Empty(t, ownedColumns(before, authAuditLogAPIKeyIDColumns), "the previous head has no api_key_id")
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, authAuditLogTable)
	require.Len(t, want.Columns, len(before.Columns)+1)
	assert.Equal(t, before, withoutAuthAuditAPIKeyID(want), "the migration owns only the one column")
	assert.Equal(t, before.Indexes, want.Indexes, "no index")
	assert.Equal(t, before.ForeignKeys, want.ForeignKeys, "no foreign key")
	owned := ownedColumns(want, authAuditLogAPIKeyIDColumns)
	require.Len(t, owned, 1)
	// MySQL 8 shows bigint unsigned, MariaDB 10.11 the display width bigint(20) unsigned.
	assert.Regexp(t, `^api_key_id bigint(\(20\))? unsigned nullable=YES `, owned[0])

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "aak_fresh")
		require.NoError(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "a fresh SQL schema carries what MigrateDatabase builds")
		id := insertAPIKeyIDAuditRow(t, tdb, "req-fresh")
		assertAPIKeyIDAuditRowKept(t, tdb, id, "req-fresh")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, authAuditLogTable), "rollback removes the one column only")
		require.NoError(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
		require.ErrorIs(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "aak_mig")
			require.NoError(t, tdb.m.Migrate(authAuditLogAPIKeyIDPreviousVersion))
			const requestID = "req-populated"
			id := insertAPIKeyIDAuditRow(t, tdb, requestID)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
			}

			require.NoError(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogAPIKeyIDVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "the SQL migration and MigrateDatabase produce the same table")
			assertAPIKeyIDAuditRowKept(t, tdb, id, requestID)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), migrate.ErrNoChange)

			// Rollback keeps the row and removes only what #551 added.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogAPIKeyIDPreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, authAuditLogTable), "rollback removes the one column only")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM auth_audit_log WHERE id = ? AND request_id = ?", id, requestID).Scan(&count))
			assert.Equal(t, 1, count, "rollback keeps the audit row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), "reapply")
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
			assertAPIKeyIDAuditRowKept(t, tdb, id, requestID)
			require.ErrorIs(t, tdb.m.Migrate(authAuditLogAPIKeyIDVersion), migrate.ErrNoChange)
		})
	}
}
