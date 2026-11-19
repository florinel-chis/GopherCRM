package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Where an effective single sign-on value came from.
const (
	SSOSourceSettings = "settings"
	SSOSourceEnv      = "env"
	SSOSourceDefault  = "default"
)

// Claims a role mapping may match on.
const (
	SSOClaimGroups = "groups"
	SSOClaimRoles  = "roles"
)

// Limits of the single sign-on settings.
const (
	SSOClientIDMaxBytes     = 255
	SSOClientSecretMaxBytes = 1024
	SSOScopesMax            = 20
	SSORoleMappingMax       = 200
	SSORoleMappingValueMax  = 256
)

// ssoDefaultScopes applies when neither a setting nor OIDC_SCOPES names any.
var ssoDefaultScopes = []string{"openid", "profile", "email"}

// SSORoleMapping maps one value of an ID-token claim to a role id.
type SSORoleMapping struct {
	Claim  string `json:"claim"`
	Value  string `json:"value"`
	RoleID uint   `json:"role_id"`
}

// SSOConfigSources says, per field, where the effective value came from:
// settings, env or default.
type SSOConfigSources struct {
	Enabled       string `json:"enabled"`
	AllowPassword string `json:"allow_password"`
	IssuerURL     string `json:"issuer_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Scopes        string `json:"scopes"`
	RoleMapping   string `json:"role_mapping"`
}

// EffectiveSSOConfig is the single sign-on configuration in force: stored
// settings over the OIDC_* environment over the defaults. Role ids are not
// checked against the database here; the sign-in flow re-checks them.
type EffectiveSSOConfig struct {
	Enabled bool
	// AllowPassword says whether accounts linked to single sign-on may also
	// use password sign-in and password reset.
	AllowPassword bool
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	RoleMapping   []SSORoleMapping
	Sources       SSOConfigSources
}

// SSOSettingsUpdate is a full replacement of the stored single sign-on
// settings. An empty field clears the stored value, so the environment
// applies again; Enabled nil and AllowPassword nil clear theirs too.
// ClientSecret nil keeps the stored secret and "" clears it.
type SSOSettingsUpdate struct {
	Enabled       *bool
	AllowPassword *bool
	IssuerURL     string
	ClientID      string
	ClientSecret  *string
	Scopes        []string
	RoleMapping   []SSORoleMapping
}

var (
	// ErrSSOSettingsInvalid is a malformed update (400 VALIDATION_ERROR). It
	// wraps the generic validation sentinel.
	ErrSSOSettingsInvalid = fmt.Errorf("invalid single sign-on settings: %w", apperrors.ErrValidation)
	// ErrSSOUnknownRole is a well-formed role mapping that names a role id no
	// row has (400 INVALID_REFERENCE).
	ErrSSOUnknownRole = errors.New("the role mapping names a role that does not exist")
)

// SSOSettingsError carries the refused fields of an update, keyed by path
// (issuer_url, scopes[1], role_mapping[2].claim). The messages never carry a
// submitted value. It unwraps to ErrSSOSettingsInvalid or ErrSSOUnknownRole.
type SSOSettingsError struct {
	Kind   error
	Fields map[string]string
}

func (e *SSOSettingsError) Error() string { return e.Kind.Error() }
func (e *SSOSettingsError) Unwrap() error { return e.Kind }

// SSOSettingsService reads and writes the single sign-on settings.
type SSOSettingsService interface {
	// SSOConfig returns the effective configuration. It re-reads the stored
	// settings on every call, so a change takes effect without a restart. It
	// fails closed: Enabled is false when its setting cannot be read, and the
	// role mapping is empty when its stored value cannot be read or parsed.
	SSOConfig(ctx context.Context) (EffectiveSSOConfig, error)
	// Update validates the whole update, checks the mapped role ids, and only
	// then writes. When the result is enabled, security.sso.enabled is written
	// last; otherwise first.
	Update(ctx context.Context, update SSOSettingsUpdate) error
	SSOPasswordPolicy
}

// SSOPasswordPolicy answers whether an account linked to single sign-on (one
// with a user_identities row) may also use password sign-in and password
// reset. The auth service reads it once per call, outside its transaction.
type SSOPasswordPolicy interface {
	// AllowPasswordForSSOAccounts reads only security.sso.allow_password,
	// then OIDC_ALLOW_PASSWORD, else false. It fails closed: a stored value
	// that cannot be read is false.
	AllowPasswordForSSOAccounts(ctx context.Context) bool
}

type ssoSettingsService struct {
	configs ConfigurationService
	roles   repository.RoleRepository
	base    config.SSOConfig
}

// NewSSOSettingsService builds the service over the configuration service
// (the only writer of the reserved security.sso.* rows, through SetSSO), the
// role repository and the boot-time OIDC_* fallbacks.
func NewSSOSettingsService(configs ConfigurationService, roles repository.RoleRepository, base config.SSOConfig) SSOSettingsService {
	return &ssoSettingsService{configs: configs, roles: roles, base: base}
}

func ssoLogger() *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithField("component", "sso_settings")
}

func (s *ssoSettingsService) SSOConfig(ctx context.Context) (EffectiveSSOConfig, error) {
	if err := ctx.Err(); err != nil {
		return EffectiveSSOConfig{}, err
	}
	var out EffectiveSSOConfig

	// Enabled fails closed: a stored value that cannot be read (a missing
	// row included) is "off", never the environment's value.
	out.Enabled, out.Sources.Enabled = false, SSOSourceDefault
	if stored, err := s.configs.GetString(models.ConfigKeySSOEnabled); err == nil {
		switch strings.TrimSpace(stored) {
		case "true":
			out.Enabled, out.Sources.Enabled = true, SSOSourceSettings
		case "false":
			out.Sources.Enabled = SSOSourceSettings
		case "":
			if s.base.EnabledSet {
				out.Enabled, out.Sources.Enabled = s.base.Enabled, SSOSourceEnv
			}
		}
	} else {
		ssoLogger().WithError(utils.LogError(err)).Warn("Single sign-on enabled setting could not be read; single sign-on is off")
	}

	out.AllowPassword, out.Sources.AllowPassword = s.effectiveAllowPassword()
	out.IssuerURL, out.Sources.IssuerURL = s.effectiveIssuer()
	out.ClientID, out.Sources.ClientID = s.effectivePlain(models.ConfigKeySSOClientID, strings.TrimSpace(s.base.ClientID))
	out.ClientSecret, out.Sources.ClientSecret = s.effectiveSecret()
	out.Scopes, out.Sources.Scopes = s.effectiveScopes()
	out.RoleMapping, out.Sources.RoleMapping = s.effectiveRoleMapping()
	return out, nil
}

// effectiveAllowPassword resolves security.sso.allow_password like Enabled:
// a stored "true" or "false", else OIDC_ALLOW_PASSWORD when it is set, else
// false. It fails closed: a stored value that cannot be read (a missing row
// included) is false, never the environment's value.
func (s *ssoSettingsService) effectiveAllowPassword() (bool, string) {
	stored, err := s.configs.GetString(models.ConfigKeySSOAllowPassword)
	if err != nil {
		ssoLogger().WithError(utils.LogError(err)).Warn("Single sign-on password setting could not be read; password sign-in and reset stay off for linked accounts")
		return false, SSOSourceDefault
	}
	switch strings.TrimSpace(stored) {
	case "true":
		return true, SSOSourceSettings
	case "false":
		return false, SSOSourceSettings
	case "":
		if s.base.AllowPasswordSet {
			return s.base.AllowPassword, SSOSourceEnv
		}
	}
	return false, SSOSourceDefault
}

// AllowPasswordForSSOAccounts reads only the password setting; it does not
// resolve the whole configuration (which decrypts the secret and the mapping).
func (s *ssoSettingsService) AllowPasswordForSSOAccounts(ctx context.Context) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	allowed, _ := s.effectiveAllowPassword()
	return allowed
}

// effectivePlain resolves a plain string setting: a non-empty stored value,
// then the environment value, then "". A read error falls back to the
// environment.
func (s *ssoSettingsService) effectivePlain(key, env string) (string, string) {
	if stored, err := s.configs.GetString(key); err == nil && strings.TrimSpace(stored) != "" {
		return strings.TrimSpace(stored), SSOSourceSettings
	}
	if env != "" {
		return env, SSOSourceEnv
	}
	return "", SSOSourceDefault
}

func (s *ssoSettingsService) effectiveIssuer() (string, string) {
	if stored, err := s.configs.GetString(models.ConfigKeySSOIssuerURL); err == nil && strings.TrimSpace(stored) != "" {
		if issuer, err := config.ParseOIDCIssuer(stored); err == nil {
			return issuer, SSOSourceSettings
		}
		ssoLogger().WithField("config_key", models.ConfigKeySSOIssuerURL).
			Warn("Stored single sign-on issuer URL is not valid and is treated as unset")
	}
	if issuer, ok := s.envIssuer(); ok {
		return issuer, SSOSourceEnv
	}
	return "", SSOSourceDefault
}

// envIssuer is OIDC_ISSUER_URL when it is set and valid. An invalid value is
// treated as unset, with a warning naming the variable but not the value.
func (s *ssoSettingsService) envIssuer() (string, bool) {
	if strings.TrimSpace(s.base.IssuerURL) == "" {
		return "", false
	}
	issuer, err := config.ParseOIDCIssuer(s.base.IssuerURL)
	if err != nil {
		ssoLogger().WithField("variable", "OIDC_ISSUER_URL").
			Warn("OIDC_ISSUER_URL is not a valid https issuer URL and is treated as unset")
		return "", false
	}
	return issuer, true
}

func (s *ssoSettingsService) effectiveSecret() (string, string) {
	if stored, err := s.configs.GetSecret(models.ConfigKeySSOClientSecret); err == nil && stored != "" {
		return stored, SSOSourceSettings
	}
	if s.base.ClientSecret != "" {
		return s.base.ClientSecret, SSOSourceEnv
	}
	return "", SSOSourceDefault
}

func (s *ssoSettingsService) effectiveScopes() ([]string, string) {
	if stored, err := s.configs.GetString(models.ConfigKeySSOScopes); err == nil {
		if scopes := strings.Fields(stored); len(scopes) > 0 {
			return scopes, SSOSourceSettings
		}
	}
	if scopes := strings.Fields(s.base.Scopes); len(scopes) > 0 {
		return scopes, SSOSourceEnv
	}
	return append([]string(nil), ssoDefaultScopes...), SSOSourceDefault
}

// effectiveRoleMapping fails closed: a stored mapping that cannot be read or
// does not parse is empty, and does NOT fall back to the environment. Only an
// empty stored value lets OIDC_ROLE_MAPPING apply.
func (s *ssoSettingsService) effectiveRoleMapping() ([]SSORoleMapping, string) {
	stored, err := s.configs.GetSecret(models.ConfigKeySSORoleMapping)
	if err != nil {
		ssoLogger().WithError(utils.LogError(err)).Warn("Single sign-on role mapping could not be read; the mapping is empty")
		return []SSORoleMapping{}, SSOSourceDefault
	}
	if strings.TrimSpace(stored) != "" {
		mapping, err := parseSSORoleMapping(stored)
		if err != nil {
			ssoLogger().WithField("config_key", models.ConfigKeySSORoleMapping).
				Warn("Stored single sign-on role mapping is not valid; the mapping is empty")
			return []SSORoleMapping{}, SSOSourceDefault
		}
		return mapping, SSOSourceSettings
	}
	if strings.TrimSpace(s.base.RoleMapping) != "" {
		mapping, err := parseSSORoleMapping(s.base.RoleMapping)
		if err != nil {
			ssoLogger().WithField("variable", "OIDC_ROLE_MAPPING").
				Warn("OIDC_ROLE_MAPPING is not a valid role mapping and is treated as unset")
			return []SSORoleMapping{}, SSOSourceDefault
		}
		return mapping, SSOSourceEnv
	}
	return []SSORoleMapping{}, SSOSourceDefault
}

// parseSSORoleMapping decodes and validates a stored or environment mapping.
func parseSSORoleMapping(raw string) ([]SSORoleMapping, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var mapping []SSORoleMapping
	if err := decoder.Decode(&mapping); err != nil {
		return nil, errors.New("role mapping is not a JSON array of {claim, value, role_id}")
	}
	if decoder.More() {
		return nil, errors.New("role mapping carries trailing data")
	}
	fields := map[string]string{}
	mapping = validateSSORoleMapping(mapping, fields)
	if len(fields) > 0 {
		return nil, errors.New("role mapping is not valid")
	}
	if mapping == nil {
		mapping = []SSORoleMapping{}
	}
	return mapping, nil
}

// validateSSORoleMapping checks each entry, records refusals in fields and
// returns the entries with their values trimmed.
func validateSSORoleMapping(mapping []SSORoleMapping, fields map[string]string) []SSORoleMapping {
	if len(mapping) > SSORoleMappingMax {
		fields["role_mapping"] = fmt.Sprintf("at most %d entries", SSORoleMappingMax)
		return mapping
	}
	out := make([]SSORoleMapping, 0, len(mapping))
	seen := map[[2]string]bool{}
	for i, entry := range mapping {
		path := fmt.Sprintf("role_mapping[%d]", i)
		entry.Value = strings.TrimSpace(entry.Value)
		if entry.Claim != SSOClaimGroups && entry.Claim != SSOClaimRoles {
			fields[path+".claim"] = "must be groups or roles"
		}
		switch {
		case entry.Value == "":
			fields[path+".value"] = "is required"
		case len(entry.Value) > SSORoleMappingValueMax:
			fields[path+".value"] = fmt.Sprintf("must be at most %d bytes", SSORoleMappingValueMax)
		}
		if entry.RoleID < 1 {
			fields[path+".role_id"] = "must be a role id"
		}
		pair := [2]string{entry.Claim, entry.Value}
		if seen[pair] {
			fields[path] = "repeats an earlier claim and value"
		}
		seen[pair] = true
		out = append(out, entry)
	}
	return out
}

func validateSSOScopes(scopes []string, fields map[string]string) {
	if len(scopes) == 0 {
		return
	}
	if len(scopes) > SSOScopesMax {
		fields["scopes"] = fmt.Sprintf("at most %d scopes", SSOScopesMax)
		return
	}
	hasOpenID := false
	for i, scope := range scopes {
		if scope == "" || strings.ContainsFunc(scope, unicode.IsSpace) {
			fields[fmt.Sprintf("scopes[%d]", i)] = "must be a non-empty token without whitespace"
		}
		if scope == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		if _, refused := fields["scopes"]; !refused {
			fields["scopes"] = "must contain openid"
		}
	}
}

type ssoWrite struct {
	key   string
	value string
}

func (s *ssoSettingsService) Update(ctx context.Context, update SSOSettingsUpdate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fields := map[string]string{}

	issuer := ""
	if strings.TrimSpace(update.IssuerURL) != "" {
		parsed, err := config.ParseOIDCIssuer(update.IssuerURL)
		if err != nil {
			fields["issuer_url"] = "must be an absolute https URL without credentials, query, fragment or zone identifier, at most 2048 bytes"
		}
		issuer = parsed
	}
	clientID := strings.TrimSpace(update.ClientID)
	if len(clientID) > SSOClientIDMaxBytes {
		fields["client_id"] = fmt.Sprintf("must be at most %d bytes", SSOClientIDMaxBytes)
	}
	if update.ClientSecret != nil && len(*update.ClientSecret) > SSOClientSecretMaxBytes {
		fields["client_secret"] = fmt.Sprintf("must be at most %d bytes", SSOClientSecretMaxBytes)
	}
	validateSSOScopes(update.Scopes, fields)
	mapping := validateSSORoleMapping(update.RoleMapping, fields)

	if len(fields) > 0 {
		return &SSOSettingsError{Kind: ErrSSOSettingsInvalid, Fields: fields}
	}

	// Only a well-formed mapping reaches the database: each role id once.
	if err := s.checkRoles(mapping); err != nil {
		return err
	}

	mappingText := ""
	if len(mapping) > 0 {
		encoded, err := json.Marshal(mapping)
		if err != nil {
			return fmt.Errorf("encode the single sign-on role mapping: %w", err)
		}
		mappingText = string(encoded)
	}

	enabledText := ""
	newEnabled := s.base.EnabledSet && s.base.Enabled
	if update.Enabled != nil {
		newEnabled = *update.Enabled
		if newEnabled {
			enabledText = "true"
		} else {
			enabledText = "false"
		}
	}

	allowPasswordText := ""
	if update.AllowPassword != nil {
		allowPasswordText = "false"
		if *update.AllowPassword {
			allowPasswordText = "true"
		}
	}

	// The password setting goes before the issuer, client, scopes and
	// mapping, so that turning it off is stored as early as possible.
	writes := []ssoWrite{
		{models.ConfigKeySSOAllowPassword, allowPasswordText},
		{models.ConfigKeySSOIssuerURL, issuer},
		{models.ConfigKeySSOClientID, clientID},
		{models.ConfigKeySSOScopes, strings.Join(update.Scopes, " ")},
		{models.ConfigKeySSORoleMapping, mappingText},
	}
	if update.ClientSecret != nil {
		writes = append(writes, ssoWrite{models.ConfigKeySSOClientSecret, *update.ClientSecret})
	}
	// A failure part-way must never leave SSO enabled over a half-written
	// configuration: enabling writes the switch last, disabling first.
	enabled := ssoWrite{models.ConfigKeySSOEnabled, enabledText}
	if newEnabled {
		writes = append(writes, enabled)
	} else {
		writes = append([]ssoWrite{enabled}, writes...)
	}

	for _, write := range writes {
		if err := s.configs.SetSSO(write.key, write.value); err != nil {
			return fmt.Errorf("store single sign-on setting %s: %w", write.key, err)
		}
	}
	return nil
}

func (s *ssoSettingsService) checkRoles(mapping []SSORoleMapping) error {
	if len(mapping) == 0 {
		return nil
	}
	if s.roles == nil {
		return errors.New("single sign-on settings: no role repository")
	}
	firstIndex := map[uint]int{}
	var ids []uint
	for i, entry := range mapping {
		if _, ok := firstIndex[entry.RoleID]; !ok {
			firstIndex[entry.RoleID] = i
			ids = append(ids, entry.RoleID)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	fields := map[string]string{}
	for _, id := range ids {
		if _, err := s.roles.GetByID(id); err != nil {
			if apperrors.IsNotFound(err) {
				fields[fmt.Sprintf("role_mapping[%d].role_id", firstIndex[id])] = "names no role"
				continue
			}
			return fmt.Errorf("check single sign-on role mapping: %w", err)
		}
	}
	if len(fields) > 0 {
		return &SSOSettingsError{Kind: ErrSSOUnknownRole, Fields: fields}
	}
	return nil
}
