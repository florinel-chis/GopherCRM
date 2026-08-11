package aeo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedRequest is one request the fake answer engine received.
type capturedRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

// fakeEngine is an httptest server standing in for an OpenAI-compatible answer
// engine. Responses are served from a script, one entry per request; the last
// entry repeats once the script is exhausted.
type fakeEngine struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	script   []engineResponse
}

type engineResponse struct {
	status int
	body   string
	// header lets a test drive the SDK's retry pacing (Retry-After: 0 keeps
	// the retry immediate instead of sleeping half a second).
	header map[string]string
}

func newFakeEngine(t *testing.T, script ...engineResponse) *fakeEngine {
	t.Helper()
	require.NotEmpty(t, script, "a fake engine needs at least one scripted response")

	fake := &fakeEngine{script: script}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		fake.mu.Lock()
		index := len(fake.requests)
		fake.requests = append(fake.requests, capturedRequest{
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
		})
		if index >= len(fake.script) {
			index = len(fake.script) - 1
		}
		response := fake.script[index]
		fake.mu.Unlock()

		for key, value := range response.header {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.status)
		_, _ = io.WriteString(w, response.body)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeEngine) calls() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// chatCompletionBody builds a minimal but schema-valid chat completion.
func chatCompletionBody(content string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"test-model",` +
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` +
		jsonString(content) + `}}]}`
}

func jsonString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// rateLimitedNow makes the SDK's single retry fire immediately.
var rateLimitedNow = map[string]string{"Retry-After": "0"}

func TestOpenAICompatProviderQuerySuccess(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   chatCompletionBody("Acme is the usual recommendation."),
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name:    ProviderOpenAI,
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		BaseURL: fake.server.URL,
	})

	answer, err := provider.Query(context.Background(), "Which CRM would you recommend?")
	require.NoError(t, err)
	assert.Equal(t, "Acme is the usual recommendation.", answer.Text)
	assert.Empty(t, answer.Citations)
	assert.Equal(t, ProviderOpenAI, provider.Name())
	assert.Equal(t, "gpt-4o-mini", provider.Model())

	calls := fake.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "/chat/completions", calls[0].Path)
	assert.Equal(t, "Bearer test-key", calls[0].Header.Get("Authorization"))

	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(calls[0].Body, &sent))
	assert.Equal(t, "gpt-4o-mini", sent.Model)
	assert.Equal(t, maxAnswerTokens, sent.MaxTokens)
	require.Len(t, sent.Messages, 1, "the prompt is sent alone, with no system message")
	assert.Equal(t, "user", sent.Messages[0].Role)
	assert.Equal(t, "Which CRM would you recommend?", sent.Messages[0].Content)
}

func TestOpenAICompatProviderResolvesBaseURLShapes(t *testing.T) {
	// Every engine is the same wrapper; only the base URL, model and citation
	// handling differ. These are the three base-URL shapes the configured
	// engines use — bare host (Perplexity), versioned path with no trailing
	// slash (Kimi) and versioned path with one (Gemini) — exercised against a
	// local server so the assertion covers the SDK's real resolution.
	tests := []struct {
		name     string
		suffix   string
		wantPath string
	}{
		{name: "bare host, as Perplexity", suffix: "", wantPath: "/chat/completions"},
		{name: "versioned path, as Kimi", suffix: "/v1", wantPath: "/v1/chat/completions"},
		{name: "trailing slash, as Gemini", suffix: "/v1beta/openai/", wantPath: "/v1beta/openai/chat/completions"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeEngine(t, engineResponse{status: http.StatusOK, body: chatCompletionBody("hi")})
			provider := NewOpenAICompatProvider(OpenAICompatConfig{
				Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL + tc.suffix,
			})

			_, err := provider.Query(context.Background(), "prompt")
			require.NoError(t, err)

			calls := fake.calls()
			require.Len(t, calls, 1)
			assert.Equal(t, tc.wantPath, calls[0].Path)
		})
	}
}

func TestOpenAICompatProviderEmptyChoices(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[]}`,
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err, "no choices is an answer with no mentions, not a failure")
	assert.Empty(t, answer.Text)
}

func TestOpenAICompatProviderEmptyContent(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   chatCompletionBody(""),
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Equal(t, "", answer.Text)
}

func TestOpenAICompatProviderRetriesRateLimitThenSucceeds(t *testing.T) {
	fake := newFakeEngine(t,
		engineResponse{
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
			header: rateLimitedNow,
		},
		engineResponse{status: http.StatusOK, body: chatCompletionBody("Acme.")},
	)

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Equal(t, "Acme.", answer.Text)
	assert.Len(t, fake.calls(), 2, "the SDK retries once; the engine must not add a retry loop of its own")
}

func TestOpenAICompatProviderRateLimitExhausted(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusTooManyRequests,
		body:   `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
		header: rateLimitedNow,
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderPerplexity, Model: "sonar", APIKey: "k", BaseURL: fake.server.URL,
	})

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProviderPerplexity, "the provider name is wrapped into the error")
	assert.Equal(t, http.StatusTooManyRequests, ProviderHTTPStatus(err))
	assert.Len(t, fake.calls(), 2, "one attempt plus exactly one retry")
}

func TestOpenAICompatProviderServerError(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusInternalServerError,
		body:   `{"error":{"message":"boom","type":"server_error"}}`,
		header: rateLimitedNow,
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, ProviderHTTPStatus(err))
}

func TestOpenAICompatProviderMalformedJSON(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body:   `{"choices": [ this is not json`,
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	_, err := provider.Query(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProviderOpenAI)
	assert.Equal(t, 0, ProviderHTTPStatus(err), "a decode failure carries no HTTP status")
	assert.Len(t, fake.calls(), 1, "a 200 with a bad body is not retried")
}

func TestOpenAICompatProviderNativeCitations(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantCitations []string
	}{
		{
			name: "top-level citations array",
			body: `{"id":"c","object":"chat.completion","created":1,"model":"sonar",` +
				`"citations":["https://acme.com/pricing","https://globex.com/compare"],` +
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Acme."}}]}`,
			wantCitations: []string{"https://acme.com/pricing", "https://globex.com/compare"},
		},
		{
			name: "search_results fallback",
			body: `{"id":"c","object":"chat.completion","created":1,"model":"sonar",` +
				`"search_results":[{"title":"Acme","url":"https://acme.com/x"},{"title":"No url"}],` +
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Acme."}}]}`,
			wantCitations: []string{"https://acme.com/x"},
		},
		{
			name: "citations win over search_results",
			body: `{"id":"c","object":"chat.completion","created":1,"model":"sonar",` +
				`"citations":["https://acme.com/a"],` +
				`"search_results":[{"url":"https://globex.com/b"}],` +
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Acme."}}]}`,
			wantCitations: []string{"https://acme.com/a"},
		},
		{
			name:          "neither field present",
			body:          chatCompletionBody("Acme."),
			wantCitations: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeEngine(t, engineResponse{status: http.StatusOK, body: tc.body})
			provider := NewOpenAICompatProvider(OpenAICompatConfig{
				Name:            ProviderPerplexity,
				Model:           "sonar",
				APIKey:          "k",
				BaseURL:         fake.server.URL,
				NativeCitations: true,
			})

			answer, err := provider.Query(context.Background(), "prompt")
			require.NoError(t, err)
			assert.Equal(t, "Acme.", answer.Text)
			assert.Equal(t, tc.wantCitations, answer.Citations)
		})
	}
}

func TestOpenAICompatProviderIgnoresCitationsWhenNotNative(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{
		status: http.StatusOK,
		body: `{"id":"c","object":"chat.completion","created":1,"model":"m",` +
			`"citations":["https://acme.com/x"],` +
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Acme."}}]}`,
	})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	answer, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Nil(t, answer.Citations, "only Perplexity is configured for native citations")
}

func TestOpenAICompatProviderOmitsAuthorizationWithoutKey(t *testing.T) {
	// Self-hosted OpenAI-compatible servers (LM Studio, vLLM, Ollama) usually
	// reject a bearer header they never issued.
	fake := newFakeEngine(t, engineResponse{status: http.StatusOK, body: chatCompletionBody("hi")})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: "custom", Model: "openai/gpt-oss-20b", APIKey: "", BaseURL: fake.server.URL,
	})

	_, err := provider.Query(context.Background(), "prompt")
	require.NoError(t, err)

	calls := fake.calls()
	require.Len(t, calls, 1)
	assert.Empty(t, calls[0].Header.Get("Authorization"))
}

func TestOpenAICompatProviderHonoursContextCancellation(t *testing.T) {
	fake := newFakeEngine(t, engineResponse{status: http.StatusOK, body: chatCompletionBody("hi")})

	provider := NewOpenAICompatProvider(OpenAICompatConfig{
		Name: ProviderOpenAI, Model: "m", APIKey: "k", BaseURL: fake.server.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	_, err := provider.Query(ctx, "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProviderOpenAI)
}
