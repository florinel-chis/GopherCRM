package middleware

import (
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// AuthOption configures Auth.
type AuthOption func(*authOptions)

type authOptions struct {
	apiKeyAudit service.AuthAuditService
}

// WithAPIKeyAudit makes Auth count every failed ApiKey authentication in the
// sign-in audit log (#454), one row per reason, account, network and minute
// (#466). Bearer failures are not recorded, and the answer stays the same 401.
func WithAPIKeyAudit(audit service.AuthAuditService) AuthOption {
	return func(o *authOptions) { o.apiKeyAudit = audit }
}

func Auth(authService service.AuthService, opts ...AuthOption) gin.HandlerFunc {
	var options authOptions
	for _, opt := range opts {
		opt(&options)
	}
	return func(c *gin.Context) {
		var user *models.User
		var err error
		var method string
		var bearerToken string

		// Check for Bearer token
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			bearerToken = strings.TrimPrefix(authHeader, "Bearer ")
			user, err = authService.ValidateToken(bearerToken)
			method = service.AuthMethodBearer
		} else if strings.HasPrefix(authHeader, "ApiKey ") {
			// Check for API Key
			apiKey := strings.TrimPrefix(authHeader, "ApiKey ")
			user, err = authService.ValidateAPIKey(apiKey)
			method = service.AuthMethodAPIKey
		} else {
			utils.RespondUnauthorized(c, "Missing or invalid authorization header")
			c.Abort()
			return
		}

		if err != nil {
			if method == service.AuthMethodAPIKey && options.apiKeyAudit != nil {
				options.apiKeyAudit.RecordAPIKeyFailure(utils.WithClientInfo(
					utils.WithRequestID(c.Request.Context(), c.GetString("request_id")),
					c.ClientIP(), c.Request.UserAgent()), err)
			}
			utils.RespondUnauthorized(c, "Invalid credentials")
			c.Abort()
			return
		}

		// Set user in context
		c.Set("user", user)
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))
		// Handlers that issue session credentials need to know how the
		// request authenticated: an API-key caller must never be handed a
		// browser session (refresh token or JWT).
		c.Set(service.AuthMethodContextKey, method)
		// The sign-in time of a Bearer token, for the routes that need a
		// recent sign-in (#532). A token from a refresh carries none.
		if method == service.AuthMethodBearer {
			if signedInAt, ok := authService.SignedInAt(bearerToken); ok {
				c.Set(service.SignedInAtContextKey, signedInAt)
			}
		}
		// The effective permissions, resolved on every request, for Bearer
		// and ApiKey alike: a role change applies to the very next request.
		publishPermissions(c, permissions.ForUser(user))

		c.Next()
	}
}

func RequireRole(roles ...models.UserRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		userRole, exists := c.Get("user_role")
		if !exists {
			utils.RespondForbidden(c, "Access denied")
			c.Abort()
			return
		}

		currentRole := models.UserRole(userRole.(string))
		for _, role := range roles {
			if currentRole == role {
				c.Next()
				return
			}
		}

		utils.RespondForbidden(c, "Insufficient permissions")
		c.Abort()
	}
}
