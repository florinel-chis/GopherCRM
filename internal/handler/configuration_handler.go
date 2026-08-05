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
// GetAll godoc
// @Summary List all configurations
// @Description Retrieve every configuration entry, ordered by category and key (admin only)
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]models.Configuration}} "Configurations retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /configurations [get]
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
// GetByCategory godoc
// @Summary List configurations in a category
// @Description Retrieve all configurations belonging to a category (admin only). An unrecognised category yields an empty list rather than an error.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param category path string true "Configuration category" Enums(general, leads, customers, tickets, tasks, security, integration, ui)
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]models.Configuration}} "Configurations retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /configurations/category/{category} [get]
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
// GetByKey godoc
// @Summary Get a configuration by key
// @Description Retrieve a single configuration entry by its key (admin only)
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Success 200 {object} utils.APIResponse{data=models.Configuration} "Configuration retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Configuration not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /configurations/{key} [get]
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
// Set godoc
// @Summary Update a configuration value
// @Description Set the value of an existing configuration and return the updated entry (admin only). Read-only configurations are rejected, as are values outside the entry's valid_values constraint.
// @Tags configurations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Param request body SetConfigurationRequest true "New configuration value"
// @Success 200 {object} utils.APIResponse{data=models.Configuration} "Configuration updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, read-only configuration, or invalid value"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Configuration not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /configurations/{key} [put]
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
// Reset godoc
// @Summary Reset a configuration to its default value
// @Description Restore a configuration's value from its stored default and return the updated entry (admin only). Read-only configurations are rejected. An unknown key currently surfaces as 500 rather than 404.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Success 200 {object} utils.APIResponse{data=models.Configuration} "Configuration reset successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Configuration is read-only"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error, including an unknown configuration key"
// @Router /configurations/{key}/reset [post]
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
// GetUIConfigurations godoc
// @Summary List UI-safe configurations
// @Description Retrieve the configurations the frontend is allowed to read: the whole ui category, general.company_name, and a synthetic leads.conversion.allowed_statuses entry. Available to any authenticated user.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]models.Configuration}} "Configurations retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /configurations/ui [get]
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