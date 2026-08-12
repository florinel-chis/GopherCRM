package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// capturingMailer implements mailer.Mailer and records the last reset link so
// the test can walk the full password-reset round trip.
type capturingMailer struct {
	lastTo      string
	lastURL     string
	lastSubject string
	lastBody    string
}

func (m *capturingMailer) SendPasswordReset(to, resetURL string) error {
	m.lastTo = to
	m.lastURL = resetURL
	return nil
}

func (m *capturingMailer) Send(to, subject, body string) error {
	m.lastTo = to
	m.lastSubject = subject
	m.lastBody = body
	return nil
}

type AuthSessionIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	userService service.UserService
	mailer      *capturingMailer
}

func (suite *AuthSessionIntegrationTestSuite) SetupSuite() {
	logConfig := &config.LoggingConfig{Level: "error", Format: "json"}
	suite.NoError(utils.InitLogger(logConfig))

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.NoError(err)
	suite.NoError(db.AutoMigrate(
		&models.User{}, &models.APIKey{},
		&models.RefreshToken{}, &models.PasswordResetToken{},
	))
	suite.db = db

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	refreshRepo := repository.NewRefreshTokenRepository(db)
	resetRepo := repository.NewPasswordResetTokenRepository(db)
	suite.mailer = &capturingMailer{}

	jwtConfig := config.JWTConfig{
		Secret:           "integration-test-secret",
		ExpiryHours:      1,
		RefreshTokenDays: 30,
	}
	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, refreshRepo, resetRepo, suite.mailer,
		jwtConfig, "http://localhost:5173", "")
	suite.userService = service.NewUserService(userRepo)

	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.ErrorHandler())

	authHandler := handler.NewAuthHandler(authService, suite.userService)

	api := suite.router.Group("/api/v1")
	{
		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)
		api.POST("/auth/refresh", authHandler.Refresh)
		api.POST("/auth/password-reset", authHandler.RequestPasswordReset)
		api.POST("/auth/password-reset/confirm", authHandler.ConfirmPasswordReset)

		protected := api.Group("")
		protected.Use(middleware.Auth(authService))
		{
			protected.POST("/auth/logout", authHandler.Logout)
			protected.POST("/auth/change-password", authHandler.ChangePassword)
		}
	}
}

func (suite *AuthSessionIntegrationTestSuite) TearDownSuite() {
	sqlDB, _ := suite.db.DB()
	sqlDB.Close()
}

func (suite *AuthSessionIntegrationTestSuite) postJSON(path, bearer string, payload interface{}) *httptest.ResponseRecorder {
	var body *bytes.Buffer
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewBuffer(b)
	} else {
		body = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *AuthSessionIntegrationTestSuite) decodeData(w *httptest.ResponseRecorder) map[string]interface{} {
	var response utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	suite.True(response.Success)
	data, ok := response.Data.(map[string]interface{})
	suite.True(ok)
	return data
}

// TestSessionLifecycle walks login → refresh (rotation) → logout →
// refresh-now-401 against real repositories on SQLite.
func (suite *AuthSessionIntegrationTestSuite) TestSessionLifecycle() {
	user := &models.User{
		Email:     "lifecycle@example.com",
		FirstName: "Life",
		LastName:  "Cycle",
		Role:      models.RoleCustomer,
	}
	suite.NoError(suite.userService.Register(user, "Password123!"))

	// Login: token + refresh_token + user.
	w := suite.postJSON("/api/v1/auth/login", "", map[string]string{
		"email": "lifecycle@example.com", "password": "Password123!",
	})
	suite.Equal(http.StatusOK, w.Code)
	data := suite.decodeData(w)
	accessToken, _ := data["token"].(string)
	refreshToken, _ := data["refresh_token"].(string)
	suite.NotEmpty(accessToken)
	suite.NotEmpty(refreshToken)
	suite.NotNil(data["user"])

	// Refresh: rotates — new pair, old refresh token now dead.
	w = suite.postJSON("/api/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken})
	suite.Equal(http.StatusOK, w.Code)
	data = suite.decodeData(w)
	newAccess, _ := data["token"].(string)
	newRefresh, _ := data["refresh_token"].(string)
	suite.NotEmpty(newAccess)
	suite.NotEmpty(newRefresh)
	suite.NotEqual(refreshToken, newRefresh)

	// Replaying the consumed token must fail: rotation revoked it.
	w = suite.postJSON("/api/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken})
	suite.Equal(http.StatusUnauthorized, w.Code)

	// Logout with no body revokes every session of the user.
	w = suite.postJSON("/api/v1/auth/logout", newAccess, nil)
	suite.Equal(http.StatusOK, w.Code)

	// The rotated refresh token is now dead too.
	w = suite.postJSON("/api/v1/auth/refresh", "", map[string]string{"refresh_token": newRefresh})
	suite.Equal(http.StatusUnauthorized, w.Code)
}

// TestPasswordResetRoundTrip requests a reset, extracts the mailed token,
// confirms with a new password, proves single use and that the old password
// is gone.
func (suite *AuthSessionIntegrationTestSuite) TestPasswordResetRoundTrip() {
	user := &models.User{
		Email:     "resetme@example.com",
		FirstName: "Reset",
		LastName:  "Me",
		Role:      models.RoleCustomer,
	}
	suite.NoError(suite.userService.Register(user, "Password123!"))

	// Unknown email and existing email must answer identically.
	wKnown := suite.postJSON("/api/v1/auth/password-reset", "", map[string]string{"email": "resetme@example.com"})
	wUnknown := suite.postJSON("/api/v1/auth/password-reset", "", map[string]string{"email": "nobody@example.com"})
	suite.Equal(http.StatusOK, wKnown.Code)
	suite.Equal(wKnown.Code, wUnknown.Code)
	suite.Equal(wKnown.Body.String(), wUnknown.Body.String())

	// The mail went to the real account with a tokenized link.
	suite.Equal("resetme@example.com", suite.mailer.lastTo)
	parsed, err := url.Parse(suite.mailer.lastURL)
	suite.NoError(err)
	rawToken := parsed.Query().Get("token")
	suite.NotEmpty(rawToken)

	// Confirm with the mailed token.
	w := suite.postJSON("/api/v1/auth/password-reset/confirm", "", map[string]string{
		"token": rawToken, "new_password": "BrandNewPass1!",
	})
	suite.Equal(http.StatusOK, w.Code)

	// The token is single-use.
	w = suite.postJSON("/api/v1/auth/password-reset/confirm", "", map[string]string{
		"token": rawToken, "new_password": "AnotherPass12!",
	})
	suite.Equal(http.StatusBadRequest, w.Code)

	// Old password dead, new password works.
	w = suite.postJSON("/api/v1/auth/login", "", map[string]string{
		"email": "resetme@example.com", "password": "Password123!",
	})
	suite.Equal(http.StatusUnauthorized, w.Code)
	w = suite.postJSON("/api/v1/auth/login", "", map[string]string{
		"email": "resetme@example.com", "password": "BrandNewPass1!",
	})
	suite.Equal(http.StatusOK, w.Code)
}

// TestChangePasswordFlow proves change-password revokes refresh tokens.
func (suite *AuthSessionIntegrationTestSuite) TestChangePasswordFlow() {
	user := &models.User{
		Email:     "changer@example.com",
		FirstName: "Change",
		LastName:  "Password",
		Role:      models.RoleCustomer,
	}
	suite.NoError(suite.userService.Register(user, "Password123!"))

	w := suite.postJSON("/api/v1/auth/login", "", map[string]string{
		"email": "changer@example.com", "password": "Password123!",
	})
	suite.Equal(http.StatusOK, w.Code)
	data := suite.decodeData(w)
	accessToken, _ := data["token"].(string)
	refreshToken, _ := data["refresh_token"].(string)

	// Wrong current password → 400.
	w = suite.postJSON("/api/v1/auth/change-password", accessToken, map[string]string{
		"current_password": "WrongPass123!", "new_password": "NewSecret123!",
	})
	suite.Equal(http.StatusBadRequest, w.Code)

	// Correct current password → 200, and the refresh token is revoked.
	w = suite.postJSON("/api/v1/auth/change-password", accessToken, map[string]string{
		"current_password": "Password123!", "new_password": "NewSecret123!",
	})
	suite.Equal(http.StatusOK, w.Code)

	w = suite.postJSON("/api/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken})
	suite.Equal(http.StatusUnauthorized, w.Code)

	// Unauthenticated change-password is rejected.
	w = suite.postJSON("/api/v1/auth/change-password", "", map[string]string{
		"current_password": "NewSecret123!", "new_password": "YetAnother12!",
	})
	suite.Equal(http.StatusUnauthorized, w.Code)
}

func TestAuthSessionIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(AuthSessionIntegrationTestSuite))
}
