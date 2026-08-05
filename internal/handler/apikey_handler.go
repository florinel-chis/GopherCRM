package handler

import (
	"net/http"
	"strconv"

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
}

type CreateAPIKeyResponse struct {
	Key    string         `json:"key"`
	APIKey *models.APIKey `json:"api_key"`
}

// Create godoc
// @Summary Create a new API key
// @Description Create an API key for the authenticated user. The plaintext key is returned only in this response — only its HMAC hash is stored, so it can never be retrieved again. Any authenticated role may create a key, and the key always belongs to the caller.
// @Tags api-keys
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateAPIKeyRequest true "API key creation request"
// @Success 201 {object} utils.APIResponse{data=CreateAPIKeyResponse} "API key created successfully; contains the plaintext key"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
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

	userID := c.GetUint("user_id")

	key, apiKey, err := h.apiKeyService.Generate(userID, req.Name)
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

// Revoke godoc
// @Summary Revoke an API key
// @Description Revoke an API key by marking it inactive; the row is kept, not deleted. Only the owner of the key may revoke it — there is no admin override, and a key belonging to another user is rejected with 403.
// @Description Note: a well-formed ID that matches no key currently surfaces as 500, not 404, because the repository returns gorm.ErrRecordNotFound while the handler tests for a different message.
// @Tags api-keys
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "API key ID"
// @Success 200 {object} utils.APIResponse "API key revoked successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid API key ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - the API key belongs to another user"
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
		if err.Error() == "unauthorized" {
			logger.Warn("Unauthorized attempt to revoke API key")
			utils.RespondForbidden(c, "You are not authorized to revoke this API key")
			return
		}
		if err.Error() == "api key not found" {
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
