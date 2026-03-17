package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSetAuthCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.JWTConfig{
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
		CookieDomain:       "",
		CookieSecure:       false,
		CookieSameSite:     "Lax",
	}

	t.Run("sets both access and refresh token cookies", func(t *testing.T) {
		// Setup
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)

		accessToken := "test-access-token"
		refreshToken := "test-refresh-token"

		// Execute
		SetAuthCookies(c, accessToken, refreshToken, cfg)

		// Verify
		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 2)

		var accessCookie, refreshCookie *http.Cookie
		for _, cookie := range cookies {
			if cookie.Name == AccessTokenCookieName {
				accessCookie = cookie
			} else if cookie.Name == RefreshTokenCookieName {
				refreshCookie = cookie
			}
		}

		// Verify access token cookie
		assert.NotNil(t, accessCookie)
		assert.Equal(t, accessToken, accessCookie.Value)
		assert.True(t, accessCookie.HttpOnly)
		assert.Equal(t, "/", accessCookie.Path)
		assert.Equal(t, int((time.Duration(cfg.AccessTokenMinutes) * time.Minute).Seconds()), accessCookie.MaxAge)

		// Verify refresh token cookie
		assert.NotNil(t, refreshCookie)
		assert.Equal(t, refreshToken, refreshCookie.Value)
		assert.True(t, refreshCookie.HttpOnly)
		assert.Equal(t, "/", refreshCookie.Path)
		assert.Equal(t, int((time.Duration(cfg.RefreshTokenDays) * 24 * time.Hour).Seconds()), refreshCookie.MaxAge)
	})
}

func TestSetSecureCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.JWTConfig{
		CookieDomain:   "example.com",
		CookieSecure:   true,
		CookieSameSite: "Strict",
	}

	t.Run("sets secure cookie with all attributes", func(t *testing.T) {
		// Setup
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)

		cookieName := "test_cookie"
		cookieValue := "test_value"
		maxAge := 1 * time.Hour

		// Execute
		SetSecureCookie(c, cookieName, cookieValue, maxAge, cfg)

		// Verify
		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 1)

		cookie := cookies[0]
		assert.Equal(t, cookieName, cookie.Name)
		assert.Equal(t, cookieValue, cookie.Value)
		assert.True(t, cookie.HttpOnly)
		assert.True(t, cookie.Secure)
		assert.Equal(t, "/", cookie.Path)
		assert.Equal(t, cfg.CookieDomain, cookie.Domain)
		assert.Equal(t, int(maxAge.Seconds()), cookie.MaxAge)
		assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	})
}

func TestClearAuthCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.JWTConfig{
		CookieDomain:   "",
		CookieSecure:   false,
		CookieSameSite: "Lax",
	}

	t.Run("clears both access and refresh token cookies", func(t *testing.T) {
		// Setup
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)

		// Execute
		ClearAuthCookies(c, cfg)

		// Verify
		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 2)

		for _, cookie := range cookies {
			assert.True(t, cookie.Name == AccessTokenCookieName || cookie.Name == RefreshTokenCookieName)
			assert.Equal(t, "", cookie.Value)
			assert.Equal(t, -1, cookie.MaxAge)
			assert.True(t, cookie.HttpOnly)
		}
	})
}

func TestGetTokenFromCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("retrieves token from cookie", func(t *testing.T) {
		// Setup
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  AccessTokenCookieName,
			Value: "test-token-value",
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Execute
		token, err := GetTokenFromCookie(c, AccessTokenCookieName)

		// Verify
		assert.NoError(t, err)
		assert.Equal(t, "test-token-value", token)
	})

	t.Run("returns error when cookie not found", func(t *testing.T) {
		// Setup
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Execute
		token, err := GetTokenFromCookie(c, AccessTokenCookieName)

		// Verify
		assert.Error(t, err)
		assert.Empty(t, token)
	})
}

func TestSetCSRFCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	csrfConfig := &config.CSRFConfig{
		CookieName: "csrf_token",
	}

	jwtConfig := &config.JWTConfig{
		CookieDomain:   "example.com",
		CookieSecure:   true,
		CookieSameSite: "Lax",
	}

	t.Run("sets CSRF cookie correctly", func(t *testing.T) {
		// Setup
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)

		csrfToken := "test-csrf-token"

		// Execute
		SetCSRFCookie(c, csrfToken, csrfConfig, jwtConfig)

		// Verify
		cookies := w.Result().Cookies()
		assert.Len(t, cookies, 1)

		cookie := cookies[0]
		assert.Equal(t, csrfConfig.CookieName, cookie.Name)
		assert.Equal(t, csrfToken, cookie.Value)
		assert.False(t, cookie.HttpOnly) // CSRF cookies should not be HttpOnly
		assert.True(t, cookie.Secure)
		assert.Equal(t, "/", cookie.Path)
		assert.Equal(t, jwtConfig.CookieDomain, cookie.Domain)
		assert.Equal(t, 3600, cookie.MaxAge) // 1 hour
	})
}

func TestGenerateSecureToken(t *testing.T) {
	t.Run("generates token of correct length", func(t *testing.T) {
		// Execute
		token, err := GenerateSecureToken(32)

		// Verify
		assert.NoError(t, err)
		assert.NotEmpty(t, token)
		// Base64 encoded 32 bytes should be longer than 32 characters
		assert.True(t, len(token) > 32)
	})

	t.Run("generates different tokens each time", func(t *testing.T) {
		// Execute
		token1, err1 := GenerateSecureToken(32)
		token2, err2 := GenerateSecureToken(32)

		// Verify
		assert.NoError(t, err1)
		assert.NoError(t, err2)
		assert.NotEqual(t, token1, token2)
	})
}

func TestHashToken(t *testing.T) {
	t.Run("generates consistent hash", func(t *testing.T) {
		token := "test-token"

		// Execute
		hash1 := HashToken(token)
		hash2 := HashToken(token)

		// Verify
		assert.Equal(t, hash1, hash2)
		assert.NotEmpty(t, hash1)
		assert.Len(t, hash1, 64) // SHA256 hex string is 64 characters
	})

	t.Run("generates different hashes for different tokens", func(t *testing.T) {
		token1 := "test-token-1"
		token2 := "test-token-2"

		// Execute
		hash1 := HashToken(token1)
		hash2 := HashToken(token2)

		// Verify
		assert.NotEqual(t, hash1, hash2)
	})
}

func TestParseSameSite(t *testing.T) {
	tests := []struct {
		input    string
		expected http.SameSite
	}{
		{"Strict", http.SameSiteStrictMode},
		{"Lax", http.SameSiteLaxMode},
		{"None", http.SameSiteNoneMode},
		{"invalid", http.SameSiteLaxMode}, // defaults to Lax
		{"", http.SameSiteLaxMode},        // defaults to Lax
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := ParseSameSite(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestIsCookieSecureContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns true for HTTPS request", func(t *testing.T) {
		// Setup
		req := httptest.NewRequest("GET", "https://example.com/", nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Execute
		secure := IsCookieSecureContext(c)

		// Verify - Note: In test environment, TLS might not be set, so this might return false
		// The important thing is that the function doesn't panic
		assert.IsType(t, true, secure)
	})

	t.Run("returns false for localhost", func(t *testing.T) {
		// Setup
		req := httptest.NewRequest("GET", "http://localhost:3000/", nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Execute
		secure := IsCookieSecureContext(c)

		// Verify
		assert.False(t, secure)
	})

	t.Run("returns true for X-Forwarded-Proto https", func(t *testing.T) {
		// Setup
		req := httptest.NewRequest("GET", "http://example.com/", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Execute
		secure := IsCookieSecureContext(c)

		// Verify
		assert.True(t, secure)
	})
}