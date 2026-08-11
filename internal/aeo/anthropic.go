package aeo

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
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
}

// NewAnthropicProvider builds a provider for the given model. An empty baseURL
// selects the SDK default; tests pass an httptest server URL, against which the
// SDK resolves the "v1/messages" path.
func NewAnthropicProvider(apiKey, model, baseURL string) *AnthropicProvider {
	opts := []anthropicopt.RequestOption{
		anthropicopt.WithAPIKey(apiKey),
		anthropicopt.WithMaxRetries(providerMaxRetries),
	}
	if baseURL != "" {
		opts = append(opts, anthropicopt.WithBaseURL(baseURL))
	}

	// Never log the key itself, only whether one is present.
	logProvider().WithFields(map[string]any{
		"provider":    ProviderAnthropic,
		"model":       model,
		"key_present": apiKey != "",
	}).Debug("AEO provider configured")

	return &AnthropicProvider{
		client: anthropic.NewClient(opts...),
		model:  model,
	}
}

func (p *AnthropicProvider) Name() string { return ProviderAnthropic }

func (p *AnthropicProvider) Model() string { return p.model }

// Query sends the prompt verbatim, with no system prompt, so the answer
// approximates what a real user asking the same question would see.
func (p *AnthropicProvider) Query(ctx context.Context, prompt string) (ProviderAnswer, error) {
	message, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: maxAnswerTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return ProviderAnswer{}, fmt.Errorf("%s: %w", p.Name(), err)
	}
	if message == nil {
		return ProviderAnswer{}, fmt.Errorf("%s: empty response", p.Name())
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
