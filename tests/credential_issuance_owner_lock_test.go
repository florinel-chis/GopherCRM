package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #342 over HTTP: POST /auth/password-reset, POST /api-keys and
// DELETE /users/{id} on the real handlers, middleware, repositories and
// in-memory SQLite (the production one-connection pool), wired like
// cmd/main.go: the auth service through NewAuthServiceWithSessions and the API
// key service through NewAPIKeyService with the shared user repository and
// transaction manager, so both issuance paths lock the owner row first. The
// rate limiters are left out: they are keyed on the client IP and take no part
// in the decision.

const (
	credLockAPISecret   = "credential-issuance-api-secret-32-bytes"
	credLockAPIPassword = "Password123!"
	credLockResetAnswer = "If an account exists for that email, a password reset link has been sent"
)

type credLockMailer struct {
	mu  sync.Mutex
	to  []string
	url []string
}

func (m *credLockMailer) SendPasswordReset(to, resetURL, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.to = append(m.to, to)
	m.url = append(m.url, resetURL)
	return nil
}

func (m *credLockMailer) Send(string, string, string) error { return nil }

func (m *credLockMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.to)
}

type credLockAPIStack struct {
	db          *gorm.DB
	router      *gin.Engine
	userService service.UserService
	mailer      *credLockMailer
}

func newCredLockAPIStack(t *testing.T) *credLockAPIStack {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	db, err := gorm.Open(sqlite.Open(":memory:"), cfg)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// The user erasure purges the credentials and redacts ticket comments.
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{}, &models.RefreshToken{},
		&models.PasswordResetToken{}, &models.TicketComment{},
	))

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	txManager := utils.NewTransactionManager(db)
	mail := &credLockMailer{}
	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db), txManager, mail,
		config.JWTConfig{Secret: credLockAPISecret, ExpiryHours: 1, RefreshTokenDays: 30},
		"http://localhost:5173", "")
	userService := service.NewUserService(userRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, userRepo, txManager, "")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())

	authHandler := handler.NewAuthHandler(authService, userService)
	api := router.Group("/api/v1")
	authRoutes := api.Group("/auth")
	authRoutes.POST("/login", authHandler.Login)
	authRoutes.POST("/password-reset", authHandler.RequestPasswordReset)
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupUserRoutes(protected, handler.NewUserHandler(userService))
	handler.SetupAPIKeyRoutes(protected, handler.NewAPIKeyHandler(apiKeyService))

	return &credLockAPIStack{db: db, router: router, userService: userService, mailer: mail}
}

func (s *credLockAPIStack) do(method, path, authorization, body string) *httptest.ResponseRecorder {
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

func (s *credLockAPIStack) register(t *testing.T, label string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{Email: label + "@credential-lock-api.test", FirstName: "Cred", LastName: "Lock",
		Role: role, IsActive: true}
	require.NoError(t, s.userService.Register(user, credLockAPIPassword))
	if role != models.RoleCustomer {
		_, err := s.userService.Update(user.ID, map[string]interface{}{"role": role})
		require.NoError(t, err)
	}
	return user
}

func (s *credLockAPIStack) bearer(t *testing.T, email string) string {
	t.Helper()
	rec := s.do(http.MethodPost, "/api/v1/auth/login", "",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, credLockAPIPassword))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]interface{})
	require.True(t, ok, rec.Body.String())
	token, _ := data["token"].(string)
	require.NotEmpty(t, token)
	return "Bearer " + token
}

func (s *credLockAPIStack) count(t *testing.T, model interface{}, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.db.Unscoped().Model(model).Where("user_id = ?", userID).Count(&n).Error)
	return n
}

func (s *credLockAPIStack) requestReset(t *testing.T, email string) {
	t.Helper()
	rec := s.do(http.MethodPost, "/api/v1/auth/password-reset", "", fmt.Sprintf(`{"email":%q}`, email))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]interface{})
	require.True(t, ok, rec.Body.String())
	assert.Equal(t, credLockResetAnswer, data["message"], "the fixed message")
}

func TestCredentialIssuanceOwnerLockAPISQLite(t *testing.T) {
	s := newCredLockAPIStack(t)
	admin := s.register(t, "admin", models.RoleAdmin)
	live := s.register(t, "live", models.RoleSales)
	inactive := s.register(t, "inactive", models.RoleSales)
	_, err := s.userService.Update(inactive.ID, map[string]interface{}{"is_active": false})
	require.NoError(t, err)

	t.Run("a reset request for a live user stores one token and sends one mail", func(t *testing.T) {
		before := s.mailer.count()
		s.requestReset(t, live.Email)
		assert.EqualValues(t, 1, s.count(t, &models.PasswordResetToken{}, live.ID))
		assert.Equal(t, before+1, s.mailer.count())
	})

	t.Run("a reset request for a deactivated user gets the identical answer, no token and no mail", func(t *testing.T) {
		before := s.mailer.count()
		s.requestReset(t, inactive.Email)
		assert.Zero(t, s.count(t, &models.PasswordResetToken{}, inactive.ID))
		assert.Equal(t, before, s.mailer.count())
	})

	t.Run("POST /api-keys by a live user answers 201 and the row exists", func(t *testing.T) {
		rec := s.do(http.MethodPost, "/api/v1/api-keys", s.bearer(t, live.Email), `{"name":"credential lock key"}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var response utils.APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		data, ok := response.Data.(map[string]interface{})
		require.True(t, ok)
		key, _ := data["key"].(string)
		assert.True(t, strings.HasPrefix(key, "gcrm_"))
		assert.EqualValues(t, 1, s.count(t, &models.APIKey{}, live.ID))
	})

	t.Run("DELETE /users/{id} by an admin leaves no api_keys or password_reset_tokens row", func(t *testing.T) {
		require.Positive(t, s.count(t, &models.APIKey{}, live.ID))
		require.Positive(t, s.count(t, &models.PasswordResetToken{}, live.ID))
		rec := s.do(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", live.ID), s.bearer(t, admin.Email), "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Zero(t, s.count(t, &models.APIKey{}, live.ID))
		assert.Zero(t, s.count(t, &models.PasswordResetToken{}, live.ID))

		// The erased owner's address now matches nothing: the same answer, no mail.
		before := s.mailer.count()
		s.requestReset(t, live.Email)
		assert.Equal(t, before, s.mailer.count())
		assert.Zero(t, s.count(t, &models.PasswordResetToken{}, live.ID))
	})
}
