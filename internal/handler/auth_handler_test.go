package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Login(email, password string) (string, error) {
	args := m.Called(email, password)
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) LoginWithTokens(email, password string) (*service.AuthTokens, error) {
	args := m.Called(email, password)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.AuthTokens), args.Error(1)
}

func (m *MockAuthService) ValidateToken(token string) (*models.User, error) {
	args := m.Called(token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockAuthService) ValidateAPIKey(key string) (*models.User, error) {
	args := m.Called(key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockAuthService) GenerateJWT(user *models.User) (string, error) {
	args := m.Called(user)
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) GenerateTokens(user *models.User) (*service.AuthTokens, error) {
	args := m.Called(user)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.AuthTokens), args.Error(1)
}

func (m *MockAuthService) RefreshAccessToken(refreshToken string) (*service.AuthTokens, error) {
	args := m.Called(refreshToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.AuthTokens), args.Error(1)
}

func (m *MockAuthService) InvalidateRefreshToken(refreshToken string) error {
	args := m.Called(refreshToken)
	return args.Error(0)
}

func (m *MockAuthService) Logout(userID uint, refreshToken string) error {
	args := m.Called(userID, refreshToken)
	return args.Error(0)
}

func (m *MockAuthService) ChangePassword(userID uint, currentPassword, newPassword string) error {
	args := m.Called(userID, currentPassword, newPassword)
	return args.Error(0)
}

func (m *MockAuthService) RequestPasswordReset(email string) error {
	args := m.Called(email)
	return args.Error(0)
}

func (m *MockAuthService) ConfirmPasswordReset(token, newPassword string) error {
	args := m.Called(token, newPassword)
	return args.Error(0)
}

func (m *MockAuthService) GenerateCSRFToken() (string, error) {
	args := m.Called()
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) ValidateCSRFToken(token string) bool {
	args := m.Called(token)
	return args.Bool(0)
}

type MockUserService struct {
	mock.Mock
}

func (m *MockUserService) Register(user *models.User, password string) error {
	args := m.Called(user, password)
	return args.Error(0)
}

func (m *MockUserService) GetByID(id uint) (*models.User, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockUserService) GetByEmail(email string) (*models.User, error) {
	args := m.Called(email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockUserService) Update(id uint, updates map[string]interface{}) (*models.User, error) {
	args := m.Called(id, updates)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockUserService) Delete(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockUserService) List(offset, limit int) ([]models.User, int64, error) {
	args := m.Called(offset, limit)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.User), args.Get(1).(int64), args.Error(2)
}

func (m *MockUserService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error) {
	args := m.Called(offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.User), args.Get(1).(int64), args.Error(2)
}

func (m *MockUserService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error) {
	args := m.Called(query, offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.User), args.Get(1).(int64), args.Error(2)
}

// Ensure the mocks satisfy the service interfaces
var _ service.AuthService = (*MockAuthService)(nil)
var _ service.UserService = (*MockUserService)(nil)

type AuthHandlerTestSuite struct {
	suite.Suite
	mockAuthService *MockAuthService
	mockUserService *MockUserService
	handler         *AuthHandler
	router          *gin.Engine
}

func (suite *AuthHandlerTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	gin.SetMode(gin.TestMode)
}

func (suite *AuthHandlerTestSuite) SetupTest() {
	suite.mockAuthService = new(MockAuthService)
	suite.mockUserService = new(MockUserService)
	suite.handler = NewAuthHandler(suite.mockAuthService, suite.mockUserService)
	suite.router = gin.New()

	// Add error handler middleware to handle validation errors
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			err := c.Errors[0]
			if err.Type == gin.ErrorTypeBind {
				utils.RespondValidationError(c, err.Error())
				return
			}
		}
	})

	suite.router.POST("/auth/register", suite.handler.Register)
	suite.router.POST("/auth/login", suite.handler.Login)
	suite.router.POST("/auth/refresh", suite.handler.Refresh)
	suite.router.POST("/auth/password-reset", suite.handler.RequestPasswordReset)
	suite.router.POST("/auth/password-reset/confirm", suite.handler.ConfirmPasswordReset)

	// Authenticated endpoints: simulate the auth middleware having populated
	// the context, exactly as middleware.Auth does after token validation.
	authed := suite.router.Group("")
	authed.Use(func(c *gin.Context) {
		c.Set("user_id", uint(42))
		c.Next()
	})
	authed.POST("/auth/logout", suite.handler.Logout)
	authed.POST("/auth/change-password", suite.handler.ChangePassword)
}

func (suite *AuthHandlerTestSuite) TearDownTest() {
	suite.mockAuthService.AssertExpectations(suite.T())
	suite.mockUserService.AssertExpectations(suite.T())
}

// Regression test for privilege escalation via public registration: a client
// supplying "role":"admin" in the register payload must never be able to
// influence the role of the created user.
func (suite *AuthHandlerTestSuite) TestRegister_RoleInjection_ForcedToCustomer() {
	var capturedUser *models.User
	suite.mockUserService.On("Register", mock.MatchedBy(func(u *models.User) bool {
		capturedUser = u
		return u.Email == "attacker@example.com"
	}), "SecurePass1!").Return(nil)
	suite.mockAuthService.On("GenerateJWT", mock.AnythingOfType("*models.User")).Return("test-token", nil)

	requestBody := map[string]interface{}{
		"email":      "attacker@example.com",
		"password":   "SecurePass1!",
		"first_name": "Eve",
		"last_name":  "Attacker",
		"role":       "admin", // Malicious role injection attempt
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	assert.NotNil(suite.T(), capturedUser)
	assert.Equal(suite.T(), models.RoleCustomer, capturedUser.Role,
		"client-supplied role must be ignored; registration must always create a customer")
}

func (suite *AuthHandlerTestSuite) TestRegister_NoRole_DefaultsToCustomer() {
	var capturedUser *models.User
	suite.mockUserService.On("Register", mock.MatchedBy(func(u *models.User) bool {
		capturedUser = u
		return u.Email == "new@example.com"
	}), "SecurePass1!").Return(nil)
	suite.mockAuthService.On("GenerateJWT", mock.AnythingOfType("*models.User")).Return("test-token", nil)

	requestBody := map[string]interface{}{
		"email":      "new@example.com",
		"password":   "SecurePass1!",
		"first_name": "New",
		"last_name":  "User",
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	assert.NotNil(suite.T(), capturedUser)
	assert.Equal(suite.T(), models.RoleCustomer, capturedUser.Role)
}

func (suite *AuthHandlerTestSuite) TestRegister_DuplicateEmail_Conflict() {
	suite.mockUserService.On("Register", mock.AnythingOfType("*models.User"), "SecurePass1!").
		Return(apperrors.ErrDuplicateEmail)

	requestBody := map[string]interface{}{
		"email":      "existing@example.com",
		"password":   "SecurePass1!",
		"first_name": "Existing",
		"last_name":  "User",
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusConflict, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
}

func (suite *AuthHandlerTestSuite) TestRegister_WeakPassword_BadRequest() {
	requestBody := map[string]interface{}{
		"email":      "weak@example.com",
		"password":   "weakpassword", // no upper, digit, or special character
		"first_name": "Weak",
		"last_name":  "Password",
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	suite.mockUserService.AssertNotCalled(suite.T(), "Register", mock.Anything, mock.Anything)
}

func (suite *AuthHandlerTestSuite) TestRegister_Success() {
	suite.mockUserService.On("Register", mock.MatchedBy(func(u *models.User) bool {
		return u.Email == "ok@example.com" &&
			u.FirstName == "Good" &&
			u.LastName == "User" &&
			u.Role == models.RoleCustomer
	}), "SecurePass1!").Return(nil)
	suite.mockAuthService.On("GenerateJWT", mock.AnythingOfType("*models.User")).Return("jwt-token", nil)

	requestBody := map[string]interface{}{
		"email":      "ok@example.com",
		"password":   "SecurePass1!",
		"first_name": "Good",
		"last_name":  "User",
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.Equal(suite.T(), "jwt-token", data["token"])
}

// LoginRequest no longer declares a remember_me field — the backend never read
// it and token lifetime comes from JWT_EXPIRY_HOURS. Clients that still send the
// key (the React app does, to drive its own localStorage/sessionStorage choice)
// must keep working: the binder uses plain ShouldBindJSON, which does not enable
// DisallowUnknownFields, so encoding/json discards the unknown key.
func (suite *AuthHandlerTestSuite) TestLogin_UnknownRememberMeField_Ignored() {
	user := &models.User{
		Email: "user@example.com",
		Role:  models.RoleCustomer,
	}
	suite.mockAuthService.On("LoginWithTokens", "user@example.com", "SecurePass1!").
		Return(&service.AuthTokens{AccessToken: "jwt-token", RefreshToken: "refresh-1", User: user}, nil)

	requestBody := map[string]interface{}{
		"email":       "user@example.com",
		"password":    "SecurePass1!",
		"remember_me": true,
	}
	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.Equal(suite.T(), "jwt-token", data["token"])
}

func (suite *AuthHandlerTestSuite) TestLogin_ResponseIncludesRefreshToken() {
	user := &models.User{Email: "user@example.com", Role: models.RoleCustomer}
	suite.mockAuthService.On("LoginWithTokens", "user@example.com", "SecurePass1!").
		Return(&service.AuthTokens{AccessToken: "jwt-token", RefreshToken: "refresh-raw", User: user}, nil)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "SecurePass1!"})
	req := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.Equal(suite.T(), "jwt-token", data["token"])
	assert.Equal(suite.T(), "refresh-raw", data["refresh_token"])
	assert.NotNil(suite.T(), data["user"])
}

func (suite *AuthHandlerTestSuite) TestRefresh_Success_MirrorsLoginShape() {
	user := &models.User{Email: "user@example.com", Role: models.RoleCustomer}
	suite.mockAuthService.On("RefreshAccessToken", "old-refresh").
		Return(&service.AuthTokens{AccessToken: "new-jwt", RefreshToken: "new-refresh", User: user}, nil)

	body, _ := json.Marshal(map[string]string{"refresh_token": "old-refresh"})
	req := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(suite.T(), response.Success)
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.Equal(suite.T(), "new-jwt", data["token"])
	assert.Equal(suite.T(), "new-refresh", data["refresh_token"])
	assert.NotNil(suite.T(), data["user"])
}

func (suite *AuthHandlerTestSuite) TestRefresh_InvalidToken_Generic401() {
	suite.mockAuthService.On("RefreshAccessToken", "revoked-or-expired").
		Return(nil, service.ErrInvalidRefreshToken)

	body, _ := json.Marshal(map[string]string{"refresh_token": "revoked-or-expired"})
	req := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	assert.False(suite.T(), response.Success)
	// The message must not disclose why the token was rejected.
	assert.Equal(suite.T(), "Invalid or expired refresh token", response.Error.Message)
	assert.NotContains(suite.T(), response.Error.Message, "revoked")
}

func (suite *AuthHandlerTestSuite) TestRefresh_MissingBody_BadRequest() {
	req := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockAuthService.AssertNotCalled(suite.T(), "RefreshAccessToken", mock.Anything)
}

// The frontend calls logout with no body at all (AuthContext.tsx); that path
// must revoke every session of the authenticated user and return 200.
func (suite *AuthHandlerTestSuite) TestLogout_NoBody_RevokesAllSessions() {
	suite.mockAuthService.On("Logout", uint(42), "").Return(nil)

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(suite.T(), response.Success)
	data, ok := response.Data.(map[string]interface{})
	assert.True(suite.T(), ok)
	assert.NotEmpty(suite.T(), data["message"])
}

func (suite *AuthHandlerTestSuite) TestLogout_WithRefreshToken_RevokesThatToken() {
	suite.mockAuthService.On("Logout", uint(42), "my-refresh").Return(nil)

	body, _ := json.Marshal(map[string]string{"refresh_token": "my-refresh"})
	req := httptest.NewRequest("POST", "/auth/logout", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.mockAuthService.AssertCalled(suite.T(), "Logout", uint(42), "my-refresh")
}

func (suite *AuthHandlerTestSuite) TestChangePassword_Success() {
	suite.mockAuthService.On("ChangePassword", uint(42), "CurrentPass1!", "NewSecret123!").Return(nil)

	body, _ := json.Marshal(map[string]string{
		"current_password": "CurrentPass1!",
		"new_password":     "NewSecret123!",
	})
	req := httptest.NewRequest("POST", "/auth/change-password", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AuthHandlerTestSuite) TestChangePassword_WrongCurrent_BadRequest() {
	suite.mockAuthService.On("ChangePassword", uint(42), "WrongPass1!", "NewSecret123!").
		Return(service.ErrInvalidCurrentPassword)

	body, _ := json.Marshal(map[string]string{
		"current_password": "WrongPass1!",
		"new_password":     "NewSecret123!",
	})
	req := httptest.NewRequest("POST", "/auth/change-password", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	assert.False(suite.T(), response.Success)
	assert.Contains(suite.T(), response.Error.Message, "current password")
}

func (suite *AuthHandlerTestSuite) TestChangePassword_WeakNewPassword_BadRequest() {
	// Fails ValidatePasswordComplexity in the handler; the service is never hit.
	body, _ := json.Marshal(map[string]string{
		"current_password": "CurrentPass1!",
		"new_password":     "weakpassword",
	})
	req := httptest.NewRequest("POST", "/auth/change-password", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockAuthService.AssertNotCalled(suite.T(), "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
}

// Anti-enumeration: the response for an existing account and an unknown one
// must be byte-for-byte identical.
func (suite *AuthHandlerTestSuite) TestPasswordReset_AntiEnumeration_IdenticalResponses() {
	suite.mockAuthService.On("RequestPasswordReset", "exists@example.com").Return(nil)
	suite.mockAuthService.On("RequestPasswordReset", "unknown@example.com").Return(nil)

	responses := make([]*httptest.ResponseRecorder, 0, 2)
	for _, email := range []string{"exists@example.com", "unknown@example.com"} {
		body, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest("POST", "/auth/password-reset", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)
		responses = append(responses, w)
	}

	assert.Equal(suite.T(), http.StatusOK, responses[0].Code)
	assert.Equal(suite.T(), responses[0].Code, responses[1].Code)
	assert.Equal(suite.T(), responses[0].Body.String(), responses[1].Body.String(),
		"existing and unknown accounts must yield identical response bodies")
}

func (suite *AuthHandlerTestSuite) TestPasswordResetConfirm_Success() {
	suite.mockAuthService.On("ConfirmPasswordReset", "valid-token", "BrandNewPass1!").Return(nil)

	body, _ := json.Marshal(map[string]string{
		"token":        "valid-token",
		"new_password": "BrandNewPass1!",
	})
	req := httptest.NewRequest("POST", "/auth/password-reset/confirm", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AuthHandlerTestSuite) TestPasswordResetConfirm_InvalidToken_Generic400() {
	suite.mockAuthService.On("ConfirmPasswordReset", "spent-token", "BrandNewPass1!").
		Return(service.ErrInvalidResetToken)

	body, _ := json.Marshal(map[string]string{
		"token":        "spent-token",
		"new_password": "BrandNewPass1!",
	})
	req := httptest.NewRequest("POST", "/auth/password-reset/confirm", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	assert.False(suite.T(), response.Success)
	// Generic message: no used/expired/unknown distinction.
	assert.Equal(suite.T(), "Invalid or expired reset token", response.Error.Message)
}

func TestAuthHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(AuthHandlerTestSuite))
}
