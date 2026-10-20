package aeo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// syncBuffer is a log sink that is safe to read while goroutines write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureAEOLogs routes the package logger into a buffer at debug level for
// the rest of the test.
func captureAEOLogs(t *testing.T) *syncBuffer {
	t.Helper()
	sink := &syncBuffer{}
	logger := logrus.New()
	logger.SetOutput(sink)
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.JSONFormatter{})
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })
	return sink
}

// reflectingBody echoes everything sensitive the request carried: the
// credential headers and the JSON body holding the prompt.
func reflectingBody(r *http.Request, body []byte) string {
	return "REMOTE-BODY key=" + r.Header.Get("Authorization") + r.Header.Get("X-Api-Key") +
		" request=" + string(body)
}

func sdkErrorBody(r *http.Request, body []byte) string {
	message := jsonString(reflectingBody(r, body))
	if strings.HasSuffix(r.URL.Path, "/messages") {
		return `{"type":"error","error":{"type":"authentication_error","message":` + message + `}}`
	}
	return `{"error":{"message":` + message + `,"type":"invalid_request_error","code":"invalid_api_key"}}`
}

func respond(status int, contentType string, body func(*http.Request, []byte) string) internetHandler {
	return func(w http.ResponseWriter, r *http.Request, requestBody []byte) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body(r, requestBody))
	}
}

// assertSanitized checks the full safe contract on one provider error.
func assertSanitized(t *testing.T, err error, category string, status int) {
	t.Helper()
	require.Error(t, err)
	var providerErr *ProviderError
	require.True(t, errors.As(err, &providerErr), "every failure is a *ProviderError, got %T", err)
	assert.Equal(t, category, providerErr.Category)
	assert.Equal(t, status, ProviderHTTPStatus(err))
	assert.LessOrEqual(t, utf8.RuneCountInString(err.Error()), maxProviderErrorLength)
	assertSafeText(t, err.Error())
	assertSafeText(t, fmt.Sprintf("%v %+v %s", err, err, err))
	// No unwrapping reaches the SDK error and its request, headers or body.
	var openaiErr *openai.Error
	var anthropicErr *anthropic.Error
	assert.False(t, errors.As(err, &openaiErr))
	assert.False(t, errors.As(err, &anthropicErr))
}

// TestAEOFailureSanitization drives every provider implementation through the
// real SDK clients and the egress guard, against a server that reflects the
// fake key, headers and prompt into its responses.
func TestAEOFailureSanitization(t *testing.T) {
	fi := newFakeInternet(t)

	oversized := func(r *http.Request, body []byte) string {
		return strings.Repeat(reflectingBody(r, body)+" ", 1<<20/len(reflectingBody(r, body))+1)
	}
	geminiEnvelope := func(r *http.Request, body []byte) string {
		return `[{"error":{"code":503,"message":` + jsonString(reflectingBody(r, body)) + `,"status":"UNAVAILABLE"}}]`
	}

	scenarios := []struct {
		name     string
		handler  internetHandler
		category string
		status   int
	}{
		{"SDK error object reflecting the key and prompt", respond(http.StatusUnauthorized, "application/json", sdkErrorBody), FailureHTTPStatus, http.StatusUnauthorized},
		{"rate limit with a reflected body", respond(http.StatusTooManyRequests, "application/json", sdkErrorBody), FailureHTTPStatus, http.StatusTooManyRequests},
		{"Gemini array envelope", respond(http.StatusServiceUnavailable, "application/json", geminiEnvelope), FailureHTTPStatus, http.StatusServiceUnavailable},
		{"non-JSON error body", respond(http.StatusBadGateway, "text/html", reflectingBody), FailureHTTPStatus, http.StatusBadGateway},
		{"oversized error body", respond(http.StatusBadRequest, "application/json", oversized), FailureHTTPStatus, http.StatusBadRequest},
	}

	for _, engine := range egressEngines {
		for _, scenario := range scenarios {
			t.Run(engine+"/"+scenario.name, func(t *testing.T) {
				fi.reset()
				logs := captureAEOLogs(t)
				fi.setHandler(scenario.handler)
				_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).
					Query(context.Background(), reflectedPrompt)
				assertSanitized(t, err, scenario.category, scenario.status)
				assertSafeText(t, logs.String())
				_, _, requests := fi.snapshot()
				require.NotEmpty(t, requests, "the fake engine was reached through the trusted origin")
				assert.Contains(t, string(requests[0].Body), reflectedPrompt, "the prompt itself is still sent")
			})
		}

		t.Run(engine+"/malformed success body", func(t *testing.T) {
			fi.reset()
			logs := captureAEOLogs(t)
			fi.setHandler(respond(http.StatusOK, "application/json", func(r *http.Request, body []byte) string {
				return `{"choices": [ ` + reflectingBody(r, body)
			}))
			_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).
				Query(context.Background(), reflectedPrompt)
			require.Error(t, err)
			category := ProviderFailureCategory(err)
			assert.Contains(t, []string{FailureInvalidResponse, FailureUnknown}, category)
			assertSanitized(t, err, category, 0)
			assertSafeText(t, logs.String())
		})

		t.Run(engine+"/DNS failure", func(t *testing.T) {
			fi.reset()
			host := officialRequest[engine].host
			fi.mu.Lock()
			saved := fi.answers[host]
			delete(fi.answers, host)
			fi.mu.Unlock()
			defer func() {
				fi.mu.Lock()
				fi.answers[host] = saved
				fi.mu.Unlock()
			}()
			_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).
				Query(context.Background(), reflectedPrompt)
			assertSanitized(t, err, FailureDNS, 0)
		})

		t.Run(engine+"/connection failure", func(t *testing.T) {
			fi.reset()
			fi.mu.Lock()
			fi.dialErr = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused " + fakeSecretKey)}
			fi.mu.Unlock()
			_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).
				Query(context.Background(), reflectedPrompt)
			assertSanitized(t, err, FailureConnection, 0)
		})

		t.Run(engine+"/cancellation", func(t *testing.T) {
			fi.reset()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).Query(ctx, reflectedPrompt)
			assertSanitized(t, err, FailureCanceled, 0)
			assert.ErrorIs(t, err, context.Canceled, "errors.Is context semantics survive sanitization")
		})

		t.Run(engine+"/timeout", func(t *testing.T) {
			fi.reset()
			fi.setHandler(func(w http.ResponseWriter, r *http.Request, _ []byte) {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).Query(ctx, reflectedPrompt)
			assertSanitized(t, err, FailureTimeout, 0)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}

	t.Run("TLS failure", func(t *testing.T) {
		wrongHost := newFakeInternet(t, "unrelated.example")
		for _, engine := range egressEngines {
			_, err := singleEngine(t, engine, "", wrongHost.guard(t, customTestOrigin, "")).
				Query(context.Background(), reflectedPrompt)
			assertSanitized(t, err, FailureTLS, 0)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		fi.reset()
		fi.setHandler(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Location", "https://evil.example/?key="+fakeSecretKey)
			w.WriteHeader(http.StatusFound)
		})
		_, err := singleEngine(t, config.AEOEgressAnthropic, "", fi.guard(t, "", "")).
			Query(context.Background(), reflectedPrompt)
		assertSanitized(t, err, FailureRedirectNotFollowed, http.StatusFound)
	})

	t.Run("successful answers and native citations are unchanged", func(t *testing.T) {
		fi.reset()
		fi.setHandler(respond(http.StatusOK, "application/json", func(*http.Request, []byte) string {
			return `{"id":"c","object":"chat.completion","created":1,"model":"sonar",` +
				`"citations":["https://acme.com/pricing"],` +
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Acme."}}]}`
		}))
		answer, err := singleEngine(t, config.AEOEgressPerplexity, "", fi.guard(t, "", "")).
			Query(context.Background(), "prompt")
		require.NoError(t, err)
		assert.Equal(t, "Acme.", answer.Text)
		assert.Equal(t, []string{"https://acme.com/pricing"}, answer.Citations)
	})

	t.Run("SDK error objects built directly are sanitized", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions?key="+fakeSecretKey, nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+fakeSecretKey)
		response := &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("REMOTE-BODY"))}
		for _, sdkErr := range []error{
			&openai.Error{StatusCode: http.StatusForbidden, Request: request, Response: response, Message: fakeSecretKey},
			&anthropic.Error{StatusCode: http.StatusForbidden, Request: request, Response: response},
		} {
			sanitized := SanitizeProviderError("openai", fmt.Errorf("prefix %s: %w", reflectedPrompt, sdkErr))
			assertSanitized(t, sanitized, FailureHTTPStatus, http.StatusForbidden)
			assert.Equal(t, "openai: authentication rejected by the provider (HTTP 403)", sanitized.Error())
		}
	})

	t.Run("custom and hostile errors are reduced to safe categories", func(t *testing.T) {
		hostile := []struct {
			err      error
			category string
		}{
			{errors.New("upstream said Authorization: Bearer " + fakeSecretKey + " for " + reflectedPrompt), FailureUnknown},
			{fmt.Errorf("%s: %w", fakeSecretKey, context.DeadlineExceeded), FailureTimeout},
			{fmt.Errorf("%s: %w", fakeSecretKey, context.Canceled), FailureCanceled},
			{&net.DNSError{Err: fakeSecretKey, Name: "evil.example"}, FailureDNS},
			{fmt.Errorf("decode %s: %w", fakeSecretKey, io.ErrUnexpectedEOF), FailureInvalidResponse},
			{&ProviderError{Provider: fakeSecretKey, Category: "REMOTE-BODY " + fakeSecretKey, StatusCode: 99999}, FailureUnknown},
			{panickingError{}, FailureUnknown},
		}
		for _, tc := range hostile {
			sanitized := SanitizeProviderError("custom engine", tc.err)
			assertSanitized(t, sanitized, tc.category, 0)
			assert.True(t, strings.HasPrefix(sanitized.Error(), "custom engine: "))
		}
		assert.NoError(t, SanitizeProviderError("openai", nil))
		assert.ErrorIs(t, SanitizeProviderError("x", fmt.Errorf("wrapped: %w", context.DeadlineExceeded)), context.DeadlineExceeded)
	})

	t.Run("SafeQuery contains panics without their value", func(t *testing.T) {
		logs := captureAEOLogs(t)
		_, err := SafeQuery(context.Background(), &fakeProvider{name: "boom", panicValue: "panic " + fakeSecretKey}, reflectedPrompt)
		assertSanitized(t, err, FailurePanic, 0)
		assert.Equal(t, "boom: provider panicked", err.Error())
		assertSafeText(t, logs.String())
		assert.Contains(t, logs.String(), FailurePanic)
	})
}

// panickingError panics when inspected, the way a hostile custom error might.
type panickingError struct{}

func (panickingError) Error() string { panic("Error() must never be called: " + fakeSecretKey) }
func (panickingError) Unwrap() error { panic("Unwrap " + fakeSecretKey) }

// TestAEOFailurePersistence exercises the engine's repository boundary: every
// failed query is persisted and counted with a bounded, sanitized error, the
// log carries category and status but no secret, and successes are unchanged.
func TestAEOFailurePersistence(t *testing.T) {
	fi := newFakeInternet(t)
	fi.setHandler(func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Host == "api.openai.com" {
			respond(http.StatusServiceUnavailable, "application/json", sdkErrorBody)(w, r, body)
			return
		}
		answerOK(w, r, body)
	})
	logs := captureAEOLogs(t)

	sdkProviders := LoadProvidersWithGuard(singleEngineConfig(config.AEOEgressOpenAI, ""), fi.guard(t, "", ""))
	require.Len(t, sdkProviders, 1)
	blocked := LoadProvidersWithGuard(singleEngineConfig(config.AEOEgressGemini, "https://evil.example/v1"), fi.guard(t, "", ""))
	require.Len(t, blocked, 1)
	succeeding := LoadProvidersWithGuard(singleEngineConfig(config.AEOEgressAnthropic, ""), fi.guard(t, "", ""))
	require.Len(t, succeeding, 1)

	providers := []Provider{
		sdkProviders[0],
		blocked[0],
		succeeding[0],
		&fakeProvider{name: "custom-error", model: "m", err: errors.New(
			strings.Repeat("Authorization: Bearer "+fakeSecretKey+" "+reflectedPrompt+" ", 200))},
		&fakeProvider{name: "wrapped-sdk", model: "m", err: fmt.Errorf("%s %w", reflectedPrompt,
			&openai.Error{StatusCode: http.StatusTooManyRequests, Message: fakeSecretKey})},
		&fakeProvider{name: "panicking", model: "m", panicValue: errors.New("panic " + fakeSecretKey)},
		&fakeProvider{name: "fine", model: "m", answer: ProviderAnswer{Text: "Acme is the pick."}},
	}

	repo := &fakeAEORepo{}
	run := newTestRun()
	engine := NewEngine(repo, providers, EngineOptions{Concurrency: 2})
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts(reflectedPrompt), testProfile()))

	assert.Equal(t, RunStatusPartial, run.Status)
	assert.Equal(t, len(providers), run.TotalQueries)
	assert.Equal(t, 5, run.FailedQueries, "every failure is counted")

	answers, _ := repo.snapshot()
	require.Len(t, answers, len(providers), "every failure is persisted as an answer row")
	byProvider := map[string]models.AEOAnswer{}
	for _, answer := range answers {
		byProvider[answer.Provider] = answer
		assert.LessOrEqual(t, utf8.RuneCountInString(answer.Error), maxProviderErrorLength)
		assertSafeText(t, answer.Error)
		assertSafeText(t, answer.AnswerText)
	}

	assert.Equal(t, "openai: provider unavailable (HTTP 503)", byProvider[ProviderOpenAI].Error)
	assert.Equal(t, "gemini: endpoint not permitted by the AEO egress policy", byProvider[ProviderGemini].Error)
	assert.Equal(t, "custom-error: provider request failed", byProvider["custom-error"].Error)
	assert.Equal(t, "wrapped-sdk: rate limited by the provider (HTTP 429)", byProvider["wrapped-sdk"].Error)
	assert.Equal(t, "panicking: provider panicked", byProvider["panicking"].Error)
	assert.Empty(t, byProvider[ProviderAnthropic].Error)
	assert.Equal(t, "OK", byProvider[ProviderAnthropic].AnswerText)
	assert.Empty(t, byProvider["fine"].Error)
	assert.True(t, byProvider["fine"].BrandMentioned)

	_, _, requests := fi.snapshot()
	for _, request := range requests {
		assert.NotEqual(t, "evil.example", request.Host, "the blocked engine never sent its key")
	}

	output := logs.String()
	assertSafeText(t, output)
	assert.Contains(t, output, `"http_status":503`)
	assert.Contains(t, output, `"http_status":429`)
	assert.Contains(t, output, `"failure_category":"`+FailurePanic+`"`)
	assert.Contains(t, output, `"failure_category":"`+FailureEndpointNotPermitted+`"`)
	assert.NotContains(t, output, `"panic":`, "panic values are never logged")
}
