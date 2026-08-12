// Package aeo contains the Answer Engine Optimization provider layer: thin
// wrappers over the two official LLM SDKs, the answer-analysis helpers, the run
// engine and the daily scheduler.
//
// Every engine other than Anthropic speaks the OpenAI chat-completions dialect,
// so there is exactly one OpenAI-compatible wrapper (see openai_compat.go)
// parameterized by base URL. No HTTP client is hand-rolled here.
package aeo

import (
	"context"
	"errors"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"

	"github.com/sirupsen/logrus"
)

// Provider names. These are persisted verbatim in aeo_answers.provider and are
// what the frontend keys its per-engine series on, so they must not drift.
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGemini     = "gemini"
	ProviderKimi       = "kimi"
	ProviderPerplexity = "perplexity"
)

// Base URLs for the OpenAI-compatible engines. An empty base URL means "use the
// SDK default", which is only correct for OpenAI itself.
const (
	OpenAIBaseURL     = ""
	GeminiBaseURL     = "https://generativelanguage.googleapis.com/v1beta/openai/"
	KimiBaseURL       = "https://api.moonshot.ai/v1"
	PerplexityBaseURL = "https://api.perplexity.ai"
)

// Run triggers and statuses shared by the engine and the scheduler.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"

	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusPartial   = "partial"
	RunStatusFailed    = "failed"
)

// maxAnswerTokens caps every provider response. Answers are analyzed for brand
// mentions, not read end to end, so a modest cap keeps runs cheap.
const maxAnswerTokens = 1024

// providerMaxRetries is handed to both SDKs. Both retry 429 and 5xx with
// exponential backoff internally, which is why the engine does NOT wrap Query
// in a retry loop of its own — that would multiply out to four attempts.
const providerMaxRetries = 1

// ProviderAnswer is the normalized result of a single provider call.
type ProviderAnswer struct {
	// Text is the assistant's reply. An empty string is a legitimate answer,
	// not an error; it simply yields no mentions.
	Text string
	// Citations holds URLs the provider returned natively. Only Perplexity
	// populates this; for everyone else citations are extracted from Text.
	Citations []string
}

// Provider is one answer engine.
type Provider interface {
	Name() string
	Model() string
	Query(ctx context.Context, prompt string) (ProviderAnswer, error)
}

// LoadProviders builds the ordered set of configured engines from the whole
// application configuration. It is the boot-time entry point and logs the
// resulting roster once.
func LoadProviders(cfg *config.Config) []Provider {
	if cfg == nil {
		return nil
	}
	providers := LoadProvidersFor(cfg.AEO)
	logProvider().WithField("providers", providerNames(providers)).
		Info("AEO providers loaded")
	return providers
}

// LoadProvidersFor builds the ordered set of configured engines from an AEO
// configuration resolved at call time. An engine with no API key is absent (for
// the custom engine, an empty base URL means absent), so a deployment that
// configures nothing gets an empty slice and the service answers
// ErrNoProvidersConfigured.
//
// This is the variant the service calls per run and per status read, once
// administrator-stored keys are overlaid on the environment, so it logs at debug
// level: at info it would narrate every poll of the settings page.
func LoadProvidersFor(a config.AEOConfig) []Provider {
	providers := make([]Provider, 0, 6)

	if a.AnthropicAPIKey != "" {
		providers = append(providers, NewAnthropicProvider(a.AnthropicAPIKey, a.AnthropicModel, ""))
	}
	if a.OpenAIAPIKey != "" {
		providers = append(providers, NewOpenAICompatProvider(OpenAICompatConfig{
			Name:    ProviderOpenAI,
			Model:   a.OpenAIModel,
			APIKey:  a.OpenAIAPIKey,
			BaseURL: OpenAIBaseURL,
		}))
	}
	if a.GeminiAPIKey != "" {
		providers = append(providers, NewOpenAICompatProvider(OpenAICompatConfig{
			Name:    ProviderGemini,
			Model:   a.GeminiModel,
			APIKey:  a.GeminiAPIKey,
			BaseURL: GeminiBaseURL,
		}))
	}
	if a.MoonshotAPIKey != "" {
		providers = append(providers, NewOpenAICompatProvider(OpenAICompatConfig{
			Name:    ProviderKimi,
			Model:   a.KimiModel,
			APIKey:  a.MoonshotAPIKey,
			BaseURL: KimiBaseURL,
		}))
	}
	if a.PerplexityAPIKey != "" {
		providers = append(providers, NewOpenAICompatProvider(OpenAICompatConfig{
			Name:            ProviderPerplexity,
			Model:           a.PerplexityModel,
			APIKey:          a.PerplexityAPIKey,
			BaseURL:         PerplexityBaseURL,
			NativeCitations: true,
		}))
	}
	// The custom engine is keyed on its base URL: self-hosted servers (LM
	// Studio, vLLM, Ollama, TGI) usually need no API key at all.
	if a.CustomBaseURL != "" {
		providers = append(providers, NewOpenAICompatProvider(OpenAICompatConfig{
			Name:    customProviderName(a),
			Model:   a.CustomModel,
			APIKey:  a.CustomAPIKey,
			BaseURL: a.CustomBaseURL,
		}))
	}

	logProvider().WithField("providers", providerNames(providers)).
		Debug("AEO providers resolved")

	return providers
}

// ProviderStatuses reports every engine the module knows about, configured or
// not, so the UI can show which keys are missing. It never reveals key values.
func ProviderStatuses(cfg *config.Config) []models.AEOProviderStatus {
	if cfg == nil {
		return []models.AEOProviderStatus{}
	}
	return ProviderStatusesFor(cfg.AEO)
}

// ProviderStatusesFor is ProviderStatuses against an AEO configuration resolved
// at call time.
func ProviderStatusesFor(a config.AEOConfig) []models.AEOProviderStatus {
	return []models.AEOProviderStatus{
		{Name: ProviderAnthropic, Model: a.AnthropicModel, Configured: a.AnthropicAPIKey != ""},
		{Name: ProviderOpenAI, Model: a.OpenAIModel, Configured: a.OpenAIAPIKey != ""},
		{Name: ProviderGemini, Model: a.GeminiModel, Configured: a.GeminiAPIKey != ""},
		{Name: ProviderKimi, Model: a.KimiModel, Configured: a.MoonshotAPIKey != ""},
		{Name: ProviderPerplexity, Model: a.PerplexityModel, Configured: a.PerplexityAPIKey != ""},
		{Name: customProviderName(a), Model: a.CustomModel, Configured: a.CustomBaseURL != ""},
	}
}

// customProviderName falls back to "custom" when AEO_CUSTOM_NAME is blank so the
// status list never carries an empty name.
func customProviderName(a config.AEOConfig) string {
	if name := strings.TrimSpace(a.CustomName); name != "" {
		return name
	}
	return "custom"
}

func providerNames(providers []Provider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return names
}

// ProviderHTTPStatus recovers the HTTP status code from a provider error. Both
// SDKs surface API failures as a typed error carrying StatusCode; anything else
// (transport failure, context deadline) reports 0.
func ProviderHTTPStatus(err error) int {
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) {
		return openaiErr.StatusCode
	}
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) {
		return anthropicErr.StatusCode
	}
	return 0
}

// logProvider returns the configured application logger, or the logrus standard
// logger when the application has not initialized one (unit tests).
func logProvider() *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithField("component", "aeo")
}
