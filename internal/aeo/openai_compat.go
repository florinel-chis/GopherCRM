package aeo

import (
	"context"
	"encoding/json"

	openai "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// OpenAICompatConfig describes one engine that speaks the OpenAI
// chat-completions dialect. Every non-Anthropic engine is an instance of this:
// OpenAI itself, Gemini's compatibility endpoint, Kimi/Moonshot, Perplexity and
// any self-hosted server.
type OpenAICompatConfig struct {
	// Name is the persisted provider identifier, e.g. "openai" or "gemini".
	Name string
	// Model is the engine-specific model identifier.
	Model string
	// APIKey may be empty: self-hosted servers often need no credential, and
	// the SDK omits the Authorization header entirely when it is blank.
	APIKey string
	// BaseURL empty selects the official OpenAI endpoint.
	BaseURL string
	// NativeCitations is true only for Perplexity, which returns the sources it
	// consulted as a non-standard top-level response field.
	NativeCitations bool
	// PolicyKey names the egress policy entry that governs this engine
	// (config.AEOEgress*). Empty means Name; the custom engine sets "custom".
	PolicyKey string
	// Guard is the egress guard every request goes through. Nil means the
	// default policy, which trusts only the engine's official origin.
	Guard *EgressGuard
}

// OpenAICompatProvider is the single wrapper shared by every OpenAI-compatible
// engine. Behaviour differs only by base URL, model and citation handling.
type OpenAICompatProvider struct {
	client openai.Client
	cfg    OpenAICompatConfig

	// blocked is set when the egress policy refused the endpoint; the client
	// was then never built and every query fails without a request.
	blocked error
}

// NewOpenAICompatProvider builds a provider for the given engine.
func NewOpenAICompatProvider(cfg OpenAICompatConfig) *OpenAICompatProvider {
	if cfg.Guard == nil {
		cfg.Guard = NewEgressGuard(config.AEOEgressPolicy{})
	}
	if cfg.PolicyKey == "" {
		cfg.PolicyKey = cfg.Name
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = OpenAIBaseURL
	}
	provider := &OpenAICompatProvider{cfg: cfg}

	// Never log the key or the endpoint itself: a rejected endpoint may carry
	// a credential. Log only presence and the policy decision.
	logger := logProvider().WithFields(map[string]any{
		"provider":    cfg.Name,
		"model":       cfg.Model,
		"key_present": cfg.APIKey != "",
	})

	httpClient, err := cfg.Guard.httpClient(cfg.PolicyKey, cfg.BaseURL)
	if err != nil {
		provider.blocked = err
		logger.WithField("failure_category", FailureEndpointNotPermitted).
			Warn("AEO provider endpoint is not permitted by the egress policy; its queries will fail")
		return provider
	}

	provider.client = openai.NewClient(
		openaiopt.WithAPIKey(cfg.APIKey),
		openaiopt.WithMaxRetries(providerMaxRetries),
		openaiopt.WithBaseURL(cfg.BaseURL),
		openaiopt.WithHTTPClient(httpClient),
	)
	logger.Debug("AEO provider configured")
	return provider
}

func (p *OpenAICompatProvider) Name() string { return p.cfg.Name }

func (p *OpenAICompatProvider) Model() string { return p.cfg.Model }

// Query sends the prompt verbatim, with no system prompt, so the answer
// approximates what a real user asking the same question would see.
//
// Every failure is returned as a sanitized *ProviderError carrying a category
// and the numeric HTTP status only. Provider error bodies — including Gemini's
// [{"error": {...}}] envelope — are deliberately not read back: they can
// reflect the key, the prompt or arbitrary remote text.
func (p *OpenAICompatProvider) Query(ctx context.Context, prompt string) (ProviderAnswer, error) {
	if p.blocked != nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), p.blocked)
	}
	resp, err := p.client.Chat.Completions.New(withEgressLatch(ctx), openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(p.cfg.Model),
		MaxTokens: openai.Int(maxAnswerTokens),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), err)
	}
	if resp == nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), errEmptyResponse)
	}

	answer := ProviderAnswer{}
	// An engine may legitimately return no choices or empty content; that is an
	// answer with no mentions, not a failure.
	if len(resp.Choices) > 0 {
		answer.Text = resp.Choices[0].Message.Content
	}
	if p.cfg.NativeCitations {
		answer.Citations = p.nativeCitations(resp)
	}

	return answer, nil
}

// nativeCitationsPayload mirrors the two non-standard shapes Perplexity has
// shipped for its source list. Neither is part of the OpenAI schema, so they are
// read back off the raw response body rather than a typed SDK field.
type nativeCitationsPayload struct {
	Citations     []string `json:"citations"`
	SearchResults []struct {
		URL string `json:"url"`
	} `json:"search_results"`
}

// nativeCitations extracts provider-supplied source URLs. A body that cannot be
// decoded is logged and yields no citations — it never fails the answer, which
// is still perfectly usable for mention detection.
func (p *OpenAICompatProvider) nativeCitations(resp *openai.ChatCompletion) []string {
	raw := resp.RawJSON()
	if raw == "" {
		return nil
	}

	var payload nativeCitationsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		// The decoder's message can quote the provider's body; log the
		// category only.
		logProvider().WithFields(map[string]any{
			"provider":         p.Name(),
			"failure_category": FailureInvalidResponse,
		}).Warn("could not decode native citations from provider response")
		return nil
	}

	if len(payload.Citations) > 0 {
		return payload.Citations
	}

	urls := make([]string, 0, len(payload.SearchResults))
	for _, result := range payload.SearchResults {
		if result.URL != "" {
			urls = append(urls, result.URL)
		}
	}
	if len(urls) == 0 {
		return nil
	}
	return urls
}
