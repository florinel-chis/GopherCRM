package aeo

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/egress"
)

// The egress guard is the single outbound path for every AEO provider. A
// provider's credentials may only travel to an origin the operator trusts for
// that engine, and only over a connection whose address passed the policy:
//
//   - Before any request: the base URL must match, exactly (scheme, host and
//     port), the engine's official HTTPS origin, an AEO_EGRESS_ALLOWED_ORIGINS
//     entry or an AEO_EGRESS_LOCAL_ORIGINS entry for that same engine.
//   - On every connection: the host is resolved, every returned address is
//     validated, and the dialer connects to a validated address literal, so a
//     second DNS answer (rebinding) can never be used. Non-public addresses are
//     refused unless a local exception covers them; link-local and metadata
//     addresses are refused always.
//   - Redirects are never followed and ambient proxy variables are ignored.
//   - TLS keeps the standard library's certificate and host-name checks.
//
// See https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html
// and https://pkg.go.dev/net/http#Transport.

// Sentinel errors for policy refusals. Provider errors unwrap to them, so
// callers can classify with errors.Is. They are the internal/egress values.
var (
	ErrEndpointNotPermitted    = egress.ErrEndpointNotPermitted
	ErrDestinationNotPermitted = egress.ErrDestinationNotPermitted
	ErrRedirectNotFollowed     = egress.ErrRedirectNotFollowed
)

// officialOrigins is the default trust: each engine's own HTTPS API origin.
// The custom engine has none and needs an explicit operator entry.
var officialOrigins = map[string]string{
	config.AEOEgressAnthropic:  "https://api.anthropic.com",
	config.AEOEgressOpenAI:     "https://api.openai.com",
	config.AEOEgressGemini:     "https://generativelanguage.googleapis.com",
	config.AEOEgressKimi:       "https://api.moonshot.ai",
	config.AEOEgressPerplexity: "https://api.perplexity.ai",
}

// Resolver looks up the addresses of a host name. *net.Resolver satisfies it;
// tests inject a scripted one.
type Resolver = egress.Resolver

// DialFunc opens a connection. The guard only ever passes it a validated
// "address:port" literal, never a host name.
type DialFunc = egress.DialFunc

// EgressGuard enforces the operator egress policy for provider clients. The
// dial-time checks come from the internal/egress guard it wraps.
type EgressGuard struct {
	policy  config.AEOEgressPolicy
	guard   *egress.Guard
	rootCAs *x509.CertPool
	seams   []egress.Option
}

// EgressOption customizes a guard. The options replace the network seams only;
// none can widen the policy or disable TLS verification.
type EgressOption func(*EgressGuard)

// WithEgressResolver replaces the DNS resolver.
func WithEgressResolver(resolver Resolver) EgressOption {
	return func(g *EgressGuard) { g.seams = append(g.seams, egress.WithResolver(resolver)) }
}

// WithEgressDialer replaces the connection dialer.
func WithEgressDialer(dial DialFunc) EgressOption {
	return func(g *EgressGuard) { g.seams = append(g.seams, egress.WithDialer(dial)) }
}

// WithEgressRootCAs replaces the trusted certificate roots. Certificates and
// host names are still verified against them.
func WithEgressRootCAs(pool *x509.CertPool) EgressOption {
	return func(g *EgressGuard) {
		g.rootCAs = pool
		g.seams = append(g.seams, egress.WithRootCAs(pool))
	}
}

// NewEgressGuard builds a guard for the given operator policy.
func NewEgressGuard(policy config.AEOEgressPolicy, opts ...EgressOption) *EgressGuard {
	guard := &EgressGuard{policy: policy}
	for _, opt := range opts {
		opt(guard)
	}
	guard.guard = egress.New(guard.seams...)
	guard.seams = nil
	return guard
}

// egressRule is the one origin a provider client may reach, and how its
// addresses are judged.
type egressRule = egress.Rule

// ruleFor matches a base URL against the trust of one engine. Nothing here
// performs network I/O.
func (g *EgressGuard) ruleFor(policyKey, baseURL string) (egressRule, error) {
	if g == nil || g.policy.Err != nil {
		return egressRule{}, ErrEndpointNotPermitted
	}
	endpoint, err := config.ParseAEOEndpoint(strings.TrimSpace(baseURL), true)
	if err != nil {
		return egressRule{}, ErrEndpointNotPermitted
	}

	if official, ok := officialOrigins[policyKey]; ok {
		if origin, err := config.ParseAEOEndpoint(official, false); err == nil && origin.SameOrigin(endpoint) {
			return egressRule{AEOEndpoint: origin}, nil
		}
	}
	for _, allowed := range g.policy.AllowedOrigins {
		if allowed.Provider == policyKey && allowed.SameOrigin(endpoint) {
			return egressRule{AEOEndpoint: allowed.AEOEndpoint}, nil
		}
	}
	for _, local := range g.policy.LocalOrigins {
		if local.Provider == policyKey && local.SameOrigin(endpoint) {
			return egressRule{AEOEndpoint: local.AEOEndpoint, Local: true, Prefixes: local.Prefixes}, nil
		}
	}
	return egressRule{}, ErrEndpointNotPermitted
}

// httpClient returns a client that can reach only the approved origin of
// baseURL for this engine, or ErrEndpointNotPermitted.
func (g *EgressGuard) httpClient(policyKey, baseURL string) (*http.Client, error) {
	rule, err := g.ruleFor(policyKey, baseURL)
	if err != nil {
		return nil, err
	}
	// Ambient HTTP(S)_PROXY settings must not route around the policy, so the
	// proxy is nil. Certificate verification stays on; only the roots may be
	// replaced. CheckRedirect is a backstop: the transport already refuses
	// every 3xx response.
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.dialContext(rule),
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: g.rootCAs},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport:     &egress.Transport{Rule: rule, Next: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirectNotFollowed },
	}, nil
}

// dialContext resolves and validates every address on each new connection and
// dials only a validated literal (egress.Guard.DialContext).
func (g *EgressGuard) dialContext(rule egressRule) DialFunc {
	return g.guard.DialContext(rule)
}
