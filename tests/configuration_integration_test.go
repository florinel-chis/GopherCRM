package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type ConfigurationIntegrationTestSuite struct {
	suite.Suite
	db               *gorm.DB
	router           *gin.Engine
	configService    service.ConfigurationService
	authService      service.AuthService
	userService      service.UserService
	adminToken       string
	regularUserToken string
}

func (suite *ConfigurationIntegrationTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	err := utils.InitLogger(logConfig)
	suite.NoError(err)

	// Setup test database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.NoError(err)

	// Migrate the schema
	err = db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Configuration{})
	suite.NoError(err)

	suite.db = db

	// Setup repositories
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	configRepo := repository.NewConfigurationRepository(db)

	// Setup services
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, jwtConfig)
	suite.userService = service.NewUserService(userRepo)
	// The master secret is obviously fake: nothing in these tests stores a
	// real credential.
	secretBox := utils.NewSecretBox("configuration-integration-test-secret", "configuration-secret")
	suite.configService = service.NewConfigurationService(configRepo, secretBox)

	// Initialize default configurations
	err = suite.configService.InitializeDefaults()
	suite.NoError(err)

	// Setup handlers
	configHandler := handler.NewConfigurationHandler(suite.configService)
	authHandler := handler.NewAuthHandler(suite.authService, suite.userService)

	// Setup router
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.Logger())
	suite.router.Use(middleware.ErrorHandler())

	// Auth routes
	auth := suite.router.Group("/auth")
	{
		auth.POST("/login", authHandler.Login)
		auth.POST("/register", authHandler.Register)
	}

	// Protected routes
	api := suite.router.Group("/api/v1")
	api.Use(middleware.Auth(suite.authService))
	{
		handler.SetupConfigurationRoutes(api, configHandler)
	}

	// Create test users
	suite.createTestUsers()
}

func (suite *ConfigurationIntegrationTestSuite) createTestUsers() {
	// Create admin user
	adminUser := &models.User{
		FirstName: "Admin",
		LastName:  "User",
		Email:     "admin@test.com",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}

	err := suite.userService.Register(adminUser, "password123")
	suite.NoError(err)

	// Create regular user
	regularUser := &models.User{
		FirstName: "Regular",
		LastName:  "User",
		Email:     "user@test.com",
		Role:      models.RoleSales,
		IsActive:  true,
	}

	err = suite.userService.Register(regularUser, "password123")
	suite.NoError(err)

	// Login to get tokens
	suite.adminToken, err = suite.authService.Login("admin@test.com", "password123")
	suite.NoError(err)

	suite.regularUserToken, err = suite.authService.Login("user@test.com", "password123")
	suite.NoError(err)
}

func (suite *ConfigurationIntegrationTestSuite) TearDownSuite() {
	sqlDB, _ := suite.db.DB()
	sqlDB.Close()
}

func (suite *ConfigurationIntegrationTestSuite) TestGetUIConfigurations() {
	// Test with admin user
	req, _ := http.NewRequest("GET", "/api/v1/configurations/ui", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)

	configurations, ok := response["data"].(map[string]interface{})["configurations"].([]interface{})
	suite.True(ok)
	suite.Greater(len(configurations), 0)

	// Test with regular user (should also work)
	req, _ = http.NewRequest("GET", "/api/v1/configurations/ui", nil)
	req.Header.Set("Authorization", "Bearer "+suite.regularUserToken)

	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestGetAllConfigurations_AdminOnly() {
	// Test with admin user - should work
	req, _ := http.NewRequest("GET", "/api/v1/configurations", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)

	configurations, ok := response["data"].(map[string]interface{})["configurations"].([]interface{})
	suite.True(ok)
	suite.Greater(len(configurations), 0)

	// Test with regular user - should fail
	req, _ = http.NewRequest("GET", "/api/v1/configurations", nil)
	req.Header.Set("Authorization", "Bearer "+suite.regularUserToken)

	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestGetConfigurationByCategory() {
	// Test getting UI configurations
	req, _ := http.NewRequest("GET", "/api/v1/configurations/category/ui", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)

	configurations, ok := response["data"].(map[string]interface{})["configurations"].([]interface{})
	suite.True(ok)

	// Verify all configurations are UI category
	for _, configInterface := range configurations {
		config := configInterface.(map[string]interface{})
		assert.Equal(suite.T(), "ui", config["category"])
	}
}

func (suite *ConfigurationIntegrationTestSuite) TestGetConfigurationByKey() {
	// Test getting specific configuration
	req, _ := http.NewRequest("GET", "/api/v1/configurations/general.company_name", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)

	config := response["data"].(map[string]interface{})
	assert.Equal(suite.T(), "general.company_name", config["key"])
	assert.Equal(suite.T(), "GopherCRM", config["value"])
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration() {
	// Test setting a configuration value
	requestBody := map[string]interface{}{
		"value": "Test Company",
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/v1/configurations/general.company_name", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)

	config := response["data"].(map[string]interface{})
	assert.Equal(suite.T(), "Test Company", config["value"])

	// Verify the change persisted
	value, err := suite.configService.GetString("general.company_name")
	suite.NoError(err)
	assert.Equal(suite.T(), "Test Company", value)
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_InvalidValue() {
	// Test setting invalid value for session timeout (should be from valid values)
	requestBody := map[string]interface{}{
		"value": 999, // Invalid value
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/v1/configurations/security.session_timeout_hours", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)
	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
	assert.Contains(suite.T(), response.Error.Message, "Invalid value")
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_ReadOnly() {
	// First, let's find a read-only configuration or create one for testing
	configs, err := suite.configService.GetAll()
	suite.NoError(err)

	var readOnlyKey string
	for _, config := range configs {
		if config.IsReadOnly {
			readOnlyKey = config.Key
			break
		}
	}

	if readOnlyKey == "" {
		// Create a read-only config for testing
		readOnlyConfig := &models.Configuration{
			Key:          "test.readonly.setting",
			Value:        "readonly_value",
			Type:         models.ConfigTypeString,
			Category:     models.CategoryGeneral,
			Description:  "Test read-only configuration",
			DefaultValue: "readonly_value",
			IsSystem:     false,
			IsReadOnly:   true,
		}

		err = suite.db.Create(readOnlyConfig).Error
		suite.NoError(err)
		readOnlyKey = readOnlyConfig.Key
	}

	// Try to modify read-only configuration
	requestBody := map[string]interface{}{
		"value": "new_value",
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/api/v1/configurations/%s", readOnlyKey), bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response2 utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &response2)
	suite.NoError(err)
	assert.False(suite.T(), response2.Success)
	assert.NotNil(suite.T(), response2.Error)
	assert.Contains(suite.T(), response2.Error.Message, "read-only")
}

func (suite *ConfigurationIntegrationTestSuite) TestResetConfiguration() {
	// First set a custom value
	err := suite.configService.Set("general.company_name", "Custom Company")
	suite.NoError(err)

	// Verify the custom value
	value, err := suite.configService.GetString("general.company_name")
	suite.NoError(err)
	assert.Equal(suite.T(), "Custom Company", value)

	// Reset to default
	req, _ := http.NewRequest("POST", "/api/v1/configurations/general.company_name/reset", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Verify reset to default
	value, err = suite.configService.GetString("general.company_name")
	suite.NoError(err)
	assert.Equal(suite.T(), "GopherCRM", value) // Default value
}

func (suite *ConfigurationIntegrationTestSuite) TestBooleanConfiguration() {
	// Test setting boolean configuration
	requestBody := map[string]interface{}{
		"value": true,
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/v1/configurations/leads.conversion.require_notes", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Verify using service
	value, err := suite.configService.GetBool("leads.conversion.require_notes")
	suite.NoError(err)
	assert.True(suite.T(), value)
}

func (suite *ConfigurationIntegrationTestSuite) TestArrayConfiguration() {
	// Test setting array configuration (only use valid statuses from ValidValues)
	requestBody := map[string]interface{}{
		"value": []string{"qualified", "contacted", "new"},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/v1/configurations/leads.conversion.allowed_statuses", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Verify using service
	statuses, err := suite.configService.GetLeadConversionStatuses()
	suite.NoError(err)
	expected := []string{"qualified", "contacted", "new"}
	assert.Equal(suite.T(), expected, statuses)
}

func (suite *ConfigurationIntegrationTestSuite) TestServiceSpecificMethods() {
	// Test specific service methods

	// Test GetLeadConversionStatuses
	statuses, err := suite.configService.GetLeadConversionStatuses()
	suite.NoError(err)
	assert.Contains(suite.T(), statuses, "qualified")

	// Test IsLeadConversionRequireNotes
	// Note: TestBooleanConfiguration runs before this (alphabetical order)
	// and sets require_notes to true
	requireNotes, err := suite.configService.IsLeadConversionRequireNotes()
	suite.NoError(err)
	assert.True(suite.T(), requireNotes) // Was set to true by TestBooleanConfiguration

	// Test IsLeadConversionAutoAssignOwner
	autoAssign, err := suite.configService.IsLeadConversionAutoAssignOwner()
	suite.NoError(err)
	assert.True(suite.T(), autoAssign) // Default is true
}

// setConfiguration issues a raw PUT body so that malformed and falsy payloads
// can be exercised exactly as a client would send them.
func (suite *ConfigurationIntegrationTestSuite) setConfiguration(key, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("PUT", "/api/v1/configurations/"+key, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

// TestSetConfiguration_FalseBoolean is the headline falsy-value case: turning a
// boolean configuration off used to be rejected as a missing field.
func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_FalseBoolean() {
	err := suite.configService.Set("tickets.auto_assign_support", true)
	suite.NoError(err)

	w := suite.setConfiguration("tickets.auto_assign_support", `{"value": false}`)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	config := response["data"].(map[string]interface{})
	assert.Equal(suite.T(), "false", config["value"])

	value, err := suite.configService.GetBool("tickets.auto_assign_support")
	suite.NoError(err)
	assert.False(suite.T(), value)
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_ZeroInteger() {
	// A dedicated integer entry with no valid_values constraint, so that 0 is
	// rejected only if the binding rejects it.
	suite.NoError(suite.db.Where("config_key = ?", "test.integer.setting").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.integer.setting",
		Value:        "42",
		Type:         models.ConfigTypeInteger,
		Category:     models.CategoryGeneral,
		Description:  "Test integer configuration",
		DefaultValue: "42",
	}).Error)

	w := suite.setConfiguration("test.integer.setting", `{"value": 0}`)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]interface{}
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	config := response["data"].(map[string]interface{})
	assert.Equal(suite.T(), "0", config["value"])

	value, err := suite.configService.GetInt("test.integer.setting")
	suite.NoError(err)
	assert.Equal(suite.T(), 0, value)
}

// TestSetConfiguration_EmptyString documents what IsValidValue permits: an entry
// with no valid_values constraint accepts any string, the empty one included.
func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_EmptyString() {
	suite.NoError(suite.db.Where("config_key = ?", "test.string.setting").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.string.setting",
		Value:        "something",
		Type:         models.ConfigTypeString,
		Category:     models.CategoryGeneral,
		Description:  "Test string configuration",
		DefaultValue: "something",
	}).Error)

	w := suite.setConfiguration("test.string.setting", `{"value": ""}`)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	value, err := suite.configService.GetString("test.string.setting")
	suite.NoError(err)
	assert.Equal(suite.T(), "", value)
}

// TestSetConfiguration_TypeMismatch is the silent-coercion defect: a value of
// the wrong JSON type used to be coerced ("yes" on a boolean entry became
// "false", a number on a string entry became "") and answered 200. It must now
// be rejected as an invalid value, and the stored value must survive intact.
func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_TypeMismatch() {
	suite.NoError(suite.db.Where("config_key = ?", "test.mismatch.integer").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.mismatch.integer",
		Value:        "42",
		Type:         models.ConfigTypeInteger,
		Category:     models.CategoryGeneral,
		Description:  "Test integer configuration",
		DefaultValue: "42",
	}).Error)

	suite.NoError(suite.db.Where("config_key = ?", "test.mismatch.string").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.mismatch.string",
		Value:        "something",
		Type:         models.ConfigTypeString,
		Category:     models.CategoryGeneral,
		Description:  "Test string configuration",
		DefaultValue: "something",
	}).Error)

	suite.NoError(suite.configService.Set("tickets.auto_assign_support", true))

	cases := []struct {
		name     string
		key      string
		body     string
		unharmed string
	}{
		{name: "string on boolean entry", key: "tickets.auto_assign_support", body: `{"value": "yes"}`, unharmed: "true"},
		{name: "number on boolean entry", key: "tickets.auto_assign_support", body: `{"value": 1}`, unharmed: "true"},
		{name: "string on integer entry", key: "test.mismatch.integer", body: `{"value": "10"}`, unharmed: "42"},
		{name: "fractional number on integer entry", key: "test.mismatch.integer", body: `{"value": 3.5}`, unharmed: "42"},
		{name: "number on string entry", key: "test.mismatch.string", body: `{"value": 5}`, unharmed: "something"},
		{name: "boolean on string entry", key: "test.mismatch.string", body: `{"value": false}`, unharmed: "something"},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			w := suite.setConfiguration(tc.key, tc.body)
			assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

			var response utils.APIResponse
			suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
			assert.False(suite.T(), response.Success)
			assert.NotNil(suite.T(), response.Error)
			assert.Contains(suite.T(), response.Error.Message, "Invalid value")

			config, err := suite.configService.GetByKey(tc.key)
			suite.NoError(err)
			assert.Equal(suite.T(), tc.unharmed, config.Value, "a rejected value must not be written")
		})
	}
}

// TestSetConfiguration_CorrectTypesStillAccepted guards the falsy coverage
// against the strictness added alongside it: false, 0 and "" are of the right
// type and must still be stored.
func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_CorrectTypesStillAccepted() {
	suite.NoError(suite.db.Where("config_key = ?", "test.accepted.integer").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.accepted.integer",
		Value:        "42",
		Type:         models.ConfigTypeInteger,
		Category:     models.CategoryGeneral,
		Description:  "Test integer configuration",
		DefaultValue: "42",
	}).Error)

	suite.NoError(suite.db.Where("config_key = ?", "test.accepted.string").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.accepted.string",
		Value:        "something",
		Type:         models.ConfigTypeString,
		Category:     models.CategoryGeneral,
		Description:  "Test string configuration",
		DefaultValue: "something",
	}).Error)

	cases := []struct {
		name  string
		key   string
		body  string
		value string
	}{
		{name: "false on boolean entry", key: "tickets.auto_assign_support", body: `{"value": false}`, value: "false"},
		{name: "true on boolean entry", key: "tickets.auto_assign_support", body: `{"value": true}`, value: "true"},
		{name: "zero on integer entry", key: "test.accepted.integer", body: `{"value": 0}`, value: "0"},
		{name: "integral number on integer entry", key: "test.accepted.integer", body: `{"value": 7}`, value: "7"},
		{name: "empty string on string entry", key: "test.accepted.string", body: `{"value": ""}`, value: ""},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			w := suite.setConfiguration(tc.key, tc.body)
			assert.Equal(suite.T(), http.StatusOK, w.Code)

			var response map[string]interface{}
			suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
			config := response["data"].(map[string]interface{})
			assert.Equal(suite.T(), tc.value, config["value"])
		})
	}
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_AbsentValue() {
	w := suite.setConfiguration("general.company_name", `{}`)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_NullValue() {
	w := suite.setConfiguration("general.company_name", `{"value": null}`)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration_UnknownKey() {
	w := suite.setConfiguration("no.such.key", `{"value": "x"}`)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// TestResetConfiguration_UnknownKey is the reported defect: resetting a key that
// does not exist answered 500 instead of 404.
func (suite *ConfigurationIntegrationTestSuite) TestResetConfiguration_UnknownKey() {
	req, _ := http.NewRequest("POST", "/api/v1/configurations/no.such.key/reset", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	var response utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
}

func (suite *ConfigurationIntegrationTestSuite) TestResetConfiguration_ReadOnly() {
	suite.NoError(suite.db.Where("config_key = ?", "test.readonly.reset").Delete(&models.Configuration{}).Error)
	suite.NoError(suite.db.Create(&models.Configuration{
		Key:          "test.readonly.reset",
		Value:        "readonly_value",
		Type:         models.ConfigTypeString,
		Category:     models.CategoryGeneral,
		Description:  "Test read-only configuration",
		DefaultValue: "readonly_value",
		IsReadOnly:   true,
	}).Error)

	req, _ := http.NewRequest("POST", "/api/v1/configurations/test.readonly.reset/reset", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.Contains(suite.T(), response.Error.Message, "read-only")
}

func (suite *ConfigurationIntegrationTestSuite) TestConfigurationNotFound() {
	// Test getting non-existent configuration
	req, _ := http.NewRequest("GET", "/api/v1/configurations/non.existent.key", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestUnauthorizedAccess() {
	// Test without token
	req, _ := http.NewRequest("GET", "/api/v1/configurations", nil)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
}

// TestSensitiveConfiguration_EndToEnd walks a provider key through the whole
// stack: stored through the API, encrypted in the row, masked on the way back
// out, readable only as a secret, and clearable.
func (suite *ConfigurationIntegrationTestSuite) TestSensitiveConfiguration_EndToEnd() {
	const key = "integration.aeo.gemini_api_key"
	// Obviously fake: no test in this repository carries a real credential.
	const plaintext = "not-a-real-gemini-key-0007"

	// Before anything is stored the entry reads as unset.
	req, _ := http.NewRequest("GET", "/api/v1/configurations/"+key, nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	suite.Require().Equal(http.StatusOK, w.Code)

	var before struct {
		Data map[string]interface{} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &before))
	assert.Equal(suite.T(), true, before.Data["is_sensitive"])
	assert.Equal(suite.T(), false, before.Data["is_set"])
	assert.Equal(suite.T(), "", before.Data["value"])

	// Store the key through the ordinary update endpoint.
	bodyBytes, _ := json.Marshal(map[string]interface{}{"value": plaintext})
	req, _ = http.NewRequest("PUT", "/api/v1/configurations/"+key, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Require().Equal(http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), plaintext, "the stored key must never be echoed back")

	var after struct {
		Data map[string]interface{} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &after))
	assert.Equal(suite.T(), true, after.Data["is_set"])
	assert.Equal(suite.T(), "", after.Data["value"])

	// The row itself holds ciphertext.
	var stored models.Configuration
	suite.Require().NoError(suite.db.Where("config_key = ?", key).First(&stored).Error)
	assert.True(suite.T(), utils.IsSealed(stored.Value), "stored value is not encrypted: %q", stored.Value)
	assert.NotContains(suite.T(), stored.Value, plaintext)

	// The plaintext comes back only through GetSecret; the typed getters refuse.
	secret, err := suite.configService.GetSecret(key)
	suite.NoError(err)
	assert.Equal(suite.T(), plaintext, secret)

	_, err = suite.configService.GetString(key)
	suite.Error(err)

	// Listing every configuration must not leak it either.
	req, _ = http.NewRequest("GET", "/api/v1/configurations", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	suite.Require().Equal(http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), plaintext)
	assert.NotContains(suite.T(), w.Body.String(), stored.Value)

	// Clearing writes an empty value and reports the entry as unset again.
	bodyBytes, _ = json.Marshal(map[string]interface{}{"value": ""})
	req, _ = http.NewRequest("PUT", "/api/v1/configurations/"+key, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	suite.Require().Equal(http.StatusOK, w.Code)

	var cleared struct {
		Data map[string]interface{} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &cleared))
	assert.Equal(suite.T(), false, cleared.Data["is_set"])

	secret, err = suite.configService.GetSecret(key)
	suite.NoError(err)
	assert.Equal(suite.T(), "", secret)
}

// The effective answer-engine configuration prefers a stored key and falls back
// to the environment value for everything that is not set.
func (suite *ConfigurationIntegrationTestSuite) TestEffectiveAEOConfig_UsesStoredKeys() {
	suite.Require().NoError(suite.configService.Set("integration.aeo.openai_api_key", "not-a-real-openai-key-0008"))

	effective := service.EffectiveAEOConfig(config.AEOConfig{
		OpenAIAPIKey: "env-openai-key",
		GeminiAPIKey: "env-gemini-key",
	}, suite.configService)

	assert.Equal(suite.T(), "not-a-real-openai-key-0008", effective.OpenAIAPIKey)
	assert.Equal(suite.T(), "env-gemini-key", effective.GeminiAPIKey)
}

func TestConfigurationIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationIntegrationTestSuite))
}
