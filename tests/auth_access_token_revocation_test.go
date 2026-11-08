package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #236 over HTTP: the real auth routes, the real middleware.Auth and the real
// GET /users/me on in-memory SQLite with the production one-connection pool.
// After a logout, a password change or a password-reset confirmation, an
// access token issued before it answers exactly what an expired token
// answers, an API key of the same user still authenticates, and a token
// issued afterwards (the renewed one, a fresh login) is accepted. The rate
// limiters are left out: they are keyed on the client IP and would throttle
// the many logins of one test, and they do not take part in the decision.

const (
	atvAPISecret   = "access-token-revocation-api-secret-32b"
	atvAPIPassword = "Password123!"
	atvAPINew      = "NewSecret123!"
)

type atvAPIStack struct {
	router      *gin.Engine
	userService service.UserService
	apiKeys     service.APIKeyService
	mailer      *capturingMailer
}

func newATVAPIStack(t *testing.T) *atvAPIStack {
	t.Helper()
	// This file has no TestMain: the logger is initialised here.
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{},
		&models.RefreshToken{}, &models.PasswordResetToken{},
	))

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	mail := &capturingMailer{}
	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), mail,
		config.JWTConfig{Secret: atvAPISecret, ExpiryHours: 1, RefreshTokenDays: 30},
		"http://localhost:5173", "")
	userService := service.NewUserService(userRepo)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())

	authHandler := handler.NewAuthHandler(authService, userService)
	api := router.Group("/api/v1")
	authRoutes := api.Group("/auth")
	authRoutes.POST("/login", authHandler.Login)
	authRoutes.POST("/refresh", authHandler.Refresh)
	authRoutes.POST("/password-reset", authHandler.RequestPasswordReset)
	authRoutes.POST("/password-reset/confirm", authHandler.ConfirmPasswordReset)

	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupUserRoutes(protected, handler.NewUserHandler(userService))
	protectedAuth := protected.Group("/auth")
	protectedAuth.POST("/logout", authHandler.Logout)
	protectedAuth.POST("/change-password", authHandler.ChangePassword)

	return &atvAPIStack{
		router:      router,
		userService: userService,
		apiKeys:     service.NewAPIKeyService(apiKeyRepo, ""),
		mailer:      mail,
	}
}

func (s *atvAPIStack) do(method, path, authorization, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func (s *atvAPIStack) me(authorization string) *httptest.ResponseRecorder {
	return s.do(http.MethodGet, "/api/v1/users/me", authorization, "")
}

func (s *atvAPIStack) register(t *testing.T, label string) *models.User {
	t.Helper()
	user := &models.User{Email: label + "@atv-api.test", FirstName: "Token", LastName: "Version",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, s.userService.Register(user, atvAPIPassword))
	return user
}

func atvAPIData(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	data, ok := response.Data.(map[string]interface{})
	require.True(t, ok, rec.Body.String())
	return data
}

func (s *atvAPIStack) login(t *testing.T, email, password string) (string, string) {
	t.Helper()
	rec := s.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := atvAPIData(t, rec)
	access, _ := data["token"].(string)
	refresh, _ := data["refresh_token"].(string)
	require.NotEmpty(t, access)
	require.NotEmpty(t, refresh)
	return access, refresh
}

func (s *atvAPIStack) apiKey(t *testing.T, user *models.User) string {
	t.Helper()
	raw, _, err := s.apiKeys.Generate(user.ID, "atv-api", nil)
	require.NoError(t, err)
	return "ApiKey " + raw
}

// expiredToken is a correctly signed access token of the user that expired a
// minute ago: the reference answer for a rejected credential.
func expiredToken(t *testing.T, user *models.User) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id":       user.ID,
		"email":         user.Email,
		"role":          user.Role,
		"token_version": 0,
		"exp":           time.Now().Add(-time.Minute).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(atvAPISecret))
	require.NoError(t, err)
	return token
}

// unauthorizedBody is the response body without its per-request metadata.
func unauthorizedBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	delete(body, "meta")
	return body
}

// assertEnded checks the answer to an access token that a session-ending
// event revoked: 401, the unified envelope, UNAUTHORIZED "Invalid
// credentials", byte for byte what an expired token gets.
func (s *atvAPIStack) assertEnded(t *testing.T, user *models.User, access string) {
	t.Helper()
	rec := s.me("Bearer " + access)
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, response.Success)
	assert.Nil(t, response.Data)
	if assert.NotNil(t, response.Error) {
		assert.Equal(t, utils.ErrCodeUnauthorized, response.Error.Code)
		assert.Equal(t, "Invalid credentials", response.Error.Message)
	}
	assert.Equal(t, unauthorizedBody(t, s.me("Bearer "+expiredToken(t, user))), unauthorizedBody(t, rec),
		"the same answer an expired token gets")
}

func (s *atvAPIStack) assertAccepted(t *testing.T, user *models.User, authorization, what string) {
	t.Helper()
	rec := s.me(authorization)
	require.Equal(t, http.StatusOK, rec.Code, "%s: %s", what, rec.Body.String())
	data := atvAPIData(t, rec)
	assert.EqualValues(t, user.ID, data["id"], what)
	assert.NotContains(t, data, "token_version", "the counter is never serialised")
}

func TestAccessTokenRevocationAPISQLite(t *testing.T) {
	s := newATVAPIStack(t)

	t.Run("logout with a Bearer token and no body", func(t *testing.T) {
		user := s.register(t, "logout-bearer")
		access, _ := s.login(t, user.Email, atvAPIPassword)
		key := s.apiKey(t, user)
		s.assertAccepted(t, user, "Bearer "+access, "before the logout")

		rec := s.do(http.MethodPost, "/api/v1/auth/logout", "Bearer "+access, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		s.assertEnded(t, user, access)
		s.assertAccepted(t, user, key, "the API key of the same user")
		fresh, _ := s.login(t, user.Email, atvAPIPassword)
		s.assertAccepted(t, user, "Bearer "+fresh, "a fresh login")
	})

	t.Run("logout with a Bearer token and the session's refresh token", func(t *testing.T) {
		user := s.register(t, "logout-body")
		access, refresh := s.login(t, user.Email, atvAPIPassword)
		key := s.apiKey(t, user)
		s.assertAccepted(t, user, "Bearer "+access, "before the logout")

		rec := s.do(http.MethodPost, "/api/v1/auth/logout", "Bearer "+access, fmt.Sprintf(`{"refresh_token":%q}`, refresh))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		s.assertEnded(t, user, access)
		s.assertAccepted(t, user, key, "the API key of the same user")
		fresh, _ := s.login(t, user.Email, atvAPIPassword)
		s.assertAccepted(t, user, "Bearer "+fresh, "a fresh login")
	})

	t.Run("logout by an API-key caller", func(t *testing.T) {
		user := s.register(t, "logout-apikey")
		access, _ := s.login(t, user.Email, atvAPIPassword)
		key := s.apiKey(t, user)
		s.assertAccepted(t, user, "Bearer "+access, "before the logout")

		rec := s.do(http.MethodPost, "/api/v1/auth/logout", key, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		s.assertEnded(t, user, access)
		s.assertAccepted(t, user, key, "the API key that logged out")
		fresh, _ := s.login(t, user.Email, atvAPIPassword)
		s.assertAccepted(t, user, "Bearer "+fresh, "a fresh login")
	})

	t.Run("change-password", func(t *testing.T) {
		user := s.register(t, "change")
		access, refresh := s.login(t, user.Email, atvAPIPassword)
		otherAccess, _ := s.login(t, user.Email, atvAPIPassword)
		key := s.apiKey(t, user)
		s.assertAccepted(t, user, "Bearer "+access, "before the change")

		rec := s.do(http.MethodPost, "/api/v1/auth/change-password", "Bearer "+access,
			fmt.Sprintf(`{"current_password":%q,"new_password":%q,"refresh_token":%q}`, atvAPIPassword, atvAPINew, refresh))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		renewed, _ := atvAPIData(t, rec)["token"].(string)
		require.NotEmpty(t, renewed)

		s.assertEnded(t, user, access)
		s.assertEnded(t, user, otherAccess)
		s.assertAccepted(t, user, "Bearer "+renewed, "the token renewed by the change")
		s.assertAccepted(t, user, key, "the API key of the same user")
		fresh, _ := s.login(t, user.Email, atvAPINew)
		s.assertAccepted(t, user, "Bearer "+fresh, "a fresh login")
	})

	t.Run("password-reset/confirm", func(t *testing.T) {
		user := s.register(t, "reset")
		access, _ := s.login(t, user.Email, atvAPIPassword)
		key := s.apiKey(t, user)
		s.assertAccepted(t, user, "Bearer "+access, "before the reset")

		rec := s.do(http.MethodPost, "/api/v1/auth/password-reset", "", fmt.Sprintf(`{"email":%q}`, user.Email))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, user.Email, s.mailer.lastTo)
		link, err := url.Parse(s.mailer.lastURL)
		require.NoError(t, err)
		token := link.Query().Get("token")
		require.NotEmpty(t, token)

		rec = s.do(http.MethodPost, "/api/v1/auth/password-reset/confirm", "",
			fmt.Sprintf(`{"token":%q,"new_password":%q}`, token, atvAPINew))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		s.assertEnded(t, user, access)
		s.assertAccepted(t, user, key, "the API key of the same user")
		fresh, _ := s.login(t, user.Email, atvAPINew)
		s.assertAccepted(t, user, "Bearer "+fresh, "a fresh login")
	})
}
