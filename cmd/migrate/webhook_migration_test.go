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

// #345: 20261120120000_add_outbound_webhooks creates webhook_endpoints and
// webhook_deliveries only when they are missing, with the columns and indexes
// AutoMigrate produces (no foreign key); rollback drops those two tables and
// nothing else.

const (
	webhooksVersion         = 20261120120000
	webhooksPreviousVersion = rolesVersion
	webhooksMigrationName   = "20261120120000_add_outbound_webhooks"
)

var webhookMigrationTables = []string{"webhook_endpoints", "webhook_deliveries"}

// TestWebhookMigrationFiles needs no database: the pair exists with its
// headers, creates both tables only when missing with the model's columns and
// index names and drops only them. It is no longer the newest migration
// (#382).
func TestWebhookMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, webhooksMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, webhooksMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Outbound webhooks"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove outbound webhooks"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, table := range webhookMigrationTables {
		assert.Contains(t, upSQL, "create table if not exists "+table+" (", "the up creates %s only when missing", table)
		assert.Contains(t, downSQL, "drop table if exists "+table+";", "the down drops %s", table)
	}
	for _, fragment := range []string{
		"url varchar(2048) not null",
		"event_types varchar(255) not null",
		"secret_sealed varchar(255) not null",
		"is_active tinyint(1) not null,",
		"endpoint_id bigint unsigned not null",
		"event_id varchar(64) not null",
		"event_type varchar(32) not null",
		"record_id bigint unsigned not null",
		"occurred_at datetime(3) not null",
		"status varchar(20) not null default 'pending'",
		"attempts int not null default 0",
		"next_attempt_at datetime(3) null",
		"last_attempt_at datetime(3) null",
		"lease_until datetime(3) null",
		"last_http_status int null",
		"error_code varchar(50) not null default ''",
		"unique key idx_webhook_deliveries_endpoint_event (endpoint_id, event_id)",
		"key idx_webhook_deliveries_status_next (status, next_attempt_at)",
		"key idx_webhook_deliveries_updated_at (updated_at)",
	} {
		assert.Contains(t, upSQL, fragment)
	}
	assert.NotContains(t, upSQL, "is_active tinyint(1) not null default", "is_active has no column default")
	assert.NotContains(t, upSQL, "foreign key")
	assert.NotContains(t, upSQL, "deleted_at")
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.Equal(t, 2, strings.Count(downSQL, "drop table"), "rollback drops exactly the two tables")

	// The model declares the same index names.
	for field, fragment := range map[string]string{
		"EndpointID":    "uniqueIndex:idx_webhook_deliveries_endpoint_event,priority:1",
		"EventID":       "uniqueIndex:idx_webhook_deliveries_endpoint_event,priority:2",
		"Status":        "index:idx_webhook_deliveries_status_next,priority:1",
		"NextAttemptAt": "index:idx_webhook_deliveries_status_next,priority:2",
		"UpdatedAt":     "index:idx_webhook_deliveries_updated_at",
	} {
		tag, ok := reflectField(models.WebhookDelivery{}, field)
		require.True(t, ok, field)
		assert.Contains(t, tag, fragment, field)
	}

	// The newest-migration and latestMigrationVersion assertions moved on:
	// TestQuoteHistorySourceMigrationFiles holds them (#400).
	version, err := strconv.Atoi(strings.SplitN(webhooksMigrationName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, webhooksVersion, version)
	assert.Less(t, webhooksVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Greater(t, webhooksVersion, webhooksPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	for _, table := range webhookMigrationTables {
		assert.Contains(t, string(source), `"`+table+`"`, "main_test.go's expected-table list names %s", table)
	}
}

func snapshotWebhookTables(t *testing.T, tdb *migrationTestDB) map[string]tableSchema {
	t.Helper()
	out := map[string]tableSchema{}
	for _, table := range webhookMigrationTables {
		out[table] = snapshotTable(t, tdb, table)
	}
	// The width of event_types belongs to 20261129120007 (#515) and is
	// compared by TestWebhookEventTypesWidthMigration.
	out[webhookEndpointsTable] = withEventTypesBeforeWidening(out[webhookEndpointsTable])
	return out
}

func insertMigrationWebhookRows(t *testing.T, db *sql.DB, eventBase int) {
	t.Helper()
	result, err := db.Exec("INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active) VALUES (NOW(3), NOW(3), 'https://example.com/x', 'lead.created', 'enc:v1:x', 0)")
	require.NoError(t, err)
	endpointID, err := result.LastInsertId()
	require.NoError(t, err)
	insert := "INSERT INTO webhook_deliveries (endpoint_id, event_id, event_type, record_id, occurred_at, next_attempt_at) VALUES (?, ?, 'lead.created', 1, NOW(3), NOW(3))"
	event := fmt.Sprintf("evt_%032d", eventBase)
	_, err = db.Exec(insert, endpointID, event)
	require.NoError(t, err)
	_, err = db.Exec(insert, endpointID, event)
	require.Error(t, err, "one row per endpoint and event")
	var status, errorCode string
	var attempts int
	require.NoError(t, db.QueryRow("SELECT status, attempts, error_code FROM webhook_deliveries WHERE event_id = ?", event).Scan(&status, &attempts, &errorCode))
	assert.Equal(t, "pending", status)
	assert.Equal(t, 0, attempts)
	assert.Equal(t, "", errorCode)
}

func TestWebhookMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the tables as MigrateDatabase creates them on top
	// of the previous SQL head (wantModel), and the same without the columns
	// #503 and #569 added to webhook_endpoints (want), which is what this
	// migration creates at its version.
	reference := openMigrationTestDB(t, baseDSN, "whk_ref")
	require.NoError(t, reference.m.Migrate(webhooksPreviousVersion))
	for _, table := range webhookMigrationTables {
		assert.NotContains(t, listTables(t, reference.sql, reference.name), table)
	}
	runMigrateDatabase(t, reference.dsn)
	wantModel := snapshotWebhookTables(t, reference)
	want := map[string]tableSchema{
		"webhook_endpoints":  withoutColumns(withoutColumns(wantModel["webhook_endpoints"], webhookSecretRotationColumns), webhookEndpointPauseColumns),
		"webhook_deliveries": wantModel["webhook_deliveries"],
	}
	require.Len(t, wantModel["webhook_endpoints"].Columns, len(want["webhook_endpoints"].Columns)+len(webhookSecretRotationColumns)+len(webhookEndpointPauseColumns))
	require.NotEmpty(t, want["webhook_endpoints"].Columns)
	require.NotEmpty(t, want["webhook_deliveries"].Columns)
	assert.Contains(t, want["webhook_deliveries"].Indexes, "idx_webhook_deliveries_endpoint_event (endpoint_id,event_id) non_unique=0")
	assert.Contains(t, want["webhook_deliveries"].Indexes, "idx_webhook_deliveries_status_next (status,next_attempt_at) non_unique=1")
	assert.Contains(t, want["webhook_deliveries"].Indexes, "idx_webhook_deliveries_updated_at (updated_at) non_unique=1")
	for _, table := range webhookMigrationTables {
		assert.Empty(t, want[table].ForeignKeys, "%s has no foreign key", table)
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "whk_fresh")
		// Up to this migration, not to the head: the step back below must
		// undo this one, and newer migrations follow it (#382).
		require.NoError(t, tdb.m.Migrate(webhooksVersion))
		assert.Equal(t, want, snapshotWebhookTables(t, tdb), "a fresh SQL schema carries what MigrateDatabase creates")
		insertMigrationWebhookRows(t, tdb.sql, 1)

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, webhookMigrationTables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the two tables and nothing else")
		require.NoError(t, tdb.m.Migrate(webhooksVersion))
		assert.Equal(t, want, snapshotWebhookTables(t, tdb))

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		for _, table := range webhookMigrationTables {
			assert.NotContains(t, listTables(t, tdb.sql, tdb.name), table)
		}
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "whk_mig")
			require.NoError(t, tdb.m.Migrate(webhooksPreviousVersion))
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
				insertMigrationWebhookRows(t, tdb.sql, 2)
			}

			require.NoError(t, tdb.m.Migrate(webhooksVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhooksVersion), version)
			if autoMigrateFirst {
				// The tables AutoMigrate created are kept as they are.
				assert.Equal(t, wantModel, snapshotWebhookTables(t, tdb), "the tables AutoMigrate created are kept")
			} else {
				assert.Equal(t, want, snapshotWebhookTables(t, tdb), "the SQL migration and AutoMigrate produce the same tables")
			}
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM webhook_deliveries").Scan(&count))
				assert.Equal(t, 1, count, "tables AutoMigrate created first are kept with their rows")
			}

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, wantModel, snapshotWebhookTables(t, tdb), "MigrateDatabase adds only the #503 columns")
			insertMigrationWebhookRows(t, tdb.sql, 3)
			require.ErrorIs(t, tdb.m.Migrate(webhooksVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhooksPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, webhookMigrationTables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the two tables and nothing else")

			require.NoError(t, tdb.m.Migrate(webhooksVersion))
			assert.Equal(t, want, snapshotWebhookTables(t, tdb))
			insertMigrationWebhookRows(t, tdb.sql, 4)
		})
	}
}
