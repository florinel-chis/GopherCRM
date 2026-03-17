package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/gin-gonic/gin"
)

const (
	AccessTokenCookieName  = "access_token"
	RefreshTokenCookieName = "refresh_token"
)

// SetAuthCookies sets both access and refresh tokens as httpOnly cookies
func SetAuthCookies(c *gin.Context, accessToken, refreshToken string, cfg *config.JWTConfig) {
	// Set access token cookie (short-lived)
	SetSecureCookie(c, AccessTokenCookieName, accessToken, 
		time.Duration(cfg.AccessTokenMinutes)*time.Minute, cfg)
	
	// Set refresh token cookie (long-lived)
	SetSecureCookie(c, RefreshTokenCookieName, refreshToken, 
		time.Duration(cfg.RefreshTokenDays)*24*time.Hour, cfg)
}

// SetSecureCookie sets a secure, httpOnly cookie with proper attributes
func SetSecureCookie(c *gin.Context, name, value string, maxAge time.Duration, cfg *config.JWTConfig) {
	sameSite := http.SameSiteLaxMode
	switch cfg.CookieSameSite {
	case "Strict":
		sameSite = http.SameSiteStrictMode
	case "None":
		sameSite = http.SameSiteNoneMode
	default:
		sameSite = http.SameSiteLaxMode
	}

	c.SetSameSite(sameSite)
	c.SetCookie(
		name,                        // name
		value,                       // value
		int(maxAge.Seconds()),       // maxAge
		"/",                         // path
		cfg.CookieDomain,           // domain
		cfg.CookieSecure,           // secure
		true,                       // httpOnly
	)
}

// ClearAuthCookies removes both access and refresh token cookies
func ClearAuthCookies(c *gin.Context, cfg *config.JWTConfig) {
	ClearCookie(c, AccessTokenCookieName, cfg)
	ClearCookie(c, RefreshTokenCookieName, cfg)
}

// ClearCookie removes a cookie by setting it to expire immediately
func ClearCookie(c *gin.Context, name string, cfg *config.JWTConfig) {
	sameSite := http.SameSiteLaxMode
	switch cfg.CookieSameSite {
	case "Strict":
		sameSite = http.SameSiteStrictMode
	case "None":
		sameSite = http.SameSiteNoneMode
	default:
		sameSite = http.SameSiteLaxMode
	}

	c.SetSameSite(sameSite)
	c.SetCookie(
		name,                 // name
		"",                   // value
		-1,                   // maxAge (expire immediately)
		"/",                  // path
		cfg.CookieDomain,    // domain
		cfg.CookieSecure,    // secure
		true,                // httpOnly
	)
}

// GetTokenFromCookie extracts a token from the specified cookie
func GetTokenFromCookie(c *gin.Context, cookieName string) (string, error) {
	return c.Cookie(cookieName)
}

// SetCSRFCookie sets a CSRF token cookie
func SetCSRFCookie(c *gin.Context, token string, cfg *config.CSRFConfig, jwtCfg *config.JWTConfig) {
	sameSite := http.SameSiteLaxMode
	switch jwtCfg.CookieSameSite {
	case "Strict":
		sameSite = http.SameSiteStrictMode
	case "None":
		sameSite = http.SameSiteNoneMode
	default:
		sameSite = http.SameSiteLaxMode
	}

	c.SetSameSite(sameSite)
	c.SetCookie(
		cfg.CookieName,          // name
		token,                   // value
		3600,                    // maxAge (1 hour)
		"/",                     // path
		jwtCfg.CookieDomain,    // domain
		jwtCfg.CookieSecure,    // secure
		false,                  // httpOnly (false for CSRF token so JS can read it)
	)
}

// GenerateSecureToken generates a cryptographically secure random token
func GenerateSecureToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// HashToken creates a SHA256 hash of a token for secure storage
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// ParseSameSite converts string to http.SameSite enum
func ParseSameSite(sameSite string) http.SameSite {
	switch sameSite {
	case "Strict":
		return http.SameSiteStrictMode
	case "None":
		return http.SameSiteNoneMode
	case "Lax":
		return http.SameSiteLaxMode
	default:
		return http.SameSiteLaxMode
	}
}

// GetCookieMaxAge converts time.Duration to int seconds for cookie maxAge
func GetCookieMaxAge(duration time.Duration) int {
	return int(duration.Seconds())
}

// IsCookieSecureContext determines if cookies should be secure based on the request
func IsCookieSecureContext(c *gin.Context) bool {
	// Check if request is HTTPS
	if c.Request.TLS != nil {
		return true
	}
	
	// Check X-Forwarded-Proto header (for reverse proxies)
	if proto := c.GetHeader("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	
	// Check for localhost in development
	host := c.Request.Host
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return false
	}
	
	return false
}

// ValidateCookieConfig validates cookie configuration settings
func ValidateCookieConfig(cfg *config.JWTConfig) error {
	if cfg.CookieSameSite != "Strict" && cfg.CookieSameSite != "Lax" && cfg.CookieSameSite != "None" {
		Logger.Warn("Invalid SameSite cookie setting, defaulting to Lax")
		cfg.CookieSameSite = "Lax"
	}
	
	return nil
}