package service

import (
	"github.com/florinel-chis/gophercrm/internal/config"
	"strings"
)

// Configuration keys holding the answer-engine credentials. They are seeded by
// models.DefaultConfigurations as sensitive entries, so their values are
// encrypted at rest and are read back only through ConfigurationService.GetSecret.
const (
	ConfigAEOAnthropicKey  = "integration.aeo.anthropic_api_key"
	ConfigAEOOpenAIKey     = "integration.aeo.openai_api_key"
	ConfigAEOGeminiKey     = "integration.aeo.gemini_api_key"
	ConfigAEOMoonshotKey   = "integration.aeo.moonshot_api_key"
	ConfigAEOPerplexityKey = "integration.aeo.perplexity_api_key"

	// ConfigAEOGenerationEngine names the engine prompt generation runs on.
	// Unlike the key entries above it is not sensitive: it holds a provider
	// name, not a credential.
	ConfigAEOGenerationEngine = "integration.aeo.generation_engine"
)

// EffectiveAEOConfig overlays the administrator-stored provider keys on the
// boot-time keys, models and endpoint configuration and returns the result.
//
// A stored key wins over the environment; an entry that was never set, has been
// cleared, or cannot be read falls back to whatever the environment provided.
// Models and base URLs follow the same precedence using non-sensitive settings.
// The custom engine and the schedule stay environment-only.
//
// It is called on every run creation and every provider-status read, so a key
// entered in the UI takes effect without a restart. A configuration lookup that
// fails is not an error here: the environment value is a better answer than a
// broken module.
func EffectiveAEOConfig(base config.AEOConfig, configs ConfigurationService) config.AEOConfig {
	if configs == nil {
		return base
	}

	overlay := func(key string, target *string) {
		secret, err := configs.GetSecret(key)
		if err != nil || secret == "" {
			return
		}
		*target = secret
	}

	overlay(ConfigAEOAnthropicKey, &base.AnthropicAPIKey)
	overlay(ConfigAEOOpenAIKey, &base.OpenAIAPIKey)
	overlay(ConfigAEOGeminiKey, &base.GeminiAPIKey)
	overlay(ConfigAEOMoonshotKey, &base.MoonshotAPIKey)
	overlay(ConfigAEOPerplexityKey, &base.PerplexityAPIKey)

	for _, provider := range []struct {
		name           string
		model, baseURL *string
	}{
		{"anthropic", &base.AnthropicModel, &base.AnthropicBaseURL},
		{"openai", &base.OpenAIModel, &base.OpenAIBaseURL},
		{"gemini", &base.GeminiModel, &base.GeminiBaseURL},
		{"kimi", &base.KimiModel, &base.KimiBaseURL},
		{"perplexity", &base.PerplexityModel, &base.PerplexityBaseURL},
	} {
		for suffix, target := range map[string]*string{"model": provider.model, "base_url": provider.baseURL} {
			value, err := configs.GetString("integration.aeo." + provider.name + "_" + suffix)
			if err == nil && strings.TrimSpace(value) != "" {
				*target = strings.TrimSpace(value)
			}
		}
	}

	return base
}
