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
