package handler

import (
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type ConfigurationHandler struct {
	configService service.ConfigurationService
}

func NewConfigurationHandler(configService service.ConfigurationService) *ConfigurationHandler {
	return &ConfigurationHandler{configService: configService}
}

type SetConfigurationRequest struct {
	Value interface{} `json:"value" binding:"required"`
}

type ResetConfigurationRequest struct {
	Keys []string `json:"keys" binding:"required"`
}

// GetAll returns all configurations
func (h *ConfigurationHandler) GetAll(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.GetAll")

	currentUserRole := c.GetString("user_role")
	
	// Only admin users can view all configurations
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only admin users can view configurations")
		return
	}

	configs, err := h.configService.GetAll()
	if err != nil {
		logger.WithError(err).Error("Failed to get configurations")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"configurations": configs})
	utils.RespondSuccess(c, http.StatusOK, gin.H{"configurations": configs})
}

// GetByCategory returns configurations by category
func (h *ConfigurationHandler) GetByCategory(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.GetByCategory")

	currentUserRole := c.GetString("user_role")
	
	// Only admin users can view configurations
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only admin users can view configurations")
		return
	}

	category := c.Param("category")
	if category == "" {
		utils.RespondBadRequest(c, "Category parameter is required")
		return
	}

	configs, err := h.configService.GetByCategory(models.ConfigurationCategory(category))
	if err != nil {
		logger.WithError(err).Error("Failed to get configurations by category")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"configurations": configs})
	utils.RespondSuccess(c, http.StatusOK, gin.H{"configurations": configs})
}

// GetByKey returns a specific configuration
func (h *ConfigurationHandler) GetByKey(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.GetByKey")

	currentUserRole := c.GetString("user_role")
	
	// Only admin users can view configurations
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only admin users can view configurations")
		return
	}

	key := c.Param("key")
	if key == "" {
		utils.RespondBadRequest(c, "Key parameter is required")
		return
	}

	config, err := h.configService.GetByKey(key)
	if err != nil {
		logger.WithError(err).Warn("Configuration not found")
		utils.RespondNotFound(c, "Configuration not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, config)
	utils.RespondSuccess(c, http.StatusOK, config)
}

// Set updates a configuration value
func (h *ConfigurationHandler) Set(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.Set")

	currentUserRole := c.GetString("user_role")
	
	// Only admin users can modify configurations
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only admin users can modify configurations")
		return
	}

	key := c.Param("key")
	if key == "" {
		utils.RespondBadRequest(c, "Key parameter is required")
		return
	}

	var req SetConfigurationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if err := h.configService.Set(key, req.Value); err != nil {
		logger.WithError(err).Error("Failed to set configuration")
		if err.Error() == "configuration not found: "+key {
			utils.RespondNotFound(c, "Configuration not found")
		} else if err.Error() == "configuration is read-only" {
			utils.RespondBadRequest(c, "Configuration is read-only")
		} else if err.Error() == "invalid value for configuration" {
			utils.RespondBadRequest(c, "Invalid value for configuration")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	// Get updated configuration to return
	config, err := h.configService.GetByKey(key)
	if err != nil {
		logger.WithError(err).Error("Failed to get updated configuration")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, config)
	utils.RespondSuccess(c, http.StatusOK, config)
}

// Reset resets a configuration to its default value
func (h *ConfigurationHandler) Reset(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.Reset")

	currentUserRole := c.GetString("user_role")
	
	// Only admin users can reset configurations
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only admin users can reset configurations")
		return
	}

	key := c.Param("key")
	if key == "" {
		utils.RespondBadRequest(c, "Key parameter is required")
		return
	}

	if err := h.configService.Reset(key); err != nil {
		logger.WithError(err).Error("Failed to reset configuration")
		if err.Error() == "configuration not found: "+key {
			utils.RespondNotFound(c, "Configuration not found")
		} else if err.Error() == "configuration is read-only" {
			utils.RespondBadRequest(c, "Configuration is read-only")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	// Get reset configuration to return
	config, err := h.configService.GetByKey(key)
	if err != nil {
		logger.WithError(err).Error("Failed to get reset configuration")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, config)
	utils.RespondSuccess(c, http.StatusOK, config)
}

// GetUIConfigurations returns configurations that are safe for UI consumption
func (h *ConfigurationHandler) GetUIConfigurations(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.GetUIConfigurations")

	// This endpoint is accessible to all authenticated users
	configs, err := h.configService.GetByCategory(models.CategoryUI)
	if err != nil {
		logger.WithError(err).Error("Failed to get UI configurations")
		utils.RespondInternalError(c)
		return
	}

	// Also get some general configurations that are safe for UI
	generalConfigs, err := h.configService.GetByCategory(models.CategoryGeneral)
	if err != nil {
		logger.WithError(err).Warn("Failed to get general configurations")
	} else {
		// Filter to only safe general configurations
		for _, config := range generalConfigs {
			if config.Key == "general.company_name" {
				configs = append(configs, config)
			}
		}
	}

	// Get lead conversion statuses for frontend
	conversionStatuses, err := h.configService.GetLeadConversionStatuses()
	if err == nil {
		// Create a synthetic configuration for the frontend
		configs = append(configs, models.Configuration{
			Key:         "leads.conversion.allowed_statuses",
			Value:       utils.JSONMarshal(conversionStatuses),
			Type:        models.ConfigTypeArray,
			Category:    models.CategoryLeads,
			Description: "Lead statuses that allow conversion to customer",
		})
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"configurations": configs})
	utils.RespondSuccess(c, http.StatusOK, gin.H{"configurations": configs})
}