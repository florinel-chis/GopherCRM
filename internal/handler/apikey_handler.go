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

type APIKeyHandler struct {
	apiKeyService service.APIKeyService
}

func NewAPIKeyHandler(apiKeyService service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{apiKeyService: apiKeyService}
}

type CreateAPIKeyRequest struct {
	Name string `json:"name" binding:"required,min=3,max=100"`
	// ExpiresAt is an optional RFC3339 timestamp. Left empty the key never
	// expires. It is taken as a string rather than a time.Time so a malformed
	// value produces a plain 400 with a message naming the field, instead of the
	// opaque unmarshal error the time codec would raise.
	ExpiresAt string `json:"expires_at"`
}

type CreateAPIKeyResponse struct {
	Key    string         `json:"key"`
	APIKey *models.APIKey `json:"api_key"`
}

// UpdateAPIKeyRequest carries pointers so an omitted field is distinguishable
// from a zero value — omitting is_active must not silently deactivate a key.
type UpdateAPIKeyRequest struct {
	Name     *string `json:"name" binding:"omitempty,min=3,max=100"`
	IsActive *bool   `json:"is_active"`
}

// Create godoc
// @Summary Create a new API key
// @Description Create an API key for the authenticated user. The plaintext key is returned only in this response — only its HMAC hash is stored, so it can never be retrieved again. Any authenticated role may create a key, and the key always belongs to the caller.
// @Description
// @Description The optional `expires_at` field is an RFC3339 timestamp (for example `2026-12-31T23:59:59Z`). Omit it and the key never expires. A timestamp in the past is rejected with 400 rather than minting a credential that could never authenticate. Expiry is enforced on every request in AuthService.ValidateAPIKey and cannot be lifted afterwards — only revocation is reversible.
// @Tags api-keys
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateAPIKeyRequest true "API key creation request"
// @Success 201 {object} utils.APIResponse{data=CreateAPIKeyResponse} "API key created successfully; contains the plaintext key"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, or expires_at is not RFC3339 or lies in the past"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /api-keys [post]
func (h *APIKeyHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.Create")

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
		logger.WithError(err).Error("Failed to generate API key")
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
// @Summary List API keys
// @Description List the API keys owned by the authenticated user. Scoped to the caller's own keys only — there is no admin-wide view and no pagination or filtering. Revoked (inactive) keys are included; the plaintext key is never returned, only its prefix.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.APIKey} "API keys retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /api-keys [get]
func (h *APIKeyHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "APIKeyHandler.List")

	userID := c.GetUint("user_id")

	apiKeys, err := h.apiKeyService.List(userID)
	if err != nil {
		logger.WithError(err).Error("Failed to list API keys")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKeys)
	utils.RespondSuccess(c, http.StatusOK, apiKeys)
}

// Get godoc
// @Summary Get a single API key
// @Description Fetch one API key owned by the authenticated user. Scoped to the caller's own keys — a key belonging to another user is rejected with 403, and there is no admin override. Neither the stored hash nor the plaintext key is ever returned; only the prefix, so a key can be identified without exposing it.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Success 200 {object} utils.APIResponse{data=models.APIKey} "API key retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid API key ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - the API key belongs to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "API key not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(err).Error("Failed to fetch API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKey)
	utils.RespondSuccess(c, http.StatusOK, apiKey)
}

// Update godoc
// @Summary Update an API key
// @Description Rename an API key and/or change whether it is active. Only the fields present in the body are applied; a body carrying neither `name` nor `is_active` is rejected with 400 rather than treated as a successful no-op. Only the owner may update a key — there is no admin override, and a key belonging to another user is rejected with 403.
// @Description
// @Description Setting `is_active` to true on a revoked key **reactivates** it. Revocation is an owner-controlled flag, not a tombstone, so the owner who turned it off may turn it back on; the plaintext key is unchanged and starts working again. Callers wanting an irreversible kill should create a new key and stop using the old one. Expiry is not affected by this endpoint: an expired key stays unusable no matter what `is_active` says, because expiry is checked separately at authentication time.
// @Tags api-keys
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Param request body UpdateAPIKeyRequest true "Fields to update; at least one of name or is_active is required"
// @Success 200 {object} utils.APIResponse{data=models.APIKey} "API key updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid API key ID, invalid name, or no updatable field supplied"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - the API key belongs to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "API key not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	apiKey, err := h.apiKeyService.Update(uint(id), userID, req.Name, req.IsActive)
	if err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.Warn("Unauthorized attempt to update API key")
			utils.RespondForbidden(c, "You are not authorized to update this API key")
			return
		}
		if apperrors.IsNotFound(err) {
			logger.WithError(err).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(err).Error("Failed to update API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, apiKey)
	utils.RespondSuccess(c, http.StatusOK, apiKey)
}

// Revoke godoc
// @Summary Revoke an API key
// @Description Revoke an API key by marking it inactive; the row is kept, not deleted. Only the owner of the key may revoke it — there is no admin override, and a key belonging to another user is rejected with 403.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Success 200 {object} utils.APIResponse "API key revoked successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid API key ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - the API key belongs to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "API key not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("API key not found")
			utils.RespondNotFound(c, "API key not found")
			return
		}

		logger.WithError(err).Error("Failed to revoke API key")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, gin.H{"message": "API key revoked successfully"})
}
