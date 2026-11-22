// Package egress holds the outbound-request guard shared by the AEO provider
// clients and the webhook sender. It owns the dial-time checks (resolve the
// host, validate every answer, dial only a validated literal), the refusal of
// redirects and the per-query refusal latch. Which origins a caller may reach
// is the caller's policy: the AEO module matches a base URL against its
// engine trust (internal/aeo/egress.go), the webhook sender admits public
// https URLs only (PublicHTTPClient).
//
// See https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html
// and https://pkg.go.dev/net/http#Transport.
package egress

import (
	"context"
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

// Sentinel errors for policy refusals. Client errors unwrap to them, so
// callers can classify with errors.Is.
var (
	ErrEndpointNotPermitted    = errors.New("aeo: provider endpoint is not permitted by the egress policy")
	ErrDestinationNotPermitted = errors.New("aeo: provider destination address is not permitted by the egress policy")
	ErrRedirectNotFollowed     = errors.New("aeo: provider redirect was not followed")
)

// Resolver looks up the addresses of a host name. *net.Resolver satisfies it;
// tests inject a scripted one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a connection. The guard only ever passes it a validated
// "address:port" literal, never a host name.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Guard holds the network seams of the outbound path: the resolver, the
// dialer and the trusted certificate roots. It carries no trust of its own.
type Guard struct {
	resolver Resolver
	dial     DialFunc
	rootCAs  *x509.CertPool
}

// Option customizes a guard. The options replace the network seams only;
// none can widen what a rule admits or disable TLS verification.
type Option func(*Guard)

// WithResolver replaces the DNS resolver.
func WithResolver(resolver Resolver) Option {
	return func(g *Guard) {
		if resolver != nil {
			g.resolver = resolver
		}
	}
}

// WithDialer replaces the connection dialer.
func WithDialer(dial DialFunc) Option {
	return func(g *Guard) {
		if dial != nil {
			g.dial = dial
		}
	}
}

// WithRootCAs replaces the trusted certificate roots. Certificates and host
// names are still verified against them.
func WithRootCAs(pool *x509.CertPool) Option {
	return func(g *Guard) { g.rootCAs = pool }
}

// New builds a guard with the system resolver and dialer unless replaced.
func New(opts ...Option) *Guard {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guard := &Guard{resolver: net.DefaultResolver, dial: dialer.DialContext}
	for _, opt := range opts {
		opt(guard)
	}
	return guard
}

// Rule is the one origin a client may reach, and how its addresses are
// judged: public unicast only, or, for a local exception, only the listed
// prefixes.
type Rule struct {
	config.AEOEndpoint
	Local    bool
	Prefixes []netip.Prefix
}

// DialContext resolves and validates every address on each new connection and
// dials only a validated literal.
func (g *Guard) DialContext(rule Rule) DialFunc {
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
func (r Rule) permits(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if alwaysBlockedAddress(addr) {
		return false
	}
	if r.Local {
		for _, prefix := range r.Prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	return publicAddress(addr)
}

// permitsURL reports whether a request URL targets exactly the rule's origin.
func (r Rule) permitsURL(u *url.URL) bool {
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

// Transport checks every request against the rule's origin and turns any 3xx
// response into a terminal failure. A refusal is recorded in the query's
// latch (WithLatch), so a client retry fails locally.
type Transport struct {
	Rule Rule
	Next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	latch := egressLatchFrom(req.Context())
	if err := latch.get(); err != nil {
		closeRequestBody(req)
		return nil, err
	}
	if !t.Rule.permitsURL(req.URL) {
		closeRequestBody(req)
		return nil, latch.set(ErrEndpointNotPermitted)
	}

	resp, err := t.Next.RoundTrip(req)
	if err != nil {
		if errors.Is(err, ErrDestinationNotPermitted) {
			latch.set(ErrDestinationNotPermitted)
		}
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The Location and body are remote-controlled and are discarded
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

// egressLatch remembers a policy refusal for one logical query. The provider
// SDKs retry transport errors once; the latch makes that retry fail locally,
// so a refused redirect or destination never causes a second request.
type egressLatch struct {
	mu  sync.Mutex
	err error
}

type egressLatchKey struct{}

// WithLatch returns a context that carries a fresh refusal latch for one
// logical query.
func WithLatch(ctx context.Context) context.Context {
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
