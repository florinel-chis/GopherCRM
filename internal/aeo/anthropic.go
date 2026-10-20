package aeo

import (
	"context"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// anthropicTextBlockType is the discriminator of a text content block. Answers
// may also carry thinking or tool blocks, which contribute no prose and are
// skipped.
const anthropicTextBlockType = "text"

// AnthropicProvider queries the Anthropic Messages API through the official Go
// SDK. Retries and backoff are the SDK's job (see providerMaxRetries).
type AnthropicProvider struct {
	client anthropic.Client
	model  string

	// blocked is set when the egress policy refused the endpoint; the client
	// was then never built and every query fails without a request.
	blocked error
}

// NewAnthropicProvider builds a provider for the given model. An empty baseURL
// selects the official endpoint, against which the SDK resolves the
// "v1/messages" path. The client sends only through guard; a nil guard means
// the default policy, which trusts only the official origin.
func NewAnthropicProvider(apiKey, model, baseURL string, guard *EgressGuard) *AnthropicProvider {
	if guard == nil {
		guard = NewEgressGuard(config.AEOEgressPolicy{})
	}
	baseURL = providerBaseURL(baseURL, AnthropicBaseURL)
	provider := &AnthropicProvider{model: model}

	// Never log the key or the endpoint itself, only whether one is present
	// and whether the policy accepted it.
	logger := logProvider().WithFields(map[string]any{
		"provider":    ProviderAnthropic,
		"model":       model,
		"key_present": apiKey != "",
	})

	httpClient, err := guard.httpClient(config.AEOEgressAnthropic, baseURL)
	if err != nil {
		provider.blocked = err
		logger.WithField("failure_category", FailureEndpointNotPermitted).
			Warn("AEO provider endpoint is not permitted by the egress policy; its queries will fail")
		return provider
	}

	provider.client = anthropic.NewClient(
		anthropicopt.WithAPIKey(apiKey),
		anthropicopt.WithMaxRetries(providerMaxRetries),
		anthropicopt.WithBaseURL(baseURL),
		anthropicopt.WithHTTPClient(httpClient),
	)
	logger.Debug("AEO provider configured")
	return provider
}

func (p *AnthropicProvider) Name() string { return ProviderAnthropic }

func (p *AnthropicProvider) Model() string { return p.model }

// Query sends the prompt verbatim, with no system prompt, so the answer
// approximates what a real user asking the same question would see. Every
// failure is returned as a sanitized *ProviderError.
func (p *AnthropicProvider) Query(ctx context.Context, prompt string) (ProviderAnswer, error) {
	if p.blocked != nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), p.blocked)
	}
	message, err := p.client.Messages.New(withEgressLatch(ctx), anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: maxAnswerTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), err)
	}
	if message == nil {
		return ProviderAnswer{}, SanitizeProviderError(p.Name(), errEmptyResponse)
	}

	// A response with no text blocks is not an error: it simply yields no
	// mentions.
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == anthropicTextBlockType {
			text.WriteString(block.Text)
		}
	}

	return ProviderAnswer{Text: text.String()}, nil
}
