package config

import (
	"errors"
	"os"
	"strings"
)

// SSOConfig holds the boot-time OpenID Connect settings, read once from the
// OIDC_* variables. They are fallbacks: an administrator's value under
// security.sso.* in Settings wins, and the service re-reads the stored
// settings on every call. Load never fails because of them; the service
// validates the issuer and the role mapping and treats an invalid value as
// unset. The OpenID Connect sign-in (POST /auth/oidc/start and
// /auth/oidc/callback) uses them with the redirect URI APP_BASE_URL/auth/callback.
type SSOConfig struct {
	// Enabled is OIDC_ENABLED ("true"/"1" or "false"/"0", default false).
	Enabled bool
	// EnabledSet reports whether OIDC_ENABLED held a recognised boolean, so
	// the effective configuration can say whether the value came from the
	// environment or is the default.
	EnabledSet bool
	// IssuerURL is OIDC_ISSUER_URL, raw.
	IssuerURL string
	// ClientID is OIDC_CLIENT_ID, raw.
	ClientID string
	// ClientSecret is OIDC_CLIENT_SECRET, raw.
	ClientSecret string
	// Scopes is OIDC_SCOPES, raw and space-separated.
	Scopes string
	// RoleMapping is OIDC_ROLE_MAPPING, raw JSON: an array of
	// {"claim","value","role_id"} objects.
	RoleMapping string
}

func loadSSOConfig() SSOConfig {
	enabledRaw := strings.ToLower(strings.TrimSpace(os.Getenv("OIDC_ENABLED")))
	return SSOConfig{
		Enabled:      getEnvAsBool("OIDC_ENABLED", false),
		EnabledSet:   enabledRaw == "true" || enabledRaw == "1" || enabledRaw == "false" || enabledRaw == "0",
		IssuerURL:    os.Getenv("OIDC_ISSUER_URL"),
		ClientID:     os.Getenv("OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		Scopes:       os.Getenv("OIDC_SCOPES"),
		RoleMapping:  os.Getenv("OIDC_ROLE_MAPPING"),
	}
}

// OIDCIssuerMaxBytes caps an issuer URL.
const OIDCIssuerMaxBytes = 2048

// ParseOIDCIssuer validates an OpenID Connect issuer URL and returns it
// trimmed and otherwise unchanged, because issuers are compared exactly. It
// applies the same https rule as the AEO base URLs (ParseAEOEndpoint): https
// only, a strict host and port, no credentials, query, fragment or zone
// identifier. A path is allowed (Entra ID issuers end in /<tenant>/v2.0). The
// error never echoes the value.
func ParseOIDCIssuer(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > OIDCIssuerMaxBytes {
		return "", errors.New("issuer URL is longer than 2048 bytes")
	}
	if _, err := ParseAEOEndpoint(trimmed, false); err != nil {
		reason := strings.TrimPrefix(err.Error(), "base URL ")
		return "", errors.New("issuer URL " + reason)
	}
	return trimmed, nil
}
