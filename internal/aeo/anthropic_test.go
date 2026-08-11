package aeo

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// messageBody builds a minimal but schema-valid Messages API response from a
// list of {type, text} content blocks expressed as raw JSON.
func messageBody(contentBlocks string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5",` +
		`"content":[` + contentBlocks + `],"stop_reason":"end_turn","stop_sequence":null,` +
		`"usage":{"input_tokens":10,"output_tokens":20}}`
}

func TestAnthropicProviderQuerySuccess(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   messageBody(`{"type":"text","text":"Acme is a good fit."}`),
	})

	provider := NewAnthropicProvider("test-key", "claude-opus-5", fake.server.URL)

	answer, err := provider.Query(context.Background(), "Which CRM would you recommend?")
	require.NoError(t, err)
	assert.Equal(t, "Acme is a good fit.", answer.Text)
	assert.Empty(t, answer.Citations)
	assert.Equal(t, ProviderAnthropic, provider.Name())
	assert.Equal(t, "claude-opus-5", provider.Model())

	calls := fake.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "/v1/messages", calls[0].Path)
	assert.Equal(t, "test-key", calls[0].Header.Get("X-Api-Key"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(calls[0].Body, &sent))
	assert.Equal(t, "claude-opus-5", sent["model"])
	assert.EqualValues(t, maxAnswerTokens, sent["max_tokens"])
	assert.NotContains(t, sent, "system", "the prompt is sent alone, with no system prompt")

	messages, ok := sent["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", message["role"])
	assert.Contains(t, string(calls[0].Body), "Which CRM would you recommend?")
}

func TestAnthropicProviderConcatenatesTextBlocksAndSkipsOthers(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body: messageBody(
			`{"type":"thinking","thinking":"internal reasoning","signature":"sig"},` +
				`{"type":"text","text":"Acme"},` +
				`{"type":"text","text":" and Globex."}`,
		),
	})

	provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Equal(t, "Acme and Globex.", answer.Text)
}

func TestAnthropicProviderEmptyContent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "no blocks at all", body: messageBody(``)},
		{name: "one empty text block", body: messageBody(`{"type":"text","text":""}`)},
		{name: "only non-text blocks", body: messageBody(`{"type":"thinking","thinking":"hm","signature":"s"}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeEngine(t, engineResponse{status: http.StatusOK, body: tc.body})
			provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

			answer, err := provider.Query(context.Background(), "prompt")
			require.NoError(t, err, "an empty answer is not a failure")
			assert.Equal(t, "", answer.Text)
		})
	}
}

func TestAnthropicProviderRetriesRateLimitThenSucceeds(t *testing.T) {
	fake := newFakeEngine(t,
		engineResponse{
			status: http.StatusTooManyRequests,
			body:   `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			header: rateLimitedNow,
		},
		engineResponse{status: http.StatusOK, body: messageBody(`{"type":"text","text":"Acme."}`)},
	)

	provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Equal(t, "Acme.", answer.Text)
	assert.Len(t, fake.calls(), 2, "the SDK retries once; the engine must not add a retry loop of its own")
}

func TestAnthropicProviderRateLimitExhausted(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusTooManyRequests,
		body:   `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		header: rateLimitedNow,
	})

	provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProviderAnthropic)
	assert.Equal(t, http.StatusTooManyRequests, ProviderHTTPStatus(err))
	assert.Len(t, fake.calls(), 2, "one attempt plus exactly one retry")
}

func TestAnthropicProviderServerError(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusInternalServerError,
		body:   `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
		header: rateLimitedNow,
	})

	provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, ProviderHTTPStatus(err))
}

func TestAnthropicProviderMalformedJSON(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   `{"content": [ this is not json`,
	})

	provider := NewAnthropicProvider("k", "claude-opus-5", fake.server.URL)

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProviderAnthropic)
	assert.Equal(t, 0, ProviderHTTPStatus(err), "a decode failure carries no HTTP status")
	assert.Len(t, fake.calls(), 1, "a 200 with a bad body is not retried")
}
