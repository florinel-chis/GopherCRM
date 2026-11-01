package repository

import (
	"encoding/json"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	// The deals default currency ships as EUR and is admin-editable; a boot
	// must not put EUR back.
	currency, err := repo.GetByKey("deals.default_currency")
	require.NoError(t, err)
	assert.Equal(t, "EUR", currency.Value)
	assert.Equal(t, models.CategoryDeals, currency.Category)
	currency.Value = "RON"
	require.NoError(t, repo.Update(currency))

	require.NoError(t, repo.InitializeDefaults())

	currency, err = repo.GetByKey("deals.default_currency")
	require.NoError(t, err)
	assert.Equal(t, "RON", currency.Value)

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

// Rows seeded before the model carried timestamps hold a NULL created_at, which
// reads back as the zero time. Re-writing that zero into the row is rejected by
// MySQL under NO_ZERO_DATE (Error 1292), which aborted the whole seeding
// transaction on boot and left every default unrefreshed.
func TestInitializeDefaultsRepairsMissingCreatedAt(t *testing.T) {
	db := setupConfigurationDB(t)
	repo := NewConfigurationRepository(db)

	legacy := models.Configuration{
		Key:         "general.company_name",
		Value:       "Acme Industries",
		Type:        models.ConfigTypeString,
		Category:    models.CategoryGeneral,
		Description: "seeded before timestamps existed",
	}
	require.NoError(t, repo.Create(&legacy))
	require.NoError(t, db.Model(&models.Configuration{}).Where("id = ?", legacy.ID).
		UpdateColumns(map[string]any{"created_at": nil, "updated_at": nil}).Error)

	require.NoError(t, repo.InitializeDefaults())

	repaired, err := repo.GetByKey("general.company_name")
	require.NoError(t, err)
	assert.False(t, repaired.CreatedAt.IsZero(), "created_at must be populated, not written back as zero")
	assert.Equal(t, "Acme Industries", repaired.Value, "the administrator's value must survive the repair")
}

// Issue #104: the allowed-status metadata listed "lost", which is not a lead
// status, and lacked "unqualified", which is. A row seeded with the old
// metadata gets the corrected list on the next boot, and the statuses an
// administrator chose stay exactly as they were.
func TestInitializeDefaultsCorrectsLeadConversionStatusMetadata(t *testing.T) {
	db := setupConfigurationDB(t)
	repo := NewConfigurationRepository(db)

	seeded := models.Configuration{
		Key:          "leads.conversion.allowed_statuses",
		Value:        `["contacted"]`,
		Type:         models.ConfigTypeArray,
		Category:     models.CategoryLeads,
		DefaultValue: `["qualified"]`,
		IsSystem:     true,
		ValidValues:  `["new", "contacted", "qualified", "converted", "lost"]`,
	}
	require.NoError(t, repo.Create(&seeded))

	require.NoError(t, repo.InitializeDefaults())

	upgraded, err := repo.GetByKey("leads.conversion.allowed_statuses")
	require.NoError(t, err)
	assert.Equal(t, `["contacted"]`, upgraded.Value, "the administrator's statuses are kept")
	assert.NotContains(t, upgraded.ValidValues, `"lost"`)
	assert.Contains(t, upgraded.ValidValues, `"unqualified"`)
	assert.True(t, upgraded.IsValidValue([]interface{}{"unqualified"}))
	assert.False(t, upgraded.IsValidValue([]interface{}{"lost"}))
}

// Every status in the shipped metadata is a real lead status, and every
// status a lead can be converted from is offered.
func TestLeadConversionStatusMetadataMatchesTheLeadEnum(t *testing.T) {
	var entry *models.Configuration
	for _, config := range models.DefaultConfigurations() {
		if config.Key == "leads.conversion.allowed_statuses" {
			c := config
			entry = &c
		}
	}
	require.NotNil(t, entry)
	var listed []string
	require.NoError(t, json.Unmarshal([]byte(entry.ValidValues), &listed))
	assert.ElementsMatch(t, []string{
		string(models.LeadStatusNew), string(models.LeadStatusContacted),
		string(models.LeadStatusQualified), string(models.LeadStatusUnqualified),
	}, listed)
	var value, defaultValue []string
	require.NoError(t, json.Unmarshal([]byte(entry.Value), &value))
	require.NoError(t, json.Unmarshal([]byte(entry.DefaultValue), &defaultValue))
	assert.Subset(t, listed, value)
	assert.Subset(t, listed, defaultValue)
}

// An upgraded database still holds the retired entries the earlier seed wrote,
// live or soft-deleted. Booting must remove both, hard, and seed no
// replacement.
func TestInitializeDefaultsRemovesRetiredEntries(t *testing.T) {
	db := setupConfigurationDB(t)
	repo := NewConfigurationRepository(db)

	require.NotEmpty(t, models.RetiredConfigurationKeys)
	for i, key := range models.RetiredConfigurationKeys {
		row := models.Configuration{Key: key, Value: "true", Type: models.ConfigTypeBoolean, Category: models.CategoryGeneral}
		require.NoError(t, db.Create(&row).Error)
		if i%2 == 1 {
			require.NoError(t, db.Delete(&row).Error)
		}
	}

	require.NoError(t, repo.InitializeDefaults())

	var remaining int64
	require.NoError(t, db.Unscoped().Model(&models.Configuration{}).
		Where("config_key IN ?", models.RetiredConfigurationKeys).Count(&remaining).Error)
	assert.Zero(t, remaining, "retired entries must be deleted, soft-deleted ones included")

	for _, retired := range models.RetiredConfigurationKeys {
		for _, seeded := range models.DefaultConfigurations() {
			assert.NotEqual(t, retired, seeded.Key, "a retired key is still seeded")
		}
	}
}
