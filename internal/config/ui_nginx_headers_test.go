package config

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const uiNginxConfigFile = "gocrm-ui/docker/nginx.conf"

// uiBrowserHeaders is the header set every web app location sends, in the
// Docker image's nginx config and in the provisioning guide's TLS site. The
// same values are set for `vite preview` in gocrm-ui/vite.config.ts.
var uiBrowserHeaders = []struct{ name, value string }{
	{"Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"},
	{"X-Frame-Options", "DENY"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "same-origin"},
}

var nginxAddHeader = regexp.MustCompile(`(?m)^\s*add_header\s.*$`)

// nginxAddHeaders returns the trimmed add_header directives of a block.
func nginxAddHeaders(block string) []string {
	found := nginxAddHeader.FindAllString(block, -1)
	for i := range found {
		found[i] = strings.TrimSpace(found[i])
	}
	return found
}

// assertUIBrowserHeaders checks that a location carries exactly the browser
// header set, plus the extra add_header lines given.
func assertUIBrowserHeaders(t *testing.T, block, what string, extra ...string) {
	t.Helper()
	want := append([]string{}, extra...)
	for _, h := range uiBrowserHeaders {
		want = append(want, `add_header `+h.name+` "`+h.value+`" always;`)
	}
	assert.ElementsMatch(t, want, nginxAddHeaders(block), "%s add_header set", what)
}

// assertNoAddHeaders checks that a block sends no header of its own.
func assertNoAddHeaders(t *testing.T, block, what string) {
	t.Helper()
	assert.Empty(t, nginxAddHeaders(block), "%s has no add_header", what)
}

var nginxLocationOpener = regexp.MustCompile(`(?m)^[ \t]*location\s[^{;\n]*\{`)

// nginxOutsideLocations returns text with every location block removed.
func nginxOutsideLocations(t *testing.T, text string) string {
	t.Helper()
	for {
		loc := nginxLocationOpener.FindStringIndex(text)
		if loc == nil {
			return text
		}
		block := nginxBlock(t, text[loc[0]:], text[loc[0]:loc[1]])
		text = text[:loc[0]] + text[loc[0]+len(block):]
	}
}

func TestUINginxBrowserHeaders(t *testing.T) {
	conf := strings.ReplaceAll(readRepositoryFile(t, uiNginxConfigFile), "\r\n", "\n")

	assertUIBrowserHeaders(t, nginxBlock(t, conf, "location / {"), "location /")
	assertUIBrowserHeaders(t, nginxBlock(t, conf, "location /assets/ {"), "location /assets/",
		`add_header Cache-Control "public, max-age=31536000, immutable";`)
	assertNoAddHeaders(t, nginxBlock(t, conf, "location /api/v1/ {"), "location /api/v1/")
	assertNoAddHeaders(t, nginxBlock(t, conf, "location /scim/v2/ {"), "location /scim/v2/")
	outside := nginxOutsideLocations(t, conf)
	require.Contains(t, outside, "server_name", "server level text kept")
	require.NotContains(t, outside, "try_files", "every location removed")
	assertNoAddHeaders(t, outside, "server level")
}

// uiNginxBackendLocations are the locations of the UI image's nginx config
// that forward a backend route family. The production Compose override drops
// the backend's host port, so these are the only way in from outside.
var uiNginxBackendLocations = []string{"location /api/v1/ {", "location /scim/v2/ {"}

const uiNginxBackendProxyPass = "proxy_pass http://backend:8080;"

var nginxProxyPassLine = regexp.MustCompile(`(?m)^\s*proxy_pass\s.*$`)

// nginxProxyPasses returns the trimmed proxy_pass directives of a block.
func nginxProxyPasses(block string) []string {
	found := nginxProxyPassLine.FindAllString(block, -1)
	for i := range found {
		found[i] = strings.TrimSpace(found[i])
	}
	return found
}

// nginxDirectiveLines returns the sorted trimmed lines inside a location
// block, without its opener, its closing brace, comments and blank lines.
func nginxDirectiveLines(t *testing.T, block string) []string {
	t.Helper()
	lines := strings.Split(block, "\n")
	require.GreaterOrEqual(t, len(lines), 2, "block spans more than one line")
	require.Equal(t, "}", strings.TrimSpace(lines[len(lines)-1]), "block ends with a closing brace")
	var out []string
	for _, line := range lines[1 : len(lines)-1] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// requireUINginxBackendLocation returns the block of a backend location of the
// UI image's nginx config after checking it holds exactly one proxy_pass to
// the backend service, without a URI part.
func requireUINginxBackendLocation(t *testing.T, conf, opener string) string {
	t.Helper()
	block := nginxBlock(t, conf, opener)
	assert.Equal(t, []string{uiNginxBackendProxyPass}, nginxProxyPasses(block), "%s proxy_pass", opener)
	return block
}

func TestUINginxBackendLocations(t *testing.T) {
	conf := strings.ReplaceAll(readRepositoryFile(t, uiNginxConfigFile), "\r\n", "\n")

	api := nginxDirectiveLines(t, requireUINginxBackendLocation(t, conf, "location /api/v1/ {"))
	scim := nginxDirectiveLines(t, requireUINginxBackendLocation(t, conf, "location /scim/v2/ {"))
	assert.Equal(t, api, scim, "location /scim/v2/ has the directives of location /api/v1/")
	for _, lines := range [][]string{api, scim} {
		for _, header := range []string{"Host", "X-Real-IP", "X-Forwarded-For", "X-Forwarded-Proto"} {
			found := false
			for _, line := range lines {
				if strings.HasPrefix(line, "proxy_set_header "+header+" ") {
					found = true
				}
			}
			assert.True(t, found, "proxy_set_header %s", header)
		}
	}
}
