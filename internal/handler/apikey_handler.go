package handler

import (
	"net/http"
	"strconv"

	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/service"
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
	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		c.Status(http.StatusBadRequest)
		return
	}

	userID := c.GetUint("user_id")
	
	key, apiKey, err := h.apiKeyService.Generate(userID, req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "api_key_generation_failed",
			"message": "Failed to generate API key",
		})
		return
	}

	c.JSON(http.StatusCreated, CreateAPIKeyResponse{
		Key:    key,
		APIKey: apiKey,
	})
}

func (h *APIKeyHandler) List(c *gin.Context) {
	userID := c.GetUint("user_id")
	
	apiKeys, err := h.apiKeyService.List(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "list_api_keys_failed",
			"message": "Failed to retrieve API keys",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"api_keys": apiKeys,
	})
}

func (h *APIKeyHandler) Revoke(c *gin.Context) {
	idParam := c.Param("id")
	id, err := strconv.ParseUint(idParam, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_id",
			"message": "Invalid API key ID",
		})
		return
	}

	userID := c.GetUint("user_id")
	
	if err := h.apiKeyService.Revoke(uint(id), userID); err != nil {
		if err.Error() == "unauthorized" {
			c.JSON(http.StatusForbidden, gin.H{
				"error":   "forbidden",
				"message": "You are not authorized to revoke this API key",
			})
			return
		}
		
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "revoke_failed",
			"message": "Failed to revoke API key",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "API key revoked successfully",
	})
}