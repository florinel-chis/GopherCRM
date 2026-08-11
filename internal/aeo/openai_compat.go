package aeo

import (
	"context"
	"encoding/json"
	"fmt"

	openai "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"
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
	// BaseURL empty selects the SDK default, which is only correct for OpenAI.
	BaseURL string
	// NativeCitations is true only for Perplexity, which returns the sources it
	// consulted as a non-standard top-level response field.
	NativeCitations bool
}

// OpenAICompatProvider is the single wrapper shared by every OpenAI-compatible
// engine. Behaviour differs only by base URL, model and citation handling.
type OpenAICompatProvider struct {
	client openai.Client
	cfg    OpenAICompatConfig
}

// NewOpenAICompatProvider builds a provider for the given engine.
func NewOpenAICompatProvider(cfg OpenAICompatConfig) *OpenAICompatProvider {
	opts := []openaiopt.RequestOption{
		openaiopt.WithAPIKey(cfg.APIKey),
		openaiopt.WithMaxRetries(providerMaxRetries),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, openaiopt.WithBaseURL(cfg.BaseURL))
	}

	// Never log the key itself, only whether one is present.
	logProvider().WithFields(map[string]any{
		"provider":    cfg.Name,
		"model":       cfg.Model,
		"base_url":    cfg.BaseURL,
		"key_present": cfg.APIKey != "",
	}).Debug("AEO provider configured")

	return &OpenAICompatProvider{
		client: openai.NewClient(opts...),
		cfg:    cfg,
	}
}

func (p *OpenAICompatProvider) Name() string { return p.cfg.Name }

func (p *OpenAICompatProvider) Model() string { return p.cfg.Model }

// Query sends the prompt verbatim, with no system prompt, so the answer
// approximates what a real user asking the same question would see.
func (p *OpenAICompatProvider) Query(ctx context.Context, prompt string) (ProviderAnswer, error) {
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(p.cfg.Model),
		MaxTokens: openai.Int(maxAnswerTokens),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		return ProviderAnswer{}, fmt.Errorf("%s: %w", p.Name(), err)
	}
	if resp == nil {
		return ProviderAnswer{}, fmt.Errorf("%s: empty response", p.Name())
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
		logProvider().WithFields(map[string]any{
			"provider": p.Name(),
			"error":    err.Error(),
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
