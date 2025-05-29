package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/handler"
	"github.com/florinel-chis/gocrm/internal/middleware"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository"
	"github.com/florinel-chis/gocrm/internal/service"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type ErrorHandlingTestSuite struct {
	suite.Suite
	db     *gorm.DB
	router *gin.Engine
	cfg    *config.Config
}

func (suite *ErrorHandlingTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)

	// Setup test database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.Require().NoError(err)
	suite.db = db
	models.DB = db

	// Run migrations
	err = models.MigrateDatabase()
	suite.Require().NoError(err)

	// Setup test configuration
	suite.cfg = &config.Config{
		JWT: config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		},
	}

	// Setup router
	suite.setupRouter()
}

func (suite *ErrorHandlingTestSuite) setupRouter() {
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	
	// Add middleware
	suite.router.Use(middleware.RequestID())
	suite.router.Use(middleware.Logger())
	suite.router.Use(middleware.Recovery())
	suite.router.Use(middleware.ErrorHandler())

	// Setup dependencies
	userRepo := repository.NewUserRepository(suite.db)
	apiKeyRepo := repository.NewAPIKeyRepository(suite.db)
	authService := service.NewAuthService(userRepo, apiKeyRepo, suite.cfg.JWT)
	userService := service.NewUserService(userRepo)
	authHandler := handler.NewAuthHandler(authService, userService)

	// Setup routes
	api := suite.router.Group("/api/v1")
	{
		public := api.Group("")
		{
			public.POST("/auth/register", authHandler.Register)
			public.POST("/auth/login", authHandler.Login)
		}

		protected := api.Group("")
		protected.Use(middleware.Auth(authService))
		{
			// Add a test endpoint that panics
			protected.GET("/panic", func(c *gin.Context) {
				panic("test panic")
			})
			
			// Add a test endpoint that returns 404
			protected.GET("/users/:id", func(c *gin.Context) {
				utils.RespondNotFound(c, "User not found")
			})
		}
	}
}

func (suite *ErrorHandlingTestSuite) TearDownSuite() {
	if suite.db != nil {
		sqlDB, _ := suite.db.DB()
		sqlDB.Close()
	}
}

func (suite *ErrorHandlingTestSuite) TestValidationError_400() {
	// Test missing required fields
	invalidPayloads := []struct {
		name    string
		payload map[string]interface{}
		errors  map[string]string
	}{
		{
			name:    "missing email",
			payload: map[string]interface{}{"password": "password123", "first_name": "John", "last_name": "Doe"},
			errors:  map[string]string{"Email": "Email is required"},
		},
		{
			name:    "invalid email",
			payload: map[string]interface{}{"email": "invalid-email", "password": "password123", "first_name": "John", "last_name": "Doe"},
			errors:  map[string]string{"Email": "Email must be a valid email address"},
		},
		{
			name:    "short password",
			payload: map[string]interface{}{"email": "test@example.com", "password": "short", "first_name": "John", "last_name": "Doe"},
			errors:  map[string]string{"Password": "Password must be at least 8 characters long"},
		},
		{
			name:    "missing first name",
			payload: map[string]interface{}{"email": "test@example.com", "password": "password123", "last_name": "Doe"},
			errors:  map[string]string{"FirstName": "FirstName is required"},
		},
	}

	for _, tc := range invalidPayloads {
		suite.Run(tc.name, func() {
			body, _ := json.Marshal(tc.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			suite.router.ServeHTTP(rec, req)

			assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

			var response utils.APIResponse
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			assert.NoError(suite.T(), err)

			assert.False(suite.T(), response.Success)
			assert.NotNil(suite.T(), response.Error)
			assert.Equal(suite.T(), "VALIDATION_ERROR", response.Error.Code)
			assert.NotNil(suite.T(), response.Error.Details)
			
			// Check that expected validation errors are present
			details, ok := response.Error.Details.(map[string]interface{})
			assert.True(suite.T(), ok)
			for field, expectedError := range tc.errors {
				assert.Equal(suite.T(), expectedError, details[field])
			}
		})
	}
}

func (suite *ErrorHandlingTestSuite) TestAuthError_401() {
	// Test invalid credentials
	payload := map[string]string{
		"email":    "nonexistent@example.com",
		"password": "wrongpassword",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
	assert.Equal(suite.T(), "UNAUTHORIZED", response.Error.Code)
	assert.Equal(suite.T(), "Invalid email or password", response.Error.Message)
}

func (suite *ErrorHandlingTestSuite) TestAuthError_403() {
	// Test accessing protected endpoint without token
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/123", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
	assert.Equal(suite.T(), "UNAUTHORIZED", response.Error.Code)
}

func (suite *ErrorHandlingTestSuite) TestNotFoundError_404() {
	// First create a valid user and get token
	user := &models.User{
		Email:     "testuser404@example.com",
		FirstName: "Test",
		LastName:  "User",
		Role:      models.RoleAdmin,
	}
	err := user.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(user)

	authService := service.NewAuthService(
		repository.NewUserRepository(suite.db),
		repository.NewAPIKeyRepository(suite.db),
		suite.cfg.JWT,
	)
	token, err := authService.GenerateJWT(user)
	suite.Require().NoError(err)

	// Test accessing non-existent resource
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/999", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)

	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
	assert.Equal(suite.T(), "NOT_FOUND", response.Error.Code)
	assert.Equal(suite.T(), "User not found", response.Error.Message)
}

func (suite *ErrorHandlingTestSuite) TestInternalError_500() {
	// First create a valid user and get token
	user := &models.User{
		Email:     "testuser500@example.com",
		FirstName: "Test",
		LastName:  "User",
		Role:      models.RoleAdmin,
	}
	err := user.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(user)

	authService := service.NewAuthService(
		repository.NewUserRepository(suite.db),
		repository.NewAPIKeyRepository(suite.db),
		suite.cfg.JWT,
	)
	token, err := authService.GenerateJWT(user)
	suite.Require().NoError(err)

	// Test endpoint that causes a panic
	req := httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
	assert.Equal(suite.T(), "An unexpected error occurred", response.Error.Message)
}

func (suite *ErrorHandlingTestSuite) TestConsistentErrorFormat() {
	// Test that all error responses follow the same format
	testCases := []struct {
		name           string
		setupRequest   func() *http.Request
		expectedStatus int
		expectedCode   string
	}{
		{
			name: "validation error",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer([]byte("{}")))
				req.Header.Set("Content-Type", "application/json")
				return req
			},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name: "unauthorized error",
			setupRequest: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/v1/users/123", nil)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "UNAUTHORIZED",
		},
	}

	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			req := tc.setupRequest()
			rec := httptest.NewRecorder()

			suite.router.ServeHTTP(rec, req)

			assert.Equal(suite.T(), tc.expectedStatus, rec.Code)

			var response utils.APIResponse
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			assert.NoError(suite.T(), err)

			// Verify consistent structure
			assert.False(suite.T(), response.Success)
			assert.NotNil(suite.T(), response.Error)
			assert.Equal(suite.T(), tc.expectedCode, response.Error.Code)
			assert.NotEmpty(suite.T(), response.Error.Message)
			assert.Nil(suite.T(), response.Data)
		})
	}
}

func TestErrorHandlingTestSuite(t *testing.T) {
	suite.Run(t, new(ErrorHandlingTestSuite))
}