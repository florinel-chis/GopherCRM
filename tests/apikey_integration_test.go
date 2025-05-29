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

type APIKeyIntegrationTestSuite struct {
	suite.Suite
	db            *gorm.DB
	router        *gin.Engine
	authService   service.AuthService
	userService   service.UserService
	apiKeyService service.APIKeyService
	testUser      *models.User
	authToken     string
}

func (suite *APIKeyIntegrationTestSuite) SetupSuite() {
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
	err = db.AutoMigrate(&models.User{}, &models.APIKey{})
	suite.NoError(err)
	
	suite.db = db
	
	// Setup repositories
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	
	// Setup services
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, jwtConfig)
	suite.userService = service.NewUserService(userRepo)
	suite.apiKeyService = service.NewAPIKeyService(apiKeyRepo)
	
	// Create test user
	suite.testUser = &models.User{
		Email:     "testuser@example.com",
		FirstName: "Test",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	err = suite.userService.Register(suite.testUser, "password123")
	suite.NoError(err)
	
	// Get auth token
	suite.authToken, err = suite.authService.GenerateJWT(suite.testUser)
	suite.NoError(err)
	
	// Setup router
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.ErrorHandler())
	
	// Setup handlers
	apiKeyHandler := handler.NewAPIKeyHandler(suite.apiKeyService)
	
	// Setup routes
	api := suite.router.Group("/api/v1")
	protected := api.Group("")
	protected.Use(middleware.Auth(suite.authService))
	{
		protected.POST("/api-keys", apiKeyHandler.Create)
		protected.GET("/api-keys", apiKeyHandler.List)
		protected.DELETE("/api-keys/:id", apiKeyHandler.Revoke)
		
		// Test endpoint that accepts API key auth
		protected.GET("/test-api-key", func(c *gin.Context) {
			user, _ := c.Get("user")
			c.JSON(http.StatusOK, gin.H{
				"message": "API key authentication successful",
				"user":    user,
			})
		})
	}
}

func (suite *APIKeyIntegrationTestSuite) TearDownSuite() {
	sqlDB, _ := suite.db.DB()
	sqlDB.Close()
}

func (suite *APIKeyIntegrationTestSuite) TestCreateAPIKey() {
	tests := []struct {
		name           string
		payload        map[string]interface{}
		authHeader     string
		expectedStatus int
		checkResponse  func(t *testing.T, body []byte)
	}{
		{
			name: "successful creation",
			payload: map[string]interface{}{
				"name": "Production API Key",
			},
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, body []byte) {
				var response handler.CreateAPIKeyResponse
				err := json.Unmarshal(body, &response)
				assert.NoError(t, err)
				assert.NotEmpty(t, response.Key)
				assert.Contains(t, response.Key, "gcrm_")
				assert.NotNil(t, response.APIKey)
				assert.Equal(t, "Production API Key", response.APIKey.Name)
				assert.True(t, response.APIKey.IsActive)
			},
		},
		{
			name: "missing name",
			payload: map[string]interface{}{},
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "short name",
			payload: map[string]interface{}{
				"name": "ab",
			},
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "no authentication",
			payload: map[string]interface{}{
				"name": "Test Key",
			},
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			body, _ := json.Marshal(tt.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
			
			if tt.checkResponse != nil {
				tt.checkResponse(suite.T(), w.Body.Bytes())
			}
		})
	}
}

func (suite *APIKeyIntegrationTestSuite) TestListAPIKeys() {
	// Clear any existing API keys
	suite.db.Where("user_id = ?", suite.testUser.ID).Delete(&models.APIKey{})
	
	// Create some API keys
	key1, _, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key 1")
	suite.NoError(err)
	suite.NotEmpty(key1)
	
	_, apiKey2, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key 2")
	suite.NoError(err)
	
	// Revoke one key
	err = suite.apiKeyService.Revoke(apiKey2.ID, suite.testUser.ID)
	suite.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+suite.authToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var response map[string][]models.APIKey
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	
	apiKeys := response["api_keys"]
	assert.Len(suite.T(), apiKeys, 2)
	
	// Check that the keys are returned (one active, one inactive)
	activeCount := 0
	for _, key := range apiKeys {
		if key.IsActive {
			activeCount++
		}
		assert.Empty(suite.T(), key.KeyHash) // Should not expose hash
	}
	assert.Equal(suite.T(), 1, activeCount)
}

func (suite *APIKeyIntegrationTestSuite) TestRevokeAPIKey() {
	// Create an API key
	_, apiKey, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key to Revoke")
	suite.NoError(err)
	
	// Create another user and their key
	otherUser := &models.User{
		Email:     "other@example.com",
		FirstName: "Other",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	err = suite.userService.Register(otherUser, "password123")
	suite.NoError(err)
	
	_, otherKey, err := suite.apiKeyService.Generate(otherUser.ID, "Other User Key")
	suite.NoError(err)

	tests := []struct {
		name           string
		apiKeyID       string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "successful revocation",
			apiKeyID:       fmt.Sprintf("%d", apiKey.ID),
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "revoke other user's key",
			apiKeyID:       fmt.Sprintf("%d", otherKey.ID),
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "invalid key ID",
			apiKeyID:       "invalid",
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "no authentication",
			apiKeyID:       fmt.Sprintf("%d", apiKey.ID),
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/api-keys/"+tt.apiKeyID, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
		})
	}
}

func (suite *APIKeyIntegrationTestSuite) TestAPIKeyAuthentication() {
	// Create an API key
	key, _, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Auth Key")
	suite.NoError(err)
	
	tests := []struct {
		name           string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "valid API key",
			authHeader:     "ApiKey " + key,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid API key",
			authHeader:     "ApiKey invalid-key",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "JWT still works",
			authHeader:     "Bearer " + suite.authToken,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test-api-key", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
		})
	}
}

func TestAPIKeyIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(APIKeyIntegrationTestSuite))
}