package models

import (
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preSourceUserIdentitiesDDL is user_identities exactly as AutoMigrate
// created it under #435, before #505: no source column.
var preSourceUserIdentitiesDDL = []string{
	"CREATE TABLE `user_identities` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime," +
		"`user_id` integer NOT NULL,`issuer` varchar(512) NOT NULL,`subject` varchar(255) NOT NULL," +
		"CONSTRAINT `fk_user_identities_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT)",
	"CREATE UNIQUE INDEX `idx_user_identities_issuer_subject` ON `user_identities`(`issuer`,`subject`)",
	"CREATE INDEX `idx_user_identities_user_id` ON `user_identities`(`user_id`)",
}

// TestUserIdentitySourceSQLiteUpgrade is the SQLite half of #505: SQLite
// installs never run the SQL migration, so MigrateDatabase on a file holding
// the pre-#505 user_identities table adds source NOT NULL DEFAULT 'oidc',
// keeps every existing identity byte-identical and reading oidc, and a second
// run changes nothing.
func TestUserIdentitySourceSQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "identities-pre-505.db"),
	}))
	// Every other table as the application builds it, user_identities as
	// #435 left it.
	require.NoError(t, MigrateDatabase())
	require.NoError(t, DB.Exec("DROP TABLE user_identities").Error)
	for _, statement := range preSourceUserIdentitiesDDL {
		require.NoError(t, DB.Exec(statement).Error)
	}
	require.NoError(t, DB.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
		VALUES ('linked-505@example.com', 'x', 'L', 'U', 'sales', 1, '2026-11-20 09:30:00', '2026-11-20 09:30:00')`).Error)
	require.NoError(t, DB.Exec(`INSERT INTO user_identities (created_at, updated_at, user_id, issuer, subject)
		SELECT '2026-11-20 09:30:00', '2026-11-20 09:30:00', id, 'https://idp.example.test/505', 'Kept-Subject-505' FROM users
		WHERE email = 'linked-505@example.com'`).Error)
	require.False(t, DB.Migrator().HasColumn("user_identities", "source"), "the fixture predates source")

	type rawIdentity struct {
		ID        uint
		CreatedAt string
		UpdatedAt string
		UserID    uint
		Issuer    string
		Subject   string
	}
	readRaw := func() rawIdentity {
		var row rawIdentity
		require.NoError(t, DB.Raw("SELECT id, CAST(created_at AS TEXT) AS created_at, CAST(updated_at AS TEXT) AS updated_at, user_id, issuer, subject FROM user_identities").Scan(&row).Error)
		return row
	}
	before := readRaw()
	require.NotZero(t, before.ID)

	require.NoError(t, MigrateDatabase())

	names, columns := webhookColumns(t, "user_identities")
	assert.Equal(t, []string{"created_at", "id", "issuer", "source", "subject", "updated_at", "user_id"}, names)
	assert.Equal(t, "varchar(8)", columns["source"].Type)
	assert.Equal(t, 1, columns["source"].NotNull, "source is NOT NULL")
	if assert.NotNil(t, columns["source"].Dflt, "source has a default") {
		assert.Contains(t, *columns["source"].Dflt, "oidc")
	}

	assert.Equal(t, before, readRaw(), "every other column is byte-identical")
	var source string
	require.NoError(t, DB.Raw("SELECT source FROM user_identities WHERE id = ?", before.ID).Scan(&source).Error)
	assert.Equal(t, UserIdentitySourceOIDC, source, "the existing row reads oidc")
	var stored UserIdentity
	require.NoError(t, DB.First(&stored, before.ID).Error)
	assert.Equal(t, UserIdentitySourceOIDC, stored.Source)

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
}
