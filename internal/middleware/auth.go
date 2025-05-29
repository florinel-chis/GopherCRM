package middleware

import (
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

func Auth(authService service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var user *models.User
		var err error

		// Check for Bearer token
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			user, err = authService.ValidateToken(token)
		} else if strings.HasPrefix(authHeader, "ApiKey ") {
			// Check for API Key
			apiKey := strings.TrimPrefix(authHeader, "ApiKey ")
			user, err = authService.ValidateAPIKey(apiKey)
		} else {
			utils.RespondUnauthorized(c, "Missing or invalid authorization header")
			c.Abort()
			return
		}

		if err != nil {
			utils.RespondUnauthorized(c, "Invalid credentials")
			c.Abort()
			return
		}

		// Set user in context
		c.Set("user", user)
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))

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