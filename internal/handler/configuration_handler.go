package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
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

// SetConfigurationRequest carries the new value for a configuration entry.
//
// The value is captured raw rather than as an interface{} so that "the key is
// absent" and "the key is present but falsy" are structurally distinct: the
// raw bytes of false, 0 and "" are non-empty, so binding:"required" rejects
// only a genuinely missing field. A literal null does bind (as the four bytes
// "null") and is rejected explicitly in the handler, since there is no
// configuration type a null could be stored as.
type SetConfigurationRequest struct {
	Value json.RawMessage `json:"value" binding:"required"`
}

// ConfigurationResponse is a configuration entry as the API returns it: every
// field of the stored entry, plus is_set.
//
// A sensitive entry's value never leaves the process — not even in its
// encrypted form, which would still be a credential-shaped blob to anyone who
// obtained the key material. The response carries an empty value and reports
// only whether one is stored, which is all the UI needs to show a "configured"
// state next to a write-only input.
type ConfigurationResponse struct {
	models.Configuration
	// IsSet reports whether the entry currently holds a value. For a
	// sensitive entry it is the only evidence of its content.
	IsSet bool `json:"is_set"`
}

// newConfigurationResponse masks one entry. is_set is computed before masking,
// so it describes what is stored rather than what is returned.
func newConfigurationResponse(config models.Configuration) ConfigurationResponse {
	response := ConfigurationResponse{Configuration: config, IsSet: config.Value != ""}
	if config.IsSensitive {
		response.Value = ""
	}
	return response
}

// newConfigurationResponses masks a list. It returns an empty slice rather than
// nil so the JSON is always an array.
func newConfigurationResponses(configs []models.Configuration) []ConfigurationResponse {
	responses := make([]ConfigurationResponse, 0, len(configs))
	for _, config := range configs {
		responses = append(responses, newConfigurationResponse(config))
	}
	return responses
}

// GetAll returns all configurations
// GetAll godoc
// @Summary List all configurations
// @Description Retrieve every configuration entry, ordered by category and key (admin only). A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]ConfigurationResponse}} "Configurations retrieved successfully"
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

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}

// GetByCategory returns configurations by category
// GetByCategory godoc
// @Summary List configurations in a category
// @Description Retrieve all configurations belonging to a category (admin only). An unrecognised category yields an empty list rather than an error. A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param category path string true "Configuration category" Enums(general, leads, customers, tickets, tasks, security, integration, ui)
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]ConfigurationResponse}} "Configurations retrieved successfully"
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

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}

// GetByKey returns a specific configuration
// GetByKey godoc
// @Summary Get a configuration by key
// @Description Retrieve a single configuration entry by its key (admin only). A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "Configuration retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Configuration not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(err).Warn("Configuration not found")
			utils.RespondNotFound(c, "Configuration not found")
			return
		}
		logger.WithError(err).Error("Failed to get configuration")
		utils.RespondInternalError(c)
		return
	}

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Set updates a configuration value
// Set godoc
// @Summary Update a configuration value
// @Description Set the value of an existing configuration and return the updated entry (admin only). A sensitive entry (is_sensitive) takes its new value in plaintext, stores it encrypted and is returned masked — send an empty string to clear it. The value must match the entry's declared type — a boolean entry takes only true/false, an integer entry only a whole number, a string entry only a string — and a mismatch is rejected rather than coerced. Read-only configurations are rejected, as are values outside the entry's valid_values constraint.
// @Tags configurations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Param request body SetConfigurationRequest true "New configuration value. Any JSON value is accepted, falsy ones (false, 0, \"\") included; the value field itself must be present and must not be null."
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "Configuration updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Missing or null value, a value whose type does not match the configuration's declared type, read-only configuration, or a value outside the entry's valid_values constraint"
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

	// Decode the raw value only now that we know the field was present. A
	// falsy value survives this round trip; a literal null decodes to nil and
	// is not a value any configuration type can hold.
	var value interface{}
	if err := json.Unmarshal(req.Value, &value); err != nil {
		logger.WithError(err).Warn("Malformed configuration value")
		utils.RespondBadRequest(c, "Invalid configuration value")
		return
	}
	if value == nil {
		utils.RespondBadRequest(c, "Configuration value cannot be null")
		return
	}

	if err := h.configService.Set(key, value); err != nil {
		logger.WithError(err).Error("Failed to set configuration")
		switch {
		case apperrors.IsNotFound(err):
			utils.RespondNotFound(c, "Configuration not found")
		case errors.Is(err, apperrors.ErrConfigurationReadOnly):
			utils.RespondBadRequest(c, "Configuration is read-only")
		case errors.Is(err, apperrors.ErrConfigurationInvalidValue):
			utils.RespondBadRequest(c, "Invalid value for configuration")
		default:
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

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Reset resets a configuration to its default value
// Reset godoc
// @Summary Reset a configuration to its default value
// @Description Restore a configuration's value from its stored default and return the updated entry (admin only). Read-only configurations are rejected. A sensitive entry is returned masked; resetting one clears it, since sensitive entries carry no default.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key"
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "Configuration reset successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Configuration is read-only"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Configuration not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		switch {
		case apperrors.IsNotFound(err):
			utils.RespondNotFound(c, "Configuration not found")
		case errors.Is(err, apperrors.ErrConfigurationReadOnly):
			utils.RespondBadRequest(c, "Configuration is read-only")
		default:
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

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// GetUIConfigurations returns configurations that are safe for UI consumption
// GetUIConfigurations godoc
// @Summary List UI-safe configurations
// @Description Retrieve the configurations the frontend is allowed to read: the whole ui category, general.company_name, and a synthetic leads.conversion.allowed_statuses entry. Available to any authenticated user. A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=object{configurations=[]ConfigurationResponse}} "Configurations retrieved successfully"
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

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}
