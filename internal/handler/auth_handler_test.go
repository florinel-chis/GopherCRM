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
	suite.mockAuthService.On("Login", "user@example.com", "SecurePass1!").Return("jwt-token", nil)
	suite.mockUserService.On("GetByEmail", "user@example.com").Return(user, nil)

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

func TestAuthHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(AuthHandlerTestSuite))
}
