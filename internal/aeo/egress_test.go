package aeo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// Obviously fake credentials and personal data. Every negative assertion
// checks that none of them reaches an error string, a log line or a request
// to an untrusted destination.
const (
	fakeSecretKey   = "sk-fake-0123456789-SECRET"
	reflectedPrompt = "PII-PROMPT jane.doe@example.com"
)

// publicTestAddr is what the scripted resolver answers for trusted hosts. The
// fake dialer never connects to it: it records the validated literal and
// connects to the local TLS server instead.
var publicTestAddr = netip.MustParseAddr("93.184.216.34")

// egressEngines lists every engine wiring path, in roster order.
var egressEngines = []string{
	config.AEOEgressAnthropic, config.AEOEgressOpenAI, config.AEOEgressGemini,
	config.AEOEgressKimi, config.AEOEgressPerplexity, config.AEOEgressCustom,
}

// officialRequest is where each engine's default (or, for custom, its
// operator-allowed) endpoint lands.
var officialRequest = map[string]struct{ host, path string }{
	config.AEOEgressAnthropic:  {"api.anthropic.com", "/v1/messages"},
	config.AEOEgressOpenAI:     {"api.openai.com", "/v1/chat/completions"},
	config.AEOEgressGemini:     {"generativelanguage.googleapis.com", "/v1beta/openai/chat/completions"},
	config.AEOEgressKimi:       {"api.moonshot.ai", "/v1/chat/completions"},
	config.AEOEgressPerplexity: {"api.perplexity.ai", "/chat/completions"},
	config.AEOEgressCustom:     {"llm.example.com", "/v1/chat/completions"},
}

// customTestOrigin is the operator-allowed origin of the custom engine.
const customTestOrigin = "custom=https://llm.example.com"

func officialAEOConfig() config.AEOConfig {
	var cfg config.AEOConfig
	cfg.AnthropicAPIKey, cfg.AnthropicModel = "anthropic-"+fakeSecretKey, "claude-test"
	cfg.OpenAIAPIKey, cfg.OpenAIModel = "openai-"+fakeSecretKey, "gpt-test"
	cfg.GeminiAPIKey, cfg.GeminiModel = "gemini-"+fakeSecretKey, "gemini-test"
	cfg.MoonshotAPIKey, cfg.KimiModel = "moonshot-"+fakeSecretKey, "kimi-test"
	cfg.PerplexityAPIKey, cfg.PerplexityModel = "perplexity-"+fakeSecretKey, "sonar-test"
	cfg.CustomName, cfg.CustomModel = "lmstudio", "local-test"
	cfg.CustomAPIKey, cfg.CustomBaseURL = "custom-"+fakeSecretKey, "https://llm.example.com/v1"
	return cfg
}

// singleEngineConfig configures one engine with the given base URL override
// (blank keeps the default; for custom, the allowed test origin).
func singleEngineConfig(engine, baseURL string) config.AEOConfig {
	full := officialAEOConfig()
	var cfg config.AEOConfig
	switch engine {
	case config.AEOEgressAnthropic:
		cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicBaseURL = full.AnthropicAPIKey, full.AnthropicModel, baseURL
	case config.AEOEgressOpenAI:
		cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIBaseURL = full.OpenAIAPIKey, full.OpenAIModel, baseURL
	case config.AEOEgressGemini:
		cfg.GeminiAPIKey, cfg.GeminiModel, cfg.GeminiBaseURL = full.GeminiAPIKey, full.GeminiModel, baseURL
	case config.AEOEgressKimi:
		cfg.MoonshotAPIKey, cfg.KimiModel, cfg.KimiBaseURL = full.MoonshotAPIKey, full.KimiModel, baseURL
	case config.AEOEgressPerplexity:
		cfg.PerplexityAPIKey, cfg.PerplexityModel, cfg.PerplexityBaseURL = full.PerplexityAPIKey, full.PerplexityModel, baseURL
	case config.AEOEgressCustom:
		cfg.CustomName, cfg.CustomModel, cfg.CustomAPIKey = full.CustomName, full.CustomModel, full.CustomAPIKey
		cfg.CustomBaseURL = full.CustomBaseURL
		if baseURL != "" {
			cfg.CustomBaseURL = baseURL
		}
	}
	return cfg
}

func singleEngine(t *testing.T, engine, baseURL string, guard *EgressGuard) Provider {
	t.Helper()
	providers := LoadProvidersWithGuard(singleEngineConfig(engine, baseURL), guard)
	require.Len(t, providers, 1)
	return providers[0]
}

// ---------------------------------------------------------------------------
// Fake internet: a TLS server whose certificate covers the official hosts, a
// scripted resolver and a dialer that records every validated address.
// ---------------------------------------------------------------------------

type internetRequest struct {
	Host   string
	Path   string
	Header http.Header
	Body   []byte
}

type internetHandler func(w http.ResponseWriter, r *http.Request, body []byte)

type fakeInternet struct {
	server *httptest.Server
	roots  *x509.CertPool

	mu       sync.Mutex
	handler  internetHandler
	answers  map[string][]netip.Addr
	sequence map[string][][]netip.Addr
	dialErr  error
	lookups  []string
	dials    []string
	requests []internetRequest
}

var fakeInternetHosts = []string{
	"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com",
	"api.moonshot.ai", "api.perplexity.ai", "llm.example.com",
}

func newFakeInternet(t *testing.T, certHosts ...string) *fakeInternet {
	t.Helper()
	if len(certHosts) == 0 {
		certHosts = fakeInternetHosts
	}
	cert, roots := newTestCertificate(t, certHosts)
	fi := &fakeInternet{
		roots:    roots,
		answers:  map[string][]netip.Addr{},
		sequence: map[string][][]netip.Addr{},
	}
	for _, host := range fakeInternetHosts {
		fi.answers[host] = []netip.Addr{publicTestAddr}
	}
	fi.server = httptest.NewUnstartedServer(http.HandlerFunc(fi.serve))
	fi.server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	// Refused handshakes are expected in some tests; keep them off stderr.
	fi.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	fi.server.StartTLS()
	t.Cleanup(fi.server.Close)
	return fi
}

// newTestCertificate issues a self-signed certificate for hosts, trusted only
// through the returned pool. Verification itself is never relaxed.
func newTestCertificate(t *testing.T, hosts []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "AEO egress test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

func (fi *fakeInternet) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fi.mu.Lock()
	fi.requests = append(fi.requests, internetRequest{Host: r.Host, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	handler := fi.handler
	fi.mu.Unlock()
	if handler == nil {
		handler = answerOK
	}
	handler(w, r, body)
}

// answerOK is a successful answer in the dialect the request path implies.
func answerOK(w http.ResponseWriter, r *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/messages") {
		_, _ = io.WriteString(w, messageBody(`{"type":"text","text":"OK"}`))
		return
	}
	_, _ = io.WriteString(w, chatCompletionBody("OK"))
}

func (fi *fakeInternet) setHandler(handler internetHandler) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.handler = handler
}

func (fi *fakeInternet) reset() {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.handler, fi.dialErr = nil, nil
	fi.lookups, fi.dials, fi.requests = nil, nil, nil
}

// LookupNetIP makes fakeInternet the guard's Resolver.
func (fi *fakeInternet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.lookups = append(fi.lookups, host)
	if queue := fi.sequence[host]; len(queue) > 0 {
		fi.sequence[host] = queue[1:]
		return queue[0], nil
	}
	if addrs, ok := fi.answers[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (fi *fakeInternet) dial(ctx context.Context, _, address string) (net.Conn, error) {
	fi.mu.Lock()
	fi.dials = append(fi.dials, address)
	dialErr := fi.dialErr
	fi.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dialErr != nil {
		return nil, dialErr
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", fi.server.Listener.Addr().String())
}

func (fi *fakeInternet) guard(t *testing.T, allowed, local string, opts ...EgressOption) *EgressGuard {
	t.Helper()
	policy := config.ParseAEOEgressPolicy(allowed, local)
	require.NoError(t, policy.Err)
	base := []EgressOption{WithEgressResolver(fi), WithEgressDialer(fi.dial), WithEgressRootCAs(fi.roots)}
	return NewEgressGuard(policy, append(base, opts...)...)
}

func (fi *fakeInternet) snapshot() (lookups, dials []string, requests []internetRequest) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return append([]string(nil), fi.lookups...), append([]string(nil), fi.dials...),
		append([]internetRequest(nil), fi.requests...)
}

func assertNoTraffic(t *testing.T, fi *fakeInternet) {
	t.Helper()
	lookups, dials, requests := fi.snapshot()
	assert.Empty(t, lookups, "no DNS lookup may happen for a refused endpoint")
	assert.Empty(t, dials, "no connection may be opened for a refused endpoint")
	assert.Empty(t, requests, "no request may carry the credential")
}

// assertSafeText fails when text carries a credential, header, reflected
// prompt, remote body marker or untrusted destination.
func assertSafeText(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{
		fakeSecretKey, "SECRET", "Bearer", "Authorization", "authorization", "X-Api-Key", "x-api-key",
		reflectedPrompt, "jane.doe", "REMOTE-BODY", "evil.example", "169.254.169.254", "meta-data",
	} {
		assert.NotContains(t, text, forbidden)
	}
}

// ---------------------------------------------------------------------------
// TestAEOEndpointTrust
// ---------------------------------------------------------------------------

func TestAEOEndpointTrust(t *testing.T) {
	fi := newFakeInternet(t)

	t.Run("every provider wiring path reaches its trusted origin through a pinned address", func(t *testing.T) {
		fi.reset()
		providers := LoadProvidersWithGuard(officialAEOConfig(), fi.guard(t, customTestOrigin, ""))
		require.Len(t, providers, len(egressEngines))
		for _, provider := range providers {
			answer, err := provider.Query(context.Background(), "Which CRM?")
			require.NoError(t, err, provider.Name())
			assert.Equal(t, "OK", answer.Text)
		}

		lookups, dials, requests := fi.snapshot()
		require.Len(t, requests, len(egressEngines))
		require.Len(t, dials, len(egressEngines), "one connection per engine")
		require.Len(t, lookups, len(egressEngines), "each connection resolves once")
		for i, engine := range egressEngines {
			want := officialRequest[engine]
			assert.Equal(t, want.host, requests[i].Host, engine)
			assert.Equal(t, want.path, requests[i].Path, engine)
			assert.Equal(t, want.host, lookups[i], engine)
			assert.Equal(t, "93.184.216.34:443", dials[i], "%s dials the validated literal", engine)
		}
		assert.Equal(t, "anthropic-"+fakeSecretKey, requests[0].Header.Get("X-Api-Key"))
		assert.Equal(t, "Bearer openai-"+fakeSecretKey, requests[1].Header.Get("Authorization"))
		assert.Equal(t, "Bearer custom-"+fakeSecretKey, requests[5].Header.Get("Authorization"))
	})

	t.Run("an operator-allowed origin is trusted for its engine only", func(t *testing.T) {
		fi.reset()
		guard := fi.guard(t, "openai=https://llm.example.com", "")
		answer, err := singleEngine(t, config.AEOEgressOpenAI, "https://llm.example.com/v1", guard).
			Query(context.Background(), "prompt")
		require.NoError(t, err)
		assert.Equal(t, "OK", answer.Text)
		_, _, requests := fi.snapshot()
		require.Len(t, requests, 1)
		assert.Equal(t, "llm.example.com", requests[0].Host)

		fi.reset()
		_, err = singleEngine(t, config.AEOEgressGemini, "https://llm.example.com/v1", guard).
			Query(context.Background(), "prompt")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted)
		assertNoTraffic(t, fi)
	})

	t.Run("an explicit default port and host case are the same official origin", func(t *testing.T) {
		fi.reset()
		_, err := singleEngine(t, config.AEOEgressOpenAI, "https://API.OpenAI.com:443/v1/", fi.guard(t, "", "")).
			Query(context.Background(), "prompt")
		require.NoError(t, err)
		_, dials, requests := fi.snapshot()
		assert.Equal(t, []string{"93.184.216.34:443"}, dials)
		require.Len(t, requests, 1)
	})

	rejected := []struct {
		name, engine, baseURL string
	}{
		{"HTTP downgrade", config.AEOEgressOpenAI, "http://api.openai.com/v1/"},
		{"alternate port", config.AEOEgressOpenAI, "https://api.openai.com:8443/v1/"},
		{"neighbouring port", config.AEOEgressOpenAI, "https://api.openai.com:444/v1/"},
		{"suffix trick", config.AEOEgressOpenAI, "https://api.openai.com.evil.example/v1/"},
		{"subdomain trick", config.AEOEgressOpenAI, "https://evil.api.openai.com/v1/"},
		{"lookalike host", config.AEOEgressOpenAI, "https://api-openai.com/v1/"},
		{"cross-provider endpoint", config.AEOEgressOpenAI, "https://api.anthropic.com/v1/"},
		{"another engine's operator origin", config.AEOEgressOpenAI, "https://llm.example.com/v1"},
		{"arbitrary public host", config.AEOEgressOpenAI, "https://evil.example/v1"},
		{"loopback literal", config.AEOEgressOpenAI, "https://127.0.0.1/v1/"},
		{"IPv6 loopback literal", config.AEOEgressOpenAI, "https://[::1]/v1/"},
		{"IPv4-mapped loopback literal", config.AEOEgressOpenAI, "https://[::ffff:127.0.0.1]/v1/"},
		{"metadata literal", config.AEOEgressOpenAI, "http://169.254.169.254/latest/meta-data/"},
		{"public literal", config.AEOEgressOpenAI, "https://93.184.216.34/v1/"},
		{"numeric host", config.AEOEgressOpenAI, "https://2130706433/v1/"},
		{"credentials in the URL", config.AEOEgressOpenAI, "https://user:" + fakeSecretKey + "@api.openai.com/v1/"},
		{"query", config.AEOEgressOpenAI, "https://api.openai.com/v1/?key=" + fakeSecretKey},
		{"bare query", config.AEOEgressOpenAI, "https://api.openai.com/v1/?"},
		{"fragment", config.AEOEgressOpenAI, "https://api.openai.com/v1/#frag"},
		{"zone identifier", config.AEOEgressOpenAI, "https://[fe80::1%25en0]/v1/"},
		{"opaque form", config.AEOEgressOpenAI, "https:api.openai.com"},
		{"anthropic sent to openai", config.AEOEgressAnthropic, "https://api.openai.com/"},
		{"anthropic over http", config.AEOEgressAnthropic, "http://api.anthropic.com/"},
		{"gemini over http", config.AEOEgressGemini, "http://generativelanguage.googleapis.com/v1beta/openai/"},
		{"gemini to an evil suffix", config.AEOEgressGemini, "https://generativelanguage.googleapis.com.evil.example/"},
		{"kimi regional origin is not trusted by default", config.AEOEgressKimi, "https://api.moonshot.cn/v1"},
		{"perplexity alternate port", config.AEOEgressPerplexity, "https://api.perplexity.ai:8443"},
		{"custom engine on an unlisted origin", config.AEOEgressCustom, "https://evil.example/v1"},
		{"custom engine on a loopback origin without an exception", config.AEOEgressCustom, "http://127.0.0.1:1234/v1"},
	}
	for _, tc := range rejected {
		t.Run("refused before any request: "+tc.name, func(t *testing.T) {
			fi.reset()
			logs := captureAEOLogs(t)
			provider := singleEngine(t, tc.engine, tc.baseURL, fi.guard(t, customTestOrigin, ""))
			_, err := provider.Query(context.Background(), reflectedPrompt)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrEndpointNotPermitted)
			assert.Equal(t, FailureEndpointNotPermitted, ProviderFailureCategory(err))
			assert.Equal(t, 0, ProviderHTTPStatus(err))
			assertSafeText(t, err.Error())
			assertSafeText(t, logs.String())
			assertNoTraffic(t, fi)
		})
	}

	t.Run("a malformed operator policy fails closed, even for official origins", func(t *testing.T) {
		fi.reset()
		policy := config.ParseAEOEgressPolicy("openai=http://proxy.example.com", "")
		require.Error(t, policy.Err)
		guard := NewEgressGuard(policy, WithEgressResolver(fi), WithEgressDialer(fi.dial), WithEgressRootCAs(fi.roots))
		for _, engine := range egressEngines {
			_, err := singleEngine(t, engine, "", guard).Query(context.Background(), "prompt")
			assert.ErrorIs(t, err, ErrEndpointNotPermitted, engine)
		}
		assertNoTraffic(t, fi)
	})

	t.Run("production constructors default to official-origin trust", func(t *testing.T) {
		anthropicProvider := NewAnthropicProvider(fakeSecretKey, "m", "https://evil.example/", nil)
		_, err := anthropicProvider.Query(context.Background(), "prompt")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted)

		compat := NewOpenAICompatProvider(OpenAICompatConfig{Name: ProviderOpenAI, Model: "m", APIKey: fakeSecretKey, BaseURL: "http://127.0.0.1:1/v1"})
		_, err = compat.Query(context.Background(), "prompt")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted)

		// A custom display name cannot borrow another engine's trust.
		custom := LoadProvidersFor(config.AEOConfig{CustomName: ProviderOpenAI, CustomBaseURL: "https://api.openai.com/v1"})
		require.Len(t, custom, 1)
		_, err = custom[0].Query(context.Background(), "prompt")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted)
	})

	t.Run("the transport refuses a request outside the approved origin", func(t *testing.T) {
		fi.reset()
		client, err := fi.guard(t, "", "").httpClient(config.AEOEgressOpenAI, OpenAIBaseURL)
		require.NoError(t, err)
		for _, target := range []string{
			"https://evil.example/v1/chat/completions",
			"http://api.openai.com/v1/chat/completions",
			"https://api.openai.com:8443/v1/chat/completions",
			"https://user:pw@api.openai.com/v1/chat/completions",
		} {
			req, err := http.NewRequest(http.MethodPost, target, strings.NewReader("{}"))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+fakeSecretKey)
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			assert.ErrorIs(t, err, ErrEndpointNotPermitted, target)
		}
		assertNoTraffic(t, fi)
	})

	t.Run("ambient proxy settings cannot route around the policy", func(t *testing.T) {
		fi.reset()
		for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY"} {
			t.Setenv(name, "http://127.0.0.1:9")
		}
		guard := fi.guard(t, "", "")
		client, err := guard.httpClient(config.AEOEgressOpenAI, OpenAIBaseURL)
		require.NoError(t, err)
		guarded, ok := client.Transport.(*guardedTransport)
		require.True(t, ok)
		transport, ok := guarded.next.(*http.Transport)
		require.True(t, ok)
		assert.Nil(t, transport.Proxy, "no proxy function, so HTTP(S)_PROXY is ignored")
		assert.False(t, transport.TLSClientConfig.InsecureSkipVerify)

		_, err = singleEngine(t, config.AEOEgressOpenAI, "", guard).Query(context.Background(), "prompt")
		require.NoError(t, err)
		_, dials, requests := fi.snapshot()
		assert.Equal(t, []string{"93.184.216.34:443"}, dials, "the validated target is dialed, never the proxy")
		require.Len(t, requests, 1)
		assert.Equal(t, "/v1/chat/completions", requests[0].Path, "an origin-form request, not a proxy request")
	})
}

// ---------------------------------------------------------------------------
// TestAEODialPolicy
// ---------------------------------------------------------------------------

type scriptedResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
	err     error
	hosts   []string
}

func (r *scriptedResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	if r.err != nil {
		return nil, r.err
	}
	index := len(r.hosts) - 1
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	return r.answers[index], nil
}

func (r *scriptedResolver) lookups() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts)
}

type recordingDialer struct {
	mu        sync.Mutex
	addresses []string
	fail      map[string]bool
}

func (d *recordingDialer) dial(_ context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses = append(d.addresses, address)
	if d.fail[address] {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (d *recordingDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...)
}

func parseAddrs(values ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParseAddr(value))
	}
	return out
}

// dialThrough runs the guard's connection-time check for one engine endpoint.
func dialThrough(t *testing.T, guard *EgressGuard, engine, baseURL, address string) (net.Conn, error) {
	t.Helper()
	rule, err := guard.ruleFor(engine, baseURL)
	require.NoError(t, err)
	conn, err := guard.dialContext(rule)(context.Background(), "tcp", address)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, err
}

func TestAEODialPolicy(t *testing.T) {
	blocked := []string{
		"10.0.0.1", "172.16.5.4", "192.168.1.1", "127.0.0.1", "127.255.255.254", "::1",
		"169.254.169.254", "169.254.170.2", "169.254.1.1", "fe80::1", "fd00:ec2::254", "fc00::1", "fd12::1",
		"100.100.100.200", "168.63.129.16", "100.64.0.1", "0.0.0.0", "0.1.2.3", "::",
		"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2", "255.255.255.255", "240.0.0.1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::a00:1",
		"64:ff9b::a00:1", "2002:a00:1::1", "2001:0:4136:e378::1", "2001:db8::1",
		"192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9", "192.88.99.1",
	}
	for _, value := range blocked {
		t.Run("blocks "+value, func(t *testing.T) {
			resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs(value)}}
			dialer := &recordingDialer{}
			guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
			_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
			assert.ErrorIs(t, err, ErrDestinationNotPermitted)
			assert.Equal(t, 1, resolver.lookups())
			assert.Empty(t, dialer.dialed(), "a refused address is never dialed")
		})
	}

	t.Run("a zoned answer is refused", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("fe80::1%en0")}}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
		assert.ErrorIs(t, err, ErrDestinationNotPermitted)
		assert.Empty(t, dialer.dialed())
	})

	allowed := map[string]string{
		"93.184.216.34":        "93.184.216.34:443",
		"2606:4700:4700::1111": "[2606:4700:4700::1111]:443",
		"::ffff:93.184.216.34": "93.184.216.34:443",
	}
	for value, want := range allowed {
		t.Run("dials the validated literal for "+value, func(t *testing.T) {
			resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs(value)}}
			dialer := &recordingDialer{}
			guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
			conn, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
			require.NoError(t, err)
			require.NotNil(t, conn)
			assert.Equal(t, []string{want}, dialer.dialed())
			assert.Equal(t, 1, resolver.lookups(), "no second DNS lookup")
		})
	}

	t.Run("a mixed public and private answer is refused as a whole", func(t *testing.T) {
		for _, answer := range [][]netip.Addr{parseAddrs("93.184.216.34", "10.0.0.1"), parseAddrs("127.0.0.1", "93.184.216.34")} {
			resolver := &scriptedResolver{answers: [][]netip.Addr{answer}}
			dialer := &recordingDialer{}
			guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
			_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
			assert.ErrorIs(t, err, ErrDestinationNotPermitted)
			assert.Empty(t, dialer.dialed())
		}
	})

	t.Run("DNS rebinding is caught on the next connection", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs("93.184.216.34"), parseAddrs("127.0.0.1")}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
		require.NoError(t, err)
		_, err = dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
		assert.ErrorIs(t, err, ErrDestinationNotPermitted)
		assert.Equal(t, 2, resolver.lookups(), "every connection re-resolves and re-validates")
		assert.Equal(t, []string{"93.184.216.34:443"}, dialer.dialed(), "the rebound answer is never dialed")
	})

	t.Run("every validated address is tried in order", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs("93.184.216.34", "2606:4700:4700::1111")}}
		dialer := &recordingDialer{fail: map[string]bool{"93.184.216.34:443": true}}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
		require.NoError(t, err)
		assert.Equal(t, []string{"93.184.216.34:443", "[2606:4700:4700::1111]:443"}, dialer.dialed())
	})

	t.Run("a dial target other than the approved origin is refused unresolved", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs("93.184.216.34")}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		for _, target := range []string{"evil.example:443", "api.openai.com:80", "93.184.216.34:443", "garbage"} {
			_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, target)
			assert.ErrorIs(t, err, ErrDestinationNotPermitted, target)
		}
		assert.Equal(t, 0, resolver.lookups())
		assert.Empty(t, dialer.dialed())
	})

	t.Run("a resolver failure is a DNS failure with nothing dialed", func(t *testing.T) {
		resolver := &scriptedResolver{err: &net.DNSError{Err: "no such host", Name: "api.openai.com", IsNotFound: true}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		_, err := dialThrough(t, guard, config.AEOEgressOpenAI, OpenAIBaseURL, "api.openai.com:443")
		require.Error(t, err)
		assert.Equal(t, FailureDNS, ProviderFailureCategory(SanitizeProviderError(ProviderOpenAI, err)))
		assert.Empty(t, dialer.dialed())
	})

	t.Run("a local exception is limited to its engine, origin and addresses", func(t *testing.T) {
		policy := config.ParseAEOEgressPolicy("",
			"custom=http://127.0.0.1:1234,custom=http://ollama:11434@172.18.0.0/16")
		require.NoError(t, policy.Err)
		resolver := &scriptedResolver{answers: [][]netip.Addr{
			parseAddrs("172.18.0.5"), parseAddrs("172.19.0.1"), parseAddrs("169.254.169.254"), parseAddrs("93.184.216.34"),
			parseAddrs("172.18.0.6", "10.0.0.1"),
		}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(policy, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))

		_, err := dialThrough(t, guard, config.AEOEgressCustom, "http://127.0.0.1:1234/v1", "127.0.0.1:1234")
		require.NoError(t, err)
		assert.Equal(t, 0, resolver.lookups(), "an address literal needs no lookup")

		_, err = dialThrough(t, guard, config.AEOEgressCustom, "http://ollama:11434/v1", "ollama:11434")
		require.NoError(t, err, "inside the declared range")
		for i := 0; i < 4; i++ {
			_, err = dialThrough(t, guard, config.AEOEgressCustom, "http://ollama:11434/v1", "ollama:11434")
			assert.ErrorIs(t, err, ErrDestinationNotPermitted,
				"outside the range, metadata, public space and mixed answers are refused")
		}
		assert.Equal(t, []string{"127.0.0.1:1234", "172.18.0.5:11434"}, dialer.dialed())

		for _, engine := range []string{config.AEOEgressOpenAI, config.AEOEgressAnthropic} {
			_, err = guard.ruleFor(engine, "http://127.0.0.1:1234/v1")
			assert.ErrorIs(t, err, ErrEndpointNotPermitted, "a local exception never extends to %s", engine)
		}
		_, err = guard.ruleFor(config.AEOEgressCustom, "http://127.0.0.1:1235/v1")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted, "nor to another port")
		_, err = guard.ruleFor(config.AEOEgressCustom, "https://127.0.0.1:1234/v1")
		assert.ErrorIs(t, err, ErrEndpointNotPermitted, "nor to another scheme")
	})

	t.Run("an official origin resolving to a private address is refused end to end", func(t *testing.T) {
		fi := newFakeInternet(t)
		fi.answers["api.openai.com"] = parseAddrs("10.0.0.7")
		_, err := singleEngine(t, config.AEOEgressOpenAI, "", fi.guard(t, "", "")).Query(context.Background(), reflectedPrompt)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDestinationNotPermitted)
		assert.Equal(t, FailureDestinationNotPermitted, ProviderFailureCategory(err))
		lookups, dials, requests := fi.snapshot()
		assert.Len(t, lookups, 1, "the latch stops the SDK retry from resolving again")
		assert.Empty(t, dials)
		assert.Empty(t, requests)
	})

	t.Run("TLS certificate and host-name verification stay on", func(t *testing.T) {
		wrongHost := newFakeInternet(t, "unrelated.example")
		_, err := singleEngine(t, config.AEOEgressOpenAI, "", wrongHost.guard(t, "", "")).Query(context.Background(), "prompt")
		require.Error(t, err)
		assert.Equal(t, FailureTLS, ProviderFailureCategory(err))
		_, dials, requests := wrongHost.snapshot()
		assert.NotEmpty(t, dials)
		assert.Empty(t, requests, "no request is sent over an unverified connection")

		untrusted := newFakeInternet(t)
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(untrusted), WithEgressDialer(untrusted.dial))
		_, err = singleEngine(t, config.AEOEgressOpenAI, "", guard).Query(context.Background(), "prompt")
		require.Error(t, err)
		assert.Equal(t, FailureTLS, ProviderFailureCategory(err), "an unknown authority is refused")
		_, _, requests = untrusted.snapshot()
		assert.Empty(t, requests)
	})

	t.Run("context cancellation stops the connection", func(t *testing.T) {
		fi := newFakeInternet(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := singleEngine(t, config.AEOEgressOpenAI, "", fi.guard(t, "", "")).Query(ctx, "prompt")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		_, _, requests := fi.snapshot()
		assert.Empty(t, requests)
	})
}

// ---------------------------------------------------------------------------
// TestAEORedirectPolicy
// ---------------------------------------------------------------------------

func TestAEORedirectPolicy(t *testing.T) {
	fi := newFakeInternet(t)

	redirects := []struct {
		name     string
		status   int
		location string
	}{
		{"cross-origin", http.StatusFound, "https://evil.example/steal?key=" + fakeSecretKey},
		{"same-host downgrade", http.StatusMovedPermanently, "http://api.openai.com/v1/chat/completions"},
		{"same origin", http.StatusTemporaryRedirect, "https://api.openai.com/v2/chat/completions"},
		{"public to metadata", http.StatusPermanentRedirect, "http://169.254.169.254/latest/meta-data/"},
		{"public to loopback", http.StatusSeeOther, "http://127.0.0.1:8080/"},
		{"multiple choices without location", http.StatusMultipleChoices, ""},
		{"not modified", http.StatusNotModified, ""},
	}

	check := func(t *testing.T, engine string, status int, location string) {
		t.Helper()
		fi.reset()
		logs := captureAEOLogs(t)
		fi.setHandler(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			if location != "" {
				w.Header().Set("Location", location)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "REMOTE-BODY "+fakeSecretKey)
		})

		_, err := singleEngine(t, engine, "", fi.guard(t, customTestOrigin, "")).Query(context.Background(), reflectedPrompt)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRedirectNotFollowed)
		assert.Equal(t, FailureRedirectNotFollowed, ProviderFailureCategory(err))
		assert.Equal(t, status, ProviderHTTPStatus(err), "the numeric status survives")
		assertSafeText(t, err.Error())
		assertSafeText(t, logs.String())

		lookups, dials, requests := fi.snapshot()
		require.Len(t, requests, 1, "a redirect never causes a second request, not even the SDK retry")
		assert.Equal(t, officialRequest[engine].host, requests[0].Host)
		assert.Equal(t, []string{officialRequest[engine].host}, lookups, "the redirect target is never resolved")
		assert.Equal(t, []string{"93.184.216.34:443"}, dials)
	}

	for _, tc := range redirects {
		t.Run("openai/"+tc.name, func(t *testing.T) {
			check(t, config.AEOEgressOpenAI, tc.status, tc.location)
		})
	}
	for _, engine := range egressEngines {
		if engine == config.AEOEgressOpenAI {
			continue
		}
		t.Run(engine+"/cross-origin", func(t *testing.T) {
			check(t, engine, http.StatusFound, "https://evil.example/steal?key="+fakeSecretKey)
		})
	}

	t.Run("the client backstop refuses to follow even without the transport check", func(t *testing.T) {
		client, err := fi.guard(t, "", "").httpClient(config.AEOEgressOpenAI, OpenAIBaseURL)
		require.NoError(t, err)
		assert.ErrorIs(t, client.CheckRedirect(nil, nil), ErrRedirectNotFollowed)
	})
}
