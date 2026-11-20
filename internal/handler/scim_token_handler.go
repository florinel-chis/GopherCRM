package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	scimTokenNameRule = "must be 1 to 100 characters after trimming"
	scimTokenIDRule   = "must be a positive integer"
	scimTokenBodyRule = "must be a JSON object with a name"
)

// SCIMTokenHandler administers the SCIM provider tokens (#468).
type SCIMTokenHandler struct {
	tokens service.SCIMTokenService
}

func NewSCIMTokenHandler(tokens service.SCIMTokenService) *SCIMTokenHandler {
	return &SCIMTokenHandler{tokens: tokens}
}

// SCIMTokenResponse is one SCIM provider token, without the token or its hash.
//
// @Description A SCIM provider token. Neither the token nor its hash is ever returned after creation.
type SCIMTokenResponse struct {
	// Token id.
	ID uint `json:"id" example:"3"`
	// The label given at creation.
	Name string `json:"name" example:"Entra ID"`
	// UTC creation time (RFC 3339).
	CreatedAt time.Time `json:"created_at" example:"2026-11-26T09:00:00Z"`
	// The last successful SCIM authentication with the token (best-effort); null when never used.
	LastUsedAt *time.Time `json:"last_used_at" example:"2026-11-26T10:00:00Z"`
	// When the token was revoked; null while it is usable.
	RevokedAt *time.Time `json:"revoked_at" example:"2026-11-27T09:00:00Z"`
}

// SCIMTokenCreatedResponse is the answer of POST /settings/scim/tokens.
//
// @Description A new SCIM provider token. token is shown only in this answer; only its HMAC is stored.
type SCIMTokenCreatedResponse struct {
	// The bearer token for the identity provider: scim_ followed by 64 hex characters. Shown once.
	Token string `json:"token" example:"scim_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`
	// The stored token.
	SCIMToken SCIMTokenResponse `json:"scim_token"`
}

// SCIMTokenCreateRequest is the body of POST /settings/scim/tokens.
//
// @Description Body of POST /settings/scim/tokens.
type SCIMTokenCreateRequest struct {
	// A label for the token, such as the identity provider's name; trimmed, 1 to 100 characters.
	Name string `json:"name" example:"Entra ID"`
}

func newSCIMTokenResponse(token *models.SCIMToken) SCIMTokenResponse {
	return SCIMTokenResponse{
		ID:         token.ID,
		Name:       token.Name,
		CreatedAt:  token.CreatedAt.UTC(),
		LastUsedAt: utcTime(token.LastUsedAt),
		RevokedAt:  utcTime(token.RevokedAt),
	}
}

func utcTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

// scimTokenLog is the logger of one token operation: the operation and the
// token id only, never a token or a name.
func scimTokenLog(operation string) *logrus.Entry {
	return utils.Logger.WithField("operation", operation)
}

// List godoc
// @Summary List the SCIM provider tokens (admin)
// @Description Requires settings.scim_manage (route guard, repeated in the handler); only the admin system role holds it. Every SCIM provider token, revoked ones included, newest first (id descending). Neither a token nor its hash is returned. An identity provider sends a token as Authorization: Bearer <token> on /scim/v2, which lies outside the API base path and is not described here.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]SCIMTokenResponse} "The tokens, newest first"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold settings.scim_manage (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the tokens could not be read"
// @Router /settings/scim/tokens [get]
func (h *SCIMTokenHandler) List(c *gin.Context) {
	if denyWithoutPermission(c, permissions.SettingsSCIMManage) {
		return
	}
	tokens, err := h.tokens.List(c.Request.Context())
	if err != nil {
		scimTokenLog("scim_token_list").WithError(utils.LogError(err)).Error("Failed to list SCIM tokens")
		utils.RespondInternalError(c)
		return
	}
	out := make([]SCIMTokenResponse, 0, len(tokens))
	for i := range tokens {
		out = append(out, newSCIMTokenResponse(&tokens[i]))
	}
	utils.RespondSuccess(c, http.StatusOK, out)
}

// Create godoc
// @Summary Create a SCIM provider token (admin)
// @Description Requires settings.scim_manage (route guard, repeated in the handler); only the admin system role holds it. Issues a bearer token for an identity provider's SCIM client: scim_ followed by the hex of 32 random bytes. The token is returned once, in this answer; only its HMAC-SHA256 under API_KEY_SECRET (else JWT_SECRET) is stored, so rotating that secret invalidates every token.
// @Tags configurations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body SCIMTokenCreateRequest true "The token's label"
// @Success 201 {object} utils.APIResponse{data=SCIMTokenCreatedResponse} "The token, shown once, and the stored row"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: the body is not a JSON object (details.body), or name is blank after trimming or longer than 100 characters (details.name)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold settings.scim_manage (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the token could not be generated or stored"
// @Router /settings/scim/tokens [post]
func (h *SCIMTokenHandler) Create(c *gin.Context) {
	if denyWithoutPermission(c, permissions.SettingsSCIMManage) {
		return
	}
	var req SCIMTokenCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// The decoder's message can quote the submitted value; it is not echoed.
		utils.RespondValidationError(c, map[string]string{"body": scimTokenBodyRule})
		return
	}
	plaintext, token, err := h.tokens.Create(c.Request.Context(), req.Name)
	if err != nil {
		if service.IsSCIMTokenNameInvalid(err) {
			utils.RespondValidationError(c, map[string]string{"name": scimTokenNameRule})
			return
		}
		scimTokenLog("scim_token_create").WithError(utils.LogError(err)).Error("Failed to create a SCIM token")
		utils.RespondInternalError(c)
		return
	}
	scimTokenLog("scim_token_create").WithField("scim_token_id", token.ID).Info("SCIM token created")
	utils.RespondSuccess(c, http.StatusCreated, SCIMTokenCreatedResponse{Token: plaintext, SCIMToken: newSCIMTokenResponse(token)})
}

// Revoke godoc
// @Summary Revoke a SCIM provider token (admin)
// @Description Requires settings.scim_manage (route guard, repeated in the handler); only the admin system role holds it. Stamps revoked_at; from then on the token is refused on /scim/v2 (401, audit reason token_revoked). The row is kept. Revoking a revoked token answers 200 with its original revoked_at.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Token ID" minimum(1)
// @Success 200 {object} utils.APIResponse{data=SCIMTokenResponse} "The revoked token"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: id is not a positive integer (details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold settings.scim_manage (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no token has this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the token could not be revoked or read back"
// @Router /settings/scim/tokens/{id} [delete]
func (h *SCIMTokenHandler) Revoke(c *gin.Context) {
	if denyWithoutPermission(c, permissions.SettingsSCIMManage) {
		return
	}
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || id == 0 {
		utils.RespondValidationError(c, map[string]string{"id": scimTokenIDRule})
		return
	}
	token, err := h.tokens.Revoke(c.Request.Context(), uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			utils.RespondNotFound(c, "SCIM token not found")
			return
		}
		scimTokenLog("scim_token_revoke").WithField("scim_token_id", id).WithError(utils.LogError(err)).Error("Failed to revoke a SCIM token")
		utils.RespondInternalError(c)
		return
	}
	scimTokenLog("scim_token_revoke").WithField("scim_token_id", token.ID).Info("SCIM token revoked")
	utils.RespondSuccess(c, http.StatusOK, newSCIMTokenResponse(token))
}
