package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// #336 over HTTP: POST /auth/login on the real handler, middleware,
// repositories and in-memory SQLite (the production one-connection pool),
// wired like cmd/main.go through NewAuthServiceWithSessions with a
// transaction manager, so every login runs the issuing transaction that locks
// the owner row. A live account logs in and gets last_login_at; after a
// password change, a deactivation or an erasure committed through the
// services the old credentials answer 401 `Invalid email or password` and add
// no refresh_tokens row; a login after a logout yields an access token an
// authenticated route accepts. The rate limiters are left out: they are keyed
// on the client IP and take no part in the decision.

const (
	loginLockAPISecret   = "login-owner-lock-api-secret-32-bytes"
	loginLockAPIPassword = "Password123!"
	loginLockAPINew      = "NewSecret123!"
)

type loginLockAPIStack struct {
	db          *gorm.DB
	router      *gin.Engine
	authService service.AuthService
	userService service.UserService
}

func newLoginLockAPIStack(t *testing.T) *loginLockAPIStack {
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
	authService := service.NewAuthServiceWithSessions(
		userRepo, repository.NewAPIKeyRepository(db), repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), &capturingMailer{},
		config.JWTConfig{Secret: loginLockAPISecret, ExpiryHours: 1, RefreshTokenDays: 30},
		"http://localhost:5173", "")
	userService := service.NewUserService(userRepo)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())

	authHandler := handler.NewAuthHandler(authService, userService)
	api := router.Group("/api/v1")
	api.Group("/auth").POST("/login", authHandler.Login)
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupUserRoutes(protected, handler.NewUserHandler(userService))
	protected.Group("/auth").POST("/logout", authHandler.Logout)

	return &loginLockAPIStack{db: db, router: router, authService: authService, userService: userService}
}

func (s *loginLockAPIStack) do(method, path, authorization, body string) *httptest.ResponseRecorder {
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

func (s *loginLockAPIStack) login(email, password string) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
}

func (s *loginLockAPIStack) register(t *testing.T, label string) *models.User {
	t.Helper()
	user := &models.User{Email: label + "@login-lock-api.test", FirstName: "Login", LastName: "Lock",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, s.userService.Register(user, loginLockAPIPassword))
	return user
}

// sessions counts every refresh_tokens row of the user.
func (s *loginLockAPIStack) sessions(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.db.Model(&models.RefreshToken{}).Where("user_id = ?", userID).Count(&n).Error)
	return n
}

func (s *loginLockAPIStack) stored(t *testing.T, userID uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, s.db.Unscoped().First(&user, userID).Error)
	return user
}

// issued checks a 200 login answer and returns its access and refresh tokens.
func loginLockIssued(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	data, ok := response.Data.(map[string]interface{})
	require.True(t, ok, rec.Body.String())
	access, _ := data["token"].(string)
	refresh, _ := data["refresh_token"].(string)
	require.NotEmpty(t, access)
	require.NotEmpty(t, refresh)
	return access, refresh
}

// loginLockRefused checks the one answer every refused login gets.
func loginLockRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	assert.Nil(t, response.Data)
	if assert.NotNil(t, response.Error) {
		assert.Equal(t, "Invalid email or password", response.Error.Message)
	}
}

func TestLoginOwnerLockAPISQLite(t *testing.T) {
	s := newLoginLockAPIStack(t)

	t.Run("a live account logs in, gets both tokens and last_login_at", func(t *testing.T) {
		user := s.register(t, "live")
		require.Nil(t, s.stored(t, user.ID).LastLoginAt)

		access, refresh := loginLockIssued(t, s.login(user.Email, loginLockAPIPassword))
		assert.NotEqual(t, access, refresh)
		assert.NotNil(t, s.stored(t, user.ID).LastLoginAt, "the issuing transaction stamps last_login_at")
		assert.EqualValues(t, 1, s.sessions(t, user.ID))
		rec := s.do(http.MethodGet, "/api/v1/users/me", "Bearer "+access, "")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	changes := []struct {
		name  string
		apply func(t *testing.T, user *models.User)
	}{
		{name: "a password change", apply: func(t *testing.T, user *models.User) {
			_, err := s.authService.ChangePassword(user.ID, loginLockAPIPassword, loginLockAPINew, "")
			require.NoError(t, err)
		}},
		{name: "a deactivation", apply: func(t *testing.T, user *models.User) {
			_, err := s.userService.Update(user.ID, map[string]interface{}{"is_active": false})
			require.NoError(t, err)
		}},
		{name: "an erasure", apply: func(t *testing.T, user *models.User) {
			require.NoError(t, s.userService.Delete(user.ID))
		}},
	}
	for i, change := range changes {
		t.Run("after "+change.name+" the old credentials answer 401 and add no session", func(t *testing.T) {
			user := s.register(t, fmt.Sprintf("refused-%d", i))
			loginLockIssued(t, s.login(user.Email, loginLockAPIPassword))
			change.apply(t, user)
			before := s.sessions(t, user.ID)

			loginLockRefused(t, s.login(user.Email, loginLockAPIPassword))
			assert.Equal(t, before, s.sessions(t, user.ID), "no refresh_tokens row is added")
		})
	}

	t.Run("a login after a logout yields an access token an authenticated route accepts", func(t *testing.T) {
		user := s.register(t, "logout")
		access, _ := loginLockIssued(t, s.login(user.Email, loginLockAPIPassword))
		rec := s.do(http.MethodPost, "/api/v1/auth/logout", "Bearer "+access, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, s.do(http.MethodGet, "/api/v1/users/me", "Bearer "+access, "").Code,
			"the logout ended the earlier access token")

		fresh, _ := loginLockIssued(t, s.login(user.Email, loginLockAPIPassword))
		rec = s.do(http.MethodGet, "/api/v1/users/me", "Bearer "+fresh, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}
