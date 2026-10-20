package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAEOEndpointSyntax covers the administrator base URL settings of every
// configured provider: only absolute HTTPS URLs with a well-formed host and
// port are stored, blank keeps the environment/default precedence, and a
// rejection never echoes the value or changes what was stored.
func TestAEOEndpointSyntax(t *testing.T) {
	valid := []struct{ input, want string }{
		{" https://api.openai.com/v1/ ", "https://api.openai.com/v1/"},
		{"https://generativelanguage.googleapis.com/v1beta/openai/", "https://generativelanguage.googleapis.com/v1beta/openai/"},
		{"https://api.perplexity.ai", "https://api.perplexity.ai"},
		{"https://proxy.example.com:8443/v1", "https://proxy.example.com:8443/v1"},
		{"HTTPS://API.Moonshot.ai/v1", "HTTPS://API.Moonshot.ai/v1"},
		{"https://[2001:4860::1]/v1", "https://[2001:4860::1]/v1"},
		{"   ", ""},
		{"", ""},
	}
	invalid := []string{
		"http://api.openai.com/v1",
		"http://localhost:1234/v1",
		"ftp://example.com",
		"/v1",
		"api.openai.com/v1",
		"https://",
		"https:///v1",
		"https:api.openai.com",
		"mailto:ops@example.com",
		"https://user:sk-fake-secret@api.openai.com/v1",
		"https://sk-fake-secret@api.openai.com/v1",
		"https://api.openai.com/v1?key=sk-fake-secret",
		"https://api.openai.com/v1?",
		"https://api.openai.com/v1#fragment",
		"https://api.openai.com/v1#",
		"https://api.openai.com:/v1",
		"https://api.openai.com:0/v1",
		"https://api.openai.com:0443/v1",
		"https://api.openai.com:65536/v1",
		"https://api.openai.com:https/v1",
		"https://[fe80::1%25en0]/v1",
		"https://[::1/v1",
		"https://[127.0.0.1]/v1",
		"https://api.openai.com./v1",
		"https://-api.openai.com/v1",
		"https://api_openai.com/v1",
		"https://2130706433/v1",
		"https://127.1/v1",
		"https://api.openai.com/v1 extra",
		"https://api.openai.com\\@evil.example/v1",
		"https://api.openai.com/\x00v1",
		"https://bücher.example/v1",
	}

	for _, provider := range aeoConfigProviders {
		key := "integration.aeo." + provider + "_base_url"
		for _, tc := range valid {
			t.Run(provider+"/valid/"+tc.input, func(t *testing.T) {
				setting := Configuration{Key: key, Type: ConfigTypeString, Value: "unchanged"}
				require.NoError(t, setting.SetValue(tc.input))
				assert.Equal(t, tc.want, setting.Value)
			})
		}
		for _, input := range invalid {
			t.Run(provider+"/invalid/"+input, func(t *testing.T) {
				setting := Configuration{Key: key, Type: ConfigTypeString, Value: "unchanged"}
				err := setting.SetValue(input)
				require.Error(t, err)
				assert.Equal(t, "unchanged", setting.Value, "a rejected value is never stored")
				assert.NotContains(t, err.Error(), "sk-fake-secret", "the error never echoes the value")
			})
		}
	}

	// The custom engine and the egress policy have no settings at all: they
	// are operator environment configuration only.
	for _, entry := range DefaultConfigurations() {
		assert.NotContains(t, entry.Key, "egress", "egress trust must not be a stored setting")
		assert.NotContains(t, entry.Key, "custom_base_url")
	}
}
