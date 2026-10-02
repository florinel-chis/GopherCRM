package integration

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
	"github.com/glebarez/sqlite"
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
	userService := service.NewUserServiceWithSessions(userRepo, repository.NewRefreshTokenRepository(suite.db))
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
	suite.db.Exec("DELETE FROM refresh_tokens")
	
	// Refresh admin user reference to ensure we have the correct ID
	suite.db.Where("email = ?", "admin@example.com").First(&suite.adminUser)
}

func (suite *UserIntegrationTestSuite) TestUserRegistration() {
	// Test registration through public endpoint
	payload := map[string]interface{}{
		"email":      "newuser@example.com",
		"password":   "Password123!",
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
	err := user.SetPassword("Password123!")
	suite.Require().NoError(err)
	suite.db.Create(user)

	// Test login
	payload := map[string]string{
		"email":    "logintest@example.com",
		"password": "Password123!",
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
		"password":   "Password123!",
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
	err := regularUser.SetPassword("Password123!")
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
		"password":   "Password123!",
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
	err := testUser.SetPassword("Password123!")
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

	// Test PUT /users/me: the profile fields change, the password does not.
	updatePayload := map[string]interface{}{
		"first_name": "UpdatedMe",
		"last_name":  "Renamed",
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
	assert.Equal(suite.T(), "Renamed", updatedData["last_name"])

	// A profile update is not a credential change: the old password still logs in.
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus("metest@example.com", "Password123!"))
}

// PUT /users/me never asks for the current password and never revokes
// sessions, so it must not change the password at all: a stolen access token
// would otherwise be enough to lock the owner out. The key is refused with a
// message that names the one password-change route, and nothing in the
// request is applied.
func (suite *UserIntegrationTestSuite) TestUpdateMe_RefusesPassword() {
	user, token := suite.createCustomer("mepassword@example.com", "Password123!")

	rec := suite.putJSON("/api/v1/users/me", token, map[string]interface{}{
		"first_name": "Hijacked",
		"password":   "NewPassword123!",
	})

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	apiErr := suite.decodeError(rec)
	assert.Contains(suite.T(), apiErr.Message, "/auth/change-password")

	var stored models.User
	suite.Require().NoError(suite.db.First(&stored, user.ID).Error)
	assert.Equal(suite.T(), "Me", stored.FirstName, "a refused request applies nothing")

	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(user.Email, "Password123!"), "the old password still logs in")
	assert.Equal(suite.T(), http.StatusUnauthorized, suite.loginStatus(user.Email, "NewPassword123!"), "the submitted password was not set")
}

// PUT /users/:id by an admin sets a password: it is the account-recovery path
// and the only one besides POST /auth/change-password that may change it.
func (suite *UserIntegrationTestSuite) TestUpdateUser_AdminSetsPassword() {
	user, _ := suite.createCustomer("recover@example.com", "Password123!")

	rec := suite.putJSON(fmt.Sprintf("/api/v1/users/%d", user.ID), suite.adminToken, map[string]interface{}{
		"password": "Recovered456!",
	})

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(user.Email, "Recovered456!"), "the new password logs in")
	assert.Equal(suite.T(), http.StatusUnauthorized, suite.loginStatus(user.Email, "Password123!"), "the old password no longer does")
}

// The password policy applies on the admin path as everywhere else, and a
// refused password leaves the stored one untouched.
func (suite *UserIntegrationTestSuite) TestUpdateUser_AdminPasswordMustMeetPolicy() {
	user, _ := suite.createCustomer("recoverweak@example.com", "Password123!")

	rec := suite.putJSON(fmt.Sprintf("/api/v1/users/%d", user.ID), suite.adminToken, map[string]interface{}{
		"password": "alllowercaseonly",
	})

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(user.Email, "Password123!"), "the old password is untouched")
}

// A password set by an admin ends every session of that user, as a change
// through POST /auth/change-password does; other users' sessions stay.
func (suite *UserIntegrationTestSuite) TestUpdateUser_AdminPasswordChangeRevokesRefreshTokens() {
	user, _ := suite.createCustomer("recoversessions@example.com", "Password123!")
	suite.insertRefreshToken(user.ID, "recover-session-1")
	suite.insertRefreshToken(user.ID, "recover-session-2")
	suite.insertRefreshToken(suite.adminUser.ID, "admin-session")

	rec := suite.putJSON(fmt.Sprintf("/api/v1/users/%d", user.ID), suite.adminToken, map[string]interface{}{
		"password": "Recovered456!",
	})
	suite.Require().Equal(http.StatusOK, rec.Code)

	assert.Equal(suite.T(), int64(0), suite.liveRefreshTokens(user.ID), "every session of the user is revoked")
	assert.Equal(suite.T(), int64(1), suite.liveRefreshTokens(suite.adminUser.ID), "the admin's own session is untouched")
}

// Without the admin role, PUT /users/:id on one's own record answers as
// PUT /users/me does: the key is refused, not silently dropped, and the
// stored password stays.
func (suite *UserIntegrationTestSuite) TestUpdateUser_SelfCannotSetPasswordHere() {
	user, token := suite.createCustomer("selfrecover@example.com", "Password123!")

	rec := suite.putJSON(fmt.Sprintf("/api/v1/users/%d", user.ID), token, map[string]interface{}{
		"password": "NewPassword123!",
	})

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	apiErr := suite.decodeError(rec)
	assert.Contains(suite.T(), apiErr.Message, "/auth/change-password")
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(user.Email, "Password123!"), "the old password still logs in")
	assert.Equal(suite.T(), http.StatusUnauthorized, suite.loginStatus(user.Email, "NewPassword123!"), "the submitted password was not set")
}

// TestUpdateUser_AdminCannotSetOwnPasswordHere: the recovery path is for
// other accounts. An admin changes their own password through
// POST /auth/change-password like everyone else, with the current one.
func (suite *UserIntegrationTestSuite) TestUpdateUser_AdminCannotSetOwnPasswordHere() {
	rec := suite.putJSON(fmt.Sprintf("/api/v1/users/%d", suite.adminUser.ID), suite.adminToken, map[string]interface{}{
		"password": "NewPassword123!",
	})

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	apiErr := suite.decodeError(rec)
	assert.Contains(suite.T(), apiErr.Message, "/auth/change-password")
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(suite.adminUser.Email, "admin123"), "the old password still logs in")
	assert.Equal(suite.T(), http.StatusUnauthorized, suite.loginStatus(suite.adminUser.Email, "NewPassword123!"), "the submitted password was not set")
}

// TestUpdateMe_RefusesPasswordKeyInAnyCase: encoding/json matches struct
// fields case-insensitively, so the refusal must not depend on the spelling
// of the key.
func (suite *UserIntegrationTestSuite) TestUpdateMe_RefusesPasswordKeyInAnyCase() {
	user, token := suite.createCustomer("casefold@example.com", "Password123!")

	rec := suite.putJSON("/api/v1/users/me", token, map[string]interface{}{
		"Password": "NewPassword123!",
	})

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	assert.Equal(suite.T(), http.StatusOK, suite.loginStatus(user.Email, "Password123!"), "the old password still logs in")
	assert.Equal(suite.T(), http.StatusUnauthorized, suite.loginStatus(user.Email, "NewPassword123!"), "the submitted password was not set")
}

// insertRefreshToken stores a live refresh token row for the user.
func (suite *UserIntegrationTestSuite) insertRefreshToken(userID uint, hash string) {
	suite.Require().NoError(suite.db.Create(&models.RefreshToken{
		UserID:    userID,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)
}

// liveRefreshTokens counts the user's refresh tokens that are not revoked.
func (suite *UserIntegrationTestSuite) liveRefreshTokens(userID uint) int64 {
	var n int64
	suite.Require().NoError(suite.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, false).Count(&n).Error)
	return n
}

// createCustomer inserts an active customer with the given credentials and
// returns the row together with a JWT for it.
func (suite *UserIntegrationTestSuite) createCustomer(email, password string) (*models.User, string) {
	user := &models.User{
		Email:     email,
		FirstName: "Me",
		LastName:  "Test",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	suite.Require().NoError(user.SetPassword(password))
	suite.Require().NoError(suite.db.Create(user).Error)
	token, err := suite.authService.GenerateJWT(user)
	suite.Require().NoError(err)
	return user, token
}

// putJSON sends a JSON body with the given bearer token and returns the recorder.
func (suite *UserIntegrationTestSuite) putJSON(path, token string, payload map[string]interface{}) *httptest.ResponseRecorder {
	body, err := json.Marshal(payload)
	suite.Require().NoError(err)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)
	return rec
}

// loginStatus returns the status code POST /auth/login answers for the credentials.
func (suite *UserIntegrationTestSuite) loginStatus(email, password string) int {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	suite.Require().NoError(err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)
	return rec.Code
}

// decodeError returns the error envelope of a response that must be a failure.
func (suite *UserIntegrationTestSuite) decodeError(rec *httptest.ResponseRecorder) *utils.APIError {
	var response utils.APIResponse
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &response))
	suite.Require().False(response.Success, "expected an error envelope, got a success response")
	suite.Require().NotNil(response.Error)
	return response.Error
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
	err := user1.SetPassword("Password123!")
	suite.Require().NoError(err)
	suite.db.Create(user1)

	// Try to create user with same email via admin endpoint
	payload := map[string]interface{}{
		"email":      "unique@example.com",
		"password":   "Password123!",
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
		"password":   "Password123!",
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
	err = user2.SetPassword("Password123!")
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