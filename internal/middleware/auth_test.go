package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockAuthService implements service.AuthService for testing.
type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Login(email, password string) (string, error) {
	args := m.Called(email, password)
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) LoginWithTokens(email, password string) (*service.AuthTokens, error) {
	args := m.Called(email, password)
	if t := args.Get(0); t != nil {
		return t.(*service.AuthTokens), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockAuthService) ValidateToken(token string) (*models.User, error) {
	args := m.Called(token)
	if u := args.Get(0); u != nil {
		return u.(*models.User), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockAuthService) ValidateAPIKey(key string) (*models.User, error) {
	args := m.Called(key)
	if u := args.Get(0); u != nil {
		return u.(*models.User), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockAuthService) GenerateJWT(user *models.User) (string, error) {
	args := m.Called(user)
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) GenerateTokens(user *models.User) (*service.AuthTokens, error) {
	args := m.Called(user)
	if t := args.Get(0); t != nil {
		return t.(*service.AuthTokens), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockAuthService) RefreshAccessToken(refreshToken string) (*service.AuthTokens, error) {
	args := m.Called(refreshToken)
	if t := args.Get(0); t != nil {
		return t.(*service.AuthTokens), args.Error(1)
	}
	return nil, args.Error(1)
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

func setupAuthTestRouter(authSvc *MockAuthService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(authSvc))
	r.GET("/protected", func(c *gin.Context) {
		userID, _ := c.Get("user_id")
		role, _ := c.Get("user_role")
		c.JSON(http.StatusOK, gin.H{"user_id": userID, "role": role})
	})
	return r
}

func TestAuth_ValidBearerToken(t *testing.T) {
	mockAuth := new(MockAuthService)
	testUser := &models.User{
		BaseModel: models.BaseModel{ID: 42},
		Email:     "test@example.com",
		Role:      models.RoleAdmin,
	}
	mockAuth.On("ValidateToken", "valid-jwt-token").Return(testUser, nil)

	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer valid-jwt-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"user_id":42`)
	assert.Contains(t, w.Body.String(), `"role":"admin"`)
	mockAuth.AssertExpectations(t)
}

func TestAuth_InvalidBearerToken(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateToken", "bad-token").Return(nil, errors.New("invalid token"))

	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid credentials")
	mockAuth.AssertExpectations(t)
}

func TestAuth_ExpiredBearerToken(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateToken", "expired-token").Return(nil, errors.New("token expired"))

	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer expired-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	mockAuth.AssertExpectations(t)
}

func TestAuth_MissingAuthorizationHeader(t *testing.T) {
	mockAuth := new(MockAuthService)
	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Missing or invalid authorization header")
}

func TestAuth_EmptyAuthorizationHeader(t *testing.T) {
	mockAuth := new(MockAuthService)
	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_UnrecognizedScheme(t *testing.T) {
	mockAuth := new(MockAuthService)
	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Missing or invalid authorization header")
}

func TestAuth_ValidAPIKey(t *testing.T) {
	mockAuth := new(MockAuthService)
	testUser := &models.User{
		BaseModel: models.BaseModel{ID: 7},
		Email:     "api@example.com",
		Role:      models.RoleSales,
	}
	mockAuth.On("ValidateAPIKey", "gcrm_testapikey123").Return(testUser, nil)

	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "ApiKey gcrm_testapikey123")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"user_id":7`)
	assert.Contains(t, w.Body.String(), `"role":"sales"`)
	mockAuth.AssertExpectations(t)
}

func TestAuth_InvalidAPIKey(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateAPIKey", "invalid-key").Return(nil, errors.New("api key not found"))

	router := setupAuthTestRouter(mockAuth)

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "ApiKey invalid-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid credentials")
	mockAuth.AssertExpectations(t)
}

func TestAuth_SetsUserContext(t *testing.T) {
	mockAuth := new(MockAuthService)
	testUser := &models.User{
		BaseModel: models.BaseModel{ID: 99},
		Email:     "ctx@example.com",
		Role:      models.RoleSupport,
	}
	mockAuth.On("ValidateToken", "ctx-token").Return(testUser, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(mockAuth))
	r.GET("/check", func(c *gin.Context) {
		user, exists := c.Get("user")
		assert.True(t, exists)
		assert.Equal(t, testUser, user)

		userID, exists := c.Get("user_id")
		assert.True(t, exists)
		assert.Equal(t, uint(99), userID)

		role, exists := c.Get("user_role")
		assert.True(t, exists)
		assert.Equal(t, "support", role)

		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/check", nil)
	req.Header.Set("Authorization", "Bearer ctx-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequireRole_Allowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_role", "admin")
		c.Next()
	})
	r.GET("/admin", RequireRole(models.RoleAdmin, models.RoleSales), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequireRole_Denied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_role", "customer")
		c.Next()
	})
	r.GET("/admin", RequireRole(models.RoleAdmin), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "Insufficient permissions")
}

func TestRequireRole_NoRoleInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin", RequireRole(models.RoleAdmin), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "Access denied")
}
