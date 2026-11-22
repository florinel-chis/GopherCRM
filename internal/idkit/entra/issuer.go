package entra

import (
	"net/url"
	"strings"
)

// IssuerHost is the host of the Entra ID global cloud. Sovereign clouds
// (login.microsoftonline.us, login.chinacloudapi.cn) are left out because
// their Graph host differs.
const IssuerHost = "login.microsoftonline.com"

// IsIssuer reports whether the issuer URL's host (without port, any letter
// case) is exactly host. A host that merely starts with it or a path that
// names it does not count, and an empty host matches nothing.
func IsIssuer(issuerURL, host string) bool {
	if host == "" {
		return false
	}
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), host)
}

// Tenant is the first path segment of the issuer URL, the tenant of an Entra
// ID issuer (https://login.microsoftonline.com/<tenant>/v2.0), or "" when the
// URL does not parse or that segment is empty.
func Tenant(issuerURL string) string {
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return ""
	}
	segment, _, _ := strings.Cut(strings.TrimPrefix(parsed.Path, "/"), "/")
	return segment
}
