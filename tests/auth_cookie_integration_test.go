package tests

import (
	"bytes"
	"encoding/json"
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
)

type AuthCookieIntegrationTestSuite struct {
	BaseIntegrationTestSuite
	authHandler *handler.AuthHandler
	authService service.AuthService
	userService service.UserService
	router      *gin.Engine
}

func (suite *AuthCookieIntegrationTestSuite) SetupSuite() {
	suite.BaseIntegrationTestSuite.SetupSuite()

	// Setup repositories
	userRepo := repository.NewUserRepository(models.DB)
	apiKeyRepo := repository.NewAPIKeyRepository(models.DB)
	refreshTokenRepo := repository.NewRefreshTokenRepository(models.DB)

	// Setup configs
	jwtConfig := config.JWTConfig{
		Secret:             "test-secret-key",
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
		CookieDomain:       "",
		CookieSecure:       false,
		CookieSameSite:     "Lax",
	}

	csrfConfig := config.CSRFConfig{
		Secret:     "test-csrf-secret",
		CookieName: "csrf_token",
		HeaderName: "X-CSRF-Token",
		Enabled:    true,
	}

	// Setup services
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, refreshTokenRepo, jwtConfig, csrfConfig)
	suite.userService = service.NewUserService(userRepo)

	// Setup handler
	suite.authHandler = handler.NewAuthHandler(suite.authService, suite.userService, &jwtConfig, &csrfConfig)

	// Setup router
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.CORS())

	// Setup routes
	api := suite.router.Group("/api/v1")
	{
		api.POST("/auth/register", suite.authHandler.Register)
		api.POST("/auth/login", suite.authHandler.Login)
		api.POST("/auth/refresh", suite.authHandler.Refresh)
		api.POST("/auth/logout", suite.authHandler.Logout)
		api.GET("/auth/csrf", middleware.CSRFToken(suite.authService, &csrfConfig, &jwtConfig))

		protected := api.Group("")
		protected.Use(middleware.Auth(suite.authService))
		{
			protected.GET("/auth/me", func(c *gin.Context) {
				user, _ := c.Get("user")
				utils.RespondSuccess(c, http.StatusOK, user)
			})
		}
	}
}

func (suite *AuthCookieIntegrationTestSuite) TestRegisterWithCookies() {
	// Create register request
	registerReq := handler.RegisterRequest{
		Email:     "cookietest@example.com",
		Password:  "password123",
		FirstName: "Cookie",
		LastName:  "Test",
		Role:      models.RoleCustomer,
	}

	reqBody, _ := json.Marshal(registerReq)
	req := httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify response
	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	var response handler.AuthResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), response.User)
	assert.Equal(suite.T(), "cookietest@example.com", response.User.Email)
	assert.NotEmpty(suite.T(), response.CSRFToken)

	// Verify cookies are set
	cookies := w.Result().Cookies()
	assert.Len(suite.T(), cookies, 3) // access_token, refresh_token, csrf_token

	var accessCookie, refreshCookie, csrfCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case utils.AccessTokenCookieName:
			accessCookie = cookie
		case utils.RefreshTokenCookieName:
			refreshCookie = cookie
		case "csrf_token":
			csrfCookie = cookie
		}
	}

	// Verify access token cookie
	assert.NotNil(suite.T(), accessCookie)
	assert.NotEmpty(suite.T(), accessCookie.Value)
	assert.True(suite.T(), accessCookie.HttpOnly)
	assert.Equal(suite.T(), "/", accessCookie.Path)

	// Verify refresh token cookie
	assert.NotNil(suite.T(), refreshCookie)
	assert.NotEmpty(suite.T(), refreshCookie.Value)
	assert.True(suite.T(), refreshCookie.HttpOnly)
	assert.Equal(suite.T(), "/", refreshCookie.Path)

	// Verify CSRF token cookie
	assert.NotNil(suite.T(), csrfCookie)
	assert.NotEmpty(suite.T(), csrfCookie.Value)
	assert.False(suite.T(), csrfCookie.HttpOnly) // CSRF tokens should not be HttpOnly
}

func (suite *AuthCookieIntegrationTestSuite) TestLoginWithCookies() {
	// First, create a user
	user := &models.User{
		Email:     "logintest@example.com",
		FirstName: "Login",
		LastName:  "Test",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	assert.NoError(suite.T(), err)

	// Create login request
	loginReq := handler.LoginRequest{
		Email:    "logintest@example.com",
		Password: "password123",
	}

	reqBody, _ := json.Marshal(loginReq)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify response
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response handler.AuthResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), response.User)
	assert.Equal(suite.T(), "logintest@example.com", response.User.Email)

	// Verify cookies are set
	cookies := w.Result().Cookies()
	assert.GreaterOrEqual(suite.T(), len(cookies), 2) // at least access_token and refresh_token
}

func (suite *AuthCookieIntegrationTestSuite) TestAuthenticatedRequestWithCookie() {
	// First, login to get cookies
	user := &models.User{
		Email:     "authtest@example.com",
		FirstName: "Auth",
		LastName:  "Test",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	assert.NoError(suite.T(), err)

	// Login
	loginReq := handler.LoginRequest{
		Email:    "authtest@example.com",
		Password: "password123",
	}

	reqBody, _ := json.Marshal(loginReq)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Extract cookies from login response
	loginCookies := w.Result().Cookies()
	var accessCookie *http.Cookie
	for _, cookie := range loginCookies {
		if cookie.Name == utils.AccessTokenCookieName {
			accessCookie = cookie
			break
		}
	}
	assert.NotNil(suite.T(), accessCookie)

	// Make authenticated request using the cookie
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(accessCookie)

	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify authenticated request succeeds
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var userResponse models.User
	err = json.Unmarshal(w.Body.Bytes(), &userResponse)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "authtest@example.com", userResponse.Email)
}

func (suite *AuthCookieIntegrationTestSuite) TestRefreshToken() {
	// First, create and login a user
	user := &models.User{
		Email:     "refreshtest@example.com",
		FirstName: "Refresh",
		LastName:  "Test",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	assert.NoError(suite.T(), err)

	// Login to get tokens
	tokens, err := suite.authService.LoginWithTokens("refreshtest@example.com", "password123")
	assert.NoError(suite.T(), err)

	// Create a request with refresh token cookie
	req := httptest.NewRequest("POST", "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{
		Name:  utils.RefreshTokenCookieName,
		Value: tokens.RefreshToken,
	})

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify refresh succeeds
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Verify new cookies are set
	cookies := w.Result().Cookies()
	assert.GreaterOrEqual(suite.T(), len(cookies), 2) // at least new access_token and refresh_token

	var newAccessCookie, newRefreshCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case utils.AccessTokenCookieName:
			newAccessCookie = cookie
		case utils.RefreshTokenCookieName:
			newRefreshCookie = cookie
		}
	}

	assert.NotNil(suite.T(), newAccessCookie)
	assert.NotNil(suite.T(), newRefreshCookie)
	assert.NotEmpty(suite.T(), newAccessCookie.Value)
	assert.NotEmpty(suite.T(), newRefreshCookie.Value)
}

func (suite *AuthCookieIntegrationTestSuite) TestLogout() {
	// First, create and login a user
	user := &models.User{
		Email:     "logouttest@example.com",
		FirstName: "Logout",
		LastName:  "Test",
		Role:      models.RoleCustomer,
	}
	err := suite.userService.Register(user, "password123")
	assert.NoError(suite.T(), err)

	// Login to get tokens
	tokens, err := suite.authService.LoginWithTokens("logouttest@example.com", "password123")
	assert.NoError(suite.T(), err)

	// Create logout request with cookies
	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{
		Name:  utils.AccessTokenCookieName,
		Value: tokens.AccessToken,
	})
	req.AddCookie(&http.Cookie{
		Name:  utils.RefreshTokenCookieName,
		Value: tokens.RefreshToken,
	})

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify logout succeeds
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Verify cookies are cleared (MaxAge = -1)
	cookies := w.Result().Cookies()
	for _, cookie := range cookies {
		if cookie.Name == utils.AccessTokenCookieName || cookie.Name == utils.RefreshTokenCookieName {
			assert.Equal(suite.T(), "", cookie.Value)
			assert.Equal(suite.T(), -1, cookie.MaxAge)
		}
	}
}

func (suite *AuthCookieIntegrationTestSuite) TestCSRFTokenEndpoint() {
	req := httptest.NewRequest("GET", "/api/v1/auth/csrf", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	// Verify CSRF token endpoint works
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Contains(suite.T(), response, "csrf_token")
	assert.NotEmpty(suite.T(), response["csrf_token"])

	// Verify CSRF cookie is set
	cookies := w.Result().Cookies()
	var csrfCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == "csrf_token" {
			csrfCookie = cookie
			break
		}
	}
	assert.NotNil(suite.T(), csrfCookie)
	assert.NotEmpty(suite.T(), csrfCookie.Value)
	assert.False(suite.T(), csrfCookie.HttpOnly) // CSRF cookies should not be HttpOnly
}

func TestAuthCookieIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(AuthCookieIntegrationTestSuite))
}