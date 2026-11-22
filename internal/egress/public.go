package egress

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// Public-only egress (#345). The webhook sender reaches URLs an administrator
// typed, so it reuses the dial-time checks of the AEO provider clients but none
// of their trust: AEO_EGRESS_ALLOWED_ORIGINS and AEO_EGRESS_LOCAL_ORIGINS never
// apply here, and neither does an engine's official origin.
//
//   - The URL must be https with a host, and carry no userinfo, query (not even
//     a bare "?") or fragment (config.ParseAEOEndpoint with allowHTTP false).
//   - On every connection the host is resolved and every returned address is
//     validated; one loopback, RFC 1918, CGNAT, unique-local, link-local,
//     metadata, multicast or unspecified answer refuses the connection
//     (IPv4-mapped forms are judged as IPv4), and only the validated literal is
//     dialled.
//   - Proxy variables are ignored, a 3xx answer is a terminal failure and its
//     Location is never followed, keep-alive is off so a discarded client
//     leaves no idle connection behind.

// PublicHTTPClient returns a client that can reach only rawURL's origin, and
// only through public addresses, or ErrEndpointNotPermitted for a URL that is
// not acceptable. Timeouts come from the request context.
func (g *Guard) PublicHTTPClient(rawURL string) (*http.Client, error) {
	if g == nil {
		return nil, ErrEndpointNotPermitted
	}
	endpoint, err := config.ParseAEOEndpoint(strings.TrimSpace(rawURL), false)
	if err != nil {
		return nil, ErrEndpointNotPermitted
	}
	// A public rule: no local exception, so permits() admits public unicast
	// only and alwaysBlockedAddress still applies first.
	rule := Rule{AEOEndpoint: endpoint}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext(rule),
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: g.rootCAs},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     true,
		MaxIdleConns:          1,
		IdleConnTimeout:       time.Second,
	}
	return &http.Client{
		Transport:     &Transport{Rule: rule, Next: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirectNotFollowed },
	}, nil
}

// CheckPublicURL validates rawURL the way PublicHTTPClient does before any
// network I/O, and additionally refuses a host that is an IP literal outside
// the public range. A host name is judged on every connection instead. The
// error never quotes the value.
func CheckPublicURL(rawURL string) error {
	endpoint, err := config.ParseAEOEndpoint(strings.TrimSpace(rawURL), false)
	if err != nil {
		return ErrEndpointNotPermitted
	}
	if literal, err := netip.ParseAddr(endpoint.Host); err == nil {
		if !(Rule{AEOEndpoint: endpoint}).permits(literal) {
			return ErrDestinationNotPermitted
		}
	}
	return nil
}

// RedirectStatus reports the status code of a refused redirect returned by a
// guarded client.
func RedirectStatus(err error) (int, bool) {
	var redirect *redirectError
	if errors.As(err, &redirect) {
		return redirect.statusCode, true
	}
	return 0, false
}
