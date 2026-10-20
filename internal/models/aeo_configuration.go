package models

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/config"
)

var aeoConfigProviders = []string{"anthropic", "openai", "gemini", "kimi", "perplexity"}

func defaultAEOProviderConfigurations() []Configuration {
	var configs []Configuration
	for _, provider := range aeoConfigProviders {
		for _, setting := range []string{"model", "base_url"} {
			configs = append(configs, Configuration{
				Key:  "integration.aeo." + provider + "_" + setting,
				Type: ConfigTypeString, Category: CategoryIntegration, IsSystem: true,
				Description: provider + " " + setting + " override; blank uses the server environment or provider default",
			})
		}
	}
	return configs
}

// These settings are visible to administrators. Credentials belong in the
// encrypted API-key settings, never URLs. A stored base URL must be HTTPS: an
// override can only select an origin the operator's environment egress policy
// already trusts for that engine (see docs/AEO_SECURITY.md), so passing this
// syntax check never widens where credentials may be sent.
func validateAEOProviderSetting(key, value string) (string, error) {
	for _, provider := range aeoConfigProviders {
		prefix := "integration.aeo." + provider + "_"
		if key != prefix+"model" && key != prefix+"base_url" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil
		}
		if key == prefix+"model" {
			if utf8.RuneCountInString(value) > AEOAnswerModelMaxLength {
				return "", fmt.Errorf("model must be at most %d characters", AEOAnswerModelMaxLength)
			}
			return value, nil
		}
		if _, err := config.ParseAEOEndpoint(value, false); err != nil {
			return "", fmt.Errorf("base URL must be an absolute HTTPS URL with a valid host and port, without credentials, query parameters, a fragment or a zone identifier")
		}
		return value, nil
	}
	return value, nil
}
