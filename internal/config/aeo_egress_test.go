package config

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAEOOperatorEgressConfig pins the environment-only egress policy: exact
// HTTPS custom origins, narrow local exceptions, and fail-closed parsing that
// never echoes the configured value.
func TestAEOOperatorEgressConfig(t *testing.T) {
	t.Run("an empty policy trusts nothing beyond the official origins", func(t *testing.T) {
		policy := ParseAEOEgressPolicy("", " , ")
		require.NoError(t, policy.Err)
		assert.Empty(t, policy.AllowedOrigins)
		assert.Empty(t, policy.LocalOrigins)
	})

	t.Run("exact custom origins are parsed per engine", func(t *testing.T) {
		policy := ParseAEOEgressPolicy(
			"openai=https://LLM-Proxy.example.com, custom=https://llm.example.com:8443/", "")
		require.NoError(t, policy.Err)
		require.Len(t, policy.AllowedOrigins, 2)
		assert.Equal(t, AEOEgressOpenAI, policy.AllowedOrigins[0].Provider)
		assert.Equal(t, "https://llm-proxy.example.com:443", policy.AllowedOrigins[0].Origin())
		assert.Equal(t, AEOEgressCustom, policy.AllowedOrigins[1].Provider)
		assert.Equal(t, "https://llm.example.com:8443", policy.AllowedOrigins[1].Origin())
		assert.Empty(t, policy.AllowedOrigins[0].Prefixes, "a remote origin never opens private addresses")
	})

	t.Run("local exceptions are scoped to one engine, origin and address range", func(t *testing.T) {
		policy := ParseAEOEgressPolicy("",
			"custom=http://127.0.0.1:1234,custom=http://ollama:11434@172.18.0.0/16,"+
				"anthropic=http://localhost:8080,custom=https://[fd12:3456::7]:8443,openai=http://10.0.0.5:8000@10.0.0.0/24")
		require.NoError(t, policy.Err)
		require.Len(t, policy.LocalOrigins, 5)

		loopback := policy.LocalOrigins[0]
		assert.Equal(t, AEOEgressCustom, loopback.Provider)
		assert.Equal(t, "http://127.0.0.1:1234", loopback.Origin())
		assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, loopback.Prefixes,
			"an address literal opens exactly that address")

		named := policy.LocalOrigins[1]
		assert.Equal(t, "ollama", named.Host)
		assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, named.Prefixes)

		localhost := policy.LocalOrigins[2]
		assert.Equal(t, AEOEgressAnthropic, localhost.Provider)
		assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
			localhost.Prefixes)

		ipv6 := policy.LocalOrigins[3]
		assert.Equal(t, "fd12:3456::7", ipv6.Host)
		assert.Equal(t, "8443", ipv6.Port)

		assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, policy.LocalOrigins[4].Prefixes)
	})

	malformed := []struct {
		name, allowed, local string
	}{
		{name: "unknown engine", allowed: "mistral=https://api.mistral.ai"},
		{name: "missing engine", allowed: "https://api.example.com"},
		{name: "remote origin over http", allowed: "openai=http://proxy.example.com"},
		{name: "remote origin with a path", allowed: "openai=https://proxy.example.com/v1"},
		{name: "remote origin with credentials", allowed: "openai=https://user:sk-secret@proxy.example.com"},
		{name: "remote origin with a query", allowed: "openai=https://proxy.example.com?token=sk-secret"},
		{name: "remote address literal", allowed: "openai=https://8.8.8.8"},
		{name: "remote zone identifier", allowed: "openai=https://[fe80::1%25en0]"},
		{name: "local public address", local: "custom=http://8.8.8.8:80"},
		{name: "local link-local address", local: "custom=http://169.254.10.10:80"},
		{name: "local metadata address", local: "custom=http://169.254.169.254"},
		{name: "local metadata range", local: "custom=http://imds:80@169.254.0.0/16"},
		{name: "local range containing IPv6 metadata", local: "custom=http://imds:80@fd00:ec2::/32"},
		{name: "local range containing Alibaba metadata", local: "custom=http://imds:80@100.64.0.0/10"},
		{name: "local GCP IPv6 metadata address", local: "custom=http://[fd20:ce::254]"},
		{name: "local range containing GCP IPv6 metadata", local: "custom=http://internal.example@fd20:ce::/64"},
		{name: "local EKS Pod Identity IPv6 address", local: "custom=http://[fd00:ec2::23]"},
		{name: "local range containing EKS Pod Identity IPv6", local: "custom=http://internal.example@fd00:ec2::20/124"},
		{name: "local EKS Pod Identity IPv4 address", local: "custom=http://169.254.170.23"},
		{name: "local range wider than a private block", local: "custom=http://llm:80@0.0.0.0/0"},
		{name: "local range spanning private blocks", local: "custom=http://llm:80@172.0.0.0/8"},
		{name: "local host name without addresses", local: "custom=http://ollama:11434"},
		{name: "local range not containing the literal", local: "custom=http://10.0.0.5@10.1.0.0/16"},
		{name: "local IPv4-mapped literal", local: "custom=http://[::ffff:127.0.0.1]:1234"},
		{name: "local IPv4-mapped range", local: "custom=http://llm:80@::ffff:10.0.0.0/104"},
		{name: "local malformed range", local: "custom=http://llm:80@10.0.0.0/33"},
		{name: "local path", local: "custom=http://127.0.0.1:1234/v1"},
		{name: "local bad port", local: "custom=http://127.0.0.1:0"},
	}
	for _, tc := range malformed {
		t.Run("fails closed: "+tc.name, func(t *testing.T) {
			policy := ParseAEOEgressPolicy(
				"openai=https://proxy.example.com,"+tc.allowed, "custom=http://127.0.0.1:1234,"+tc.local)
			require.Error(t, policy.Err)
			assert.Empty(t, policy.AllowedOrigins, "a malformed policy keeps no trusted origin")
			assert.Empty(t, policy.LocalOrigins, "a malformed policy keeps no local exception")
			assert.NotContains(t, policy.Err.Error(), "sk-secret", "errors never echo the configured value")
			assert.Contains(t, policy.Err.Error(), "entry 2")
		})
	}

	t.Run("Load reads the policy from the environment only", func(t *testing.T) {
		withCleanEnv(t, map[string]string{
			"JWT_SECRET":                 validSecret(),
			"AEO_EGRESS_ALLOWED_ORIGINS": "perplexity=https://search-proxy.example.com",
			"AEO_EGRESS_LOCAL_ORIGINS":   "custom=http://127.0.0.1:1234",
			"AEO_CUSTOM_BASE_URL":        "http://127.0.0.1:1234/v1",
		}, func() {
			cfg, err := Load()
			require.NoError(t, err)
			require.NoError(t, cfg.AEO.Egress.Err)
			require.Len(t, cfg.AEO.Egress.AllowedOrigins, 1)
			assert.Equal(t, AEOEgressPerplexity, cfg.AEO.Egress.AllowedOrigins[0].Provider)
			require.Len(t, cfg.AEO.Egress.LocalOrigins, 1)
			assert.Equal(t, "http://127.0.0.1:1234", cfg.AEO.Egress.LocalOrigins[0].Origin())
		})
	})

	t.Run("Load keeps the CRM running on a malformed policy but fails it closed", func(t *testing.T) {
		withCleanEnv(t, map[string]string{
			"JWT_SECRET":               validSecret(),
			"AEO_EGRESS_LOCAL_ORIGINS": "custom=http://169.254.169.254",
		}, func() {
			cfg, err := Load()
			require.NoError(t, err)
			require.Error(t, cfg.AEO.Egress.Err)
			assert.Empty(t, cfg.AEO.Egress.LocalOrigins)
		})
	})

	t.Run("Load defaults to no extra trust", func(t *testing.T) {
		withCleanEnv(t, map[string]string{"JWT_SECRET": validSecret()}, func() {
			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, AEOEgressPolicy{}, cfg.AEO.Egress)
		})
	})

	t.Run("local address classification excludes metadata and public space", func(t *testing.T) {
		assert.True(t, AEOLocalAddress(netip.MustParseAddr("127.0.0.1")))
		assert.True(t, AEOLocalAddress(netip.MustParseAddr("::ffff:10.1.2.3")))
		assert.True(t, AEOLocalAddress(netip.MustParseAddr("fd12::1")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("100.100.100.200")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("fd00:ec2::254")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("fd00:ec2::23")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("fd20:ce::254")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("169.254.169.254")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("169.254.170.23")))
		assert.False(t, AEOLocalAddress(netip.MustParseAddr("8.8.8.8")))
	})

	// Hard-coded, not derived from AEOMetadataAddresses, so an omission from
	// that list fails here. Sources: Google Cloud metadata overview and AWS EKS
	// Pod Identity agent setup (see docs/AEO_SECURITY.md).
	t.Run("IPv6 credential endpoints inside ULA space stay refused", func(t *testing.T) {
		for _, entry := range []string{
			"custom=http://[fd20:ce::254]",
			"custom=http://[fd20:ce::254]:80",
			"custom=http://internal.example@fd20:ce::/64",
			"custom=http://internal.example@fd20:ce::254",
			"custom=http://[fd00:ec2::23]",
			"custom=http://[fd00:ec2::23]:80",
			"custom=http://internal.example@fd00:ec2::20/124",
			"custom=http://internal.example@fd00:ec2::23",
		} {
			policy := ParseAEOEgressPolicy("", entry)
			assert.Error(t, policy.Err, entry)
			assert.Empty(t, policy.LocalOrigins, entry)
		}

		// Neighbouring ULA endpoints remain authorizable.
		for _, entry := range []string{
			"custom=http://[fd20:ce::253]",
			"custom=http://internal.example@fd20:ce::100/120",
			"custom=http://[fd00:ec2::22]",
			"custom=http://internal.example@fd00:ec2::10/124",
			"custom=http://internal.example@fd12:3456::/64",
		} {
			policy := ParseAEOEgressPolicy("", entry)
			assert.NoError(t, policy.Err, entry)
			assert.Len(t, policy.LocalOrigins, 1, entry)
		}
	})
}
