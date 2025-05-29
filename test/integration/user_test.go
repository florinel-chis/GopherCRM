package integration

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

type UserIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	cfg         *config.Config
	authService service.AuthService
	adminToken  string
	adminUser   *models.User
}

func (suite *UserIntegrationTestSuite) SetupSuite() {
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

	// Create admin user for tests
	suite.createAdminUser()
}

func (suite *UserIntegrationTestSuite) setupRouter() {
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
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, suite.cfg.JWT)
	userService := service.NewUserService(userRepo)
	authHandler := handler.NewAuthHandler(suite.authService, userService)
	userHandler := handler.NewUserHandler(userService)

	// Setup routes
	api := suite.router.Group("/api/v1")
	{
		public := api.Group("")
		{
			public.POST("/auth/register", authHandler.Register)
			public.POST("/auth/login", authHandler.Login)
		}

		protected := api.Group("")
		protected.Use(middleware.Auth(suite.authService))
		{
			handler.SetupUserRoutes(protected, userHandler)
		}
	}
}

func (suite *UserIntegrationTestSuite) createAdminUser() {
	suite.adminUser = &models.User{
		Email:     "admin@example.com",
		FirstName: "Admin",
		LastName:  "User",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	err := suite.adminUser.SetPassword("admin123")
	suite.Require().NoError(err)
	suite.db.Create(suite.adminUser)

	suite.adminToken, err = suite.authService.GenerateJWT(suite.adminUser)
	suite.Require().NoError(err)
}

func (suite *UserIntegrationTestSuite) TearDownSuite() {
	if suite.db != nil {
		sqlDB, _ := suite.db.DB()
		sqlDB.Close()
	}
}

func (suite *UserIntegrationTestSuite) SetupTest() {
	// Clean up database before each test
	suite.db.Exec("DELETE FROM users WHERE email != ?", "admin@example.com")
	suite.db.Exec("DELETE FROM api_keys")
	
	// Refresh admin user reference to ensure we have the correct ID
	suite.db.Where("email = ?", "admin@example.com").First(&suite.adminUser)
}

func (suite *UserIntegrationTestSuite) TestUserRegistration() {
	// Test registration through public endpoint
	payload := map[string]interface{}{
		"email":      "newuser@example.com",
		"password":   "password123",
		"first_name": "New",
		"last_name":  "User",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusCreated, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)

	// Verify user was created
	var user models.User
	err = suite.db.Where("email = ?", "newuser@example.com").First(&user).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleCustomer, user.Role) // Default role
}

func (suite *UserIntegrationTestSuite) TestUserLogin() {
	// Create test user
	user := &models.User{
		Email:     "logintest@example.com",
		FirstName: "Login",
		LastName:  "Test",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err := user.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(user)

	// Test login
	payload := map[string]string{
		"email":    "logintest@example.com",
		"password": "password123",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)

	// Check that token is returned
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.NotEmpty(suite.T(), data["token"])
}

func (suite *UserIntegrationTestSuite) TestProtectedRoutes() {
	// Test accessing protected route without token
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)

	// Test accessing with valid token
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *UserIntegrationTestSuite) TestUserCRUD() {
	// Create user (as admin)
	payload := map[string]interface{}{
		"email":      "cruduser@example.com",
		"password":   "password123",
		"first_name": "CRUD",
		"last_name":  "User",
		"role":       "sales",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusCreated, rec.Code)

	var createResponse utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &createResponse)
	assert.NoError(suite.T(), err)
	
	userData := createResponse.Data.(map[string]interface{})
	userID := int(userData["id"].(float64))

	// Get user
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/users/%d", userID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	// Update user
	updatePayload := map[string]interface{}{
		"first_name": "Updated",
		"is_active":  false,
	}
	body, _ = json.Marshal(updatePayload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/users/%d", userID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var updateResponse utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &updateResponse)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), updateResponse.Success)
	assert.NotNil(suite.T(), updateResponse.Data)
	
	updatedData, ok := updateResponse.Data.(map[string]interface{})
	assert.True(suite.T(), ok, "Response data should be a map")
	assert.Equal(suite.T(), "Updated", updatedData["first_name"])
	assert.Equal(suite.T(), false, updatedData["is_active"])

	// Delete user
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", userID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)

	// Verify deletion
	var deletedUser models.User
	err = suite.db.Unscoped().Where("id = ?", userID).First(&deletedUser).Error
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), deletedUser.DeletedAt)
}

func (suite *UserIntegrationTestSuite) TestPermissionEnforcement() {
	// Create regular user
	regularUser := &models.User{
		Email:     "regular@example.com",
		FirstName: "Regular",
		LastName:  "User",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err := regularUser.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(regularUser)

	regularToken, err := suite.authService.GenerateJWT(regularUser)
	suite.Require().NoError(err)

	// Test that regular user cannot list all users
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	// Test that regular user cannot create users
	payload := map[string]interface{}{
		"email":      "shouldfail@example.com",
		"password":   "password123",
		"first_name": "Should",
		"last_name":  "Fail",
		"role":       "admin",
	}
	body, _ := json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	// Test that regular user cannot delete users
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", suite.adminUser.ID), nil)
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	// Test that regular user CAN view their own profile
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/users/%d", regularUser.ID), nil)
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	// Test that regular user CAN update their own profile
	updatePayload := map[string]interface{}{
		"first_name": "UpdatedRegular",
	}
	body, _ = json.Marshal(updatePayload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/users/%d", regularUser.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	// Test that regular user CANNOT update other users
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/users/%d", suite.adminUser.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *UserIntegrationTestSuite) TestMeEndpoints() {
	// Create test user
	testUser := &models.User{
		Email:     "metest@example.com",
		FirstName: "Me",
		LastName:  "Test",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err := testUser.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(testUser)

	testToken, err := suite.authService.GenerateJWT(testUser)
	suite.Require().NoError(err)

	// Test GET /users/me
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	
	userData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "metest@example.com", userData["email"])

	// Test PUT /users/me
	updatePayload := map[string]interface{}{
		"first_name": "UpdatedMe",
		"password":   "newpassword123",
	}
	body, _ := json.Marshal(updatePayload)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var updateResponse utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &updateResponse)
	assert.NoError(suite.T(), err)
	
	updatedData := updateResponse.Data.(map[string]interface{})
	assert.Equal(suite.T(), "UpdatedMe", updatedData["first_name"])

	// Verify password was changed by attempting login with new password
	loginPayload := map[string]string{
		"email":    "metest@example.com",
		"password": "newpassword123",
	}
	body, _ = json.Marshal(loginPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *UserIntegrationTestSuite) TestEmailUniqueness() {
	// Create first user
	user1 := &models.User{
		Email:     "unique@example.com",
		FirstName: "First",
		LastName:  "User",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err := user1.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(user1)

	// Try to create user with same email via admin endpoint
	payload := map[string]interface{}{
		"email":      "unique@example.com",
		"password":   "password123",
		"first_name": "Second",
		"last_name":  "User",
		"role":       "customer",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	// Try to register with same email
	registerPayload := map[string]interface{}{
		"email":      "unique@example.com",
		"password":   "password123",
		"first_name": "Third",
		"last_name":  "User",
	}
	body, _ = json.Marshal(registerPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	// Create admin user's token to test email conflict on update
	// (Using admin token to ensure we have permission to update other users)
	
	// Create a second user
	user2 := &models.User{
		Email:     "different@example.com",
		FirstName: "Different",
		LastName:  "User",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err = user2.SetPassword("password123")
	suite.Require().NoError(err)
	suite.db.Create(user2)

	// Try to update user2 to have the same email as user1
	updatePayload := map[string]interface{}{
		"email": "unique@example.com",
	}
	body, _ = json.Marshal(updatePayload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/users/%d", user2.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	rec = httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusConflict, rec.Code)
}

func TestUserIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(UserIntegrationTestSuite))
}