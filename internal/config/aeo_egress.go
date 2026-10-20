package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Egress policy keys. Each names one AEO engine. The custom OpenAI-compatible
// engine is always "custom", whatever AEO_CUSTOM_NAME displays, so renaming it
// can never borrow another engine's trust.
const (
	AEOEgressAnthropic  = "anthropic"
	AEOEgressOpenAI     = "openai"
	AEOEgressGemini     = "gemini"
	AEOEgressKimi       = "kimi"
	AEOEgressPerplexity = "perplexity"
	AEOEgressCustom     = "custom"
)

var aeoEgressKeys = map[string]bool{
	AEOEgressAnthropic: true, AEOEgressOpenAI: true, AEOEgressGemini: true,
	AEOEgressKimi: true, AEOEgressPerplexity: true, AEOEgressCustom: true,
}

// AEOLocalNetworks are the only ranges an AEO_EGRESS_LOCAL_ORIGINS entry may
// open: loopback, RFC 1918, carrier-grade NAT and IPv6 unique-local space.
var AEOLocalNetworks = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
}

// AEOMetadataAddresses are cloud instance-metadata and platform endpoints. They
// are never reachable, not even through a local exception, because they hand
// out the host's own credentials. The IPv6 entries sit inside fc00::/7, so a
// local exception range could otherwise open them; see docs/AEO_SECURITY.md for
// the primary sources.
var AEOMetadataAddresses = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"), // AWS, GCP, Azure, OpenStack
	netip.MustParseAddr("169.254.170.2"),   // AWS ECS task metadata
	netip.MustParseAddr("169.254.170.23"),  // AWS EKS Pod Identity credentials
	netip.MustParseAddr("fd00:ec2::254"),   // AWS IMDS over IPv6
	netip.MustParseAddr("fd00:ec2::23"),    // AWS EKS Pod Identity credentials over IPv6
	netip.MustParseAddr("fd20:ce::254"),    // Google Cloud metadata over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"),   // Azure WireServer
}

// AEOEndpoint is a syntactically valid provider base URL reduced to the parts
// the egress policy compares. Host is lower-case and an IPv6 literal carries no
// brackets; Port is explicit, defaulted from the scheme.
type AEOEndpoint struct {
	Scheme string
	Host   string
	Port   string
	Path   string
}

// Origin renders scheme://host:port, the unit the egress policy trusts.
func (e AEOEndpoint) Origin() string {
	return e.Scheme + "://" + net.JoinHostPort(e.Host, e.Port)
}

// SameOrigin reports whether two endpoints share scheme, host and port.
func (e AEOEndpoint) SameOrigin(other AEOEndpoint) bool {
	return e.Scheme == other.Scheme && e.Host == other.Host && e.Port == other.Port
}

// ParseAEOEndpoint validates a provider base URL. It must be absolute, name a
// host, and carry no userinfo, query (not even a bare "?"), fragment, opaque
// part, zone identifier, or malformed host or port. Only https is accepted
// unless allowHTTP is set, which is reserved for operator-declared local
// origins. Error messages never echo the value: it may contain a secret.
func ParseAEOEndpoint(raw string, allowHTTP bool) (AEOEndpoint, error) {
	invalid := func(reason string) (AEOEndpoint, error) {
		return AEOEndpoint{}, errors.New("base URL " + reason)
	}
	if raw == "" {
		return invalid("is empty")
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= ' ' || c >= 0x7f || c == '\\' {
			return invalid("must be plain ASCII without spaces, control characters or backslashes")
		}
	}
	if strings.ContainsAny(raw, "?#") {
		return invalid("must not carry a query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || !u.IsAbs() || u.Host == "" {
		return invalid("must be an absolute URL with a host")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	default:
		return invalid("must use https")
	}
	if u.User != nil {
		return invalid("must not carry credentials")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid("must not carry a query or fragment")
	}
	if strings.Contains(u.Host, "%") {
		return invalid("must not carry a zone identifier or an encoded host")
	}
	host, port, ok := splitAEOHostPort(u.Host, u.Scheme)
	if !ok {
		return invalid("has a malformed host or port")
	}
	return AEOEndpoint{Scheme: u.Scheme, Host: host, Port: port, Path: u.Path}, nil
}

// splitAEOHostPort parses the authority of a URL strictly: a bracketed IPv6
// literal, a canonical dotted IPv4 literal or an LDH host name, and an
// optional decimal port without leading zeros.
func splitAEOHostPort(hostport, scheme string) (host, port string, ok bool) {
	rest := ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return "", "", false
		}
		addr, err := netip.ParseAddr(hostport[1:end])
		if err != nil || !addr.Is6() {
			return "", "", false
		}
		host, rest = addr.String(), hostport[end+1:]
	} else {
		if strings.Count(hostport, ":") > 1 {
			return "", "", false
		}
		host = hostport
		if i := strings.LastIndex(hostport, ":"); i >= 0 {
			host, rest = hostport[:i], hostport[i:]
		}
		host = strings.ToLower(host)
		if addr, err := netip.ParseAddr(host); err == nil {
			if !addr.Is4() {
				return "", "", false
			}
			host = addr.String()
		} else if !validAEOHostname(host) {
			return "", "", false
		}
	}

	switch {
	case rest == "":
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	case strings.HasPrefix(rest, ":"):
		port = rest[1:]
		if len(port) == 0 || len(port) > 5 || port[0] == '0' {
			return "", "", false
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", "", false
		}
	default:
		return "", "", false
	}
	return host, port, true
}

// validAEOHostname accepts lower-case letter-digit-hyphen labels. The last
// label must contain a letter, which rules out numeric address spellings such
// as "2130706433" or "127.1" that some resolvers expand to loopback.
func validAEOHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	return strings.IndexFunc(last, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
}

// AEOEgressOrigin is one operator-trusted origin for one engine. Prefixes is
// set only for local exceptions and lists the addresses that origin may reach.
type AEOEgressOrigin struct {
	Provider string
	AEOEndpoint
	Prefixes []netip.Prefix
}

// AEOEgressPolicy is the operator's outbound trust for AEO engines, read only
// from the environment. Each engine always trusts its own official HTTPS
// origin; AllowedOrigins adds exact public HTTPS origins and LocalOrigins adds
// narrowly scoped loopback/private destinations. A non-nil Err means the policy
// was malformed: the whole policy is dropped and every AEO request is refused.
type AEOEgressPolicy struct {
	AllowedOrigins []AEOEgressOrigin
	LocalOrigins   []AEOEgressOrigin
	Err            error
}

// ParseAEOEgressPolicy reads AEO_EGRESS_ALLOWED_ORIGINS and
// AEO_EGRESS_LOCAL_ORIGINS. Both are comma-separated "engine=origin" entries;
// a local entry may end in "@address" or "@CIDR". Any malformed entry fails
// the policy closed. Errors name the variable and entry number, never the
// value.
func ParseAEOEgressPolicy(allowed, local string) AEOEgressPolicy {
	var policy AEOEgressPolicy
	var problems []error

	for i, entry := range splitAEOEgressEntries(allowed) {
		origin, err := parseAEOAllowedOrigin(entry)
		if err != nil {
			problems = append(problems, fmt.Errorf("AEO_EGRESS_ALLOWED_ORIGINS entry %d: %w", i+1, err))
			continue
		}
		policy.AllowedOrigins = append(policy.AllowedOrigins, origin)
	}
	for i, entry := range splitAEOEgressEntries(local) {
		origin, err := parseAEOLocalOrigin(entry)
		if err != nil {
			problems = append(problems, fmt.Errorf("AEO_EGRESS_LOCAL_ORIGINS entry %d: %w", i+1, err))
			continue
		}
		policy.LocalOrigins = append(policy.LocalOrigins, origin)
	}

	if len(problems) > 0 {
		return AEOEgressPolicy{Err: errors.Join(problems...)}
	}
	return policy
}

func splitAEOEgressEntries(value string) []string {
	var entries []string
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func parseAEOEgressEngine(entry string) (string, string, error) {
	engine, rest, found := strings.Cut(entry, "=")
	engine = strings.ToLower(strings.TrimSpace(engine))
	if !found || !aeoEgressKeys[engine] {
		return "", "", errors.New("must start with anthropic=, openai=, gemini=, kimi=, perplexity= or custom=")
	}
	return engine, strings.TrimSpace(rest), nil
}

// parseAEOOriginOnly accepts a bare origin: a base URL whose path is empty or "/".
func parseAEOOriginOnly(raw string, allowHTTP bool) (AEOEndpoint, error) {
	endpoint, err := ParseAEOEndpoint(raw, allowHTTP)
	if err != nil {
		return AEOEndpoint{}, err
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return AEOEndpoint{}, errors.New("must be an origin (scheme://host[:port]) without a path")
	}
	endpoint.Path = ""
	return endpoint, nil
}

func parseAEOAllowedOrigin(entry string) (AEOEgressOrigin, error) {
	engine, rest, err := parseAEOEgressEngine(entry)
	if err != nil {
		return AEOEgressOrigin{}, err
	}
	endpoint, err := parseAEOOriginOnly(rest, false)
	if err != nil {
		return AEOEgressOrigin{}, err
	}
	if _, err := netip.ParseAddr(endpoint.Host); err == nil {
		return AEOEgressOrigin{}, errors.New("must name a host; address literals belong in AEO_EGRESS_LOCAL_ORIGINS")
	}
	return AEOEgressOrigin{Provider: engine, AEOEndpoint: endpoint}, nil
}

func parseAEOLocalOrigin(entry string) (AEOEgressOrigin, error) {
	engine, rest, err := parseAEOEgressEngine(entry)
	if err != nil {
		return AEOEgressOrigin{}, err
	}
	// userinfo is rejected in origins, so "@" can only be the address separator.
	originPart, prefixPart, hasPrefix := strings.Cut(rest, "@")
	endpoint, err := parseAEOOriginOnly(strings.TrimSpace(originPart), true)
	if err != nil {
		return AEOEgressOrigin{}, err
	}

	var prefixes []netip.Prefix
	if hasPrefix {
		prefix, err := parseAEOLocalPrefix(prefixPart)
		if err != nil {
			return AEOEgressOrigin{}, err
		}
		prefixes = []netip.Prefix{prefix}
	}

	if addr, literalErr := netip.ParseAddr(endpoint.Host); literalErr == nil {
		if addr.Is4In6() {
			return AEOEgressOrigin{}, errors.New("must write IPv4 addresses in dotted form")
		}
		if !AEOLocalAddress(addr) {
			return AEOEgressOrigin{}, errors.New("address is outside the loopback and private ranges")
		}
		if len(prefixes) == 0 {
			prefixes = []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())}
		} else if !prefixes[0].Contains(addr) {
			return AEOEgressOrigin{}, errors.New("address range does not contain the origin address")
		}
	} else if len(prefixes) == 0 {
		if endpoint.Host != "localhost" {
			return AEOEgressOrigin{}, errors.New("a host name needs an explicit @address or @CIDR")
		}
		prefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	}

	return AEOEgressOrigin{Provider: engine, AEOEndpoint: endpoint, Prefixes: prefixes}, nil
}

func parseAEOLocalPrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	var prefix netip.Prefix
	if strings.Contains(raw, "/") {
		parsed, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, errors.New("has a malformed address range")
		}
		prefix = parsed
	} else {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return netip.Prefix{}, errors.New("has a malformed address")
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" {
		return netip.Prefix{}, errors.New("must write IPv4 ranges in dotted form without zones")
	}
	prefix = prefix.Masked()

	inside := false
	for _, network := range AEOLocalNetworks {
		if prefix.Addr().BitLen() == network.Addr().BitLen() && prefix.Bits() >= network.Bits() && network.Contains(prefix.Addr()) {
			inside = true
			break
		}
	}
	if !inside {
		return netip.Prefix{}, errors.New("address range must lie inside one loopback or private range")
	}
	for _, metadata := range AEOMetadataAddresses {
		if prefix.Contains(metadata) {
			return netip.Prefix{}, errors.New("address range must not include a cloud metadata address")
		}
	}
	return prefix, nil
}

// AEOLocalAddress reports whether addr lies in a range a local exception may
// open and is not a metadata address.
func AEOLocalAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, metadata := range AEOMetadataAddresses {
		if addr == metadata {
			return false
		}
	}
	for _, network := range AEOLocalNetworks {
		if network.Contains(addr) {
			return true
		}
	}
	return false
}
