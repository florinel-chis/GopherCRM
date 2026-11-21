package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
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

// SignedInAt returns the expectation's time and flag when one is set up, and
// (zero, false) otherwise, so tests that do not care need no expectation.
func (m *MockAuthService) SignedInAt(token string) (time.Time, bool) {
	for _, call := range m.ExpectedCalls {
		if call.Method == "SignedInAt" {
			args := m.Called(token)
			return args.Get(0).(time.Time), args.Bool(1)
		}
	}
	return time.Time{}, false
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

func (m *MockAuthService) ChangePassword(userID uint, currentPassword, newPassword, refreshToken string) (*service.AuthTokens, error) {
	args := m.Called(userID, currentPassword, newPassword, refreshToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.AuthTokens), args.Error(1)
}

func (m *MockAuthService) RequestPasswordReset(email string) error {
	args := m.Called(email)
	return args.Error(0)
}

func (m *MockAuthService) ConfirmPasswordReset(token, newPassword string) error {
	args := m.Called(token, newPassword)
	return args.Error(0)
}

func (m *MockAuthService) IssuePasswordReset(email string) (uint, error) {
	args := m.Called(email)
	return args.Get(0).(uint), args.Error(1)
}

func (m *MockAuthService) CompletePasswordReset(token, newPassword string) (uint, error) {
	args := m.Called(token, newPassword)
	return args.Get(0).(uint), args.Error(1)
}

func setupAuthTestRouter(authSvc *MockAuthService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(authSvc))
	r.GET("/protected", func(c *gin.Context) {
		userID, _ := c.Get("user_id")
		role, _ := c.Get("user_role")
		c.JSON(http.StatusOK, gin.H{"user_id": userID, "role": role, "auth_method": c.GetString(service.AuthMethodContextKey)})
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

// Handlers that issue session credentials (change-password renewal) must be
// able to tell a bearer session from an API key; the middleware records it.
func TestAuth_RecordsAuthenticationMethod(t *testing.T) {
	user := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleSales}
	for header, method := range map[string]string{
		"Bearer some-jwt":  service.AuthMethodBearer,
		"ApiKey gcrm_abcd": service.AuthMethodAPIKey,
	} {
		mockAuth := new(MockAuthService)
		mockAuth.On("ValidateToken", "some-jwt").Return(user, nil).Maybe()
		mockAuth.On("ValidateAPIKey", "gcrm_abcd").Return(user, nil).Maybe()
		router := setupAuthTestRouter(mockAuth)

		req, _ := http.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"auth_method":"`+method+`"`, header)
	}
}

// recordingAudit is a sign-in audit writer that keeps what it was given.
type recordingAudit struct {
	apiKeyFailures []recordedAPIKeyFailure
	other          int
}

type recordedAPIKeyFailure struct {
	ctx context.Context
	err error
}

func (r *recordingAudit) Record(context.Context, service.AuthAuditEntry) uint { r.other++; return 1 }

func (r *recordingAudit) RecordPassword(context.Context, *service.AuthTokens, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordRefresh(context.Context, *service.AuthTokens, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordPasswordChange(context.Context, uint, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordPasswordResetRequest(context.Context, uint, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordPasswordResetConfirm(context.Context, uint, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordLogout(context.Context, uint, error) uint {
	r.other++
	return 1
}

func (r *recordingAudit) RecordAPIKeyFailure(ctx context.Context, err error) uint {
	r.apiKeyFailures = append(r.apiKeyFailures, recordedAPIKeyFailure{ctx: ctx, err: err})
	return 1
}

func setupAuditedAuthRouter(authSvc *MockAuthService, audit *recordingAudit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("request_id", "req-454"); c.Next() })
	r.Use(Auth(authSvc, WithAPIKeyAudit(audit)))
	r.GET("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	return r
}

func TestAuth_RecordsAPIKeyFailure(t *testing.T) {
	refusal := errors.New("invalid API key")
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateAPIKey", "bogus").Return(nil, refusal)
	mockAuth.On("ValidateAPIKey", "good").Return(&models.User{BaseModel: models.BaseModel{ID: 3}, Role: models.RoleSales}, nil)
	audit := &recordingAudit{}
	router := setupAuditedAuthRouter(mockAuth, audit)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "ApiKey bogus")
	req.Header.Set("User-Agent", "curl/8.7.1")
	req.RemoteAddr = "198.51.100.77:4321"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid credentials", "the answer is unchanged")
	if assert.Len(t, audit.apiKeyFailures, 1, "recorded once") {
		got := audit.apiKeyFailures[0]
		assert.Same(t, refusal, got.err)
		assert.Equal(t, "req-454", utils.RequestIDFromContext(got.ctx))
		assert.Equal(t, utils.ClientInfo{IP: "198.51.100.77", UserAgent: "curl/8.7.1"}, utils.ClientInfoFromContext(got.ctx))
	}
	assert.Zero(t, audit.other)

	// A valid key records nothing.
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "ApiKey good")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, audit.apiKeyFailures, 1)
	mockAuth.AssertExpectations(t)
}

func TestAuth_DoesNotRecordBearerFailure(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateToken", "bad-token").Return(nil, errors.New("invalid token"))
	audit := &recordingAudit{}
	router := setupAuditedAuthRouter(mockAuth, audit)

	for _, header := range []string{"Bearer bad-token", "", "Basic abc"} {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, header)
	}
	assert.Empty(t, audit.apiKeyFailures, "a Bearer or missing credential is never recorded")
	assert.Zero(t, audit.other)
	mockAuth.AssertExpectations(t)
}

// TestAuth_SetsSignedInAt: a Bearer request whose token carries a sign-in
// time publishes it under service.SignedInAtContextKey (#532); an ApiKey
// request and a Bearer token without one publish nothing.
func TestAuth_SetsSignedInAt(t *testing.T) {
	user := &models.User{BaseModel: models.BaseModel{ID: 42}, Role: models.RoleSales, IsActive: true}
	signedIn := time.Unix(1_790_000_000, 0)

	run := func(t *testing.T, mockAuth *MockAuthService, header string) (time.Time, bool) {
		t.Helper()
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(Auth(mockAuth))
		var got time.Time
		var present bool
		r.GET("/protected", func(c *gin.Context) {
			value, ok := c.Get(service.SignedInAtContextKey)
			present = ok
			if ok {
				got = value.(time.Time)
			}
			c.Status(http.StatusOK)
		})
		req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		return got, present
	}

	t.Run("bearer with a sign-in time", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		mockAuth.On("ValidateToken", "signed-in").Return(user, nil)
		mockAuth.On("SignedInAt", "signed-in").Return(signedIn, true)
		got, present := run(t, mockAuth, "Bearer signed-in")
		assert.True(t, present)
		assert.True(t, got.Equal(signedIn))
		mockAuth.AssertExpectations(t)
	})

	t.Run("api key", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		mockAuth.On("ValidateAPIKey", "gcrm_key").Return(user, nil)
		mockAuth.On("SignedInAt", mock.Anything).Return(signedIn, true)
		_, present := run(t, mockAuth, "ApiKey gcrm_key")
		assert.False(t, present)
		mockAuth.AssertNotCalled(t, "SignedInAt", mock.Anything)
	})

	t.Run("bearer without a sign-in time", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		mockAuth.On("ValidateToken", "refreshed").Return(user, nil)
		mockAuth.On("SignedInAt", "refreshed").Return(time.Time{}, false)
		_, present := run(t, mockAuth, "Bearer refreshed")
		assert.False(t, present)
		mockAuth.AssertExpectations(t)
	})
}
