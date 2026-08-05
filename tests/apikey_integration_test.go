package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, jwtConfig, "test-api-key-secret")
	suite.userService = service.NewUserService(userRepo)
	suite.apiKeyService = service.NewAPIKeyService(apiKeyRepo, "test-api-key-secret")
	
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
		protected.GET("/api-keys/:id", apiKeyHandler.Get)
		protected.PUT("/api-keys/:id", apiKeyHandler.Update)
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
				var apiResp utils.APIResponse
				err := json.Unmarshal(body, &apiResp)
				assert.NoError(t, err)
				assert.True(t, apiResp.Success)
				data, ok := apiResp.Data.(map[string]interface{})
				assert.True(t, ok)
				key, _ := data["key"].(string)
				assert.NotEmpty(t, key)
				assert.Contains(t, key, "gcrm_")
				apiKey, ok := data["api_key"].(map[string]interface{})
				assert.True(t, ok)
				assert.Equal(t, "Production API Key", apiKey["name"])
				assert.Equal(t, true, apiKey["is_active"])
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
	key1, _, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key 1", nil)
	suite.NoError(err)
	suite.NotEmpty(key1)
	
	_, apiKey2, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key 2", nil)
	suite.NoError(err)
	
	// Revoke one key
	err = suite.apiKeyService.Revoke(apiKey2.ID, suite.testUser.ID)
	suite.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+suite.authToken)
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var apiResp utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &apiResp)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), apiResp.Success)

	apiKeysRaw, ok := apiResp.Data.([]interface{})
	if !ok {
		suite.T().Fatalf("expected data to be an array, got %T", apiResp.Data)
	}
	assert.Len(suite.T(), apiKeysRaw, 2)

	// Check that the keys are returned (one active, one inactive)
	activeCount := 0
	for _, keyRaw := range apiKeysRaw {
		key, ok := keyRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if isActive, _ := key["is_active"].(bool); isActive {
			activeCount++
		}
	}
	assert.Equal(suite.T(), 1, activeCount)
}

func (suite *APIKeyIntegrationTestSuite) TestRevokeAPIKey() {
	// Create an API key
	_, apiKey, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Key to Revoke", nil)
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
	
	_, otherKey, err := suite.apiKeyService.Generate(otherUser.ID, "Other User Key", nil)
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
	key, _, err := suite.apiKeyService.Generate(suite.testUser.ID, "Test Auth Key", nil)
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

func (suite *APIKeyIntegrationTestSuite) TestCreateAPIKeyWithExpiry() {
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	body, _ := json.Marshal(map[string]interface{}{
		"name":       "Expiring Key",
		"expires_at": expires.Format(time.RFC3339),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.authToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	var apiResp utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &apiResp))
	data := apiResp.Data.(map[string]interface{})
	apiKeyJSON := data["api_key"].(map[string]interface{})
	assert.NotEmpty(suite.T(), apiKeyJSON["expires_at"])

	// The expiry must survive the round trip to the database, not merely be
	// echoed back from the request.
	var stored models.APIKey
	suite.NoError(suite.db.First(&stored, uint(apiKeyJSON["id"].(float64))).Error)
	suite.Require().NotNil(stored.ExpiresAt)
	assert.WithinDuration(suite.T(), expires, stored.ExpiresAt.UTC(), time.Second)
}

func (suite *APIKeyIntegrationTestSuite) TestCreateAPIKeyWithPastExpiry() {
	body, _ := json.Marshal(map[string]interface{}{
		"name":       "Stale Key",
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.authToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var count int64
	suite.db.Model(&models.APIKey{}).Where("name = ?", "Stale Key").Count(&count)
	assert.Equal(suite.T(), int64(0), count, "a rejected request must not have created a key")
}

func (suite *APIKeyIntegrationTestSuite) TestGetAPIKey() {
	_, apiKey, err := suite.apiKeyService.Generate(suite.testUser.ID, "Fetchable Key", nil)
	suite.NoError(err)

	stranger := &models.User{
		Email:     "get-stranger@example.com",
		FirstName: "Get",
		LastName:  "Stranger",
		Role:      models.RoleCustomer,
	}
	suite.NoError(suite.userService.Register(stranger, "password123"))
	_, strangerKey, err := suite.apiKeyService.Generate(stranger.ID, "Stranger Key", nil)
	suite.NoError(err)

	tests := []struct {
		name           string
		apiKeyID       string
		authHeader     string
		expectedStatus int
	}{
		{"own key", fmt.Sprintf("%d", apiKey.ID), "Bearer " + suite.authToken, http.StatusOK},
		{"another user's key", fmt.Sprintf("%d", strangerKey.ID), "Bearer " + suite.authToken, http.StatusForbidden},
		{"missing key", "99999", "Bearer " + suite.authToken, http.StatusNotFound},
		{"invalid key ID", "invalid", "Bearer " + suite.authToken, http.StatusBadRequest},
		{"no authentication", fmt.Sprintf("%d", apiKey.ID), "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys/"+tt.apiKeyID, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)

			assert.Equal(suite.T(), tt.expectedStatus, w.Code)

			if tt.expectedStatus == http.StatusOK {
				var apiResp utils.APIResponse
				suite.NoError(json.Unmarshal(w.Body.Bytes(), &apiResp))
				data := apiResp.Data.(map[string]interface{})
				assert.Equal(suite.T(), "Fetchable Key", data["name"])
				// The hash is json:"-"; assert it explicitly so a future model
				// change cannot start leaking it silently.
				_, hasHash := data["key_hash"]
				assert.False(suite.T(), hasHash)
			}
		})
	}
}

func (suite *APIKeyIntegrationTestSuite) TestUpdateAPIKey() {
	_, apiKey, err := suite.apiKeyService.Generate(suite.testUser.ID, "Updatable Key", nil)
	suite.NoError(err)

	stranger := &models.User{
		Email:     "update-stranger@example.com",
		FirstName: "Update",
		LastName:  "Stranger",
		Role:      models.RoleCustomer,
	}
	suite.NoError(suite.userService.Register(stranger, "password123"))
	_, strangerKey, err := suite.apiKeyService.Generate(stranger.ID, "Stranger Key 2", nil)
	suite.NoError(err)

	suite.Run("rename", func() {
		body, _ := json.Marshal(map[string]interface{}{"name": "Renamed Key"})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/api-keys/%d", apiKey.ID), bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+suite.authToken)

		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)

		assert.Equal(suite.T(), http.StatusOK, w.Code)

		var stored models.APIKey
		suite.NoError(suite.db.First(&stored, apiKey.ID).Error)
		assert.Equal(suite.T(), "Renamed Key", stored.Name)
		assert.True(suite.T(), stored.IsActive, "renaming must not touch the active flag")
	})

	suite.Run("deactivate", func() {
		body, _ := json.Marshal(map[string]interface{}{"is_active": false})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/api-keys/%d", apiKey.ID), bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+suite.authToken)

		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)

		assert.Equal(suite.T(), http.StatusOK, w.Code)

		var stored models.APIKey
		suite.NoError(suite.db.First(&stored, apiKey.ID).Error)
		assert.False(suite.T(), stored.IsActive)
		assert.Equal(suite.T(), "Renamed Key", stored.Name, "deactivating must not touch the name")
	})

	suite.Run("reactivate", func() {
		body, _ := json.Marshal(map[string]interface{}{"is_active": true})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/api-keys/%d", apiKey.ID), bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+suite.authToken)

		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)

		assert.Equal(suite.T(), http.StatusOK, w.Code)

		var stored models.APIKey
		suite.NoError(suite.db.First(&stored, apiKey.ID).Error)
		assert.True(suite.T(), stored.IsActive)
	})

	rejections := []struct {
		name           string
		apiKeyID       string
		payload        string
		authHeader     string
		expectedStatus int
	}{
		{"another user's key", fmt.Sprintf("%d", strangerKey.ID), `{"name":"Hijacked Key"}`, "Bearer " + suite.authToken, http.StatusForbidden},
		{"missing key", "99999", `{"name":"Ghost Key"}`, "Bearer " + suite.authToken, http.StatusNotFound},
		{"invalid name", fmt.Sprintf("%d", apiKey.ID), `{"name":"ab"}`, "Bearer " + suite.authToken, http.StatusBadRequest},
		{"empty body", fmt.Sprintf("%d", apiKey.ID), `{}`, "Bearer " + suite.authToken, http.StatusBadRequest},
		{"invalid key ID", "invalid", `{"name":"Renamed Key"}`, "Bearer " + suite.authToken, http.StatusBadRequest},
		{"no authentication", fmt.Sprintf("%d", apiKey.ID), `{"name":"Renamed Key"}`, "", http.StatusUnauthorized},
	}

	for _, tt := range rejections {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/api-keys/"+tt.apiKeyID, bytes.NewBufferString(tt.payload))
			req.Header.Set("Content-Type", "application/json")
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)

			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
		})
	}

	suite.Run("stranger's key is untouched by the forbidden attempt", func() {
		var stored models.APIKey
		suite.NoError(suite.db.First(&stored, strangerKey.ID).Error)
		assert.Equal(suite.T(), "Stranger Key 2", stored.Name)
	})
}

// Expiry has to bite at authentication time, not merely be recorded. A key past
// its expires_at must fail even though it is still flagged active, and a
// reactivated-but-expired key must stay dead.
func (suite *APIKeyIntegrationTestSuite) TestExpiredAPIKeyIsRejectedAtAuth() {
	past := time.Now().Add(-time.Hour)
	expiredKey, expiredModel, err := suite.apiKeyService.Generate(suite.testUser.ID, "Expired Key", &past)
	suite.NoError(err)
	suite.Require().NotNil(expiredModel.ExpiresAt)

	future := time.Now().Add(time.Hour)
	liveKey, _, err := suite.apiKeyService.Generate(suite.testUser.ID, "Live Key", &future)
	suite.NoError(err)

	// Service level: the expired key is refused outright.
	user, err := suite.authService.ValidateAPIKey(expiredKey)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), user)
	assert.Contains(suite.T(), err.Error(), "expired")

	user, err = suite.authService.ValidateAPIKey(liveKey)
	assert.NoError(suite.T(), err)
	suite.Require().NotNil(user)
	assert.Equal(suite.T(), suite.testUser.ID, user.ID)

	// HTTP level: the middleware rejects the expired key and accepts the live one.
	for _, tt := range []struct {
		name           string
		key            string
		expectedStatus int
	}{
		{"expired key", expiredKey, http.StatusUnauthorized},
		{"unexpired key", liveKey, http.StatusOK},
	} {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test-api-key", nil)
			req.Header.Set("Authorization", "ApiKey "+tt.key)

			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)

			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
		})
	}

	// A deactivated key is refused too, and flipping is_active back on cannot
	// resurrect an expired one.
	suite.Run("deactivated key is rejected", func() {
		suite.NoError(suite.apiKeyService.Revoke(expiredModel.ID, suite.testUser.ID))
		_, err := suite.authService.ValidateAPIKey(expiredKey)
		assert.Error(suite.T(), err)

		active := true
		_, err = suite.apiKeyService.Update(expiredModel.ID, suite.testUser.ID, nil, &active)
		suite.NoError(err)

		_, err = suite.authService.ValidateAPIKey(expiredKey)
		assert.Error(suite.T(), err, "reactivation must not undo expiry")
		assert.Contains(suite.T(), err.Error(), "expired")
	})
}

func TestAPIKeyIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(APIKeyIntegrationTestSuite))
}