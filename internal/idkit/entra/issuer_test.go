package entra

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTenant is the tenant id of the test issuers.
const testTenant = "6f0c3a1e-0505-4d2b-9a11-000000000505"

func TestOIDCEntraTenant(t *testing.T) {
	for _, tc := range []struct{ issuer, want string }{
		{"https://login.microsoftonline.com/" + testTenant + "/v2.0", testTenant},
		{"https://login.microsoftonline.com/" + testTenant, testTenant},
		{"https://login.microsoftonline.com/" + testTenant + "/", testTenant},
		{"https://login.microsoftonline.com/common/v2.0", "common"},
		{"https://login.microsoftonline.com", ""},
		{"https://login.microsoftonline.com/", ""},
		{"https://login.microsoftonline.com//v2.0", ""},
		{"https://login.microsoftonline.com/%zz/v2.0", ""},
		{"", ""},
	} {
		assert.Equal(t, tc.want, Tenant(tc.issuer), tc.issuer)
	}
}

func TestEntraIssuerAndNextLink(t *testing.T) {
	assert.Equal(t, "login.microsoftonline.com", IssuerHost)
	assert.Equal(t, "https://graph.microsoft.com", GraphBaseURL)
	assert.Equal(t, 5*time.Second, GraphTimeout)
	assert.Equal(t, "https://graph.microsoft.com/User.Read", GraphScope)
	assert.Equal(t, "/v1.0/me/getMemberObjects", memberObjectsPath)
	assert.Equal(t, `{"securityEnabledOnly":true}`, memberObjectsBody)
	assert.Equal(t, 1<<20, PageMaxBytes)
	assert.Equal(t, 10, DefaultMaxPages)
	assert.Equal(t, 50, MaxPagesLimit)

	for issuer, want := range map[string]bool{
		"https://login.microsoftonline.com/tid/v2.0":     true,
		"https://LOGIN.microsoftonline.com/tid/v2.0":     true,
		"https://login.microsoftonline.com:443/tid/v2.0": true,
		"https://login.microsoftonline.com.evil.test/x":  false,
		"https://evil.test/login.microsoftonline.com":    false,
		"https://login.microsoftonline.us/tid/v2.0":      false,
		"https://login.chinacloudapi.cn/tid/v2.0":        false,
		"not a url %": false,
	} {
		assert.Equal(t, want, IsIssuer(issuer, IssuerHost), issuer)
	}
	assert.False(t, IsIssuer("https://login.microsoftonline.com/tid/v2.0", ""), "no Entra host configured")

	for raw, want := range map[int]int{0: 10, -1: 10, 51: 10, 1: 1, 50: 50, 7: 7} {
		assert.Equal(t, want, EffectiveMaxPages(raw), "maxPages=%d", raw)
	}

	// The nextLink rule: https on the Graph host and port only.
	base, err := url.Parse("https://graph.microsoft.com")
	require.NoError(t, err)
	for link, want := range map[string]bool{
		"https://graph.microsoft.com/v1.0/me/getMemberObjects?$skiptoken=x": true,
		"https://GRAPH.microsoft.com:443/v1.0/me/getMemberObjects":          true,
		"http://graph.microsoft.com/v1.0/me/getMemberObjects":               false,
		"https://graph.microsoft.com:8443/v1.0/me/getMemberObjects":         false,
		"https://graph.microsoft.com.evil.test/v1.0/me/getMemberObjects":    false,
		"https://user@graph.microsoft.com/v1.0/me/getMemberObjects":         false,
		"/v1.0/me/getMemberObjects":                                         false,
		"https://graph.windows.net/v1.0/me/getMemberObjects":                false,
	} {
		assert.Equal(t, want, sameOrigin(link, base), link)
	}
}
