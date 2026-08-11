package aeo

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestMain silences the package's logging. The engine and scheduler log every
// query and every run, which would bury the assertions.
func TestMain(m *testing.M) {
	quiet := logrus.New()
	quiet.SetOutput(io.Discard)
	utils.Logger = quiet

	os.Exit(m.Run())
}

func fullyConfiguredAEO() config.AEOConfig {
	return config.AEOConfig{
		AnthropicAPIKey:  "anthropic-key",
		AnthropicModel:   "claude-opus-5",
		OpenAIAPIKey:     "openai-key",
		OpenAIModel:      "gpt-4o-mini",
		GeminiAPIKey:     "gemini-key",
		GeminiModel:      "gemini-2.5-flash",
		MoonshotAPIKey:   "moonshot-key",
		KimiModel:        "moonshot-v1-8k",
		PerplexityAPIKey: "perplexity-key",
		PerplexityModel:  "sonar",
		CustomName:       "lmstudio",
		CustomBaseURL:    "http://10.0.1.21:1234/v1",
		CustomModel:      "openai/gpt-oss-20b",
	}
}

func TestLoadProviders(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*config.AEOConfig)
		wantNames []string
	}{
		{
			name:      "every engine configured, in a stable order",
			mutate:    func(*config.AEOConfig) {},
			wantNames: []string{"anthropic", "openai", "gemini", "kimi", "perplexity", "lmstudio"},
		},
		{
			name:      "nothing configured",
			mutate:    func(a *config.AEOConfig) { *a = config.AEOConfig{} },
			wantNames: []string{},
		},
		{
			name: "an engine with no key is absent",
			mutate: func(a *config.AEOConfig) {
				a.GeminiAPIKey = ""
				a.MoonshotAPIKey = ""
			},
			wantNames: []string{"anthropic", "openai", "perplexity", "lmstudio"},
		},
		{
			name: "the custom engine is keyed on its base URL, not on a credential",
			mutate: func(a *config.AEOConfig) {
				a.CustomAPIKey = ""
			},
			wantNames: []string{"anthropic", "openai", "gemini", "kimi", "perplexity", "lmstudio"},
		},
		{
			name: "no custom base URL means no custom engine",
			mutate: func(a *config.AEOConfig) {
				a.CustomBaseURL = ""
				a.CustomAPIKey = "set-but-useless"
			},
			wantNames: []string{"anthropic", "openai", "gemini", "kimi", "perplexity"},
		},
		{
			name: "a custom engine with a blank name falls back to \"custom\"",
			mutate: func(a *config.AEOConfig) {
				a.CustomName = "   "
			},
			wantNames: []string{"anthropic", "openai", "gemini", "kimi", "perplexity", "custom"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aeoCfg := fullyConfiguredAEO()
			tc.mutate(&aeoCfg)

			providers := LoadProviders(&config.Config{AEO: aeoCfg})
			assert.Equal(t, tc.wantNames, providerNames(providers))
		})
	}
}

func TestLoadProvidersNilConfig(t *testing.T) {
	assert.Nil(t, LoadProviders(nil))
}

func TestLoadProvidersCarriesModelsAndCitationMode(t *testing.T) {
	providers := LoadProviders(&config.Config{AEO: fullyConfiguredAEO()})
	require.Len(t, providers, 6)

	byName := map[string]Provider{}
	for _, p := range providers {
		byName[p.Name()] = p
	}

	assert.Equal(t, "claude-opus-5", byName[ProviderAnthropic].Model())
	assert.Equal(t, "gpt-4o-mini", byName[ProviderOpenAI].Model())
	assert.Equal(t, "gemini-2.5-flash", byName[ProviderGemini].Model())
	assert.Equal(t, "moonshot-v1-8k", byName[ProviderKimi].Model())
	assert.Equal(t, "sonar", byName[ProviderPerplexity].Model())
	assert.Equal(t, "openai/gpt-oss-20b", byName["lmstudio"].Model())

	// Base URLs and the citation mode are wiring the compiler cannot check for
	// us, so assert them on the constructed values.
	expectedBaseURLs := map[string]string{
		ProviderOpenAI:     OpenAIBaseURL,
		ProviderGemini:     GeminiBaseURL,
		ProviderKimi:       KimiBaseURL,
		ProviderPerplexity: PerplexityBaseURL,
		"lmstudio":         "http://10.0.1.21:1234/v1",
	}
	for name, wantBaseURL := range expectedBaseURLs {
		compat, ok := byName[name].(*OpenAICompatProvider)
		require.True(t, ok, "%s must be an OpenAI-compatible provider", name)
		assert.Equal(t, wantBaseURL, compat.cfg.BaseURL, "%s base URL", name)
		assert.Equal(t, name == ProviderPerplexity, compat.cfg.NativeCitations,
			"only perplexity reads native citations")
	}

	_, isAnthropic := byName[ProviderAnthropic].(*AnthropicProvider)
	assert.True(t, isAnthropic)
}

func TestProviderStatuses(t *testing.T) {
	t.Run("reports every engine, configured or not", func(t *testing.T) {
		aeoCfg := fullyConfiguredAEO()
		aeoCfg.GeminiAPIKey = ""
		aeoCfg.CustomBaseURL = ""

		statuses := ProviderStatuses(&config.Config{AEO: aeoCfg})
		require.Len(t, statuses, 6)

		assert.Equal(t, "anthropic", statuses[0].Name)
		assert.True(t, statuses[0].Configured)
		assert.Equal(t, "claude-opus-5", statuses[0].Model)

		assert.Equal(t, "gemini", statuses[2].Name)
		assert.False(t, statuses[2].Configured)
		assert.Equal(t, "gemini-2.5-flash", statuses[2].Model, "the model is reported even when the key is missing")

		assert.Equal(t, "lmstudio", statuses[5].Name)
		assert.False(t, statuses[5].Configured)
	})

	t.Run("blank custom name falls back", func(t *testing.T) {
		statuses := ProviderStatuses(&config.Config{})
		require.Len(t, statuses, 6)
		assert.Equal(t, "custom", statuses[5].Name)
		for _, status := range statuses {
			assert.False(t, status.Configured)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		assert.Empty(t, ProviderStatuses(nil))
	})
}

func TestProviderHTTPStatus(t *testing.T) {
	openaiErr := &openai.Error{StatusCode: http.StatusTooManyRequests}
	anthropicErr := &anthropic.Error{StatusCode: http.StatusInternalServerError}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil error", err: nil, want: 0},
		{name: "plain error", err: errors.New("boom"), want: 0},
		{name: "openai error", err: openaiErr, want: http.StatusTooManyRequests},
		{name: "anthropic error", err: anthropicErr, want: http.StatusInternalServerError},
		{name: "wrapped openai error", err: fmt.Errorf("openai: %w", openaiErr), want: http.StatusTooManyRequests},
		{name: "wrapped anthropic error", err: fmt.Errorf("anthropic: %w", anthropicErr), want: http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ProviderHTTPStatus(tc.err))
		})
	}
}
