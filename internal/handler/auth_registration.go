package handler

import (
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// RegistrationStatusResponse reports whether public sign-up is open.
type RegistrationStatusResponse struct {
	Enabled bool `json:"enabled"`
}

// RegistrationStatus godoc
// @Summary Report whether public registration is open
// @Description Public, unauthenticated probe of the security.allow_public_registration configuration, for the login and registration screens to decide whether to offer sign-up (the authenticated /configurations/ui endpoint cannot serve a visitor who has no session yet). A configuration read failure is reported as disabled — the same answer the gate on POST /auth/register would enforce.
// @Tags auth
// @Produce json
// @Success 200 {object} utils.APIResponse{data=RegistrationStatusResponse} "Whether POST /auth/register accepts sign-ups"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Router /auth/registration [get]
func RegistrationStatus(config middleware.RegistrationConfigReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, err := config.GetBool(models.ConfigKeyAllowPublicRegistration)
		if err != nil {
			enabled = false
		}
		utils.RespondSuccess(c, http.StatusOK, RegistrationStatusResponse{Enabled: enabled})
	}
}
