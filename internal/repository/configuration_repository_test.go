package repository

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupConfigurationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Configuration{}))

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestInitializeDefaultsSeedsMissingEntries(t *testing.T) {
	repo := NewConfigurationRepository(setupConfigurationDB(t))

	require.NoError(t, repo.InitializeDefaults())

	for _, expected := range models.DefaultConfigurations() {
		stored, err := repo.GetByKey(expected.Key)
		require.NoError(t, err, "default %q was not seeded", expected.Key)
		assert.Equal(t, expected.Value, stored.Value)
		assert.Equal(t, expected.IsSensitive, stored.IsSensitive)
	}
}

// InitializeDefaults runs on every boot. Re-seeding must leave configured
// values alone: resetting them would undo an administrator's settings, and for
// a sensitive entry it would silently drop a stored secret on restart.
func TestInitializeDefaultsKeepsConfiguredValues(t *testing.T) {
	repo := NewConfigurationRepository(setupConfigurationDB(t))
	require.NoError(t, repo.InitializeDefaults())

	secret, err := repo.GetByKey("integration.aeo.gemini_api_key")
	require.NoError(t, err)
	secret.Value = "enc:v1:stored-ciphertext-stand-in"
	require.NoError(t, repo.Update(secret))

	plain, err := repo.GetByKey("general.company_name")
	require.NoError(t, err)
	plain.Value = "Acme Industries"
	require.NoError(t, repo.Update(plain))

	require.NoError(t, repo.InitializeDefaults())

	secret, err = repo.GetByKey("integration.aeo.gemini_api_key")
	require.NoError(t, err)
	assert.Equal(t, "enc:v1:stored-ciphertext-stand-in", secret.Value)

	plain, err = repo.GetByKey("general.company_name")
	require.NoError(t, err)
	assert.Equal(t, "Acme Industries", plain.Value)
}

// Metadata, unlike the value, is refreshed: an entry that predates a flag picks
// it up on the next boot instead of needing a migration.
func TestInitializeDefaultsRefreshesMetadata(t *testing.T) {
	db := setupConfigurationDB(t)
	repo := NewConfigurationRepository(db)

	stale := models.Configuration{
		Key:         "integration.aeo.openai_api_key",
		Value:       "enc:v1:stored-ciphertext-stand-in",
		Type:        models.ConfigTypeString,
		Category:    models.CategoryIntegration,
		Description: "written before the entry became sensitive",
	}
	require.NoError(t, repo.Create(&stale))

	require.NoError(t, repo.InitializeDefaults())

	refreshed, err := repo.GetByKey("integration.aeo.openai_api_key")
	require.NoError(t, err)
	assert.True(t, refreshed.IsSensitive)
	assert.True(t, refreshed.IsSystem)
	assert.Equal(t, "enc:v1:stored-ciphertext-stand-in", refreshed.Value)
}
