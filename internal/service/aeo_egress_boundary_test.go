package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// boundarySecret is an obviously fake credential; no assertion below may see
// it in an error, a log line or a request to an untrusted destination.
const boundarySecret = "sk-fake-boundary-SECRET"

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureServiceLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	sink := &lockedLogBuffer{}
	logger := logrus.New()
	logger.SetOutput(sink)
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.JSONFormatter{})
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })
	return sink
}

// countingNetwork is an offline resolver and dialer: it records every lookup
// and dial and never opens a connection.
type countingNetwork struct {
	mu        sync.Mutex
	hosts     []string
	addresses []string
}

func (n *countingNetwork) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hosts = append(n.hosts, host)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func (n *countingNetwork) dial(_ context.Context, _, address string) (net.Conn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.addresses = append(n.addresses, address)
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline test network")}
}

func (n *countingNetwork) snapshot() ([]string, []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.hosts...), append([]string(nil), n.addresses...)
}

func boundaryBaseConfig(t *testing.T) config.AEOConfig {
	t.Helper()
	var base config.AEOConfig
	base.AnthropicAPIKey, base.AnthropicModel = "env-anthropic-"+boundarySecret, "env-anthropic-model"
	base.OpenAIAPIKey, base.OpenAIModel = "env-openai-"+boundarySecret, "env-openai-model"
	base.GeminiAPIKey, base.GeminiModel = "env-gemini-"+boundarySecret, "env-gemini-model"
	base.MoonshotAPIKey, base.KimiModel = "env-moonshot-"+boundarySecret, "env-kimi-model"
	base.PerplexityAPIKey, base.PerplexityModel = "env-perplexity-"+boundarySecret, "env-perplexity-model"
	base.CustomName, base.CustomModel = "lmstudio", "local-model"
	base.CustomBaseURL, base.CustomAPIKey = "https://llm.internal.example/v1", "env-custom-"+boundarySecret
	base.Egress = config.ParseAEOEgressPolicy("openai=https://llm-proxy.example.com", "")
	require.NoError(t, base.Egress.Err)
	return base
}

// TestAEOConfigTrustBoundary proves administrator settings keep their ordinary
// precedence for keys, models and base URLs, yet can never widen the operator's
// environment egress policy for any configured provider.
func TestAEOConfigTrustBoundary(t *testing.T) {
	t.Run("stored keys, models and base URLs keep their precedence", func(t *testing.T) {
		base := boundaryBaseConfig(t)
		configs := newStubConfigurationService(map[string]string{ConfigAEOOpenAIKey: "stored-openai-" + boundarySecret})
		configs.values["integration.aeo.openai_model"] = "stored-openai-model"
		configs.values["integration.aeo.openai_base_url"] = " https://llm-proxy.example.com/v1 "

		effective := EffectiveAEOConfig(base, configs)
		assert.Equal(t, "stored-openai-"+boundarySecret, effective.OpenAIAPIKey)
		assert.Equal(t, "stored-openai-model", effective.OpenAIModel)
		assert.Equal(t, "https://llm-proxy.example.com/v1", effective.OpenAIBaseURL)
		assert.Equal(t, base.AnthropicAPIKey, effective.AnthropicAPIKey)
		assert.Equal(t, base.Egress, effective.Egress)

		network := &countingNetwork{}
		guard := aeo.NewEgressGuard(effective.Egress, aeo.WithEgressResolver(network), aeo.WithEgressDialer(network.dial))
		providers := aeo.LoadProvidersWithGuard(effective, guard)
		byName := map[string]aeo.Provider{}
		for _, provider := range providers {
			byName[provider.Name()] = provider
		}

		// The operator-allowed proxy and the official origin are both reached
		// (up to the offline dialer) through validated addresses only.
		for name, wantHost := range map[string]string{aeo.ProviderOpenAI: "llm-proxy.example.com", aeo.ProviderAnthropic: "api.anthropic.com"} {
			network.mu.Lock()
			network.hosts, network.addresses = nil, nil
			network.mu.Unlock()
			_, err := byName[name].Query(context.Background(), "prompt")
			require.Error(t, err)
			assert.Equal(t, aeo.FailureConnection, aeo.ProviderFailureCategory(err), name)
			assert.NotContains(t, err.Error(), boundarySecret)
			hosts, addresses := network.snapshot()
			require.NotEmpty(t, hosts, name)
			for _, host := range hosts {
				assert.Equal(t, wantHost, host, name)
			}
			for _, address := range addresses {
				assert.Equal(t, "93.184.216.34:443", address, name)
			}
		}
	})

	t.Run("stored and persisted overrides cannot widen the egress policy", func(t *testing.T) {
		logs := captureServiceLogs(t)
		base := boundaryBaseConfig(t)
		configs := newStubConfigurationService(map[string]string{
			ConfigAEOAnthropicKey: "stored-anthropic-" + boundarySecret,
			ConfigAEOOpenAIKey:    "stored-openai-" + boundarySecret,
		})
		// Values a compromised admin account could store — or rows persisted
		// before HTTPS-only validation existed.
		configs.values["integration.aeo.anthropic_base_url"] = "http://169.254.169.254/latest/meta-data/"
		configs.values["integration.aeo.openai_base_url"] = "https://evil.example/v1"
		configs.values["integration.aeo.gemini_base_url"] = "https://user:" + boundarySecret + "@generativelanguage.googleapis.com/v1beta/openai/"
		configs.values["integration.aeo.kimi_base_url"] = "https://api.openai.com/v1"
		configs.values["integration.aeo.perplexity_base_url"] = "https://api.perplexity.ai:8443"
		// Settings that do not exist must not be consulted either.
		configs.values["integration.aeo.egress_allowed_origins"] = "openai=https://evil.example"
		configs.values["integration.aeo.egress_local_origins"] = "anthropic=http://169.254.169.254"
		configs.values["integration.aeo.custom_base_url"] = "https://evil.example/v1"

		effective := EffectiveAEOConfig(base, configs)
		assert.Equal(t, base.Egress, effective.Egress, "the egress policy is environment-only")
		assert.Equal(t, base.CustomBaseURL, effective.CustomBaseURL, "the custom engine is environment-only")
		assert.Equal(t, "https://evil.example/v1", effective.OpenAIBaseURL, "ordinary precedence still applies")
		for key := range configs.calls {
			assert.NotContains(t, key, "egress")
			assert.NotContains(t, key, "custom")
		}

		network := &countingNetwork{}
		guard := aeo.NewEgressGuard(effective.Egress, aeo.WithEgressResolver(network), aeo.WithEgressDialer(network.dial))
		providers := aeo.LoadProvidersWithGuard(effective, guard)
		require.Len(t, providers, 6, "every configured provider is checked")
		for _, provider := range providers {
			_, err := provider.Query(context.Background(), "prompt")
			require.Error(t, err)
			assert.ErrorIs(t, err, aeo.ErrEndpointNotPermitted, provider.Name())
			assert.NotContains(t, err.Error(), boundarySecret)
			assert.NotContains(t, err.Error(), "evil.example")
		}
		hosts, addresses := network.snapshot()
		assert.Empty(t, hosts, "a refused endpoint is never resolved")
		assert.Empty(t, addresses, "a refused endpoint is never dialed, so no key leaves")
		assert.NotContains(t, logs.String(), boundarySecret)
		assert.NotContains(t, logs.String(), "evil.example")
	})

	t.Run("the production configuration source enforces the same boundary", func(t *testing.T) {
		logs := captureServiceLogs(t)
		base := boundaryBaseConfig(t)
		configs := newStubConfigurationService(map[string]string{})
		configs.values["integration.aeo.openai_base_url"] = "https://evil.example/v1"
		repo := new(mocks.AEORepository)
		repo.On("GetProfile").Return(testAEOProfile(), nil)
		svc := NewAEOService(repo, nil, nil, nil,
			WithAEOConfigSource(func() config.AEOConfig { return EffectiveAEOConfig(base, configs) }),
			WithAEOGenerationEngineSource(func() string { return aeo.ProviderOpenAI }))

		prompts, err := svc.GeneratePrompts(context.Background(), 3)
		assert.Nil(t, prompts)
		require.Error(t, err)
		assert.ErrorIs(t, err, aeo.ErrEndpointNotPermitted)
		assert.Equal(t, "openai: endpoint not permitted by the AEO egress policy", err.Error())
		assert.NotContains(t, logs.String(), boundarySecret)
		assert.NotContains(t, logs.String(), "evil.example")
	})
}

// reflectingGenerator is a hostile generation engine: its failures echo the
// fake key and the generation prompt, which carries the brand profile.
type reflectingGenerator struct {
	name   string
	mode   string
	status int
}

func (g *reflectingGenerator) Name() string  { return g.name }
func (g *reflectingGenerator) Model() string { return "hostile" }
func (g *reflectingGenerator) Query(ctx context.Context, prompt string) (aeo.ProviderAnswer, error) {
	reflected := "Authorization: Bearer " + boundarySecret + " " + prompt
	switch g.mode {
	case "panic":
		panic(errors.New("panic " + reflected))
	case "sdk":
		return aeo.ProviderAnswer{}, fmt.Errorf("%s: %w", reflected, &openai.Error{StatusCode: g.status, Message: reflected})
	case "timeout":
		return aeo.ProviderAnswer{}, fmt.Errorf("%s: %w", reflected, context.DeadlineExceeded)
	case "sanitized":
		return aeo.ProviderAnswer{}, &aeo.ProviderError{Provider: reflected, Category: reflected, StatusCode: g.status}
	default:
		return aeo.ProviderAnswer{}, errors.New(reflected)
	}
}

// TestAEOGenerationFailureSanitization exercises the prompt-generation service
// boundary with hostile errors and panics, checks the captured log, and keeps
// the normal success path.
func TestAEOGenerationFailureSanitization(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		status   int
		category string
		message  string
		is       error
	}{
		{"custom error", "", 0, aeo.FailureUnknown, "anthropic: provider request failed", nil},
		{"wrapped SDK rejection", "sdk", 401, aeo.FailureHTTPStatus, "anthropic: authentication rejected by the provider (HTTP 401)", nil},
		{"wrapped SDK outage", "sdk", 503, aeo.FailureHTTPStatus, "anthropic: provider unavailable (HTTP 503)", nil},
		{"timeout", "timeout", 0, aeo.FailureTimeout, "anthropic: request timed out", context.DeadlineExceeded},
		{"panic", "panic", 0, aeo.FailurePanic, "anthropic: provider panicked", nil},
		{"forged provider error", "sanitized", 418, aeo.FailureUnknown, "anthropic: provider request failed (HTTP 418)", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureServiceLogs(t)
			repo := new(mocks.AEORepository)
			repo.On("GetProfile").Return(testAEOProfile(), nil)
			svc := NewAEOService(repo, nil,
				[]aeo.Provider{&reflectingGenerator{name: aeo.ProviderAnthropic, mode: tc.mode, status: tc.status}}, nil)

			prompts, err := svc.GeneratePrompts(context.Background(), 3)
			assert.Nil(t, prompts)
			require.Error(t, err)
			assert.Equal(t, tc.message, err.Error())
			assert.Equal(t, tc.category, aeo.ProviderFailureCategory(err))
			assert.Equal(t, tc.status, aeo.ProviderHTTPStatus(err), "the numeric status survives for the handler")
			if tc.is != nil {
				assert.ErrorIs(t, err, tc.is)
			}
			var sdkErr *openai.Error
			assert.False(t, errors.As(err, &sdkErr), "the SDK error is not reachable by unwrapping")

			output := logs.String()
			for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), output} {
				assert.NotContains(t, text, boundarySecret)
				assert.NotContains(t, text, "Bearer")
				assert.NotContains(t, text, "Brand: Acme", "the generation prompt is never reflected")
				assert.NotContains(t, text, "Globex", "profile data is never reflected")
			}
			assert.Contains(t, output, `"failure_category":"`+tc.category+`"`)
			repo.AssertExpectations(t)
		})
	}

	t.Run("the normal success path is unchanged", func(t *testing.T) {
		captureServiceLogs(t)
		repo := new(mocks.AEORepository)
		repo.On("GetProfile").Return(testAEOProfile(), nil)
		generator := &scriptedAEOProvider{name: aeo.ProviderAnthropic, reply: `["Which CRM is best for small teams?"]`}
		svc := NewAEOService(repo, nil, []aeo.Provider{generator}, nil)

		prompts, err := svc.GeneratePrompts(context.Background(), 3)
		require.NoError(t, err)
		assert.Equal(t, []string{"Which CRM is best for small teams?"}, prompts)
		assert.True(t, strings.Contains(generator.asked, "Brand: Acme"))
	})
}
