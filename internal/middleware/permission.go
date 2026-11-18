package middleware

import (
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// publishPermissions stores the caller's effective permissions for the
// permission guards and the handlers. Auth is its only caller.
func publishPermissions(c *gin.Context, set permissions.Set) {
	c.Set(permissions.ContextKey, set)
}

// EffectivePermissions is the caller's permission set, and whether the context
// identifies a caller at all, by the one rule of permissions.Effective: the
// set Auth published wins by presence; only without one, a context that
// carries just user_role answers with that system role's matrix.
func EffectivePermissions(c *gin.Context) (permissions.Set, bool) {
	published, isPublished := c.Get(permissions.ContextKey)
	role, hasRole := c.Get("user_role")
	return permissions.Effective(published, isPublished, role, hasRole)
}

// RequirePermission admits a caller that holds the key at any scope. The
// handler narrows by scope.
func RequirePermission(key string) gin.HandlerFunc {
	return RequireAnyPermission(key)
}

// RequireAnyPermission admits a caller that holds at least one of the keys.
func RequireAnyPermission(keys ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		set, known := EffectivePermissions(c)
		if !known {
			utils.RespondForbidden(c, "Access denied")
			c.Abort()
			return
		}
		for _, key := range keys {
			if set.Has(key) {
				c.Next()
				return
			}
		}
		utils.RespondForbidden(c, "Insufficient permissions")
		c.Abort()
	}
}
