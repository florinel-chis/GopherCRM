package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// sessionCaller reports whether the request was authenticated by a signed-in
// session (a Bearer JWT, from a password or a single sign-on sign-in). An
// unset auth method is not a session.
func sessionCaller(c *gin.Context) bool {
	return c.GetString(service.AuthMethodContextKey) == service.AuthMethodBearer
}

// apiKeyCreateSignInWindow is how recent the sign-in of the session that
// creates an API key must be (#532). A sign-in exactly this old is accepted.
const apiKeyCreateSignInWindow = 15 * time.Minute

type APIKeyHandler struct {
	apiKeyService service.APIKeyService
	// now is an instance-local clock for the sign-in window; nil uses
	// time.Now.
	now func() time.Time
}

func (h *APIKeyHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func NewAPIKeyHandler(apiKeyService service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{apiKeyService: apiKeyService}
}

// @Description Body of POST /api-keys.
type CreateAPIKeyRequest struct {
	// Label for the key, 3 to 100 characters. Names need not be unique.
	Name string `json:"name" binding:"required,min=3,max=100" minLength:"3" maxLength:"100" example:"CI pipeline"`
	// ExpiresAt is an optional RFC3339 timestamp. Left empty the key expires
	// in one year. It is taken as a string rather than a time.Time so a
	// malformed value produces a plain 400 with a message naming the field,
	// instead of the opaque unmarshal error the time codec would raise.

	// Optional expiry as an RFC 3339 timestamp, which must lie in the future
	// and at most one year from now. Omitted or empty: the key expires one
	// year from now.
	ExpiresAt string `json:"expires_at" format:"date-time" example:"2026-12-31T23:59:59Z"`
}

// @Description Result of POST /api-keys: the plaintext key, shown only this once, and the stored record.
type CreateAPIKeyResponse struct {
	// The plaintext key: gcrm_ followed by 64 hexadecimal characters. Returned
	// only here; store it now, it cannot be retrieved later.
	Key string `json:"key" example:"gcrm_3f9a1c2b7d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8"`
	// The stored key record (prefix, expiry, active flag).
	APIKey *models.APIKey `json:"api_key"`
}

// UpdateAPIKeyRequest carries pointers so an omitted field is distinguishable
// from a zero value — omitting is_active must not silently deactivate a key.
//
// @Description Body of PUT /api-keys/{id}. At least one field must be present.
type UpdateAPIKeyRequest struct {
	// New label, 3 to 100 characters; omitted or null keeps the current one.
	Name *string `json:"name" binding:"omitempty,min=3,max=100" minLength:"3" maxLength:"100" example:"CI pipeline (rotated)"`
	// false revokes the key; true is accepted only on a live key and changes
	// nothing (a revoked key cannot be switched on again, 409). Omitted or null
	// keeps the current state.
	IsActive *bool `json:"is_active" example:"false"`
}

// Create godoc
// @Summary Create an API key
// @Description Staff accounts only (admin, sales, support or a custom role), from a signed-in session (a bearer token from a password or single sign-on sign-in) only: a request authenticated with an API key is 403, and so is a customer account. The session's sign-in must be at most 15 minutes old: a bearer token from POST /auth/refresh carries no sign-in time, so after a refresh the user signs in again to create a key. The key always belongs to the caller. The plaintext key (gcrm_ followed by 64 hexadecimal characters) is returned only in this response: only an HMAC of it is stored, so it can never be shown again. Use it as `Authorization: ApiKey <key>`; the gcrm_ prefix may be sent or left off. The key acts with the owner's role and stops working when it is revoked, when it expires, or when the owner is deactivated or erased.
// @Description expires_at is optional: an RFC 3339 timestamp (for example 2026-12-31T23:59:59Z) that must lie in the future and at most one year from now; omitted or empty, the key expires one year from now. Expiry cannot be changed afterwards. There is no limit on the number of keys per user.
// @Tags api-keys
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateAPIKeyRequest true "Key name and optional expiry"
// @Success 201 {object} utils.APIResponse{data=CreateAPIKeyResponse} "Key created; key holds the plaintext, shown only this once"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: name missing or not 3 to 100 characters (details maps Name to a message). BAD_REQUEST: malformed JSON (Invalid request format), expires_at must be an RFC3339 timestamp, expires_at must be in the future, or expires_at must be at most one year from now"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: API keys can be created only from a signed-in session (the request was authenticated with an API key), or Customer accounts cannot hold API keys. REAUTHENTICATION_REQUIRED: the session's sign-in is more than 15 minutes old, or the token carries no sign-in time (a token from POST /auth/refresh). The body is not read first, and nothing is written"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the key could not be generated or stored"
// @Router /api-keys [post]
func (h *APIKeyHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.Create")

	if !sessionCaller(c) {
		logger.WithField("user_id", c.GetUint("user_id")).WithField("error_code", "session_required").
			Warn("API key not created: not a signed-in session")
		utils.RespondForbidden(c, "API keys can be created only from a signed-in session")
		return
	}

	if customerAccount(c) {
		logger.WithField("user_id", c.GetUint("user_id")).WithField("error_code", "customer_account").
			Warn("API key not created: customer account")
		utils.RespondForbidden(c, "Customer accounts cannot hold API keys")
		return
	}

	signedInAt, ok := c.Get(service.SignedInAtContextKey)
	signInTime, isTime := signedInAt.(time.Time)
	if !ok || !isTime || h.clock().Sub(signInTime) > apiKeyCreateSignInWindow {
		logger.WithField("user_id", c.GetUint("user_id")).WithField("error_code", "reauthentication_required").
			Warn("API key not created: no recent sign-in")
		utils.RespondError(c, http.StatusForbidden, "REAUTHENTICATION_REQUIRED",
			"Sign in again to create an API key: creating a key needs a sign-in within the last 15 minutes", nil)
		return
	}

	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			utils.RespondBadRequest(c, "expires_at must be an RFC3339 timestamp")
			return
		}
		// A key born expired can never authenticate; that is a client bug, not a
		// credential worth issuing.
		if !parsed.After(time.Now()) {
			utils.RespondBadRequest(c, "expires_at must be in the future")
			return
		}
		expiresAt = &parsed
	}

	userID := c.GetUint("user_id")

	key, apiKey, err := h.apiKeyService.Generate(userID, req.Name, expiresAt)
	if err != nil {
		// The owner was erased, soft-deleted or deactivated after the request
		// authenticated: the issuing transaction refused it under the owner
		// lock (#342). That is the 401 a deactivated or erased account gets.
		if errors.Is(err, service.ErrAccountUnavailable) {
			logger.WithField("user_id", userID).WithField("error_code", "account_unavailable").
				Warn("API key not issued: owner unavailable")
			utils.RespondUnauthorized(c, "Unauthorized")
			return
		}
		if errors.Is(err, service.ErrAPIKeyExpiryTooLate) {
			utils.RespondBadRequest(c, "expires_at must be at most one year from now")
			return
		}
		// The owner became a customer account after the request
		// authenticated: refused under the owner lock (#532).
		if errors.Is(err, service.ErrAPIKeyCustomerAccount) {
			logger.WithField("user_id", userID).WithField("error_code", "customer_account").
				Warn("API key not issued: customer account")
			utils.RespondForbidden(c, "Customer accounts cannot hold API keys")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to generate API key")
		utils.RespondInternalError(c)
		return
	}

	response := CreateAPIKeyResponse{
		Key:    key,
		APIKey: apiKey,
	}
	utils.LogHandlerResponse(logger, http.StatusCreated, response)
	utils.RespondSuccess(c, http.StatusCreated, response)
}

// List godoc
// @Summary List the caller's API keys
// @Description Any authenticated role. Returns every key of the caller as a bare array, revoked and expired ones included, with no pagination, filtering or sorting parameters. There is no admin-wide view: each user sees only their own keys. Only the prefix of each key is returned, never the key or its hash.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.APIKey} "The caller's keys"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the keys could not be read"
// @Router /api-keys [get]
func (h *APIKeyHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.List")

	userID := c.GetUint("user_id")

	apiKeys, err := h.apiKeyService.List(userID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list API keys")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKeys)
	utils.RespondSuccess(c, http.StatusOK, apiKeys)
}

// Get godoc
// @Summary Get one of the caller's API keys
// @Description Any authenticated role, for its own keys only: a key of another user is 403, with no admin override. Only the prefix is returned, never the key or its hash.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Success 200 {object} utils.APIResponse{data=models.APIKey} "The key"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid API key ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the key belongs to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no key with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the key could not be read"
// @Router /api-keys/{id} [get]
func (h *APIKeyHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.Get")

	idParam := c.Param("id")
	id, err := strconv.ParseUint(idParam, 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid API key ID")
		return
	}

	userID := c.GetUint("user_id")

	apiKey, err := h.apiKeyService.GetByID(uint(id), userID)
	if err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.Warn("Unauthorized attempt to read API key")
			utils.RespondForbidden(c, "You are not authorized to view this API key")
			return
		}
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(utils.LogError(err)).Error("Failed to fetch API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKey)
	utils.RespondSuccess(c, http.StatusOK, apiKey)
}

// Update godoc
// @Summary Rename or revoke an API key
// @Description Owner only (a key of another user is 403, with no admin override). Applies only the fields present: name and/or is_active, each written on its own so a rename never changes is_active. A body with neither is a 400 rather than a no-op. is_active false revokes the key as DELETE does and stamps revoked_at. Revocation is final: is_active true on a revoked key is 409 CONFLICT and changes nothing, the name included; on a live key it changes nothing. is_active true from a request authenticated with an API key is 403. Expiry is not affected: an expired key stays unusable whatever is_active says.
// @Tags api-keys
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Param request body UpdateAPIKeyRequest true "Fields to change; at least one of name and is_active"
// @Success 200 {object} utils.APIResponse{data=models.APIKey} "The updated key"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid API key ID, malformed JSON (Invalid request format), or At least one of name or is_active must be provided. VALIDATION_ERROR: name not 3 to 100 characters (details maps Name to a message)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the key belongs to another user, or is_active true from a request authenticated with an API key (keys can be switched on only from a signed-in session)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no key with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: is_active true on a revoked key; a revoked API key cannot be switched on again, and nothing is changed"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the key could not be updated"
// @Router /api-keys/{id} [put]
func (h *APIKeyHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.Update")

	idParam := c.Param("id")
	id, err := strconv.ParseUint(idParam, 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid API key ID")
		return
	}

	var req UpdateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if req.Name == nil && req.IsActive == nil {
		utils.RespondBadRequest(c, "At least one of name or is_active must be provided")
		return
	}

	userID := c.GetUint("user_id")

	if req.IsActive != nil && *req.IsActive && !sessionCaller(c) {
		logger.WithField("user_id", userID).WithField("error_code", "session_required").
			Warn("API key not switched on: not a signed-in session")
		utils.RespondForbidden(c, "API keys can be switched on only from a signed-in session")
		return
	}

	apiKey, err := h.apiKeyService.Update(uint(id), userID, req.Name, req.IsActive)
	if err != nil {
		if errors.Is(err, service.ErrAPIKeyRevoked) {
			logger.WithField("user_id", userID).WithField("error_code", "api_key_revoked").
				Warn("Revoked API key not switched on")
			utils.RespondConflict(c, "A revoked API key cannot be switched on again")
			return
		}
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.Warn("Unauthorized attempt to update API key")
			utils.RespondForbidden(c, "You are not authorized to update this API key")
			return
		}
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(utils.LogError(err)).Error("Failed to update API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKey)
	utils.RespondSuccess(c, http.StatusOK, apiKey)
}

// Revoke godoc
// @Summary Revoke an API key
// @Description Owner only (a key of another user is 403, with no admin override). Marks the key inactive and stamps revoked_at; the row is kept, not deleted, and revocation is final: the key cannot be switched on again. Idempotent: revoking a revoked key succeeds again and keeps the first revoked_at. Requests already authenticated with the key are not affected; later ones are rejected.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Revoked (message: API key revoked successfully)"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid API key ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the key belongs to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no key with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the key could not be updated"
// @Router /api-keys/{id} [delete]
func (h *APIKeyHandler) Revoke(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.Revoke")

	idParam := c.Param("id")
	id, err := strconv.ParseUint(idParam, 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid API key ID")
		return
	}

	userID := c.GetUint("user_id")

	if err := h.apiKeyService.Revoke(uint(id), userID); err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.Warn("Unauthorized attempt to revoke API key")
			utils.RespondForbidden(c, "You are not authorized to revoke this API key")
			return
		}
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(utils.LogError(err)).Error("Failed to revoke API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, gin.H{"message": "API key revoked successfully"})
}
