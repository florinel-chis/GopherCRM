package models

import (
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preRotationWebhookEndpointsDDL is webhook_endpoints exactly as AutoMigrate
// created it under #345, before #503: seven columns, no rotation columns.
const preRotationWebhookEndpointsDDL = `CREATE TABLE "webhook_endpoints" (` +
	`"id" integer PRIMARY KEY AUTOINCREMENT,"created_at" datetime,"updated_at" datetime,` +
	`"url" varchar(2048) NOT NULL,"event_types" varchar(255) NOT NULL,` +
	`"secret_sealed" varchar(255) NOT NULL,"is_active" numeric NOT NULL)`

// TestWebhookSecretRotationSQLiteUpgrade is the SQLite half of #503: SQLite
// installs never run the SQL migration, so MigrateDatabase on a file holding
// the pre-#503 webhook_endpoints table adds previous_secret_sealed and
// previous_secret_expires_at (both nullable), keeps every existing endpoint
// byte-identical with both new fields NULL, and a second run changes nothing.
func TestWebhookSecretRotationSQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "webhooks-pre-503.db"),
	}))
	require.NoError(t, DB.Exec(preRotationWebhookEndpointsDDL).Error)
	require.NoError(t, DB.Exec(`INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active)
		VALUES ('2026-11-20 09:30:00', '2026-11-20 09:30:00', 'https://hooks.example.com/kept', 'lead.created,ticket.created', 'enc:v1:kept-sealed-value', 1)`).Error)
	for _, column := range []string{"previous_secret_sealed", "previous_secret_expires_at"} {
		require.False(t, DB.Migrator().HasColumn("webhook_endpoints", column), "the fixture predates %s", column)
	}

	type rawEndpoint struct {
		ID           uint
		URL          string
		EventTypes   string
		SecretSealed string
		IsActive     bool
	}
	readRaw := func() rawEndpoint {
		var row rawEndpoint
		require.NoError(t, DB.Raw("SELECT id, url, event_types, secret_sealed, is_active FROM webhook_endpoints").Scan(&row).Error)
		return row
	}
	before := readRaw()
	require.NotZero(t, before.ID)

	require.NoError(t, MigrateDatabase())

	names, columns := webhookColumns(t, "webhook_endpoints")
	assert.Equal(t, []string{"consecutive_failures", "created_at", "event_types", "id", "is_active", "paused_until", "previous_secret_expires_at",
		"previous_secret_sealed", "secret_sealed", "updated_at", "url"}, names)
	for _, column := range []string{"previous_secret_sealed", "previous_secret_expires_at"} {
		assert.Equal(t, 0, columns[column].NotNull, "%s is nullable", column)
		assert.Nil(t, columns[column].Dflt, "%s has no default", column)
	}

	assert.Equal(t, before, readRaw(), "url, event_types, secret_sealed and is_active are byte-identical")
	var nulls int64
	require.NoError(t, DB.Raw(`SELECT COUNT(*) FROM webhook_endpoints WHERE id = ? AND previous_secret_sealed IS NULL
		AND previous_secret_expires_at IS NULL`, before.ID).Scan(&nulls).Error)
	assert.EqualValues(t, 1, nulls, "both new fields are NULL")
	var stored WebhookEndpoint
	require.NoError(t, DB.First(&stored, before.ID).Error)
	assert.Nil(t, stored.PreviousSecretSealed)
	assert.Nil(t, stored.PreviousSecretExpiresAt)
	assert.Equal(t, "enc:v1:kept-sealed-value", stored.SecretSealed)

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
}
