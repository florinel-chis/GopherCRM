package aeo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
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
// callers can classify with errors.Is.
var (
	ErrEndpointNotPermitted    = errors.New("aeo: provider endpoint is not permitted by the egress policy")
	ErrDestinationNotPermitted = errors.New("aeo: provider destination address is not permitted by the egress policy")
	ErrRedirectNotFollowed     = errors.New("aeo: provider redirect was not followed")
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
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a connection. The guard only ever passes it a validated
// "address:port" literal, never a host name.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// EgressGuard enforces the operator egress policy for provider clients.
type EgressGuard struct {
	policy   config.AEOEgressPolicy
	resolver Resolver
	dial     DialFunc
	rootCAs  *x509.CertPool
}

// EgressOption customizes a guard. The options replace the network seams only;
// none can widen the policy or disable TLS verification.
type EgressOption func(*EgressGuard)

// WithEgressResolver replaces the DNS resolver.
func WithEgressResolver(resolver Resolver) EgressOption {
	return func(g *EgressGuard) {
		if resolver != nil {
			g.resolver = resolver
		}
	}
}

// WithEgressDialer replaces the connection dialer.
func WithEgressDialer(dial DialFunc) EgressOption {
	return func(g *EgressGuard) {
		if dial != nil {
			g.dial = dial
		}
	}
}

// WithEgressRootCAs replaces the trusted certificate roots. Certificates and
// host names are still verified against them.
func WithEgressRootCAs(pool *x509.CertPool) EgressOption {
	return func(g *EgressGuard) { g.rootCAs = pool }
}

// NewEgressGuard builds a guard for the given operator policy.
func NewEgressGuard(policy config.AEOEgressPolicy, opts ...EgressOption) *EgressGuard {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guard := &EgressGuard{policy: policy, resolver: net.DefaultResolver, dial: dialer.DialContext}
	for _, opt := range opts {
		opt(guard)
	}
	return guard
}

// egressRule is the one origin a provider client may reach, and how its
// addresses are judged.
type egressRule struct {
	config.AEOEndpoint
	local    bool
	prefixes []netip.Prefix
}

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
			return egressRule{AEOEndpoint: local.AEOEndpoint, local: true, prefixes: local.Prefixes}, nil
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
		Transport:     &guardedTransport{rule: rule, next: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirectNotFollowed },
	}, nil
}

// dialContext resolves and validates every address on each new connection and
// dials only a validated literal.
func (g *EgressGuard) dialContext(rule egressRule) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !sameHost(host, rule.Host) || port != rule.Port {
			return nil, ErrDestinationNotPermitted
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return nil, ErrDestinationNotPermitted
		}

		var addrs []netip.Addr
		if literal, err := netip.ParseAddr(host); err == nil {
			addrs = []netip.Addr{literal}
		} else {
			// The approved host, in its canonical lower-case form.
			addrs, err = g.resolver.LookupNetIP(ctx, "ip", rule.Host)
			if err != nil {
				return nil, err
			}
		}
		if len(addrs) == 0 {
			return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}

		// One unacceptable answer fails the connection: a mixed public and
		// private answer is how rebinding is staged.
		validated := make([]netip.Addr, 0, len(addrs))
		for _, addr := range addrs {
			if !rule.permits(addr) {
				return nil, ErrDestinationNotPermitted
			}
			validated = append(validated, addr.Unmap())
		}

		var lastErr error
		for _, addr := range validated {
			conn, err := g.dial(ctx, network, netip.AddrPortFrom(addr, uint16(portNumber)).String())
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	}
}

// permits judges one resolved address against the rule.
func (r egressRule) permits(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if alwaysBlockedAddress(addr) {
		return false
	}
	if r.local {
		for _, prefix := range r.prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	return publicAddress(addr)
}

// permitsURL reports whether a request URL targets exactly the rule's origin.
func (r egressRule) permitsURL(u *url.URL) bool {
	if u == nil || u.User != nil || !strings.EqualFold(u.Scheme, r.Scheme) || strings.Contains(u.Host, "%") {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	return port == r.Port && sameHost(u.Hostname(), r.Host)
}

// sameHost compares host names case-insensitively and address literals by
// value, so "[FD12::0001]" and "fd12::1" are one host.
func sameHost(a, b string) bool {
	addrA, errA := netip.ParseAddr(a)
	addrB, errB := netip.ParseAddr(b)
	if errA == nil || errB == nil {
		return errA == nil && errB == nil && addrA == addrB
	}
	return strings.EqualFold(a, b)
}

// alwaysBlockedAddress covers destinations no policy may open.
func alwaysBlockedAddress(addr netip.Addr) bool {
	if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return true
	}
	if addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true
	}
	for _, metadata := range config.AEOMetadataAddresses {
		if addr == metadata {
			return true
		}
	}
	return false
}

var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")

// nonPublicIPv6 lists special-purpose ranges inside 2000::/3: IETF protocol
// assignments (including Teredo), documentation, and 6to4, which embeds an
// IPv4 address.
var nonPublicIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3ffe::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

// publicAddress accepts globally routable unicast only. IPv6 is allow-listed
// to 2000::/3, which excludes loopback, unique-local, NAT64 and IPv4-compatible
// forms; IPv4-mapped addresses are judged as the IPv4 address they carry.
func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.Is4() {
		for _, prefix := range nonPublicIPv4 {
			if prefix.Contains(addr) {
				return false
			}
		}
		return true
	}
	if !globalUnicastIPv6.Contains(addr) {
		return false
	}
	for _, prefix := range nonPublicIPv6 {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// guardedTransport checks every request against the approved origin and turns
// any 3xx response into a terminal failure.
type guardedTransport struct {
	rule egressRule
	next http.RoundTripper
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	latch := egressLatchFrom(req.Context())
	if err := latch.get(); err != nil {
		closeRequestBody(req)
		return nil, err
	}
	if !t.rule.permitsURL(req.URL) {
		closeRequestBody(req)
		return nil, latch.set(ErrEndpointNotPermitted)
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		if errors.Is(err, ErrDestinationNotPermitted) {
			latch.set(ErrDestinationNotPermitted)
		}
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The Location and body are provider-controlled and are discarded
		// unread beyond a small drain.
		_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
		_ = resp.Body.Close()
		return nil, latch.set(&redirectError{statusCode: resp.StatusCode})
	}
	return resp, nil
}

func closeRequestBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

// redirectError records a refused redirect with its numeric status only.
type redirectError struct {
	statusCode int
}

func (e *redirectError) Error() string {
	return fmt.Sprintf("aeo: provider redirect (HTTP %d) was not followed", e.statusCode)
}

func (e *redirectError) Unwrap() error { return ErrRedirectNotFollowed }

// egressLatch remembers a policy refusal for one logical provider query. The
// SDKs retry transport errors once; the latch makes that retry fail locally,
// so a refused redirect or destination never causes a second request.
type egressLatch struct {
	mu  sync.Mutex
	err error
}

type egressLatchKey struct{}

func withEgressLatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, egressLatchKey{}, &egressLatch{})
}

func egressLatchFrom(ctx context.Context) *egressLatch {
	latch, _ := ctx.Value(egressLatchKey{}).(*egressLatch)
	return latch
}

func (l *egressLatch) get() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *egressLatch) set(err error) error {
	if l == nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		l.err = err
	}
	return err
}
