package models

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prePauseWebhookEndpointsDDL is webhook_endpoints exactly as AutoMigrate
// left it after #503, before #569: nine columns, no pause columns.
const prePauseWebhookEndpointsDDL = `CREATE TABLE "webhook_endpoints" (` +
	`"id" integer PRIMARY KEY AUTOINCREMENT,"created_at" datetime,"updated_at" datetime,` +
	`"url" varchar(2048) NOT NULL,"event_types" varchar(255) NOT NULL,` +
	`"secret_sealed" varchar(255) NOT NULL,"is_active" numeric NOT NULL,` +
	`"previous_secret_sealed" varchar(255),"previous_secret_expires_at" datetime)`

// TestWebhookEndpointPauseSQLiteUpgrade is the SQLite half of #569: SQLite
// installs never run the SQL migration, so MigrateDatabase on a file holding
// the post-#503 webhook_endpoints table adds consecutive_failures (NOT NULL,
// default 0) and paused_until (nullable), keeps every existing endpoint
// byte-identical with 0 and NULL in the new fields, and a second run changes
// nothing.
func TestWebhookEndpointPauseSQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "webhooks-pre-569.db"),
	}))
	require.NoError(t, DB.Exec(prePauseWebhookEndpointsDDL).Error)
	require.NoError(t, DB.Exec(`INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active,
		previous_secret_sealed, previous_secret_expires_at)
		VALUES ('2026-11-20 09:30:00', '2026-11-20 09:30:00', 'https://hooks.example.com/kept', 'lead.created,ticket.created',
		'enc:v1:kept-sealed-value', 1, 'enc:v1:kept-previous-value', '2026-11-21 09:30:00')`).Error)
	for _, column := range []string{"consecutive_failures", "paused_until"} {
		require.False(t, DB.Migrator().HasColumn("webhook_endpoints", column), "the fixture predates %s", column)
	}

	type rawEndpoint struct {
		ID                      uint
		CreatedAt               string
		UpdatedAt               string
		URL                     string
		EventTypes              string
		SecretSealed            string
		IsActive                bool
		PreviousSecretSealed    string
		PreviousSecretExpiresAt string
	}
	readRaw := func() rawEndpoint {
		var row rawEndpoint
		require.NoError(t, DB.Raw(`SELECT id, CAST(created_at AS TEXT) AS created_at, CAST(updated_at AS TEXT) AS updated_at, url,
			event_types, secret_sealed, is_active, previous_secret_sealed,
			CAST(previous_secret_expires_at AS TEXT) AS previous_secret_expires_at FROM webhook_endpoints`).Scan(&row).Error)
		return row
	}
	before := readRaw()
	require.NotZero(t, before.ID)
	_, preColumns := webhookColumns(t, "webhook_endpoints")
	require.Equal(t, "varchar(255)", strings.ToLower(preColumns["event_types"].Type), "the fixture predates #515")

	require.NoError(t, MigrateDatabase())

	names, columns := webhookColumns(t, "webhook_endpoints")
	assert.Equal(t, []string{"consecutive_failures", "created_at", "event_types", "id", "is_active", "paused_until", "previous_secret_expires_at",
		"previous_secret_sealed", "secret_sealed", "updated_at", "url"}, names)
	assert.Equal(t, 1, columns["consecutive_failures"].NotNull, "consecutive_failures is NOT NULL")
	require.NotNil(t, columns["consecutive_failures"].Dflt)
	assert.Equal(t, "0", strings.Trim(*columns["consecutive_failures"].Dflt, `'"`))
	assert.Equal(t, 0, columns["paused_until"].NotNull, "paused_until is nullable")
	assert.Nil(t, columns["paused_until"].Dflt, "paused_until has no default")

	assert.Equal(t, "varchar(512)", strings.ToLower(columns["event_types"].Type), "the table rebuild widened event_types (#515)")
	assert.Equal(t, before, readRaw(), "every existing column is byte-identical")
	assert.Equal(t, "lead.created,ticket.created", readRaw().EventTypes, "the stored event_types survives the rebuild")
	var healthy int64
	require.NoError(t, DB.Raw(`SELECT COUNT(*) FROM webhook_endpoints WHERE id = ? AND consecutive_failures = 0
		AND paused_until IS NULL`, before.ID).Scan(&healthy).Error)
	assert.EqualValues(t, 1, healthy, "the existing endpoint reads 0 and NULL")
	var stored WebhookEndpoint
	require.NoError(t, DB.First(&stored, before.ID).Error)
	assert.Equal(t, 0, stored.ConsecutiveFailures)
	assert.Nil(t, stored.PausedUntil)
	assert.Equal(t, "enc:v1:kept-sealed-value", stored.SecretSealed)

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	rows := readRaw()
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
	assert.Equal(t, rows, readRaw(), "the second MigrateDatabase leaves the row alone")
}
