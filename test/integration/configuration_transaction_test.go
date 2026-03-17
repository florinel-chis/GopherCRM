package integration

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConfigurationBulkTransactions(t *testing.T) {
	// Setup test database and dependencies
	db := setupTestDatabase(t)
	txManager := utils.NewTransactionManager(db)
	
	configRepo := repository.NewConfigurationRepository(db)
	configService := service.NewConfigurationService(configRepo, txManager)

	// Setup test configurations
	setupTestConfigurations(t, db)

	t.Run("successful bulk update", func(t *testing.T) {
		updates := map[string]interface{}{
			"app.name":        "Updated CRM",
			"app.version":     "2.0.0",
			"leads.per_page":  25,
			"email.enabled":   false,
		}

		err := configService.BulkUpdate(updates)
		require.NoError(t, err)

		// Verify all configurations were updated
		for key, expectedValue := range updates {
			actualValue, err := configService.Get(key)
			require.NoError(t, err)
			assert.Equal(t, expectedValue, actualValue, "Configuration %s was not updated correctly", key)
		}
	})

	t.Run("rollback on invalid configuration", func(t *testing.T) {
		// Get current values before update attempt
		originalName, err := configService.Get("app.name")
		require.NoError(t, err)
		originalVersion, err := configService.Get("app.version")
		require.NoError(t, err)

		// Attempt bulk update with one invalid configuration
		updates := map[string]interface{}{
			"app.name":       "New Name",
			"app.version":    "3.0.0",
			"invalid.key":    "some value", // This should cause the transaction to fail
		}

		err = configService.BulkUpdate(updates)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")

		// Verify no configurations were updated (transaction rolled back)
		currentName, err := configService.Get("app.name")
		require.NoError(t, err)
		assert.Equal(t, originalName, currentName)

		currentVersion, err := configService.Get("app.version")
		require.NoError(t, err)
		assert.Equal(t, originalVersion, currentVersion)
	})

	t.Run("rollback on read-only configuration", func(t *testing.T) {
		// Setup a read-only configuration
		readOnlyConfig := &models.Configuration{
			Key:         "system.readonly",
			Value:       "readonly_value",
			DefaultValue: "readonly_value",
			Type:        models.ConfigTypeString,
			Category:    models.ConfigCategorySystem,
			IsSystem:    true,
			IsReadOnly:  true,
		}
		require.NoError(t, db.Create(readOnlyConfig).Error)

		// Get current values before update attempt
		originalName, err := configService.Get("app.name")
		require.NoError(t, err)

		// Attempt bulk update including read-only configuration
		updates := map[string]interface{}{
			"app.name":       "Another Name",
			"system.readonly": "new_value", // This should cause the transaction to fail
		}

		err = configService.BulkUpdate(updates)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "read-only")

		// Verify no configurations were updated (transaction rolled back)
		currentName, err := configService.Get("app.name")
		require.NoError(t, err)
		assert.Equal(t, originalName, currentName)
	})

	t.Run("successful bulk reset", func(t *testing.T) {
		// Update some configurations first
		updates := map[string]interface{}{
			"app.name":       "Modified Name",
			"app.version":    "Modified Version",
			"leads.per_page": 99,
		}
		require.NoError(t, configService.BulkUpdate(updates))

		// Reset multiple configurations
		keys := []string{"app.name", "app.version", "leads.per_page"}
		err := configService.BulkReset(keys)
		require.NoError(t, err)

		// Verify all configurations were reset to defaults
		for _, key := range keys {
			config, err := configService.GetByKey(key)
			require.NoError(t, err)
			assert.Equal(t, config.DefaultValue, config.Value, "Configuration %s was not reset to default", key)
		}
	})

	t.Run("rollback on reset read-only configuration", func(t *testing.T) {
		// Setup a read-only configuration
		readOnlyConfig := &models.Configuration{
			Key:         "system.readonly2",
			Value:       "current_value",
			DefaultValue: "default_value",
			Type:        models.ConfigTypeString,
			Category:    models.ConfigCategorySystem,
			IsSystem:    true,
			IsReadOnly:  true,
		}
		require.NoError(t, db.Create(readOnlyConfig).Error)

		// Modify a regular configuration
		require.NoError(t, configService.Set("app.name", "Before Reset"))

		// Attempt to reset both regular and read-only configurations
		keys := []string{"app.name", "system.readonly2"}
		err := configService.BulkReset(keys)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "read-only")

		// Verify regular configuration was not reset (transaction rolled back)
		currentName, err := configService.Get("app.name")
		require.NoError(t, err)
		assert.Equal(t, "Before Reset", currentName)
	})

	t.Run("successful category update", func(t *testing.T) {
		// Update all configurations in a category
		updates := map[string]interface{}{
			"leads.per_page":           50,
			"leads.conversion_timeout": 86400,
		}

		err := configService.UpdateCategory(models.ConfigCategoryLeads, updates)
		require.NoError(t, err)

		// Verify configurations in the category were updated
		perPage, err := configService.Get("leads.per_page")
		require.NoError(t, err)
		assert.Equal(t, 50, perPage)

		timeout, err := configService.Get("leads.conversion_timeout")
		require.NoError(t, err)
		assert.Equal(t, 86400, timeout)
	})

	t.Run("category update with non-existent keys", func(t *testing.T) {
		// Update category with keys that don't exist in that category
		updates := map[string]interface{}{
			"leads.per_page":  30, // This exists in leads category
			"app.name":        "New Name", // This exists but in app category
			"nonexistent.key": "value", // This doesn't exist at all
		}

		err := configService.UpdateCategory(models.ConfigCategoryLeads, updates)
		require.NoError(t, err) // Should succeed, ignoring non-category keys

		// Verify only the category-matching key was updated
		perPage, err := configService.Get("leads.per_page")
		require.NoError(t, err)
		assert.Equal(t, 30, perPage)

		// Verify app.name was not updated (different category)
		appName, err := configService.Get("app.name")
		require.NoError(t, err)
		assert.NotEqual(t, "New Name", appName)
	})

	t.Run("concurrent bulk updates", func(t *testing.T) {
		results := make(chan error, 2)

		// Attempt concurrent bulk updates
		go func() {
			updates := map[string]interface{}{
				"app.name":    "Concurrent Update 1",
				"app.version": "1.1.0",
			}
			results <- configService.BulkUpdate(updates)
		}()

		go func() {
			updates := map[string]interface{}{
				"app.name":       "Concurrent Update 2",
				"leads.per_page": 15,
			}
			results <- configService.BulkUpdate(updates)
		}()

		// Collect results
		var errors []error
		for i := 0; i < 2; i++ {
			err := <-results
			errors = append(errors, err)
		}

		// Both should succeed (different configurations or proper serialization)
		for i, err := range errors {
			assert.NoError(t, err, "Concurrent update %d should succeed", i+1)
		}

		// Verify final state is consistent
		appName, err := configService.Get("app.name")
		require.NoError(t, err)
		assert.True(t, appName == "Concurrent Update 1" || appName == "Concurrent Update 2")
	})
}

// setupTestConfigurations creates test configuration data
func setupTestConfigurations(t *testing.T, db *gorm.DB) {
	configs := []models.Configuration{
		{
			Key:          "app.name",
			Value:        "GopherCRM",
			DefaultValue: "GopherCRM",
			Type:         models.ConfigTypeString,
			Category:     models.ConfigCategoryApplication,
			Description:  "Application name",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "app.version",
			Value:        "1.0.0",
			DefaultValue: "1.0.0",
			Type:         models.ConfigTypeString,
			Category:     models.ConfigCategoryApplication,
			Description:  "Application version",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "leads.per_page",
			Value:        "20",
			DefaultValue: "20",
			Type:         models.ConfigTypeInteger,
			Category:     models.ConfigCategoryLeads,
			Description:  "Number of leads per page",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "leads.conversion_timeout",
			Value:        "3600",
			DefaultValue: "3600",
			Type:         models.ConfigTypeInteger,
			Category:     models.ConfigCategoryLeads,
			Description:  "Lead conversion timeout in seconds",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "email.enabled",
			Value:        "true",
			DefaultValue: "true",
			Type:         models.ConfigTypeBoolean,
			Category:     models.ConfigCategoryEmail,
			Description:  "Enable email functionality",
			IsSystem:     false,
			IsReadOnly:   false,
		},
	}

	for _, config := range configs {
		require.NoError(t, db.Create(&config).Error)
	}
}