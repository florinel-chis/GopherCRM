package middleware

import (
	"strings"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// CSRF middleware validates CSRF tokens for state-changing requests
func CSRF(authService service.AuthService, csrfConfig *config.CSRFConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip CSRF protection if disabled
		if !csrfConfig.Enabled {
			c.Next()
			return
		}

		// Skip CSRF protection for safe HTTP methods
		method := c.Request.Method
		if method == "GET" || method == "HEAD" || method == "OPTIONS" {
			c.Next()
			return
		}

		// Skip CSRF protection for API key authentication
		// API keys are for server-to-server communication, not browser sessions
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "ApiKey ") {
			c.Next()
			return
		}

		// Get CSRF token from header
		csrfToken := c.GetHeader(csrfConfig.HeaderName)
		if csrfToken == "" {
			utils.RespondForbidden(c, "CSRF token missing")
			c.Abort()
			return
		}

		// Validate CSRF token
		if !authService.ValidateCSRFToken(csrfToken) {
			utils.RespondForbidden(c, "Invalid CSRF token")
			c.Abort()
			return
		}

		c.Next()
	}
}

// CSRFToken endpoint provides CSRF tokens to clients
func CSRFToken(authService service.AuthService, csrfConfig *config.CSRFConfig, jwtConfig *config.JWTConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !csrfConfig.Enabled {
			utils.RespondSuccess(c, 200, gin.H{"csrf_token": ""})
			return
		}

		token, err := authService.GenerateCSRFToken()
		if err != nil {
			utils.RespondInternalError(c)
			return
		}

		// Set CSRF token in cookie
		utils.SetCSRFCookie(c, token, csrfConfig, jwtConfig)

		utils.RespondSuccess(c, 200, gin.H{"csrf_token": token})
	}
}