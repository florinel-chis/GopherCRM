package service

import (
	"context"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/idkit/entra"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
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
	SSOClaimGroups = rolemap.ClaimGroups
	SSOClaimRoles  = rolemap.ClaimRoles
)

// Limits of the single sign-on settings.
const (
	SSOClientIDMaxBytes     = 255
	SSOClientSecretMaxBytes = 1024
	SSOScopesMax            = 20
	SSORoleMappingMax       = 200
	SSORoleMappingValueMax  = 256

	SSOClientCertificateMaxBytes = 16384
	SSOClientPrivateKeyMaxBytes  = 16384
	// SSOClientKeyMinBits is the smallest RSA key the client certificate may
	// carry.
	SSOClientKeyMinBits = 2048
)

// ssoDefaultScopes applies when neither a setting nor OIDC_SCOPES names any.
var ssoDefaultScopes = []string{"openid", "profile", "email"}

// SSORoleMapping maps one value of an ID-token claim to a role id.
type SSORoleMapping = rolemap.Rule

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
	// ClientCertificate is settings or default: the certificate has no
	// environment fallback.
	ClientCertificate string `json:"client_certificate"`
}

// SSOClientCertificate is a usable stored client certificate and its RSA
// private key. While one is in force, the code exchange authenticates with a
// signed client assertion (private_key_jwt) instead of the client secret.
type SSOClientCertificate struct {
	Certificate *x509.Certificate
	PrivateKey  *rsa.PrivateKey
	// ThumbprintSHA1 is the uppercase hex SHA-1 of the certificate DER, as
	// the identity provider shows it.
	ThumbprintSHA1 string
	NotAfter       time.Time
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
	// GraphMaxPages bounds the Microsoft Graph pages one Entra ID sign-in
	// reads on a group overage: OIDC_GRAPH_MAX_PAGES when it is 1..50, else
	// entra.DefaultMaxPages. Environment only, never a stored setting.
	GraphMaxPages int
	// EntraAppSigningKeys is OIDC_ENTRA_APP_SIGNING_KEYS: an Entra ID issuer's
	// ID tokens are verified against jwks_uri?appid=<client id>. Environment
	// only, never a stored setting.
	EntraAppSigningKeys bool
	// ClientCertificate is the stored client certificate pair, or nil when
	// none is stored or the stored pair is not usable. An expired certificate
	// stays in force: the identity provider refuses it, and the expiry shows.
	ClientCertificate *SSOClientCertificate
	Sources           SSOConfigSources
}

// SSOSettingsUpdate is a full replacement of the stored single sign-on
// settings. An empty field clears the stored value, so the environment
// applies again; Enabled nil and AllowPassword nil clear theirs too.
// ClientSecret nil keeps the stored secret and "" clears it.
// ClientCertificate and ClientPrivateKey go together: both nil keep the stored
// pair, both "" clear it and both non-empty replace it; any other combination
// is refused.
type SSOSettingsUpdate struct {
	Enabled       *bool
	AllowPassword *bool
	IssuerURL     string
	ClientID      string
	ClientSecret  *string
	Scopes        []string
	RoleMapping   []SSORoleMapping
	// ClientCertificate is one PEM CERTIFICATE block; ClientPrivateKey its
	// unencrypted RSA key (PKCS#8 or PKCS#1 PEM) of at least 2048 bits.
	ClientCertificate *string
	ClientPrivateKey  *string
	// ConfirmIssuerChange confirms a save that changes the effective issuer
	// while user_identities rows exist under the current one. Without it such
	// a save is refused with an *SSOIssuerChangeError.
	ConfirmIssuerChange bool
}

var (
	// ErrSSOSettingsInvalid is a malformed update (400 VALIDATION_ERROR). It
	// wraps the generic validation sentinel.
	ErrSSOSettingsInvalid = fmt.Errorf("invalid single sign-on settings: %w", apperrors.ErrValidation)
	// ErrSSOUnknownRole is a well-formed role mapping that names a role id no
	// row has (400 INVALID_REFERENCE).
	ErrSSOUnknownRole = errors.New("the role mapping names a role that does not exist")
	// ErrSSOIssuerChangeUnconfirmed is a save that would change the effective
	// issuer while identity links exist under the current one, sent without
	// ConfirmIssuerChange (409 ISSUER_CHANGE_UNCONFIRMED).
	ErrSSOIssuerChangeUnconfirmed = errors.New("changing the issuer needs confirmation while accounts are linked to the current issuer")
)

// SSOIssuerChangeError reports an unconfirmed issuer change: Links is the
// number of user_identities rows under the current effective issuer, which
// stop matching once the new issuer applies. It unwraps to
// ErrSSOIssuerChangeUnconfirmed.
type SSOIssuerChangeError struct {
	Links int64
}

func (e *SSOIssuerChangeError) Error() string { return ErrSSOIssuerChangeUnconfirmed.Error() }
func (e *SSOIssuerChangeError) Unwrap() error { return ErrSSOIssuerChangeUnconfirmed }

// SSOIssuerLinks counts the identity links stored under an issuer.
// repository.UserIdentityRepository satisfies it.
type SSOIssuerLinks interface {
	// CountByIssuer counts the user_identities rows of any source whose
	// issuer equals the argument byte for byte; "" counts nothing.
	CountByIssuer(issuer string) (int64, error)
}

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
	// then writes. When the save would change the effective issuer while
	// identity links exist under the current one and ConfirmIssuerChange is
	// false, it writes nothing and returns an *SSOIssuerChangeError with the
	// number of those links. When the result is enabled,
	// security.sso.enabled is written last; otherwise first.
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
	links   SSOIssuerLinks
	base    config.SSOConfig
	now     func() time.Time
}

// NewSSOSettingsService builds the service over the configuration service
// (the only writer of the reserved security.sso.* rows, through SetSSO), the
// role repository, the identity links counted before an issuer change and
// the boot-time OIDC_* fallbacks.
func NewSSOSettingsService(configs ConfigurationService, roles repository.RoleRepository, links SSOIssuerLinks, base config.SSOConfig) SSOSettingsService {
	return &ssoSettingsService{configs: configs, roles: roles, links: links, base: base, now: time.Now}
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
	out.ClientCertificate, out.Sources.ClientCertificate = s.effectiveClientCertificate()
	out.GraphMaxPages = effectiveGraphMaxPages(s.base.GraphMaxPages)
	out.EntraAppSigningKeys = s.base.EntraAppSigningKeys
	return out, nil
}

// effectiveGraphMaxPages keeps a value of 1..50 and replaces anything else
// (unset, 0, negative, too large) with the default (entra.EffectiveMaxPages).
func effectiveGraphMaxPages(raw int) int {
	return entra.EffectiveMaxPages(raw)
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

// effectiveClientCertificate reads the stored pair. Both empty is unset. A pair
// that cannot be read, is incomplete, does not parse or does not match is
// treated as unset too, with one warning that names only the row.
func (s *ssoSettingsService) effectiveClientCertificate() (*SSOClientCertificate, string) {
	unusable := func(key string) (*SSOClientCertificate, string) {
		ssoLogger().WithField("config_key", key).
			Warn("Stored single sign-on client certificate is not usable and is treated as unset")
		return nil, SSOSourceDefault
	}
	certPEM, err := s.configs.GetSecret(models.ConfigKeySSOClientCertificate)
	if err != nil && !apperrors.IsNotFound(err) {
		return unusable(models.ConfigKeySSOClientCertificate)
	}
	keyPEM, err := s.configs.GetSecret(models.ConfigKeySSOClientPrivateKey)
	if err != nil && !apperrors.IsNotFound(err) {
		return unusable(models.ConfigKeySSOClientPrivateKey)
	}
	switch {
	case certPEM == "" && keyPEM == "":
		return nil, SSOSourceDefault
	case certPEM == "":
		return unusable(models.ConfigKeySSOClientCertificate)
	case keyPEM == "":
		return unusable(models.ConfigKeySSOClientPrivateKey)
	}
	// The zero time skips the expiry check: an expired stored certificate
	// stays in force.
	pair, fields := parseSSOClientCertificate(certPEM, keyPEM, time.Time{})
	if _, refused := fields["client_certificate"]; refused {
		return unusable(models.ConfigKeySSOClientCertificate)
	}
	if len(fields) > 0 {
		return unusable(models.ConfigKeySSOClientPrivateKey)
	}
	return pair, SSOSourceSettings
}

// Refusal messages of the client certificate pair. They never quote a value.
const (
	ssoCertNotPEM     = "must be one PEM block of type CERTIFICATE holding an X.509 certificate"
	ssoCertExpired    = "has expired"
	ssoKeyNotPEM      = "must be one PEM block of type PRIVATE KEY (PKCS#8) or RSA PRIVATE KEY (PKCS#1)"
	ssoKeyEncrypted   = "must not be encrypted"
	ssoKeyNotRSA      = "must be an RSA key"
	ssoKeyTooSmall    = "must be an RSA key of at least 2048 bits"
	ssoKeyMismatch    = "does not match client_certificate"
	ssoPairWithKey    = "must be sent together with client_private_key"
	ssoPairWithCert   = "must be sent together with client_certificate"
	ssoPEMBlockPrefix = "-----BEGIN "
)

// singlePEMBlock decodes text that is exactly one PEM block, surrounding
// whitespace aside. pem.Decode accepts CRLF line endings.
func singlePEMBlock(text string) *pem.Block {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, ssoPEMBlockPrefix) {
		return nil
	}
	block, rest := pem.Decode([]byte(trimmed))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil
	}
	return block
}

// parseSSOClientCertificate validates a certificate and private key pair and
// returns it parsed, or the refused fields (client_certificate,
// client_private_key). A non-zero now also refuses an expired certificate.
func parseSSOClientCertificate(certPEM, keyPEM string, now time.Time) (*SSOClientCertificate, map[string]string) {
	fields := map[string]string{}
	if len(certPEM) > SSOClientCertificateMaxBytes {
		fields["client_certificate"] = fmt.Sprintf("must be at most %d bytes", SSOClientCertificateMaxBytes)
	}
	if len(keyPEM) > SSOClientPrivateKeyMaxBytes {
		fields["client_private_key"] = fmt.Sprintf("must be at most %d bytes", SSOClientPrivateKeyMaxBytes)
	}
	if len(fields) > 0 {
		return nil, fields
	}

	var cert *x509.Certificate
	if block := singlePEMBlock(certPEM); block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
		fields["client_certificate"] = ssoCertNotPEM
	} else if parsed, err := x509.ParseCertificate(block.Bytes); err != nil {
		fields["client_certificate"] = ssoCertNotPEM
	} else if !now.IsZero() && !parsed.NotAfter.After(now) {
		fields["client_certificate"] = ssoCertExpired
	} else {
		cert = parsed
	}

	key, message := parseSSOClientPrivateKey(keyPEM)
	if message != "" {
		fields["client_private_key"] = message
	}
	if cert != nil && key != nil {
		if certKey, ok := cert.PublicKey.(*rsa.PublicKey); !ok || !certKey.Equal(&key.PublicKey) {
			fields["client_private_key"] = ssoKeyMismatch
		}
	}
	if len(fields) > 0 {
		return nil, fields
	}
	// The thumbprint is the SHA-1 identifier identity providers display.
	sum := sha1.Sum(cert.Raw)
	return &SSOClientCertificate{
		Certificate:    cert,
		PrivateKey:     key,
		ThumbprintSHA1: strings.ToUpper(hex.EncodeToString(sum[:])),
		NotAfter:       cert.NotAfter,
	}, nil
}

// parseSSOClientPrivateKey returns the RSA key, or a fixed refusal message.
func parseSSOClientPrivateKey(keyPEM string) (*rsa.PrivateKey, string) {
	block := singlePEMBlock(keyPEM)
	if block == nil {
		return nil, ssoKeyNotPEM
	}
	if block.Type == "ENCRYPTED PRIVATE KEY" || block.Headers["Proc-Type"] != "" || block.Headers["DEK-Info"] != "" {
		return nil, ssoKeyEncrypted
	}
	var parsed interface{}
	switch block.Type {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, ssoKeyNotPEM
		}
		parsed = key
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, ssoKeyNotPEM
		}
		parsed = key
	case "EC PRIVATE KEY":
		return nil, ssoKeyNotRSA
	default:
		return nil, ssoKeyNotPEM
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, ssoKeyNotRSA
	}
	if key.N.BitLen() < SSOClientKeyMinBits {
		return nil, ssoKeyTooSmall
	}
	return key, ""
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
			fields["issuer_url"] = "must be an absolute https URL without credentials, query, fragment or zone identifier, at most 512 bytes"
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
	certWrites := s.validateClientCertificate(update, fields)

	if len(fields) > 0 {
		return &SSOSettingsError{Kind: ErrSSOSettingsInvalid, Fields: fields}
	}

	// Only a well-formed mapping reaches the database: each role id once.
	if err := s.checkRoles(mapping); err != nil {
		return err
	}

	if !update.ConfirmIssuerChange {
		if err := s.checkIssuerChange(issuer); err != nil {
			return err
		}
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
	// The certificate goes before its key. A read between the two writes sees
	// a mismatched or incomplete pair, which is treated as unset.
	writes = append(writes, certWrites...)
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

// validateClientCertificate checks the certificate pair of an update and
// returns its writes (none when the pair is kept). Refusals go into fields.
func (s *ssoSettingsService) validateClientCertificate(update SSOSettingsUpdate, fields map[string]string) []ssoWrite {
	cert, key := update.ClientCertificate, update.ClientPrivateKey
	switch {
	case cert == nil && key == nil:
		return nil
	case cert == nil:
		fields["client_certificate"] = ssoPairWithKey
		return nil
	case key == nil:
		fields["client_private_key"] = ssoPairWithCert
		return nil
	case *cert == "" && *key == "":
		return []ssoWrite{{models.ConfigKeySSOClientCertificate, ""}, {models.ConfigKeySSOClientPrivateKey, ""}}
	case *cert == "":
		fields["client_certificate"] = ssoPairWithKey
		return nil
	case *key == "":
		fields["client_private_key"] = ssoPairWithCert
		return nil
	}
	_, refused := parseSSOClientCertificate(*cert, *key, s.now())
	for field, message := range refused {
		fields[field] = message
	}
	if len(refused) > 0 {
		return nil
	}
	// The values are stored as sent, trimmed: re-reading them parses the same.
	return []ssoWrite{
		{models.ConfigKeySSOClientCertificate, strings.TrimSpace(*cert)},
		{models.ConfigKeySSOClientPrivateKey, strings.TrimSpace(*key)},
	}
}

// checkIssuerChange refuses a save whose effective issuer differs from the
// current one while identity links exist under the current one. requested is
// the parsed issuer of the update ("" when it clears the stored value, so
// OIDC_ISSUER_URL applies again when it is valid).
func (s *ssoSettingsService) checkIssuerChange(requested string) error {
	env, _ := s.envIssuer()
	current, err := s.currentIssuer(env)
	if err != nil {
		return err
	}
	next := requested
	if next == "" {
		next = env
	}
	if current == "" || current == next {
		return nil
	}
	if s.links == nil {
		return errors.New("single sign-on settings: no identity link counter")
	}
	count, err := s.links.CountByIssuer(current)
	if err != nil {
		return fmt.Errorf("count the identity links of the current issuer: %w", err)
	}
	if count > 0 {
		return &SSOIssuerChangeError{Links: count}
	}
	return nil
}

// currentIssuer is the effective issuer as effectiveIssuer resolves it, except
// that a stored value that cannot be read is an error rather than a fallback
// to OIDC_ISSUER_URL, so the links are never counted under the wrong issuer.
// A missing row is unset. env is the valid OIDC_ISSUER_URL, or "".
func (s *ssoSettingsService) currentIssuer(env string) (string, error) {
	stored, err := s.configs.GetString(models.ConfigKeySSOIssuerURL)
	if err != nil && !apperrors.IsNotFound(err) {
		return "", fmt.Errorf("read the stored single sign-on issuer: %w", err)
	}
	if err == nil && strings.TrimSpace(stored) != "" {
		if issuer, err := config.ParseOIDCIssuer(stored); err == nil {
			return issuer, nil
		}
	}
	return env, nil
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
