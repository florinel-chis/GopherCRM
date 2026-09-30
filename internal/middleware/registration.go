package middleware

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// RegistrationConfigReader is the one-method slice of the configuration
// service the registration gate needs, so the full ConfigurationService
// interface and its mocks stay untouched.
type RegistrationConfigReader interface {
	GetBool(key string) (bool, error)
}

// RequireRegistrationEnabled guards the public registration endpoint behind
// the security.allow_public_registration configuration. It fails closed: a
// missing row means the shipped default (off), and a configuration read error
// must not reopen sign-up, so both answer the same 403 as a disabled switch.
func RequireRegistrationEnabled(config RegistrationConfigReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, err := config.GetBool(models.ConfigKeyAllowPublicRegistration)
		if err != nil && utils.Logger != nil {
			utils.Logger.WithError(err).Warn("Registration gate could not read configuration; failing closed")
		}
		if err != nil || !enabled {
			utils.RespondForbidden(c, "Public registration is disabled")
			c.Abort()
			return
		}
		c.Next()
	}
}
