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
	db                  *gorm.DB
	router              *gin.Engine
	configService       service.ConfigurationService
	authService         service.AuthService
	userService         service.UserService
	adminToken          string
	regularUserToken    string
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
	configRepo := repository.NewConfigurationRepository(db)
	
	// Setup services
	suite.authService = service.NewAuthService(userRepo)
	suite.userService = service.NewUserService(userRepo)
	suite.configService = service.NewConfigurationService(configRepo)
	
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
	api := suite.router.Group("/api")
	api.Use(middleware.AuthRequired(suite.authService))
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
	req, _ := http.NewRequest("GET", "/api/configurations/ui", nil)
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
	req, _ = http.NewRequest("GET", "/api/configurations/ui", nil)
	req.Header.Set("Authorization", "Bearer "+suite.regularUserToken)
	
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestGetAllConfigurations_AdminOnly() {
	// Test with admin user - should work
	req, _ := http.NewRequest("GET", "/api/configurations", nil)
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
	req, _ = http.NewRequest("GET", "/api/configurations", nil)
	req.Header.Set("Authorization", "Bearer "+suite.regularUserToken)
	
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestGetConfigurationByCategory() {
	// Test getting UI configurations
	req, _ := http.NewRequest("GET", "/api/configurations/category/ui", nil)
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
	req, _ := http.NewRequest("GET", "/api/configurations/general.company_name", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)
	
	config := response["data"].(map[string]interface{})
	assert.Equal(suite.T(), "general.company_name", config["key"])
	assert.Equal(suite.T(), "GoCRM", config["value"])
}

func (suite *ConfigurationIntegrationTestSuite) TestSetConfiguration() {
	// Test setting a configuration value
	requestBody := map[string]interface{}{
		"value": "Test Company",
	}
	
	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/configurations/general.company_name", bytes.NewBuffer(bodyBytes))
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
	req, _ := http.NewRequest("PUT", "/api/configurations/security.session_timeout_hours", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	
	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)
	
	assert.Contains(suite.T(), response["error"].(string), "Invalid value")
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
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/api/configurations/%s", readOnlyKey), bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.NoError(err)
	
	assert.Contains(suite.T(), response["error"].(string), "read-only")
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
	req, _ := http.NewRequest("POST", "/api/configurations/general.company_name/reset", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	// Verify reset to default
	value, err = suite.configService.GetString("general.company_name")
	suite.NoError(err)
	assert.Equal(suite.T(), "GoCRM", value) // Default value
}

func (suite *ConfigurationIntegrationTestSuite) TestBooleanConfiguration() {
	// Test setting boolean configuration
	requestBody := map[string]interface{}{
		"value": true,
	}
	
	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/configurations/leads.conversion.require_notes", bytes.NewBuffer(bodyBytes))
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
	// Test setting array configuration
	requestBody := map[string]interface{}{
		"value": []string{"qualified", "contacted", "hot"},
	}
	
	bodyBytes, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("PUT", "/api/configurations/leads.conversion.allowed_statuses", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	// Verify using service
	statuses, err := suite.configService.GetLeadConversionStatuses()
	suite.NoError(err)
	expected := []string{"qualified", "contacted", "hot"}
	assert.Equal(suite.T(), expected, statuses)
}

func (suite *ConfigurationIntegrationTestSuite) TestServiceSpecificMethods() {
	// Test specific service methods
	
	// Test GetLeadConversionStatuses
	statuses, err := suite.configService.GetLeadConversionStatuses()
	suite.NoError(err)
	assert.Contains(suite.T(), statuses, "qualified")
	
	// Test IsLeadConversionRequireNotes
	requireNotes, err := suite.configService.IsLeadConversionRequireNotes()
	suite.NoError(err)
	assert.False(suite.T(), requireNotes) // Default is false
	
	// Test IsLeadConversionAutoAssignOwner
	autoAssign, err := suite.configService.IsLeadConversionAutoAssignOwner()
	suite.NoError(err)
	assert.True(suite.T(), autoAssign) // Default is true
}

func (suite *ConfigurationIntegrationTestSuite) TestConfigurationNotFound() {
	// Test getting non-existent configuration
	req, _ := http.NewRequest("GET", "/api/configurations/non.existent.key", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *ConfigurationIntegrationTestSuite) TestUnauthorizedAccess() {
	// Test without token
	req, _ := http.NewRequest("GET", "/api/configurations", nil)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
}

func TestConfigurationIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationIntegrationTestSuite))
}