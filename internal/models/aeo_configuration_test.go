package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAEOConnectionSettings(t *testing.T) {
	for _, tc := range []struct {
		key, value, want string
		invalid          bool
	}{
		{"model", " gemini-test ", "gemini-test", false},
		{"model", strings.Repeat("界", 120), strings.Repeat("界", 120), false},
		{"model", strings.Repeat("界", 121), "", true},
		{"model", "   ", "", false},
		{"base_url", " https://example.com/v1/ ", "https://example.com/v1/", false},
		{"base_url", "http://localhost:1234/v1", "http://localhost:1234/v1", false},
		{"base_url", "", "", false},
		{"base_url", "ftp://example.com", "", true},
		{"base_url", "/v1", "", true},
		{"base_url", "https://user:secret@example.com/v1", "", true},
		{"base_url", "https://example.com/v1?key=secret", "", true},
		{"base_url", "https://example.com/v1?", "", true},
		{"base_url", "https://example.com/v1#fragment", "", true},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			setting := Configuration{Key: "integration.aeo.gemini_" + tc.key, Type: ConfigTypeString, Value: "unchanged"}
			err := setting.SetValue(tc.value)
			if tc.invalid {
				require.Error(t, err)
				assert.Equal(t, "unchanged", setting.Value)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, setting.Value)
			}
		})
	}
	setting := Configuration{Key: "general.company_name", Type: ConfigTypeString}
	require.NoError(t, setting.SetValue("  Existing spacing  "))
	assert.Equal(t, "  Existing spacing  ", setting.Value)
}

func TestDefaultAEOConnectionSettingsPreserveEnvironmentFallback(t *testing.T) {
	seeded := map[string]Configuration{}
	for _, entry := range DefaultConfigurations() {
		seeded[entry.Key] = entry
	}
	for _, provider := range []string{"anthropic", "openai", "gemini", "kimi", "perplexity"} {
		for _, suffix := range []string{"model", "base_url"} {
			entry, ok := seeded["integration.aeo."+provider+"_"+suffix]
			require.True(t, ok)
			assert.Empty(t, entry.Value)
			assert.Empty(t, entry.DefaultValue)
			assert.True(t, entry.IsSystem)
			assert.False(t, entry.IsSensitive)
		}
	}
}
