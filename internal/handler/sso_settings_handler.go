package handler

import (
	"errors"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
)

// SSOSettingsHandler serves the single sign-on settings (#434): the stored
// security.sso.* entries over the OIDC_* environment fallbacks. No sign-in
// flow reads them yet.
type SSOSettingsHandler struct {
	sso service.SSOSettingsService
}

func NewSSOSettingsHandler(sso service.SSOSettingsService) *SSOSettingsHandler {
	return &SSOSettingsHandler{sso: sso}
}

// SSORoleMappingEntry maps one ID-token claim value to a role.
//
// @Description One role mapping entry: an ID-token claim value that grants a role.
type SSORoleMappingEntry struct {
	// The claim the value is matched in: groups or roles.
	Claim string `json:"claim" example:"groups" enums:"groups,roles"`
	// The provider's stable identifier (a group object id or an app role
	// value), never a display name. Trimmed; 1 to 256 bytes.
	Value string `json:"value" example:"3f2a6c1e-0000-4000-8000-000000000001"`
	// Id of the role the value grants (a system or a custom role).
	RoleID uint `json:"role_id" example:"2"`
}

// SSOSettingsSources says where each effective value came from.
//
// @Description Where each effective value came from: settings (stored through PUT /settings/sso), env (an OIDC_* variable) or default (built in).
type SSOSettingsSources struct {
	// Source of enabled.
	Enabled string `json:"enabled" example:"settings" enums:"settings,env,default"`
	// Source of issuer_url.
	IssuerURL string `json:"issuer_url" example:"env" enums:"settings,env,default"`
	// Source of client_id.
	ClientID string `json:"client_id" example:"env" enums:"settings,env,default"`
	// Source of the client secret (its value is never returned).
	ClientSecret string `json:"client_secret" example:"settings" enums:"settings,env,default"`
	// Source of scopes.
	Scopes string `json:"scopes" example:"default" enums:"settings,env,default"`
	// Source of role_mapping.
	RoleMapping string `json:"role_mapping" example:"settings" enums:"settings,env,default"`
}

// SSOSettingsResponse is the effective single sign-on configuration.
//
// @Description The effective single sign-on configuration: stored settings over the OIDC_* environment over the defaults. The client secret is never returned; client_secret_set says whether one is in force.
type SSOSettingsResponse struct {
	// Whether single sign-on is enabled. False when the stored setting cannot be read, whatever OIDC_ENABLED says.
	Enabled bool `json:"enabled" example:"false"`
	// The OpenID Connect issuer URL, or empty.
	IssuerURL string `json:"issuer_url" example:"https://login.microsoftonline.com/tenant-id/v2.0"`
	// The OpenID Connect client id, or empty.
	ClientID string `json:"client_id" example:"gophercrm"`
	// The scopes requested at sign-in; openid profile email by default.
	Scopes []string `json:"scopes" example:"openid,profile,email"`
	// The role mapping, never null. Empty when the stored mapping cannot be read or parsed.
	RoleMapping []SSORoleMappingEntry `json:"role_mapping"`
	// Whether a client secret is in force (stored or from OIDC_CLIENT_SECRET).
	ClientSecretSet bool `json:"client_secret_set" example:"true"`
	// Where each value came from.
	Sources SSOSettingsSources `json:"sources"`
}

// SSOSettingsRequest is the body of PUT /settings/sso: a full replacement of
// everything but the client secret.
//
// @Description Body of PUT /settings/sso. A full replacement of the stored settings except the client secret: an empty or absent field clears the stored value, so the OIDC_* variable (or the default) applies again.
type SSOSettingsRequest struct {
	// true or false stores the switch; null or absent clears it, so OIDC_ENABLED applies.
	Enabled *bool `json:"enabled" example:"true"`
	// An absolute https URL without credentials, query, fragment or zone identifier, at most 2048 bytes; a path is allowed. Empty clears it.
	IssuerURL string `json:"issuer_url" example:"https://login.microsoftonline.com/tenant-id/v2.0"`
	// At most 255 bytes, trimmed. Empty clears it.
	ClientID string `json:"client_id" example:"gophercrm"`
	// The new client secret, at most 1024 bytes, stored encrypted. Absent or null keeps the stored secret; an empty string clears it.
	ClientSecret *string `json:"client_secret" example:"not-a-real-secret"`
	// At most 20 tokens without whitespace; a non-empty list must contain openid. Empty or absent clears it.
	Scopes []string `json:"scopes" example:"openid,profile,email"`
	// At most 200 entries; claim groups or roles, a trimmed value of 1 to 256 bytes, an existing role id, no repeated claim and value. Empty or absent clears it; stored encrypted.
	RoleMapping []SSORoleMappingEntry `json:"role_mapping"`
}

func newSSOSettingsResponse(cfg service.EffectiveSSOConfig) SSOSettingsResponse {
	mapping := make([]SSORoleMappingEntry, 0, len(cfg.RoleMapping))
	for _, entry := range cfg.RoleMapping {
		mapping = append(mapping, SSORoleMappingEntry{Claim: entry.Claim, Value: entry.Value, RoleID: entry.RoleID})
	}
	scopes := cfg.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return SSOSettingsResponse{
		Enabled:         cfg.Enabled,
		IssuerURL:       cfg.IssuerURL,
		ClientID:        cfg.ClientID,
		Scopes:          scopes,
		RoleMapping:     mapping,
		ClientSecretSet: cfg.ClientSecret != "",
		Sources: SSOSettingsSources{
			Enabled:      cfg.Sources.Enabled,
			IssuerURL:    cfg.Sources.IssuerURL,
			ClientID:     cfg.Sources.ClientID,
			ClientSecret: cfg.Sources.ClientSecret,
			Scopes:       cfg.Sources.Scopes,
			RoleMapping:  cfg.Sources.RoleMapping,
		},
	}
}

// Get godoc
// @Summary Get the single sign-on settings (admin)
// @Description Requires settings.sso_manage (route guard, repeated in the handler); only the admin system role holds it, settings.view and settings.update do not. Returns the effective OpenID Connect settings: per field a non-empty stored setting, else the OIDC_* variable, else the default (disabled, scopes openid profile email, everything else empty); sources says which. The role mapping is returned decrypted; the client secret never is, client_secret_set says whether one is in force. Single sign-on is off when its stored switch cannot be read, and the mapping is empty when the stored one cannot be read or parsed. Sign-in through these settings is not implemented yet.
// @Tags configurations
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=SSOSettingsResponse} "The effective settings"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold settings.sso_manage (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the settings could not be read"
// @Router /settings/sso [get]
func (h *SSOSettingsHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "SSOSettingsHandler.Get")
	if denyWithoutPermission(c, permissions.SettingsSSOManage) {
		return
	}
	cfg, err := h.sso.SSOConfig(c.Request.Context())
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to read the single sign-on settings")
		utils.RespondInternalError(c)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, newSSOSettingsResponse(cfg))
}

// Update godoc
// @Summary Replace the single sign-on settings (admin)
// @Description Requires settings.sso_manage (route guard, repeated in the handler). Replaces every stored setting except the client secret: an empty or absent field clears the stored value so the OIDC_* variable applies again; enabled null or absent clears the switch; client_secret absent or null keeps the stored secret and an empty string clears it. The client secret and the role mapping are stored encrypted. The whole body is validated first, then the role ids are looked up, and nothing is written when any check fails. When the result is enabled the switch is written last, otherwise first, so a failure part-way never leaves single sign-on enabled over a half-written configuration. Answers with the effective settings, as GET does. Sign-in through these settings is not implemented yet.
// @Tags configurations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body SSOSettingsRequest true "The new settings"
// @Success 200 {object} utils.APIResponse{data=SSOSettingsResponse} "The effective settings after the write"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: the body is not valid JSON for the schema (details.body), or a field was refused (details maps a path such as issuer_url, scopes[0] or role_mapping[2].claim to a message). INVALID_REFERENCE: a well-formed role mapping names a role id no role has (details maps role_mapping[i].role_id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold settings.sso_manage (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: a role could not be looked up, a setting could not be stored or encrypted, or the settings could not be read back"
// @Router /settings/sso [put]
func (h *SSOSettingsHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "SSOSettingsHandler.Update")
	if denyWithoutPermission(c, permissions.SettingsSSOManage) {
		return
	}

	var req SSOSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// The decoder's message can quote the submitted value; it is not echoed.
		utils.RespondValidationError(c, map[string]string{"body": "must be a JSON object matching the single sign-on settings schema"})
		return
	}

	update := service.SSOSettingsUpdate{
		Enabled:      req.Enabled,
		IssuerURL:    req.IssuerURL,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
		Scopes:       req.Scopes,
	}
	for _, entry := range req.RoleMapping {
		update.RoleMapping = append(update.RoleMapping, service.SSORoleMapping{Claim: entry.Claim, Value: entry.Value, RoleID: entry.RoleID})
	}

	if err := h.sso.Update(c.Request.Context(), update); err != nil {
		var settingsErr *service.SSOSettingsError
		switch {
		case errors.As(err, &settingsErr) && errors.Is(err, service.ErrSSOUnknownRole):
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, "The role mapping names a role that does not exist", settingsErr.Fields)
		case errors.As(err, &settingsErr):
			utils.RespondValidationError(c, settingsErr.Fields)
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to store the single sign-on settings")
			utils.RespondInternalError(c)
		}
		return
	}

	cfg, err := h.sso.SSOConfig(c.Request.Context())
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to read the single sign-on settings back")
		utils.RespondInternalError(c)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, newSSOSettingsResponse(cfg))
}
