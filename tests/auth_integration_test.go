package tests

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

type AuthIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	authService service.AuthService
	userService service.UserService
}

func (suite *AuthIntegrationTestSuite) SetupSuite() {
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
	
	// Setup router
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.ErrorHandler())
	
	// Setup handlers
	authHandler := handler.NewAuthHandler(suite.authService, suite.userService)
	
	// Setup routes
	api := suite.router.Group("/api/v1")
	{
		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)
		
		// Protected route for testing
		protected := api.Group("")
		protected.Use(middleware.Auth(suite.authService))
		{
			protected.GET("/protected", func(c *gin.Context) {
				user, _ := c.Get("user")
				c.JSON(http.StatusOK, gin.H{
					"message": "Protected route accessed",
					"user":    user,
				})
			})
		}
	}
}

func (suite *AuthIntegrationTestSuite) TearDownSuite() {
	// Clean up
	sqlDB, _ := suite.db.DB()
	sqlDB.Close()
}

func (suite *AuthIntegrationTestSuite) TestRegisterEndpoint() {
	tests := []struct {
		name           string
		payload        map[string]interface{}
		expectedStatus int
		expectedError  string
	}{
		{
			name: "successful registration",
			payload: map[string]interface{}{
				"email":      "newuser@example.com",
				"password":   "password123",
				"first_name": "New",
				"last_name":  "User",
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name: "duplicate email",
			payload: map[string]interface{}{
				"email":      "duplicate@example.com",
				"password":   "password123",
				"first_name": "Duplicate",
				"last_name":  "User",
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name: "duplicate email second attempt",
			payload: map[string]interface{}{
				"email":      "duplicate@example.com",
				"password":   "password123",
				"first_name": "Duplicate",
				"last_name":  "User",
			},
			expectedStatus: http.StatusConflict,
			expectedError:  "user with this email already exists",
		},
		{
			name: "missing required fields",
			payload: map[string]interface{}{
				"email": "missing@example.com",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "invalid email format",
			payload: map[string]interface{}{
				"email":      "invalid-email",
				"password":   "password123",
				"first_name": "Invalid",
				"last_name":  "Email",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "short password",
			payload: map[string]interface{}{
				"email":      "short@example.com",
				"password":   "short",
				"first_name": "Short",
				"last_name":  "Password",
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			body, _ := json.Marshal(tt.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
			
			if tt.expectedError != "" {
				var response utils.APIResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.False(suite.T(), response.Success)
				assert.Contains(suite.T(), response.Error.Message, tt.expectedError)
			}
			
			if tt.expectedStatus == http.StatusCreated {
				var response utils.APIResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.True(suite.T(), response.Success)
				
				authData := response.Data.(map[string]interface{})
				assert.NotEmpty(suite.T(), authData["token"])
				assert.NotNil(suite.T(), authData["user"])
			}
		})
	}
}

func (suite *AuthIntegrationTestSuite) TestLoginEndpoint() {
	// Create a test user
	user := &models.User{
		Email:     "login@example.com",
		FirstName: "Login",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	suite.NoError(err)

	tests := []struct {
		name           string
		payload        map[string]interface{}
		expectedStatus int
		expectedError  string
	}{
		{
			name: "successful login",
			payload: map[string]interface{}{
				"email":    "login@example.com",
				"password": "password123",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "wrong password",
			payload: map[string]interface{}{
				"email":    "login@example.com",
				"password": "wrongpassword",
			},
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "Invalid email or password",
		},
		{
			name: "non-existent user",
			payload: map[string]interface{}{
				"email":    "nonexistent@example.com",
				"password": "password123",
			},
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "Invalid email or password",
		},
		{
			name: "missing email",
			payload: map[string]interface{}{
				"password": "password123",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "missing password",
			payload: map[string]interface{}{
				"email": "login@example.com",
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			body, _ := json.Marshal(tt.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
			
			if tt.expectedError != "" {
				var response utils.APIResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.False(suite.T(), response.Success)
				assert.Contains(suite.T(), response.Error.Message, tt.expectedError)
			}
			
			if tt.expectedStatus == http.StatusOK {
				var response utils.APIResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.True(suite.T(), response.Success)
				
				authData := response.Data.(map[string]interface{})
				assert.NotEmpty(suite.T(), authData["token"])
				assert.NotNil(suite.T(), authData["user"])
			}
		})
	}
}

func (suite *AuthIntegrationTestSuite) TestProtectedRoute() {
	// Create a test user and get token
	user := &models.User{
		Email:     "protected@example.com",
		FirstName: "Protected",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	suite.NoError(err)
	
	token, err := suite.authService.Login("protected@example.com", "password123")
	suite.NoError(err)

	tests := []struct {
		name           string
		authHeader     string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "valid bearer token",
			authHeader:     "Bearer " + token,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing auth header",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "Missing or invalid authorization header",
		},
		{
			name:           "invalid token format",
			authHeader:     "InvalidFormat " + token,
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "Missing or invalid authorization header",
		},
		{
			name:           "invalid token",
			authHeader:     "Bearer invalid-token",
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "Invalid credentials",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			
			assert.Equal(suite.T(), tt.expectedStatus, w.Code)
			
			if tt.expectedError != "" {
				var response utils.APIResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.False(suite.T(), response.Success)
				assert.Contains(suite.T(), response.Error.Message, tt.expectedError)
			}
			
			if tt.expectedStatus == http.StatusOK {
				var response map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(suite.T(), err)
				assert.Equal(suite.T(), "Protected route accessed", response["message"])
			}
		})
	}
}

func TestAuthIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(AuthIntegrationTestSuite))
}