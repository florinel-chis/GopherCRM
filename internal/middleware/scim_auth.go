package middleware

import (
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// SCIMTokenIDContextKey holds the id of the SCIM provider token that
// authenticated the request (#468).
const SCIMTokenIDContextKey = "scim_token_id"

// scimAuthFailedDetail is the detail of every SCIM 401, whatever the reason.
const scimAuthFailedDetail = "Authentication failed"

// SCIMAuth authenticates /scim/v2 with a SCIM provider token sent as
// Authorization: Bearer <token> (#468). A user's JWT or API key is not a SCIM
// token and is refused. A refusal writes one sign-in audit row (method scim,
// outcome refused, reason token_missing, token_invalid or token_revoked) and
// answers 401 with the SCIM error body, the same for every reason. On success
// the token id is set in the context; no user is.
func SCIMAuth(tokens service.SCIMTokenService, audit service.AuthAuditService) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := utils.WithClientInfo(
			utils.WithRequestID(c.Request.Context(), c.GetString("request_id")),
			c.ClientIP(), c.Request.UserAgent())
		token, reason := tokens.Authenticate(ctx, c.GetHeader("Authorization"))
		if token == nil {
			if reason == "" {
				reason = service.AuthAuditReasonTokenInvalid
			}
			if audit != nil {
				audit.Record(ctx, service.AuthAuditEntry{
					Method:  service.AuthAuditMethodSCIM,
					Outcome: service.AuthAuditOutcomeRefused,
					Reason:  reason,
				})
			}
			scim.WriteError(c, http.StatusUnauthorized, scimAuthFailedDetail)
			c.Abort()
			return
		}
		c.Set(SCIMTokenIDContextKey, token.ID)
		c.Next()
	}
}
