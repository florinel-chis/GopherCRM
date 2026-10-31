package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
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
// configuration type a null could be stored as. The spec documents Value as
// any JSON value (scripts/docs/swag-overrides maps json.RawMessage to
// interface{}).
//
// @Description Body of PUT /configurations/{key}.
type SetConfigurationRequest struct {
	// The new value, as a JSON value of the entry's declared type: a string
	// for string, true or false for boolean, a whole number for integer, any
	// JSON value for float, json and array (stored as its JSON text). Falsy
	// values (false, 0, "") are accepted; the field must be present and must
	// not be null. For a sensitive entry send the plaintext secret, or "" to
	// clear it.
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
//
// @Description A configuration entry as the API returns it. A sensitive entry (is_sensitive) always carries an empty value; is_set tells whether one is stored.
type ConfigurationResponse struct {
	models.Configuration
	// IsSet reports whether the entry currently holds a value. For a
	// sensitive entry it is the only evidence of its content.

	// Whether the entry currently holds a non-empty value, computed before
	// masking. For a sensitive entry it is the only evidence of its content.
	// Read-only.
	IsSet bool `json:"is_set" example:"true" readonly:"true"`
}

// ConfigurationListResponse documents the data payload of the configuration
// list endpoints for the OpenAPI spec; the handlers build it as a gin.H.
//
// @Description A list of configuration entries.
type ConfigurationListResponse struct {
	// The entries, never null (an empty list is []).
	Configurations []ConfigurationResponse `json:"configurations"`
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
// @Summary List all configurations (admin)
// @Description Admin only (route guard, repeated in the handler). Returns every configuration entry, ordered by category and then key, in data.configurations; there is no pagination. A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored. Entries are seeded by the server; the API cannot create or delete them.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ConfigurationListResponse} "Every configuration entry"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the configurations could not be read"
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
		logger.WithError(utils.LogError(err)).Error("Failed to get configurations")
		utils.RespondInternalError(c)
		return
	}

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}

// GetByCategory returns configurations by category
// GetByCategory godoc
// @Summary List the configurations of a category (admin)
// @Description Admin only (route guard, repeated in the handler). Returns the entries of one category ordered by key in data.configurations. The category is not validated: any other value returns an empty list, not an error. A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param category path string true "Configuration category; any other value yields an empty list" Enums(general, leads, customers, deals, tickets, tasks, security, integration, ui)
// @Success 200 {object} utils.APIResponse{data=ConfigurationListResponse} "The entries of the category (possibly empty)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the configurations could not be read"
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
		logger.WithError(utils.LogError(err)).Error("Failed to get configurations by category")
		utils.RespondInternalError(c)
		return
	}

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}

// GetByKey returns a specific configuration
// GetByKey godoc
// @Summary Get a configuration entry (admin)
// @Description Admin only (route guard, repeated in the handler). Returns one entry by its key. A sensitive entry (is_sensitive) is masked: its value is always empty and is_set reports whether one is stored. The key ui is not reachable here: GET /configurations/ui answers it.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key, e.g. security.allow_public_registration"
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "The entry"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no entry with this key"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the entry could not be read"
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
			logger.WithError(utils.LogError(err)).Warn("Configuration not found")
			utils.RespondNotFound(c, "Configuration not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to get configuration")
		utils.RespondInternalError(c)
		return
	}

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Set updates a configuration value
// Set godoc
// @Summary Change a configuration value (admin)
// @Description Admin only (route guard, repeated in the handler). Sets the value of an existing entry and returns it. Checks, in order: the entry exists (404), is not read-only (400), the value is in valid_values when the entry has that list (for an array entry every element must be listed), and the value matches the declared type: a string entry takes only a JSON string, a boolean entry only true or false, an integer entry only a whole number (a fraction is refused, not truncated); float, json and array entries accept any JSON value and store its JSON text. The AEO provider entries integration.aeo.{engine}_model and integration.aeo.{engine}_base_url are trimmed and checked further: a model name of at most 120 characters, a base URL that is an absolute https URL with a valid host and port and without credentials, query, fragment or zone identifier.
// @Description A sensitive entry (is_sensitive) takes the plaintext secret, stores it encrypted (AES-256-GCM, key derived from API_KEY_SECRET or JWT_SECRET) and is returned masked; send an empty string to clear it. Changes take effect on the next read of the setting, without a restart.
// @Tags configurations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key, e.g. security.allow_public_registration"
// @Param request body SetConfigurationRequest true "The new value; any JSON value except null, falsy values included"
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "The updated entry"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: value missing (details maps Value to a message). BAD_REQUEST: malformed JSON (Invalid request format), Configuration value cannot be null, Configuration is read-only, or Invalid value for configuration (outside valid_values, wrong type, or a rejected AEO provider setting)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no entry with this key"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the value could not be stored or a sensitive value could not be encrypted"
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
		logger.WithError(utils.LogError(err)).Warn("Malformed configuration value")
		utils.RespondBadRequest(c, "Invalid configuration value")
		return
	}
	if value == nil {
		utils.RespondBadRequest(c, "Configuration value cannot be null")
		return
	}

	if err := h.configService.Set(key, value); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to set configuration")
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
		logger.WithError(utils.LogError(err)).Error("Failed to get updated configuration")
		utils.RespondInternalError(c)
		return
	}

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Reset resets a configuration to its default value
// Reset godoc
// @Summary Reset a configuration to its default (admin)
// @Description Admin only (route guard, repeated in the handler). Copies default_value into value and returns the entry; read-only entries are refused. The default is restored as stored, without the type and valid_values checks of PUT. A sensitive entry has no default, so resetting it clears the stored secret; it is returned masked.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param key path string true "Configuration key, e.g. security.allow_public_registration"
// @Success 200 {object} utils.APIResponse{data=ConfigurationResponse} "The reset entry"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Configuration is read-only"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no entry with this key"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the entry could not be stored"
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
		logger.WithError(utils.LogError(err)).Error("Failed to reset configuration")
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
		logger.WithError(utils.LogError(err)).Error("Failed to get reset configuration")
		utils.RespondInternalError(c)
		return
	}

	response := newConfigurationResponse(*config)
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// leadConversionUIConfigurations returns the synthetic lead-conversion
// entries of GET /configurations/ui.
func (h *ConfigurationHandler) leadConversionUIConfigurations(logger *logrus.Entry) []models.Configuration {
	var entries []models.Configuration
	if statuses, err := h.configService.GetLeadConversionStatuses(); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to get the allowed lead conversion statuses")
	} else {
		entries = append(entries, models.Configuration{
			Key:         service.ConfigLeadConversionAllowedStatuses,
			Value:       utils.JSONMarshal(statuses),
			Type:        models.ConfigTypeArray,
			Category:    models.CategoryLeads,
			Description: "Lead statuses that allow conversion to customer",
			IsReadOnly:  true,
		})
	}
	if required, err := h.configService.IsLeadConversionRequireNotes(); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to get the lead conversion notes setting")
	} else {
		entries = append(entries, models.Configuration{
			Key:         service.ConfigLeadConversionRequireNotes,
			Value:       strconv.FormatBool(required),
			Type:        models.ConfigTypeBoolean,
			Category:    models.CategoryLeads,
			Description: "Whether conversion notes are required when converting leads",
			IsReadOnly:  true,
		})
	}
	if assign, err := h.configService.IsLeadConversionAutoAssignOwner(); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to get the lead conversion owner setting")
	} else {
		entries = append(entries, models.Configuration{
			Key:         service.ConfigLeadConversionAutoAssignOwner,
			Value:       strconv.FormatBool(assign),
			Type:        models.ConfigTypeBoolean,
			Category:    models.CategoryLeads,
			Description: "Whether to automatically assign the lead owner as customer owner",
			IsReadOnly:  true,
		})
	}
	return entries
}

// GetUIConfigurations returns configurations that are safe for UI consumption
// GetUIConfigurations godoc
// @Summary List the UI-safe configurations
// @Description Any authenticated role. Returns, in data.configurations and in this order: every entry of the ui category, general.company_name, deals.default_currency (the currency a new deal takes when none is sent), and three synthetic read-only lead-conversion entries holding the values the convert endpoint enforces: leads.conversion.allowed_statuses (array), leads.conversion.require_notes (boolean) and leads.conversion.auto_assign_owner (boolean). The synthetic entries are not stored rows: their id is 0, their timestamps are the zero time (0001-01-01T00:00:00Z) and is_read_only is true. An entry whose setting cannot be read is left out instead of failing the request (except the ui category itself). A sensitive entry would be masked, but none of these is sensitive.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ConfigurationListResponse} "The UI-safe entries"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the ui category could not be read"
// @Router /configurations/ui [get]
func (h *ConfigurationHandler) GetUIConfigurations(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ConfigurationHandler.GetUIConfigurations")

	// This endpoint is accessible to all authenticated users
	configs, err := h.configService.GetByCategory(models.CategoryUI)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get UI configurations")
		utils.RespondInternalError(c)
		return
	}

	// Also get some general configurations that are safe for UI
	generalConfigs, err := h.configService.GetByCategory(models.CategoryGeneral)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to get general configurations")
	} else {
		// Filter to only safe general configurations
		for _, config := range generalConfigs {
			if config.Key == "general.company_name" {
				configs = append(configs, config)
			}
		}
	}

	// The default deal currency: the deal form pre-fills it. Not sensitive,
	// and read by key because it is the only entry of its category the UI
	// needs.
	if currency, err := h.configService.GetByKey(service.ConfigDealsDefaultCurrency); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to get the default deal currency configuration")
	} else {
		configs = append(configs, *currency)
	}

	// The lead-conversion settings the convert dialog follows (issue #104).
	// They are synthetic read-only entries carrying the effective values —
	// the same values ConvertToCustomer enforces — rather than the stored
	// rows, whose metadata is admin business. A failed read drops the entry
	// instead of failing the endpoint; the server enforces the rule anyway.
	configs = append(configs, h.leadConversionUIConfigurations(logger)...)

	payload := gin.H{"configurations": newConfigurationResponses(configs)}
	utils.LogHandlerResponse(logger, http.StatusOK, payload)
	utils.RespondSuccess(c, http.StatusOK, payload)
}
