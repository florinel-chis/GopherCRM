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

// #569: 20261129120005_webhook_endpoint_pause adds
// webhook_endpoints.consecutive_failures (int NOT NULL DEFAULT 0) and
// paused_until (datetime(3) NULL), each only when missing, so an endpoint
// whose sent deliveries keep failing can be paused. No backfill, no index.
// The down half drops the two columns.

const (
	webhookEndpointPauseVersion         = 20261129120005
	webhookEndpointPausePreviousVersion = refreshTokenSessionVersion
	webhookEndpointPauseMigrationName   = "20261129120005_webhook_endpoint_pause"
)

// webhookEndpointPauseColumns are the columns the migration adds.
var webhookEndpointPauseColumns = []string{"consecutive_failures", "paused_until"}

// TestWebhookEndpointPauseMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the two columns, each behind an
// INFORMATION_SCHEMA probe, the down half drops only them, the model declares
// the same, and it follows the previous migration.
func TestWebhookEndpointPauseMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, webhookEndpointPauseMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, webhookEndpointPauseMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Webhook endpoint pause (#569)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove webhook endpoint pause (#569)\n-- Date: "), "the down half has its header")
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

	probe := "information_schema.columns where table_schema = database() and table_name = 'webhook_endpoints' and column_name = "
	assert.Contains(t, upStatements, probe+"'consecutive_failures'), 'select 1', 'alter table webhook_endpoints add column consecutive_failures int not null default 0'",
		"consecutive_failures is added only when missing")
	assert.Contains(t, upStatements, probe+"'paused_until'), 'select 1', 'alter table webhook_endpoints add column paused_until datetime(3) null'",
		"paused_until is added only when missing")
	assert.Equal(t, 2, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(upStatements, "add column"), "exactly two columns")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "index", "unique"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, adds no index or key and changes nothing else")
	}

	assert.Contains(t, downStatements, probe+"'paused_until'), 'alter table webhook_endpoints drop column paused_until', 'select 1'")
	assert.Contains(t, downStatements, probe+"'consecutive_failures'), 'alter table webhook_endpoints drop column consecutive_failures', 'select 1'")
	assert.Equal(t, 2, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(downStatements, "drop column"), "the down half drops the two columns only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "modify ", "create "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the same: a 32-bit NOT NULL counter with default 0,
	// and a nullable datetime with no default and no index.
	tag, ok := reflectField(models.WebhookEndpoint{}, "ConsecutiveFailures")
	require.True(t, ok)
	assert.Equal(t, "size:32;not null;default:0", tag)
	tag, ok = reflectField(models.WebhookEndpoint{}, "PausedUntil")
	require.True(t, ok)
	assert.Equal(t, "", tag, "a nullable datetime with no default and no index")

	// The newest-migration assertions moved to
	// TestAuthAuditLogAPIKeyIDMigrationFiles (#551).
	assert.Less(t, webhookEndpointPauseVersion, latestMigrationVersion)
	assert.Equal(t, webhookEndpointPausePreviousVersion+1, webhookEndpointPauseVersion)
}

// insertPauseEndpointRow writes one endpoint as #503 left it, without the new
// columns, and returns its id.
func insertPauseEndpointRow(t *testing.T, tdb *migrationTestDB, url string) int64 {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active,
		previous_secret_sealed, previous_secret_expires_at)
		VALUES (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), ?, 'lead.created,ticket.created', 'enc:v1:kept-sealed', 1, 'enc:v1:kept-previous', UTC_TIMESTAMP(3))`, url)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertPauseEndpointRowKept checks that the endpoint written before the
// migration is there unchanged and reads 0 and NULL in the new columns.
func assertPauseEndpointRowKept(t *testing.T, tdb *migrationTestDB, id int64, url string) {
	t.Helper()
	var gotURL, types, sealed, previous string
	var active bool
	var failures int
	var pausedNull bool
	require.NoError(t, tdb.sql.QueryRow(`SELECT url, event_types, secret_sealed, is_active, previous_secret_sealed,
		consecutive_failures, paused_until IS NULL FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&gotURL, &types, &sealed, &active, &previous, &failures, &pausedNull))
	assert.Equal(t, url, gotURL)
	assert.Equal(t, "lead.created,ticket.created", types)
	assert.Equal(t, "enc:v1:kept-sealed", sealed)
	assert.Equal(t, "enc:v1:kept-previous", previous)
	assert.True(t, active)
	assert.Equal(t, 0, failures, "consecutive_failures reads 0")
	assert.True(t, pausedNull, "paused_until reads NULL")
}

func TestWebhookEndpointPauseMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table at the previous SQL head (before), and
	// as MigrateDatabase builds it on top of that head (want).
	reference := openMigrationTestDB(t, baseDSN, "wep_ref")
	require.NoError(t, reference.m.Migrate(webhookEndpointPausePreviousVersion))
	before := snapshotTable(t, reference, webhookEndpointsTable)
	require.Empty(t, ownedColumns(before, webhookEndpointPauseColumns), "the previous head has no pause columns")
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, webhookEndpointsTable)
	require.Len(t, want.Columns, len(before.Columns)+2)
	assert.Equal(t, before, withoutColumns(want, webhookEndpointPauseColumns), "the migration owns only the two columns")
	owned := ownedColumns(want, webhookEndpointPauseColumns)
	require.Len(t, owned, 2)
	for _, line := range owned {
		switch {
		case strings.HasPrefix(line, "consecutive_failures "):
			// MySQL 8 shows int, MariaDB 10.11 the display width int(11).
			assert.Regexp(t, `^consecutive_failures int(\(11\))? nullable=NO default=0 `, line)
		case strings.HasPrefix(line, "paused_until "):
			assert.Contains(t, line, "paused_until datetime(3) nullable=YES")
		}
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wep_fresh")
		require.NoError(t, tdb.m.Migrate(webhookEndpointPauseVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "a fresh SQL schema carries what MigrateDatabase builds")
		id := insertPauseEndpointRow(t, tdb, "https://example.com/fresh")
		assertPauseEndpointRowKept(t, tdb, id, "https://example.com/fresh")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable), "rollback removes the two columns only")
		require.NoError(t, tdb.m.Migrate(webhookEndpointPauseVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
		require.ErrorIs(t, tdb.m.Migrate(webhookEndpointPauseVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "wep_mig")
			require.NoError(t, tdb.m.Migrate(webhookEndpointPausePreviousVersion))
			const url = "https://example.com/populated"
			id := insertPauseEndpointRow(t, tdb, url)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
			}

			require.NoError(t, tdb.m.Migrate(webhookEndpointPauseVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhookEndpointPauseVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "the SQL migration and MigrateDatabase produce the same table")
			assertPauseEndpointRowKept(t, tdb, id, url)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(webhookEndpointPauseVersion), migrate.ErrNoChange)

			// Rollback keeps the row and removes only what #569 added.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhookEndpointPausePreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable), "rollback removes the two columns only")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM webhook_endpoints WHERE id = ? AND secret_sealed = 'enc:v1:kept-sealed'", id).Scan(&count))
			assert.Equal(t, 1, count, "rollback keeps the endpoint row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(webhookEndpointPauseVersion), "reapply")
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
			assertPauseEndpointRowKept(t, tdb, id, url)
			require.ErrorIs(t, tdb.m.Migrate(webhookEndpointPauseVersion), migrate.ErrNoChange)
		})
	}
}
