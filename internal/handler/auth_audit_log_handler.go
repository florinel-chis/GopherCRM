package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// AuthAuditLogHandler serves the read API of the sign-in audit log (#455).
type AuthAuditLogHandler struct {
	service service.AuthAuditLogService
}

// NewAuthAuditLogHandler returns the handler over the audit reader.
func NewAuthAuditLogHandler(s service.AuthAuditLogService) *AuthAuditLogHandler {
	return &AuthAuditLogHandler{service: s}
}

// Rules of the 400 details; the value is never echoed.
const (
	authAuditMethodRule  = "must be password, oidc, refresh, api_key or scim"
	authAuditOutcomeRule = "must be success or refused"
	authAuditReasonRule  = "must be a known reason code"
	authAuditUserIDRule  = "must be a positive integer"
	authAuditTimeRule    = "must be an RFC 3339 date-time"
	authAuditRangeRule   = "must be after from"
)

// AuthAuditEntryResponse is one row of the sign-in audit log: the stored
// columns and nothing joined.
type AuthAuditEntryResponse struct {
	// Row id.
	ID uint `json:"id" example:"42"`
	// UTC time of the outcome (RFC 3339).
	CreatedAt time.Time `json:"created_at" example:"2026-11-18T09:00:00Z"`
	// How the caller tried to sign in.
	Method string `json:"method" enums:"password,oidc,refresh,api_key,scim" example:"password"`
	// Whether the sign-in succeeded.
	Outcome string `json:"outcome" enums:"success,refused" example:"refused"`
	// The stable reason code of a refusal; empty on success.
	Reason string `json:"reason" example:"invalid_credentials"`
	// The account the outcome concerns; null when it is not known.
	UserID *uint `json:"user_id" example:"7"`
	// The single sign-on login attempt of a callback; null otherwise.
	AttemptID *uint `json:"attempt_id" example:"3"`
	// The request id of the sign-in request; empty when none was usable.
	RequestID string `json:"request_id" example:"3f2a9c1e-5b7d-4e2a-9c1e-5b7d4e2a9c1e"`
	// hex(HMAC-SHA256) of a refused single sign-on issuer and subject; null otherwise.
	SubjectRef *string `json:"subject_ref" example:"9b74c9897bac770ffc029102a200c5de"`
	// The roles claim values used for the role mapping; null when not reached.
	ClaimRoles []string `json:"claim_roles"`
	// The groups claim values used for the role mapping; null when not reached.
	ClaimGroups []string `json:"claim_groups"`
	// The client's /24 (IPv4) or /48 (IPv6) network; empty when unknown.
	ClientNetwork string `json:"client_network" example:"203.0.113.0/24"`
	// Browser and operating system family from a fixed vocabulary; empty without a user agent.
	UserAgentFamily string `json:"user_agent_family" example:"Firefox on Linux"`
	// How many outcomes the row counts: 1, or for an api_key refusal the number of refusals of that reason, account and network in that UTC minute. Such a row keeps the request id, user-agent family and created_at of the minute's first refusal.
	Attempts uint `json:"attempts" example:"1"`
}

// decodeAuthAuditClaims decodes a stored JSON array; anything else is nil.
func decodeAuthAuditClaims(stored *string) []string {
	if stored == nil {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(*stored), &values); err != nil || values == nil {
		return nil
	}
	return values
}

// ListSignIns godoc
// @Summary List the sign-in audit log
// @Description One page of the sign-in audit log, newest first (created_at descending, then id descending). Each row is one password login, single sign-on callback, refresh, refused SCIM authentication or successful SCIM user create, replace or patch, or the failed API-key authentications of one reason, account, client network and UTC minute counted in attempts: ids, the method, the outcome, the reason code, the request id, the HMAC subject reference, the claim values used for the role mapping, the client network, the user-agent family and attempts. No email and no name is joined. Rows are pruned after AUTH_AUDIT_RETENTION_DAYS.
// @Description Filters: method, outcome, reason, user_id, from (inclusive) and to (exclusive); an absent or empty value is ignored, values are matched exactly and not trimmed. Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default).
// @Description Permission: audit.sign_ins_view (among the system roles, admin only).
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param method query string false "Only rows of this method (unknown value: 400)" Enums(api_key, oidc, password, refresh, scim)
// @Param outcome query string false "Only rows with this outcome (unknown value: 400)" Enums(refused, success)
// @Param reason query string false "Only rows with this reason code (unknown value, case included: 400)" Enums(account_erased, account_inactive, account_locked, claims_invalid, customer_login_role, discovery_failed, email_missing, email_taken, group_overage, groups_malformed, groups_unavailable, id_token_invalid, id_token_missing, identity_mismatch, identity_taken, internal_error, invalid_credentials, issuer_changed, issuer_invalid, key_expired, key_invalid, key_revoked, last_admin, no_mapping_match, nonce_mismatch, not_configured, password_changed, role_missing, roles_malformed, sso_account, sso_disabled, state_invalid, subject_invalid, token_exchange_failed, token_invalid, token_missing, token_revoked, unavailable, userinfo_subject_mismatch)
// @Param user_id query int false "Only rows of this user (positive integer; otherwise 400)" minimum(1)
// @Param from query string false "Only rows created at or after this RFC 3339 date-time (otherwise 400)" format(date-time)
// @Param to query string false "Only rows created before this RFC 3339 date-time; must be after from (otherwise 400)" format(date-time)
// @Success 200 {object} utils.APIResponse{data=[]AuthAuditEntryResponse,meta=utils.APIMeta} "Sign-in audit rows retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - method, outcome or reason is unknown, user_id is not a positive integer, from or to is not RFC 3339, or from is not before to (error.details keyed by the parameter, to for the range; the value is not echoed)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold audit.sign_ins_view (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /audit/sign-ins [get]
func (h *AuthAuditLogHandler) ListSignIns(c *gin.Context) {
	offset, limit := utils.ParseOffsetLimit(c)
	filter := repository.AuthAuditFilter{
		Method:  c.Query("method"),
		Outcome: c.Query("outcome"),
		Reason:  c.Query("reason"),
	}
	details := map[string]string{}
	if filter.Method != "" && !service.IsAuthAuditMethod(filter.Method) {
		details["method"] = authAuditMethodRule
	}
	if filter.Outcome != "" && !service.IsAuthAuditOutcome(filter.Outcome) {
		details["outcome"] = authAuditOutcomeRule
	}
	if filter.Reason != "" && !service.IsAuthAuditReason(filter.Reason) {
		details["reason"] = authAuditReasonRule
	}
	if raw := c.Query("user_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || id == 0 {
			details["user_id"] = authAuditUserIDRule
		} else {
			userID := uint(id)
			filter.UserID = &userID
		}
	}
	filter.From = authAuditTimeQuery(c, "from", details)
	filter.To = authAuditTimeQuery(c, "to", details)
	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		details["to"] = authAuditRangeRule
	}
	if len(details) > 0 {
		utils.RespondValidationError(c, details)
		return
	}

	entries, total, err := h.service.ListSignIns(c.Request.Context(), filter, offset, limit)
	if err != nil {
		utils.Logger.WithField("operation", "auth_audit_list").WithError(utils.LogError(err)).
			WithField("error_code", utils.ErrCodeInternal).
			Error("Failed to list the sign-in audit log")
		utils.RespondInternalError(c)
		return
	}
	rows := make([]AuthAuditEntryResponse, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, authAuditEntryResponse(entry))
	}
	utils.RespondSuccessWithMeta(c, http.StatusOK, rows, formListMeta(c, offset, limit, total))
}

// authAuditTimeQuery reads an RFC 3339 query parameter as a UTC time: nil
// when absent or empty, and nil with a details entry when it does not parse.
func authAuditTimeQuery(c *gin.Context, name string, details map[string]string) *time.Time {
	raw := c.Query(name)
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		details[name] = authAuditTimeRule
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func authAuditEntryResponse(entry models.AuthAuditLog) AuthAuditEntryResponse {
	return AuthAuditEntryResponse{
		ID:              entry.ID,
		CreatedAt:       entry.CreatedAt.UTC(),
		Method:          entry.Method,
		Outcome:         entry.Outcome,
		Reason:          entry.Reason,
		UserID:          entry.UserID,
		AttemptID:       entry.AttemptID,
		RequestID:       entry.RequestID,
		SubjectRef:      entry.SubjectRef,
		ClaimRoles:      decodeAuthAuditClaims(entry.ClaimRoles),
		ClaimGroups:     decodeAuthAuditClaims(entry.ClaimGroups),
		ClientNetwork:   entry.ClientNetwork,
		UserAgentFamily: entry.UserAgentFamily,
		Attempts:        entry.Attempts,
	}
}
