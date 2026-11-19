package handler

import (
	"context"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// RegistrationStatusResponse reports whether public sign-up is open and
// whether single sign-on is on.
//
// @Description Whether public sign-up is open and whether single sign-on is on.
type RegistrationStatusResponse struct {
	// True when POST /auth/register currently accepts sign-ups; false when the
	// setting is off or could not be read.
	Enabled bool `json:"enabled" example:"false"`
	// True when the effective single sign-on switch is on; false when it is
	// off or cannot be read.
	SSOEnabled bool `json:"sso_enabled" example:"false"`
}

// SSOStatusReader is the part of the single sign-on settings the public
// registration probe reads: only the effective switch is reported.
type SSOStatusReader interface {
	SSOConfig(ctx context.Context) (service.EffectiveSSOConfig, error)
}

// RegistrationStatus godoc
// @Summary Report whether public registration is open and whether single sign-on is on
// @Description Public, unauthenticated probe of the security.allow_public_registration setting, so the login and sign-up screens can decide whether to offer registration (the authenticated GET /configurations/ui cannot serve a visitor without a session). A setting that cannot be read is reported as disabled, the same answer the gate on POST /auth/register enforces. Read-only, no side effects.
// @Description The probe also reports sso_enabled, the effective single sign-on switch, so the login screen can offer the single sign-on button; nothing else of the single sign-on configuration is exposed. A configuration that cannot be read is reported as off. POST /auth/oidc/start still enforces the switch.
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route (so polling it consumes the login budget).
// @Tags auth
// @Produce json
// @Success 200 {object} utils.APIResponse{data=RegistrationStatusResponse} "Whether POST /auth/register accepts sign-ups and whether single sign-on is on"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Router /auth/registration [get]
func RegistrationStatus(config middleware.RegistrationConfigReader, sso SSOStatusReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, err := config.GetBool(models.ConfigKeyAllowPublicRegistration)
		if err != nil {
			enabled = false
		}
		ssoEnabled := false
		if sso != nil {
			if cfg, ssoErr := sso.SSOConfig(c.Request.Context()); ssoErr == nil {
				ssoEnabled = cfg.Enabled
			}
		}
		utils.RespondSuccess(c, http.StatusOK, RegistrationStatusResponse{Enabled: enabled, SSOEnabled: ssoEnabled})
	}
}
