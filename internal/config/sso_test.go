package config

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_SSOEnvironment(t *testing.T) {
	withCleanEnv(t, map[string]string{"JWT_SECRET": validSecret()}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, SSOConfig{}, cfg.SSO, "nothing set: SSO off and every field empty")
	})

	withCleanEnv(t, map[string]string{
		"JWT_SECRET":           validSecret(),
		"OIDC_ENABLED":         "true",
		"OIDC_ALLOW_PASSWORD":  "1",
		"OIDC_ISSUER_URL":      "https://idp.example.com",
		"OIDC_CLIENT_ID":       "crm",
		"OIDC_CLIENT_SECRET":   "not-a-real-secret",
		"OIDC_SCOPES":          "openid email",
		"OIDC_ROLE_MAPPING":    `[{"claim":"groups","value":"g1","role_id":2}]`,
		"OIDC_GRAPH_MAX_PAGES": "7",
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, SSOConfig{
			Enabled:          true,
			EnabledSet:       true,
			AllowPassword:    true,
			AllowPasswordSet: true,
			IssuerURL:        "https://idp.example.com",
			ClientID:         "crm",
			ClientSecret:     "not-a-real-secret",
			Scopes:           "openid email",
			RoleMapping:      `[{"claim":"groups","value":"g1","role_id":2}]`,
			GraphMaxPages:    7,
		}, cfg.SSO)
	})

	// An explicit false is recognised, so the source can say env.
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":          validSecret(),
		"OIDC_ALLOW_PASSWORD": "false",
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.SSO.AllowPassword)
		assert.True(t, cfg.SSO.AllowPasswordSet)
	})

	// Invalid values never stop the boot; the service judges them.
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":           validSecret(),
		"OIDC_ENABLED":         "maybe",
		"OIDC_ALLOW_PASSWORD":  "yes",
		"OIDC_ISSUER_URL":      "http://not-https.example.com",
		"OIDC_ROLE_MAPPING":    "not json",
		"OIDC_GRAPH_MAX_PAGES": "many",
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.SSO.Enabled)
		assert.False(t, cfg.SSO.EnabledSet)
		assert.False(t, cfg.SSO.AllowPassword, "an unrecognised value counts as unset")
		assert.False(t, cfg.SSO.AllowPasswordSet)
		assert.Equal(t, "http://not-https.example.com", cfg.SSO.IssuerURL)
		assert.Equal(t, 0, cfg.SSO.GraphMaxPages, "a non-number reads as unset")
	})
}

func TestEnvExample_OIDCVariablesDocumented(t *testing.T) {
	text := readRepositoryFile(t, ".env.example")
	for _, name := range []string{"OIDC_ENABLED", "OIDC_ALLOW_PASSWORD", "OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_SCOPES", "OIDC_ROLE_MAPPING", "OIDC_GRAPH_MAX_PAGES"} {
		assert.Regexp(t, regexp.MustCompile(`(?m)^(# )?`+name+`=`), text, "%s is documented", name)
		assert.NotRegexp(t, regexp.MustCompile(`(?m)^`+name+`=`), text, "%s stays commented", name)
	}
	assert.Contains(t, text, "Single sign-on (OpenID Connect)")
}

func TestParseOIDCIssuer(t *testing.T) {
	for _, raw := range []string{
		"https://login.microsoftonline.com/tenant-id/v2.0",
		"https://idp.example.com:8443",
		"https://idp.example.com",
	} {
		got, err := ParseOIDCIssuer(raw)
		assert.NoError(t, err, raw)
		assert.Equal(t, raw, got, "returned unchanged")
	}

	got, err := ParseOIDCIssuer("  https://idp.example.com/realms/crm  ")
	assert.NoError(t, err)
	assert.Equal(t, "https://idp.example.com/realms/crm", got, "only trimmed")

	atLimit := "https://idp.example.com/" + strings.Repeat("a", 512-len("https://idp.example.com/"))
	require.Len(t, atLimit, 512)
	got, err = ParseOIDCIssuer(atLimit)
	assert.NoError(t, err, "512 bytes is the width of user_identities.issuer")
	assert.Equal(t, atLimit, got)

	long := "https://idp.example.com/" + strings.Repeat("a", 2049-len("https://idp.example.com/"))
	require.Len(t, long, 2049)
	overLimit := atLimit + "a"
	require.Len(t, overLimit, 513)
	_, err = ParseOIDCIssuer(overLimit)
	require.Error(t, err)
	assert.Equal(t, "issuer URL is longer than 512 bytes", err.Error())
	for _, raw := range []string{
		"",
		"http://idp.example.com",
		"https://user:pw@idp.example.com",
		"https://idp.example.com/?a=1",
		"https://idp.example.com/?",
		"https://idp.example.com/#f",
		"idp.example.com",
		"https://idp.example.com:99999",
		"https://[fe80::1%25en0]/",
		long,
		overLimit,
	} {
		_, err := ParseOIDCIssuer(raw)
		if assert.Error(t, err, "%.60q", raw) {
			assert.True(t, strings.HasPrefix(err.Error(), "issuer URL "), err.Error())
			if raw != "" {
				assert.NotContains(t, err.Error(), raw, "the error never echoes the value")
			}
		}
	}
}
