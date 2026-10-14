package models

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
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

// These settings are visible to administrators and endpoints are logged by
// the SDK. Credentials belong in the encrypted API-key settings, never URLs.
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
		endpoint, err := url.Parse(value)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" ||
			endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
			return "", fmt.Errorf("base URL must be an absolute HTTP(S) URL without credentials, query parameters or a fragment")
		}
		return value, nil
	}
	return value, nil
}
