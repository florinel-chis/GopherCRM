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
// token and is refused. A refusal is counted in the aggregated sign-in audit
// row of its reason, client network and UTC minute (method scim, outcome
// refused, reason token_missing, token_invalid or token_revoked; #553) and
// answers 401 with the SCIM error body, the same for every reason. On success
// the token id is set in the context; no user is.
//
// limits applies the two SCIM limits (#555), whatever the HTTP method; nil
// applies none. An accepted request is charged to the SCIM token limit of
// its token and never to the client address. A refused request is charged to
// the SCIM refusal limit of its client address before the audit write; over
// that limit it answers the SCIM 429 and records nothing in the audit log. The refusal
// limit is judged after the token lookup by design: a throttled refusal costs
// one indexed lookup, and in exchange refusals from a shared provider address
// never hold back a valid token.
func SCIMAuth(tokens service.SCIMTokenService, audit service.AuthAuditService, limits *SCIMRateLimits) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := utils.WithClientInfo(
			utils.WithRequestID(c.Request.Context(), c.GetString("request_id")),
			c.ClientIP(), c.Request.UserAgent())
		token, reason := tokens.Authenticate(ctx, c.GetHeader("Authorization"))
		if token == nil {
			if !limits.admitRefusal(c) {
				return
			}
			if reason == "" {
				reason = service.AuthAuditReasonTokenInvalid
			}
			if audit != nil {
				audit.RecordAggregated(ctx, service.AuthAuditEntry{
					Method:  service.AuthAuditMethodSCIM,
					Outcome: service.AuthAuditOutcomeRefused,
					Reason:  reason,
				})
			}
			scim.WriteError(c, http.StatusUnauthorized, scimAuthFailedDetail)
			c.Abort()
			return
		}
		if !limits.admitToken(c, token.ID) {
			return
		}
		c.Set(SCIMTokenIDContextKey, token.ID)
		c.Next()
	}
}
