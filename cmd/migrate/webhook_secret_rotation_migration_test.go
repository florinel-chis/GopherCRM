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

// #503: 20261127120000_webhook_secret_rotation adds
// webhook_endpoints.previous_secret_sealed (varchar(255) NULL) and
// previous_secret_expires_at (datetime(3) NULL), each only when missing, so
// a rotated signing secret keeps signing next to the new one for its overlap
// window. No backfill, no index. The down half drops the two columns.

const (
	webhookSecretRotationVersion         = 20261127120000
	webhookSecretRotationPreviousVersion = scimTokensVersion
	webhookSecretRotationMigrationName   = "20261127120000_webhook_secret_rotation"
	webhookEndpointsTable                = "webhook_endpoints"
)

// webhookSecretRotationColumns are the columns the migration adds.
var webhookSecretRotationColumns = []string{"previous_secret_sealed", "previous_secret_expires_at"}

// TestWebhookSecretRotationMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the two columns, each behind an
// INFORMATION_SCHEMA probe, the down half drops only them, the model declares
// the same, and it is older than the newest migration.
func TestWebhookSecretRotationMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, webhookSecretRotationMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, webhookSecretRotationMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Webhook signing-secret rotation (#503)"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove webhook signing-secret rotation (#503)"), "the down half has its header")

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

	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'webhook_endpoints' and column_name = 'previous_secret_sealed'), 'select 1', 'alter table webhook_endpoints add column previous_secret_sealed varchar(255) null'",
		"previous_secret_sealed is added only when missing")
	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'webhook_endpoints' and column_name = 'previous_secret_expires_at'), 'select 1', 'alter table webhook_endpoints add column previous_secret_expires_at datetime(3) null'",
		"previous_secret_expires_at is added only when missing")
	assert.Equal(t, 2, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(upStatements, "add column"), "exactly two columns")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "index", "unique", "default"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, adds no index or key and changes nothing else")
	}

	assert.Contains(t, downStatements, "column_name = 'previous_secret_expires_at'), 'alter table webhook_endpoints drop column previous_secret_expires_at', 'select 1'")
	assert.Contains(t, downStatements, "column_name = 'previous_secret_sealed'), 'alter table webhook_endpoints drop column previous_secret_sealed', 'select 1'")
	assert.Equal(t, 2, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(downStatements, " drop "), "the down half drops the two columns only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the same: nullable, no default, no index.
	tag, ok := reflectField(models.WebhookEndpoint{}, "PreviousSecretSealed")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(255)", tag)
	tag, ok = reflectField(models.WebhookEndpoint{}, "PreviousSecretExpiresAt")
	require.True(t, ok)
	assert.Equal(t, "", tag, "a nullable datetime with no default and no index")

	// The newest-migration assertions moved to
	// TestUserIdentitySourceMigrationFiles (#505).
	assert.Less(t, webhookSecretRotationVersion, latestMigrationVersion)
	assert.Greater(t, webhookSecretRotationVersion, webhookSecretRotationPreviousVersion)
}

// insertRotationEndpointRow writes one endpoint as #345 wrote it, without the
// new columns, and returns its id.
func insertRotationEndpointRow(t *testing.T, tdb *migrationTestDB, url string) int64 {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active)
		VALUES (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), ?, 'lead.created,ticket.created', 'enc:v1:kept-sealed', 1)`, url)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertRotationEndpointRowKept checks that the endpoint written before the
// migration is there unchanged and reads NULL in both new columns.
func assertRotationEndpointRowKept(t *testing.T, tdb *migrationTestDB, id int64, url string) {
	t.Helper()
	var gotURL, types, sealed string
	var active bool
	var nulls int
	require.NoError(t, tdb.sql.QueryRow(`SELECT url, event_types, secret_sealed, is_active,
		(previous_secret_sealed IS NULL) + (previous_secret_expires_at IS NULL) FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&gotURL, &types, &sealed, &active, &nulls))
	assert.Equal(t, url, gotURL)
	assert.Equal(t, "lead.created,ticket.created", types)
	assert.Equal(t, "enc:v1:kept-sealed", sealed)
	assert.True(t, active)
	assert.Equal(t, 2, nulls, "both new columns read NULL")
}

func TestWebhookSecretRotationMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table at the previous SQL head (before), and
	// as MigrateDatabase builds it on top of that head without the columns a
	// later migration owns (#569) (want). model is what MigrateDatabase
	// leaves, the later columns included.
	reference := openMigrationTestDB(t, baseDSN, "wsr_ref")
	require.NoError(t, reference.m.Migrate(webhookSecretRotationPreviousVersion))
	before := snapshotTable(t, reference, webhookEndpointsTable)
	runMigrateDatabase(t, reference.dsn)
	model := snapshotTable(t, reference, webhookEndpointsTable)
	want := withoutColumns(model, webhookEndpointPauseColumns)
	require.Len(t, model.Columns, len(want.Columns)+len(webhookEndpointPauseColumns))
	// at reads the table at this migration's version: what the SQL head of
	// that version and MigrateDatabase agree on.
	at := func(tdb *migrationTestDB) tableSchema {
		return withoutColumns(snapshotTable(t, tdb, webhookEndpointsTable), webhookEndpointPauseColumns)
	}
	require.Len(t, want.Columns, len(before.Columns)+2)
	assert.Equal(t, before, withoutColumns(want, webhookSecretRotationColumns), "the migration owns only the two columns")
	owned := ownedColumns(want, webhookSecretRotationColumns)
	require.Len(t, owned, 2)
	for _, line := range owned {
		switch {
		case strings.HasPrefix(line, "previous_secret_sealed "):
			assert.Contains(t, line, "previous_secret_sealed varchar(255) nullable=YES")
		case strings.HasPrefix(line, "previous_secret_expires_at "):
			assert.Contains(t, line, "previous_secret_expires_at datetime(3) nullable=YES")
		}
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wsr_fresh")
		require.NoError(t, tdb.m.Migrate(webhookSecretRotationVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "a fresh SQL schema carries what MigrateDatabase builds")
		id := insertRotationEndpointRow(t, tdb, "https://example.com/fresh")
		assertRotationEndpointRowKept(t, tdb, id, "https://example.com/fresh")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable), "rollback removes the two columns only")
		require.NoError(t, tdb.m.Migrate(webhookSecretRotationVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
		require.ErrorIs(t, tdb.m.Migrate(webhookSecretRotationVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "wsr_mig")
			require.NoError(t, tdb.m.Migrate(webhookSecretRotationPreviousVersion))
			const url = "https://example.com/populated"
			id := insertRotationEndpointRow(t, tdb, url)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, at(tdb))
			}

			require.NoError(t, tdb.m.Migrate(webhookSecretRotationVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhookSecretRotationVersion), version)
			assert.Equal(t, want, at(tdb), "the SQL migration and MigrateDatabase produce the same table")
			assertRotationEndpointRowKept(t, tdb, id, url)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, at(tdb), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(webhookSecretRotationVersion), migrate.ErrNoChange)

			// Rollback keeps the row and removes only what #503 added.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhookSecretRotationPreviousVersion), version)
			assert.Equal(t, before, at(tdb), "rollback removes the two columns only (MigrateDatabase added the #569 columns, which stay)")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM webhook_endpoints WHERE id = ? AND secret_sealed = 'enc:v1:kept-sealed'", id).Scan(&count))
			assert.Equal(t, 1, count, "rollback keeps the endpoint row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(webhookSecretRotationVersion), "reapply")
			assert.Equal(t, want, at(tdb))
			assertRotationEndpointRowKept(t, tdb, id, url)
			require.ErrorIs(t, tdb.m.Migrate(webhookSecretRotationVersion), migrate.ErrNoChange)
		})
	}
}
