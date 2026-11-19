package utils

import (
	"context"
	"net/netip"
	"regexp"
	"strings"
)

// requestIDKey is the context key of the request id; unexported so no other
// package can set or read it except through the functions below.
type requestIDKey struct{}

// WithRequestID returns a copy of ctx that carries the request id, so a
// service can put it on its log lines. The id comes from the client's
// X-Request-ID header unchecked: log it only as a field, never in a message.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request id WithRequestID stored, or ""
// when there is none.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// clientInfoKey is the context key of the client facts WithClientInfo stores.
type clientInfoKey struct{}

// ClientInfo is the client address and user agent of a request. Both are
// client input: they are reduced (ClientNetwork, UserAgentFamily) before they
// are stored or logged, never kept whole.
type ClientInfo struct {
	IP        string
	UserAgent string
}

// WithClientInfo returns a copy of ctx that carries the client IP (as Gin's
// ClientIP resolves it) and the User-Agent header of the request.
func WithClientInfo(ctx context.Context, clientIP, userAgent string) context.Context {
	return context.WithValue(ctx, clientInfoKey{}, ClientInfo{IP: clientIP, UserAgent: userAgent})
}

// ClientInfoFromContext returns what WithClientInfo stored, or the zero value.
func ClientInfoFromContext(ctx context.Context) ClientInfo {
	if ctx == nil {
		return ClientInfo{}
	}
	info, _ := ctx.Value(clientInfoKey{}).(ClientInfo)
	return info
}

// ClientNetwork reduces an IP address to its network: a.b.c.0/24 for IPv4
// (an IPv4-mapped IPv6 address included) and the /48 prefix for IPv6, for
// example 2001:db8:1::/48. Anything that does not parse is "".
func ClientNetwork(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	addr = addr.Unmap().WithZone("")
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return ""
	}
	return prefix.String()
}

// The fixed vocabulary of UserAgentFamily, checked in this order. The first
// list entry whose marker the user agent contains names the browser or the
// operating system; nothing of the client's text is ever returned.
var (
	userAgentBrowsers = []struct{ name, marker string }{
		{"Edge", "Edg/"},
		{"Opera", "OPR/"},
		{"Firefox", "Firefox/"},
		{"Chrome", "Chrome/"},
		{"Chrome", "CriOS/"},
		{"Safari", "Safari/"},
		{"curl", "curl/"},
	}
	userAgentSystems = []struct{ name, marker string }{
		{"iOS", "iPhone"},
		{"iOS", "iPad"},
		{"Android", "Android"},
		{"ChromeOS", "CrOS"},
		{"Windows", "Windows"},
		{"macOS", "Mac OS X"},
		{"macOS", "Macintosh"},
		{"Linux", "Linux"},
	}
)

// UserAgentFamily maps a User-Agent header to "<Browser> on <OS>" from fixed
// lists: Edge, Opera, Firefox, Chrome, Safari, curl or Other, on iOS,
// Android, ChromeOS, Windows, macOS, Linux or Other. An empty user agent is "".
func UserAgentFamily(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return ""
	}
	browser, system := "Other", "Other"
	for _, candidate := range userAgentBrowsers {
		if strings.Contains(ua, candidate.marker) {
			browser = candidate.name
			break
		}
	}
	for _, candidate := range userAgentSystems {
		if strings.Contains(ua, candidate.marker) {
			system = candidate.name
			break
		}
	}
	return browser + " on " + system
}

// auditRequestIDPattern is what a request id must look like to be stored.
var auditRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// SafeRequestID returns the request id when it matches
// ^[A-Za-z0-9._:-]{1,64}$ and "" otherwise: the X-Request-ID header is
// unchecked client input.
func SafeRequestID(id string) string {
	if auditRequestIDPattern.MatchString(id) {
		return id
	}
	return ""
}
