package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubConfigurationService answers GetSecret from a map and records how often it
// was asked. Every other method panics: EffectiveAEOConfig must read secrets
// only through GetSecret, never through a typed getter.
type stubConfigurationService struct {
	ConfigurationService
	secrets map[string]string
	errs    map[string]error
	calls   map[string]int
}

func newStubConfigurationService(secrets map[string]string) *stubConfigurationService {
	return &stubConfigurationService{
		secrets: secrets,
		errs:    map[string]error{},
		calls:   map[string]int{},
	}
}

func (s *stubConfigurationService) GetSecret(key string) (string, error) {
	s.calls[key]++
	if err := s.errs[key]; err != nil {
		return "", err
	}
	return s.secrets[key], nil
}

// envAEOConfig is the boot-time configuration: obviously fake keys, one per
// engine, so an overlay that touches the wrong field is visible.
func envAEOConfig() config.AEOConfig {
	return config.AEOConfig{
		AnthropicAPIKey:  "env-anthropic-key",
		AnthropicModel:   "env-anthropic-model",
		OpenAIAPIKey:     "env-openai-key",
		GeminiAPIKey:     "env-gemini-key",
		MoonshotAPIKey:   "env-moonshot-key",
		PerplexityAPIKey: "env-perplexity-key",
		CustomBaseURL:    "http://localhost:1234/v1",
		CustomAPIKey:     "env-custom-key",
		ScheduleEnabled:  true,
		ScheduleHour:     6,
	}
}

// The configuration key constants must name the entries that are actually
// seeded, or the overlay would silently read nothing.
func TestAEOConfigurationKeysMatchTheSeededDefaults(t *testing.T) {
	seeded := map[string]models.Configuration{}
	for _, config := range models.DefaultConfigurations() {
		seeded[config.Key] = config
	}

	for _, key := range []string{
		ConfigAEOAnthropicKey,
		ConfigAEOOpenAIKey,
		ConfigAEOGeminiKey,
		ConfigAEOMoonshotKey,
		ConfigAEOPerplexityKey,
	} {
		config, ok := seeded[key]
		require.True(t, ok, "configuration %q is not seeded", key)
		assert.True(t, config.IsSensitive)
		assert.True(t, config.IsSystem)
	}
}

func TestEffectiveAEOConfig_StoredKeysWinOverTheEnvironment(t *testing.T) {
	configs := newStubConfigurationService(map[string]string{
		ConfigAEOAnthropicKey:  "stored-anthropic-key",
		ConfigAEOOpenAIKey:     "stored-openai-key",
		ConfigAEOGeminiKey:     "stored-gemini-key",
		ConfigAEOMoonshotKey:   "stored-moonshot-key",
		ConfigAEOPerplexityKey: "stored-perplexity-key",
	})

	effective := EffectiveAEOConfig(envAEOConfig(), configs)

	assert.Equal(t, "stored-anthropic-key", effective.AnthropicAPIKey)
	assert.Equal(t, "stored-openai-key", effective.OpenAIAPIKey)
	assert.Equal(t, "stored-gemini-key", effective.GeminiAPIKey)
	assert.Equal(t, "stored-moonshot-key", effective.MoonshotAPIKey)
	assert.Equal(t, "stored-perplexity-key", effective.PerplexityAPIKey)

	// Everything else is environment-only and must survive untouched.
	assert.Equal(t, "env-anthropic-model", effective.AnthropicModel)
	assert.Equal(t, "http://localhost:1234/v1", effective.CustomBaseURL)
	assert.Equal(t, "env-custom-key", effective.CustomAPIKey)
	assert.True(t, effective.ScheduleEnabled)
	assert.Equal(t, 6, effective.ScheduleHour)
}

func TestEffectiveAEOConfig_UnsetAndClearedKeysFallBackToTheEnvironment(t *testing.T) {
	configs := newStubConfigurationService(map[string]string{
		ConfigAEOGeminiKey: "stored-gemini-key",
		// The others are absent from the map, which is what an unset or
		// cleared entry looks like: GetSecret answers "" with no error.
	})

	effective := EffectiveAEOConfig(envAEOConfig(), configs)

	assert.Equal(t, "stored-gemini-key", effective.GeminiAPIKey)
	assert.Equal(t, "env-anthropic-key", effective.AnthropicAPIKey)
	assert.Equal(t, "env-openai-key", effective.OpenAIAPIKey)
	assert.Equal(t, "env-moonshot-key", effective.MoonshotAPIKey)
	assert.Equal(t, "env-perplexity-key", effective.PerplexityAPIKey)
}

// A configuration lookup that fails — the entry is missing, or the database is
// unreachable — must not take the engine down with it.
func TestEffectiveAEOConfig_LookupFailureFallsBackToTheEnvironment(t *testing.T) {
	configs := newStubConfigurationService(map[string]string{
		ConfigAEOOpenAIKey: "stored-openai-key",
	})
	configs.errs[ConfigAEOAnthropicKey] = errors.New("connection refused")

	effective := EffectiveAEOConfig(envAEOConfig(), configs)

	assert.Equal(t, "env-anthropic-key", effective.AnthropicAPIKey)
	assert.Equal(t, "stored-openai-key", effective.OpenAIAPIKey)
}

// An engine with neither a stored nor an environment key stays unconfigured:
// the overlay never invents a value.
func TestEffectiveAEOConfig_LeavesUnconfiguredEnginesEmpty(t *testing.T) {
	configs := newStubConfigurationService(map[string]string{})

	effective := EffectiveAEOConfig(config.AEOConfig{}, configs)

	assert.Equal(t, "", effective.AnthropicAPIKey)
	assert.Equal(t, "", effective.OpenAIAPIKey)
	assert.Equal(t, "", effective.GeminiAPIKey)
	assert.Equal(t, "", effective.MoonshotAPIKey)
	assert.Equal(t, "", effective.PerplexityAPIKey)
}

func TestEffectiveAEOConfig_WithoutAConfigurationServiceReturnsTheBase(t *testing.T) {
	base := envAEOConfig()

	assert.Equal(t, base, EffectiveAEOConfig(base, nil))
}

func TestEffectiveAEOConfig_ReadsEveryKeyOnEveryCall(t *testing.T) {
	configs := newStubConfigurationService(map[string]string{})

	EffectiveAEOConfig(envAEOConfig(), configs)
	EffectiveAEOConfig(envAEOConfig(), configs)

	for _, key := range []string{
		ConfigAEOAnthropicKey,
		ConfigAEOOpenAIKey,
		ConfigAEOGeminiKey,
		ConfigAEOMoonshotKey,
		ConfigAEOPerplexityKey,
	} {
		assert.Equal(t, 2, configs.calls[key], "configuration %q was not re-read", key)
	}
}
