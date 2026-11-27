package config

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin docs/operations/systemd.md (#231) to the deploy
// scripts it describes (scripts/deploy/receive-release.sh, bootstrap.sh,
// scripts/release/build.sh), to .env.example and to config.Load. Every value
// the guide must repeat is parsed from those files at test time, so a change
// to a script default fails here until the guide follows.

const serverProvisioningGuideFile = "docs/operations/systemd.md"

// serverProvisioningChangelogName is the guide's file name in the CHANGELOG
// entries of #231 and #305. CHANGELOG entries are history, so they keep the
// name the guide had when they were written (#798 moved it).
const serverProvisioningChangelogName = "SERVER_PROVISIONING.md"

// deployDefaults are the receive-release.sh defaults the guide is built on.
type deployDefaults struct {
	Root      string
	WebRoot   string
	EnvFile   string
	Service   string
	HealthURL string
	Port      string
}

var receiveReleaseDefaultPattern = regexp.MustCompile(`^(ROOT|WEB_ROOT|ENV_FILE|SERVICE|HEALTH_URL)=\$\{GOPHERCRM_[A-Z_]+:-(.+)\}$`)

func parseDeployDefaults(t *testing.T) deployDefaults {
	t.Helper()
	found := map[string]string{}
	for _, line := range strings.Split(readRepositoryFile(t, "scripts/deploy/receive-release.sh"), "\n") {
		m := receiveReleaseDefaultPattern.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		_, dup := found[m[1]]
		require.False(t, dup, "receive-release.sh sets the default of %s twice", m[1])
		found[m[1]] = m[2]
	}
	for _, name := range []string{"ROOT", "WEB_ROOT", "ENV_FILE", "SERVICE", "HEALTH_URL"} {
		require.NotEmpty(t, found[name], "receive-release.sh default of %s not found", name)
	}
	d := deployDefaults{
		Root:      found["ROOT"],
		WebRoot:   found["WEB_ROOT"],
		Service:   found["SERVICE"],
		HealthURL: found["HEALTH_URL"],
	}
	d.EnvFile = strings.ReplaceAll(strings.ReplaceAll(found["ENV_FILE"], "${ROOT}", d.Root), "$ROOT", d.Root)
	require.NotContains(t, d.EnvFile, "$", "ENV_FILE default still holds a variable after expanding $ROOT")
	u, err := url.Parse(d.HealthURL)
	require.NoError(t, err, "HEALTH_URL default")
	d.Port = u.Port()
	require.NotEmpty(t, d.Port, "HEALTH_URL default %q carries no port", d.HealthURL)
	return d
}

func readProvisioningGuide(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(readRepositoryFile(t, serverProvisioningGuideFile), "\r\n", "\n")
}

// guideFence is one fenced code block: its info string, its body and the byte
// offset of its opening line in the text it was parsed from.
type guideFence struct {
	info   string
	body   string
	offset int
}

func parseGuideFences(t *testing.T, text string) []guideFence {
	t.Helper()
	var fences []guideFence
	var current *guideFence
	var body []string
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if current == nil {
				current = &guideFence{info: strings.TrimSpace(strings.TrimPrefix(trimmed, "```")), offset: offset}
				body = nil
			} else {
				require.Equal(t, "```", trimmed, "a fence closes with a bare ```")
				current.body = strings.Join(body, "\n")
				fences = append(fences, *current)
				current = nil
			}
		} else if current != nil {
			body = append(body, line)
		}
		offset += len(line) + 1
	}
	require.Nil(t, current, "unterminated fenced block")
	return fences
}

func fencesWithInfo(fences []guideFence, info string) []guideFence {
	var out []guideFence
	for _, f := range fences {
		if f.info == info {
			out = append(out, f)
		}
	}
	return out
}

func fencesContaining(fences []guideFence, needle string) []guideFence {
	var out []guideFence
	for _, f := range fences {
		if strings.Contains(f.body, needle) {
			out = append(out, f)
		}
	}
	return out
}

// guideSection returns the text from the line equal to heading to the next
// line starting with "## ", or to the end of the document.
func guideSection(t *testing.T, doc, heading string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if line == heading {
			require.Equal(t, -1, start, "heading %q appears twice", heading)
			start = i
		}
	}
	require.NotEqual(t, -1, start, "heading %q not found", heading)
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// lineWithAll reports whether one physical line holds every needle.
func lineWithAll(text string, needles ...string) bool {
	for _, line := range strings.Split(text, "\n") {
		ok := true
		for _, n := range needles {
			if !strings.Contains(line, n) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// unitDirectives parses an ini block into section -> key -> values.
func unitDirectives(t *testing.T, body string) map[string]map[string][]string {
	t.Helper()
	out := map[string]map[string][]string{}
	section := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if out[section] == nil {
				out[section] = map[string][]string{}
			}
			continue
		}
		require.NotEmpty(t, section, "unit line %q outside a section", line)
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "unit line %q is not key=value", line)
		key = strings.TrimSpace(key)
		out[section][key] = append(out[section][key], strings.TrimSpace(value))
	}
	return out
}

func singleDirective(t *testing.T, unit map[string]map[string][]string, section, key string) string {
	t.Helper()
	values := unit[section][key]
	require.Len(t, values, 1, "[%s] %s= must be set exactly once", section, key)
	return values[0]
}

// TestServerProvisioningGuideDefaults pins AC-1.
func TestServerProvisioningGuideDefaults(t *testing.T) {
	d := parseDeployDefaults(t)
	guide := readProvisioningGuide(t)
	for _, value := range []string{d.Root, d.WebRoot, d.EnvFile, d.Service + ".service", d.HealthURL} {
		assert.Contains(t, guide, value, "the guide names the receive-release default %s", value)
	}
}

// TestServerProvisioningGuideUnit pins AC-2.
func TestServerProvisioningGuideUnit(t *testing.T) {
	d := parseDeployDefaults(t)
	guide := readProvisioningGuide(t)
	units := fencesContaining(fencesWithInfo(parseGuideFences(t, guide), "ini"), "[Service]")
	require.Len(t, units, 1, "exactly one ```ini block holds [Service]")
	assert.Contains(t, guide, "/etc/systemd/system/"+d.Service+".service", "the guide names the unit path")

	unit := unitDirectives(t, units[0].body)
	for _, key := range []string{"After", "Wants"} {
		values := unit["Unit"][key]
		require.NotEmpty(t, values, "[Unit] %s=", key)
		var targets []string
		for _, v := range values {
			targets = append(targets, strings.Fields(v)...)
		}
		assert.Contains(t, targets, "network-online.target", "[Unit] %s=", key)
	}

	want := map[string]string{
		"User":             "gophercrm",
		"Group":            "gophercrm",
		"ExecStart":        d.Root + "/bin/gophercrm",
		"EnvironmentFile":  d.EnvFile,
		"StateDirectory":   "gophercrm",
		"WorkingDirectory": "/var/lib/gophercrm",
		"Restart":          "on-failure",
		"NoNewPrivileges":  "yes",
		"ProtectSystem":    "strict",
		"ProtectHome":      "yes",
		"PrivateTmp":       "yes",
	}
	for key, value := range want {
		assert.Equal(t, value, singleDirective(t, unit, "Service", key), "[Service] %s=", key)
	}
	assert.NotEqual(t, "root", strings.ToLower(singleDirective(t, unit, "Service", "User")))
	assert.NotEqual(t, "root", strings.ToLower(singleDirective(t, unit, "Service", "Group")))
	assert.False(t, strings.HasPrefix(singleDirective(t, unit, "Service", "EnvironmentFile"), "-"),
		"EnvironmentFile must not be optional")
	stop, err := strconv.Atoi(singleDirective(t, unit, "Service", "TimeoutStopSec"))
	require.NoError(t, err, "TimeoutStopSec is an integer")
	assert.GreaterOrEqual(t, stop, 30)
	assert.Equal(t, "multi-user.target", singleDirective(t, unit, "Install", "WantedBy"))

	for section, keys := range unit {
		_, has := keys["Environment"]
		assert.False(t, has, "[%s] must not set Environment=", section)
	}
}

// guideDotenv is the parsed ```dotenv block.
type guideDotenv struct {
	body      string
	active    map[string]string
	commented []string
}

var (
	dotenvActiveLine    = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
	dotenvCommentedLine = regexp.MustCompile(`^#\s*([A-Z][A-Z0-9_]*)=`)
)

func parseGuideDotenv(t *testing.T, guide string) guideDotenv {
	t.Helper()
	blocks := fencesWithInfo(parseGuideFences(t, guide), "dotenv")
	require.Len(t, blocks, 1, "exactly one ```dotenv block")
	env := guideDotenv{body: blocks[0].body, active: map[string]string{}}
	for _, line := range strings.Split(env.body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if m := dotenvCommentedLine.FindStringSubmatch(line); m != nil {
				env.commented = append(env.commented, m[1])
			}
			continue
		}
		m := dotenvActiveLine.FindStringSubmatch(line)
		require.NotNil(t, m, "dotenv line %q is not KEY=VALUE", line)
		key, raw := m[1], m[2]
		_, dup := env.active[key]
		require.False(t, dup, "dotenv key %s is active twice", key)
		value := raw
		if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') && raw[len(raw)-1] == raw[0] {
			value = raw[1 : len(raw)-1]
			require.NotContains(t, value, string(raw[0]), "dotenv %s: embedded quote", key)
		} else {
			require.False(t, strings.ContainsAny(raw, "\"' \t"), "dotenv %s: bare value with quotes or whitespace", key)
		}
		require.False(t, strings.ContainsAny(value, "\\$`"), "dotenv %s: backslash, $ or backtick", key)
		env.active[key] = value
	}
	return env
}

// TestServerProvisioningGuideEnvTemplate pins AC-3.
func TestServerProvisioningGuideEnvTemplate(t *testing.T) {
	d := parseDeployDefaults(t)
	env := parseGuideDotenv(t, readProvisioningGuide(t))

	example := readRepositoryFile(t, ".env.example")
	keys := append([]string(nil), env.commented...)
	for key := range env.active {
		keys = append(keys, key)
	}
	for _, key := range keys {
		re := regexp.MustCompile(`(?m)^(# )?` + regexp.QuoteMeta(key) + `=`)
		assert.True(t, re.MatchString(example), ".env.example documents %s", key)
	}

	want := map[string]string{
		"DB_DRIVER":            "mysql",
		"DB_HOST":              "127.0.0.1",
		"DB_PORT":              "3306",
		"DB_NAME":              "gophercrm",
		"DB_USER":              "gophercrm",
		"SERVER_HOST":          "127.0.0.1",
		"SERVER_PORT":          d.Port,
		"SERVER_MODE":          "production",
		"API_PREFIX":           "/api/v1",
		"TRUSTED_PROXIES":      "127.0.0.1",
		"APP_BASE_URL":         "https://crm.example.com",
		"PUBLIC_BASE_URL":      "https://crm.example.com",
		"CORS_ALLOWED_ORIGINS": "https://crm.example.com",
	}
	for key, value := range want {
		got, ok := env.active[key]
		if assert.True(t, ok, "dotenv sets %s", key) {
			assert.Equal(t, value, got, "dotenv %s", key)
		}
	}
	assert.NotEqual(t, "root", strings.ToLower(env.active["DB_USER"]), "DB_USER must not be root")
	_, hasPath := env.active["DB_PATH"]
	assert.False(t, hasPath, "no active DB_PATH")
	for _, key := range []string{"DB_PASSWORD", "JWT_SECRET"} {
		assert.True(t, strings.HasPrefix(env.active[key], "CHANGE_THIS"), "%s is an active CHANGE_THIS placeholder", key)
	}
	for key, value := range env.active {
		secret := key == "API_KEY_SECRET" || key == "SMTP_PASSWORD" || key == "RECAPTCHA_SECRET_KEY" ||
			strings.HasSuffix(key, "_API_KEY")
		if secret {
			assert.True(t, value == "" || strings.HasPrefix(value, "CHANGE_THIS"), "%s must be empty or a placeholder", key)
		}
	}

	t.Setenv("DB_PATH", "")
	for key, value := range env.active {
		t.Setenv(key, value)
	}
	_, err := Load()
	require.Error(t, err, "config.Load refuses the placeholder JWT_SECRET")
	assert.Contains(t, err.Error(), "JWT_SECRET")

	t.Setenv("JWT_SECRET", validSecret())
	cfg, err := Load()
	require.NoError(t, err)
	port, err := strconv.Atoi(d.Port)
	require.NoError(t, err)
	assert.Equal(t, DriverMySQL, cfg.Database.Driver)
	assert.Equal(t, "127.0.0.1", cfg.Database.Host)
	assert.Equal(t, 3306, cfg.Database.Port)
	assert.Equal(t, env.active["DB_NAME"], cfg.Database.Name)
	assert.Equal(t, env.active["DB_USER"], cfg.Database.User)
	assert.Equal(t, env.active["DB_PASSWORD"], cfg.Database.Password)
	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, port, cfg.Server.Port)
	assert.Equal(t, "production", cfg.Server.Mode)
	assert.Equal(t, "/api/v1", cfg.API.Prefix)
	require.NotEmpty(t, cfg.Server.TrustedProxies)
	loopback := net.ParseIP("127.0.0.1")
	for _, entry := range cfg.Server.TrustedProxies {
		if ip := net.ParseIP(entry); ip != nil {
			assert.True(t, ip.Equal(loopback), "trusted proxy %s covers 127.0.0.1", entry)
			continue
		}
		_, cidr, err := net.ParseCIDR(entry)
		if assert.NoError(t, err, "trusted proxy %s", entry) {
			assert.True(t, cidr.Contains(loopback), "trusted proxy %s covers 127.0.0.1", entry)
		}
	}
}

// nginxBlock returns the brace-balanced block that starts at opener.
func nginxBlock(t *testing.T, text, opener string) string {
	t.Helper()
	start := strings.Index(text, opener)
	require.NotEqual(t, -1, start, "nginx block %q not found", opener)
	require.Equal(t, -1, strings.Index(text[start+len(opener):], opener), "nginx block %q appears twice", opener)
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	t.Fatalf("nginx block %q is not closed", opener)
	return ""
}

var nginxRootDirective = regexp.MustCompile(`(?m)^\s*root\s+(\S+);`)

func assertNginxDirective(t *testing.T, block, pattern, what string) {
	t.Helper()
	assert.Regexp(t, regexp.MustCompile(`(?m)^\s*`+pattern+`\s*$`), block, what)
}

// acmeRoot returns the root of the HTTP site's ACME challenge location.
func acmeRoot(t *testing.T, httpSite string) string {
	t.Helper()
	loc := nginxBlock(t, httpSite, "location /.well-known/acme-challenge/ {")
	m := nginxRootDirective.FindAllStringSubmatch(loc, -1)
	require.Len(t, m, 1, "the ACME location has one root")
	return m[0][1]
}

// TestServerProvisioningGuideNginx pins AC-4.
func TestServerProvisioningGuideNginx(t *testing.T) {
	d := parseDeployDefaults(t)
	fences := parseGuideFences(t, readProvisioningGuide(t))
	sites := fencesWithInfo(fences, "nginx")
	require.Len(t, sites, 2, "exactly two ```nginx blocks")
	certbot := fencesContaining(fences, "certbot certonly --webroot")
	require.Len(t, certbot, 1, "exactly one block runs certbot certonly --webroot")
	httpSite, tlsSite := sites[0], sites[1]
	require.Less(t, httpSite.offset, certbot[0].offset, "HTTP site before certbot")
	require.Less(t, certbot[0].offset, tlsSite.offset, "certbot before TLS site")

	assertNginxDirective(t, httpSite.body, `listen 80;`, "HTTP site listens on 80")
	assertNginxDirective(t, httpSite.body, `server_name crm\.example\.com;`, "HTTP server_name")
	assert.Contains(t, httpSite.body, "return 301 https://$host$request_uri;")
	acme := acmeRoot(t, httpSite.body)
	assert.True(t, path.IsAbs(acme), "ACME root %s is absolute", acme)
	acme = path.Clean(acme)
	webRoot := path.Clean(d.WebRoot)
	assert.NotEqual(t, webRoot, acme, "ACME root is not the web root")
	assert.False(t, strings.HasPrefix(acme, webRoot+"/"), "ACME root %s is not inside the web root", acme)

	var certbotLine []string
	for _, line := range strings.Split(certbot[0].body, "\n") {
		if strings.Contains(line, "certbot certonly --webroot") {
			certbotLine = strings.Fields(line)
		}
	}
	hasPair := func(flag, value string) bool {
		for i := 0; i+1 < len(certbotLine); i++ {
			if certbotLine[i] == flag && strings.TrimRight(certbotLine[i+1], "/") == strings.TrimRight(value, "/") {
				return true
			}
		}
		return false
	}
	assert.True(t, hasPair("-w", acme), "certbot -w %s", acme)
	assert.True(t, hasPair("-d", "crm.example.com"), "certbot -d crm.example.com")

	tls := tlsSite.body
	assertNginxDirective(t, tls, `listen 443 ssl;`, "TLS site listens on 443 ssl")
	assertNginxDirective(t, tls, `server_name crm\.example\.com;`, "TLS server_name")
	assertNginxDirective(t, tls, regexp.QuoteMeta("ssl_certificate /etc/letsencrypt/live/crm.example.com/fullchain.pem;"), "ssl_certificate")
	assertNginxDirective(t, tls, regexp.QuoteMeta("ssl_certificate_key /etc/letsencrypt/live/crm.example.com/privkey.pem;"), "ssl_certificate_key")
	assertNginxDirective(t, tls, regexp.QuoteMeta("root "+d.WebRoot+";"), "TLS root is the web root")

	proxyPass := "proxy_pass http://127.0.0.1:" + d.Port + ";"
	for _, opener := range []string{"location /api/v1/ {", "location = /health {", "location /scim/v2/ {"} {
		loc := nginxBlock(t, tls, opener)
		passes := regexp.MustCompile(`(?m)^\s*proxy_pass\s.*$`).FindAllString(loc, -1)
		require.Len(t, passes, 1, "%s has one proxy_pass", opener)
		assert.Equal(t, proxyPass, strings.TrimSpace(passes[0]), "%s proxy_pass without URI part", opener)
		for _, header := range []string{
			"proxy_set_header Host $host;",
			"proxy_set_header X-Real-IP $remote_addr;",
			"proxy_set_header X-Forwarded-For $remote_addr;",
			"proxy_set_header X-Forwarded-Proto $scheme;",
		} {
			assertNginxDirective(t, loc, regexp.QuoteMeta(header), opener+" "+header)
		}
	}
	spa := nginxBlock(t, tls, "location / {")
	assertNginxDirective(t, spa, regexp.QuoteMeta("try_files $uri $uri/ /index.html;"), "SPA fallback")

	// The web app locations send the browser header set of the Docker image's
	// nginx config; the proxied locations and the server level send none.
	assertUIBrowserHeaders(t, spa, "TLS location /")
	assertUIBrowserHeaders(t, nginxBlock(t, tls, "location = /quote-response {"), "TLS location = /quote-response")
	assertNoAddHeaders(t, nginxBlock(t, tls, "location /api/v1/ {"), "TLS location /api/v1/")
	assertNoAddHeaders(t, nginxBlock(t, tls, "location = /health {"), "TLS location = /health")
	assertNoAddHeaders(t, nginxBlock(t, tls, "location /scim/v2/ {"), "TLS location /scim/v2/")
	assert.Equal(t, nginxDirectiveLines(t, nginxBlock(t, tls, "location /api/v1/ {")),
		nginxDirectiveLines(t, nginxBlock(t, tls, "location /scim/v2/ {")),
		"TLS location /scim/v2/ has the directives of location /api/v1/")
	tlsOutside := nginxOutsideLocations(t, tls)
	require.NotContains(t, tlsOutside, "try_files", "every TLS location removed")
	assertNoAddHeaders(t, tlsOutside, "TLS server level")
	assertNoAddHeaders(t, httpSite.body, "HTTP site")
	// nginx's defaults, written out so the Apache site can be pinned to them (#749).
	assertNginxDirective(t, tlsOutside, regexp.QuoteMeta("client_max_body_size 1m;"), "TLS server-level client_max_body_size")
	assertNginxDirective(t, tlsOutside, regexp.QuoteMeta("proxy_read_timeout 60s;"), "TLS server-level proxy_read_timeout")
}

// nginxProxiedPaths returns the paths of the locations of a site that hold a
// proxy_pass, with the exact-match modifier dropped (`= /health` -> `/health`).
func nginxProxiedPaths(t *testing.T, site string) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	for _, loc := range nginxLocationOpener.FindAllString(site, -1) {
		opener := strings.TrimSpace(loc)
		if len(nginxProxyPasses(nginxBlock(t, site, opener))) == 0 {
			continue
		}
		fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(opener, "location"), "{"))
		if len(fields) == 2 && fields[0] == "=" {
			fields = fields[1:]
		}
		require.Len(t, fields, 1, "proxied location %q has one path and at most the = modifier", opener)
		paths[fields[0]] = true
	}
	return paths
}

// apacheLine is one directive line of an Apache block: the trimmed text, its
// fields and the directive name in lower case (Apache directive names are
// case-insensitive).
type apacheLine struct {
	text      string
	fields    []string
	directive string
}

// apacheLines returns the non-blank, non-comment lines of an Apache block.
func apacheLines(block string) []apacheLine {
	var out []apacheLine
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		out = append(out, apacheLine{text: line, fields: fields, directive: strings.ToLower(fields[0])})
	}
	return out
}

// apacheDirectiveLines returns the lines whose directive is one of names
// (lower case).
func apacheDirectiveLines(block string, names ...string) []apacheLine {
	var out []apacheLine
	for _, l := range apacheLines(block) {
		for _, n := range names {
			if l.directive == n {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// apacheSingle returns the value fields of the one line of a directive.
func apacheSingle(t *testing.T, block, directive, what string) []string {
	t.Helper()
	lines := apacheDirectiveLines(block, strings.ToLower(directive))
	require.Len(t, lines, 1, "%s sets %s exactly once", what, directive)
	return lines[0].fields[1:]
}

// apacheArg drops the quotes and a trailing slash from a path argument.
func apacheArg(arg string) string {
	arg = strings.Trim(arg, `"'`)
	if len(arg) > 1 {
		arg = strings.TrimRight(arg, "/")
	}
	return arg
}

// apacheTagBlock returns the text between the one `<tag args>` line whose
// arguments satisfy match and its `</tag>` line, and text without that whole
// section. The tag is compared case-insensitively.
func apacheTagBlock(t *testing.T, text, tag string, match func(args string) bool) (inner, outside string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	open := regexp.MustCompile(`(?i)^\s*<` + regexp.QuoteMeta(tag) + `(\s+[^>]*)?>\s*$`)
	closing := regexp.MustCompile(`(?i)^\s*</` + regexp.QuoteMeta(tag) + `>\s*$`)
	from, to := -1, -1
	for i, line := range lines {
		m := open.FindStringSubmatch(line)
		if m == nil || !match(strings.TrimSpace(m[1])) {
			continue
		}
		require.Equal(t, -1, from, "<%s> matches twice", tag)
		from = i
		for j := i + 1; j < len(lines); j++ {
			require.False(t, open.MatchString(lines[j]), "<%s> nested in <%s>", tag, tag)
			if closing.MatchString(lines[j]) {
				to = j
				break
			}
		}
		require.NotEqual(t, -1, to, "<%s> is not closed", tag)
	}
	require.NotEqual(t, -1, from, "<%s> not found", tag)
	inner = strings.Join(lines[from+1:to], "\n")
	outside = strings.Join(append(append([]string{}, lines[:from]...), lines[to+1:]...), "\n")
	return inner, outside
}

// apacheProxyPaths returns path -> target of the ProxyPass (or ProxyPassMatch)
// or ProxyPassReverse lines of a block. A ProxyPassMatch "^<path>$" counts as
// the entry for <path>.
func apacheProxyPaths(t *testing.T, block string, reverse bool) map[string]string {
	t.Helper()
	names := []string{"proxypass", "proxypassmatch"}
	if reverse {
		names = []string{"proxypassreverse"}
	}
	out := map[string]string{}
	for _, l := range apacheDirectiveLines(block, names...) {
		require.GreaterOrEqual(t, len(l.fields), 3, "%q is <directive> <path> <target>", l.text)
		p := strings.Trim(l.fields[1], `"'`)
		if l.directive == "proxypassmatch" {
			p = strings.TrimSuffix(strings.TrimPrefix(p, "^"), "$")
		}
		_, dup := out[p]
		require.False(t, dup, "%s %s appears twice", l.fields[0], p)
		out[p] = strings.Trim(l.fields[2], `"'`)
	}
	return out
}

// nginxServerSize parses a server-level nginx size (1m, 64k, 1024) into bytes.
func nginxServerSize(t *testing.T, server, directive string) int64 {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*`+regexp.QuoteMeta(directive)+`\s+(\d+)([kKmMgG]?);\s*$`).FindAllStringSubmatch(server, -1)
	require.Len(t, m, 1, "the nginx TLS server level sets %s once", directive)
	n, err := strconv.ParseInt(m[0][1], 10, 64)
	require.NoError(t, err)
	switch strings.ToLower(m[0][2]) {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	}
	return n
}

// nginxServerSeconds parses a server-level nginx time (60s, 1m, 60) into seconds.
func nginxServerSeconds(t *testing.T, server, directive string) int64 {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*`+regexp.QuoteMeta(directive)+`\s+(\d+)(s|m|h)?;\s*$`).FindAllStringSubmatch(server, -1)
	require.Len(t, m, 1, "the nginx TLS server level sets %s once", directive)
	n, err := strconv.ParseInt(m[0][1], 10, 64)
	require.NoError(t, err)
	switch m[0][2] {
	case "m":
		n *= 60
	case "h":
		n *= 3600
	}
	return n
}

// fenceEnd returns the offset just after the closing line of the fence that
// opens at offset in text.
func fenceEnd(t *testing.T, text string, offset int) int {
	t.Helper()
	rel := strings.Index(text[offset+3:], "\n```")
	require.NotEqual(t, -1, rel, "fence at %d is closed", offset)
	end := offset + 3 + rel + 1
	if nl := strings.Index(text[end:], "\n"); nl != -1 {
		return end + nl + 1
	}
	return len(text)
}

// fenceProse returns the paragraph directly before the fence that opens at
// offset and the text after it up to the next fence or heading.
func fenceProse(t *testing.T, text string, offset int) string {
	t.Helper()
	before := strings.TrimRight(text[:offset], "\n")
	if i := strings.LastIndex(before, "\n\n"); i != -1 {
		before = before[i+2:]
	}
	after := text[fenceEnd(t, text, offset):]
	var kept []string
	for _, line := range strings.Split(after, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(line, "#") {
			break
		}
		kept = append(kept, line)
	}
	return before + "\n" + strings.Join(kept, "\n")
}

// TestServerProvisioningGuideApache pins the Apache 2.4 setup of the guide
// against its nginx TLS site (#728, #749).
func TestServerProvisioningGuideApache(t *testing.T) {
	d := parseDeployDefaults(t)
	guide := readProvisioningGuide(t)
	require.Len(t, fencesWithInfo(parseGuideFences(t, guide), "apache"), 3, "exactly three ```apache blocks")
	section := guideSection(t, guide, "## Reverse proxy and TLS")
	sectionFences := parseGuideFences(t, section)
	apache := fencesWithInfo(sectionFences, "apache")
	require.Len(t, apache, 3, "the ```apache blocks are in Reverse proxy and TLS")
	sites := fencesWithInfo(sectionFences, "nginx")
	require.Len(t, sites, 2, "the section holds the HTTP and the TLS nginx site")
	nginxTLS := sites[1].body

	pick := func(needle, what string) guideFence {
		t.Helper()
		found := fencesContaining(apache, needle)
		require.Len(t, found, 1, "one ```apache block is the %s", what)
		return found[0]
	}
	httpFence := pick("<VirtualHost *:80>", ":80 site")
	httpsFence := pick("<VirtualHost *:443>", ":443 site")
	cfFence := pick("CF-Connecting-IP", "Cloudflare block")
	require.NotEqual(t, httpsFence.offset, cfFence.offset, "the Cloudflare block is separate from the :443 site")
	require.NotEqual(t, httpFence.offset, cfFence.offset, "the Cloudflare block is separate from the :80 site")

	vhostArgs := func(port string) func(string) bool {
		return func(args string) bool { return args == "*:"+port }
	}
	httpSite, _ := apacheTagBlock(t, httpFence.body, "VirtualHost", vhostArgs("80"))
	https, _ := apacheTagBlock(t, httpsFence.body, "VirtualHost", vhostArgs("443"))
	webRoot := path.Clean(d.WebRoot)
	directory, vhostLevel := apacheTagBlock(t, https, "Directory", func(args string) bool { return apacheArg(args) == webRoot })
	cf := cfFence.body

	// (i) Proxied paths equal the nginx TLS site's, with the backend target.
	want := nginxProxiedPaths(t, nginxTLS)
	require.Equal(t, map[string]bool{"/api/v1/": true, "/health": true, "/scim/v2/": true}, want,
		"the TLS site proxies these paths")
	for _, reverse := range []bool{false, true} {
		got := apacheProxyPaths(t, https, reverse)
		paths := map[string]bool{}
		for p, target := range got {
			paths[p] = true
			assert.Equal(t, "http://127.0.0.1:"+d.Port+p, target, "proxy target of %s (reverse=%v)", p, reverse)
		}
		assert.Equal(t, want, paths, "Apache :443 proxied paths (reverse=%v) equal the nginx site's", reverse)
	}
	proxyFamily := []string{"proxypass", "proxypassmatch", "proxypassreverse"}
	assert.Empty(t, apacheDirectiveLines(directory, proxyFamily...), "no ProxyPass inside <Directory>")
	assert.Empty(t, apacheDirectiveLines(httpFence.body, proxyFamily...), "no ProxyPass in the :80 site")
	assert.Empty(t, apacheDirectiveLines(cf, proxyFamily...), "no ProxyPass in the Cloudflare block")
	for _, f := range apache {
		for _, l := range apacheDirectiveLines(f.body, proxyFamily...) {
			assert.NotContains(t, l.text, "/quote-response", "/quote-response is not proxied")
		}
	}

	// (ii) The proxy headers, one for one with the nginx proxy_set_header lines.
	vhostTexts := map[string]bool{}
	for _, l := range apacheLines(vhostLevel) {
		vhostTexts[l.text] = true
	}
	apiLoc := nginxBlock(t, nginxTLS, "location /api/v1/ {")
	for apacheText, nginxText := range map[string]string{
		"ProxyPreserveHost On":                                    "proxy_set_header Host $host;",
		`RequestHeader set X-Real-IP "expr=%{REMOTE_ADDR}"`:       "proxy_set_header X-Real-IP $remote_addr;",
		`RequestHeader set X-Forwarded-For "expr=%{REMOTE_ADDR}"`: "proxy_set_header X-Forwarded-For $remote_addr;",
		`RequestHeader set X-Forwarded-Proto "https"`:             "proxy_set_header X-Forwarded-Proto $scheme;",
	} {
		assertNginxDirective(t, apiLoc, regexp.QuoteMeta(nginxText), "nginx "+nginxText)
		assert.True(t, vhostTexts[apacheText], "the :443 site sets %s (nginx %s)", apacheText, nginxText)
	}
	assert.True(t, vhostTexts["ProxyAddHeaders Off"], "the :443 site sets ProxyAddHeaders Off")
	requestHeaders := map[string]int{}
	for _, l := range apacheDirectiveLines(https, "requestheader") {
		require.GreaterOrEqual(t, len(l.fields), 3, "%q", l.text)
		requestHeaders[strings.ToLower(l.fields[2])]++
	}
	for _, h := range []string{"x-real-ip", "x-forwarded-for", "x-forwarded-proto"} {
		assert.Equal(t, 1, requestHeaders[h], "the :443 site sets %s once", h)
	}

	// (iii) Web root and SPA fallback.
	assert.Equal(t, webRoot, apacheArg(strings.Join(apacheSingle(t, vhostLevel, "DocumentRoot", ":443 site"), " ")),
		"DocumentRoot is the web root")
	dirTexts := map[string]bool{}
	for _, l := range apacheLines(directory) {
		dirTexts[l.text] = true
	}
	for _, line := range []string{
		"RewriteEngine On",
		"RewriteCond %{REQUEST_FILENAME} !-f",
		"RewriteRule ^ index.html [L]",
	} {
		assert.True(t, dirTexts[line], "<Directory %s> holds %s", webRoot, line)
	}

	// (iv) Browser headers inside <Directory> only, none of any action elsewhere.
	var wantHeaders []string
	for _, h := range uiBrowserHeaders {
		wantHeaders = append(wantHeaders, `Header always set `+h.name+` "`+h.value+`"`)
	}
	texts := func(lines []apacheLine) []string {
		var out []string
		for _, l := range lines {
			out = append(out, l.text)
		}
		return out
	}
	assert.ElementsMatch(t, wantHeaders, texts(apacheDirectiveLines(https, "header")), ":443 site Header lines")
	assert.ElementsMatch(t, wantHeaders, texts(apacheDirectiveLines(directory, "header")), "<Directory> Header lines")
	assert.Empty(t, apacheDirectiveLines(vhostLevel, "header"), "no Header line at :443 vhost level")
	assert.Empty(t, apacheDirectiveLines(httpFence.body, "header"), "no Header line in the :80 site")
	assert.Empty(t, apacheDirectiveLines(cf, "header"), "no Header line in the Cloudflare block")

	// (v) Body limit and timeout equal the nginx TLS site's server level.
	nginxServer := nginxOutsideLocations(t, nginxTLS)
	maxBody := strconv.FormatInt(nginxServerSize(t, nginxServer, "client_max_body_size"), 10)
	body := apacheSingle(t, https, "LimitRequestBody", ":443 site")
	require.Len(t, body, 1)
	assert.Equal(t, maxBody, body[0], "LimitRequestBody equals client_max_body_size")
	// LimitRequestBody does not reach requests mod_proxy_http forwards, so the
	// limit on proxied bodies is a vhost-level Content-Length check answering 413.
	for _, line := range []string{
		"RewriteEngine On",
		`RewriteCond expr "%{HTTP:Content-Length} -gt ` + maxBody + `"`,
		"RewriteRule ^ - [R=413,L]",
	} {
		assert.True(t, vhostTexts[line], ":443 vhost level holds %s", line)
	}
	var bodyBullet string
	for _, para := range strings.Split(section, "\n- ") {
		if strings.HasPrefix(para, "The body limit") {
			bodyBullet = para
		}
	}
	require.NotEmpty(t, bodyBullet, "a bullet explains the body limit")
	for _, word := range []string{"LimitRequestBody", "proxied", "Content-Length", "413", "chunked"} {
		assert.Contains(t, bodyBullet, word, "the body limit bullet names %s", word)
	}
	timeout := apacheSingle(t, https, "ProxyTimeout", ":443 site")
	require.Len(t, timeout, 1)
	assert.Equal(t, strconv.FormatInt(nginxServerSeconds(t, nginxServer, "proxy_read_timeout"), 10), timeout[0],
		"ProxyTimeout equals proxy_read_timeout")

	// (vi) The :80 site redirects 301 to https.
	redirected := false
	for _, l := range apacheLines(httpSite) {
		switch l.directive {
		case "redirect":
			if len(l.fields) == 4 && (strings.EqualFold(l.fields[1], "permanent") || l.fields[1] == "301") &&
				l.fields[2] == "/" && strings.TrimRight(l.fields[3], "/") == "https://crm.example.com" {
				redirected = true
			}
		case "rewriterule":
			if len(l.fields) == 4 && strings.HasPrefix(l.fields[2], "https://crm.example.com") &&
				strings.Contains(l.fields[3], "R=301") {
				redirected = true
			}
		}
	}
	assert.True(t, redirected, "the :80 site redirects 301 to https://crm.example.com")
	assert.Equal(t, []string{"crm.example.com"}, apacheSingle(t, httpSite, "ServerName", ":80 site"))
	assert.Equal(t, []string{"crm.example.com"}, apacheSingle(t, vhostLevel, "ServerName", ":443 site"))

	// (vii) Modules, certificate and the /quote-response access log.
	modulesFound := false
	for _, line := range strings.Split(section, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "a2enmod" {
			require.False(t, modulesFound, "one a2enmod line")
			modulesFound = true
			assert.ElementsMatch(t, []string{"proxy", "proxy_http", "rewrite", "headers", "ssl", "setenvif"}, fields[1:],
				"a2enmod modules")
		}
	}
	assert.True(t, modulesFound, "the section enables the Apache modules")
	assert.True(t, lineWithAll(section, "certbot certonly --apache", "-d crm.example.com"), "certbot certonly --apache")
	nolog := ""
	for _, l := range apacheDirectiveLines(vhostLevel, "setenvif") {
		if len(l.fields) == 4 && l.fields[1] == "Request_URI" && strings.Trim(l.fields[2], `"`) == "^/quote-response$" {
			nolog = l.fields[3]
		}
	}
	require.NotEmpty(t, nolog, `SetEnvIf Request_URI "^/quote-response$" <var>`)
	custom := apacheDirectiveLines(vhostLevel, "customlog")
	require.Len(t, custom, 1, "one CustomLog")
	assert.Equal(t, "env=!"+nolog, custom[0].fields[len(custom[0].fields)-1], "CustomLog skips /quote-response")

	// (viii) Cloudflare: REMOTE_ADDR lines first, the conditional lines after them.
	cfLines := apacheLines(cf)
	indexOf := func(text string) int {
		for i, l := range cfLines {
			if l.text == text {
				return i
			}
		}
		return -1
	}
	assert.Equal(t, 1, len(apacheDirectiveLines(cf, "setenvifnocase")), "one SetEnvIfNoCase")
	assert.True(t, lineWithAll(cf, "SetEnvIfNoCase CF-Connecting-IP", "CF_CLIENT_IP="), "SetEnvIfNoCase CF-Connecting-IP")
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP"} {
		remote := indexOf(`RequestHeader set ` + h + ` "expr=%{REMOTE_ADDR}"`)
		conditional := indexOf(`RequestHeader set ` + h + ` "%{CF_CLIENT_IP}e" env=CF_CLIENT_IP`)
		require.NotEqual(t, -1, remote, "the Cloudflare block repeats the %s REMOTE_ADDR line", h)
		require.NotEqual(t, -1, conditional, "the Cloudflare block sets %s from CF_CLIENT_IP", h)
		assert.Less(t, remote, conditional, "%s: the REMOTE_ADDR line comes before the conditional line", h)
	}
	prose := normalizeDocText(fenceProse(t, section, cfFence.offset))
	for _, word := range []string{"after", "REMOTE_ADDR", "fallback", "firewall", "Cloudflare's address ranges"} {
		assert.Contains(t, prose, word, "the Cloudflare prose says %q", word)
	}

	// (ix) Updating an existing server.
	const updating = "### Updating an existing server"
	_, after, found := strings.Cut(section, "\n"+updating+"\n")
	require.True(t, found, "the section holds %s", updating)
	var trimmed []string
	for _, line := range strings.Split(after, "\n") {
		trimmed = append(trimmed, strings.TrimSpace(line))
	}
	assert.Contains(t, trimmed, "nginx -t && systemctl reload nginx", "nginx reload step")
	assert.Contains(t, trimmed, "apachectl configtest && systemctl reload apache2", "Apache reload step")
	assert.True(t, lineWithAll(after, "/scim/v2/ServiceProviderConfig", "401"), "the SCIM check expects 401")
	assert.True(t, lineWithAll(after, "/health", "revision"), "the /health check names the revision")
}

// githubAnchor is GitHub's slug of a heading: lower case, spaces to dashes,
// punctuation other than dashes and underscores dropped.
func githubAnchor(heading string) string {
	text := strings.ToLower(strings.TrimSpace(strings.TrimLeft(heading, "#")))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestServerProvisioningGuideReverseProxyLinks pins #749 AC-5: README,
// development/local-setup.md and operations/docker.md link the guide's
// reverse-proxy section and name both servers.
func TestServerProvisioningGuideReverseProxyLinks(t *testing.T) {
	const heading = "## Reverse proxy and TLS"
	guideSection(t, readProvisioningGuide(t), heading)
	anchor := githubAnchor(heading)
	require.Equal(t, "reverse-proxy-and-tls", anchor)
	link := regexp.MustCompile(`\]\([^)\s]*systemd\.md#` + regexp.QuoteMeta(anchor) + `\)`)
	for _, rel := range []string{"README.md", "docs/development/local-setup.md", "docs/operations/docker.md"} {
		text := strings.ReplaceAll(readRepositoryFile(t, rel), "\r\n", "\n")
		ok := false
		for _, para := range regexp.MustCompile(`\n[ \t]*\n`).Split(text, -1) {
			if link.MatchString(para) && strings.Contains(para, "nginx") && strings.Contains(para, "Apache") {
				ok = true
				break
			}
		}
		assert.True(t, ok, "%s has a paragraph that links operations/systemd.md#%s and names nginx and Apache", rel, anchor)
	}
}

var provisioningGuideHeadings = []string{
	"## Scope",
	"## Packages",
	"## Service user",
	"## Directories",
	"## Environment file",
	"## Systemd unit",
	"## MySQL database",
	"## Reverse proxy and TLS",
	"## Bootstrap and first deploy",
	"## Schema migrations",
	"## Verify the deploy",
	"## First admin",
	"## Backups",
}

var bootstrapToolLoop = regexp.MustCompile(`(?m)^\s*for tool in ([^;]+);\s*do`)

// bootstrapServerTools returns the tools of the remote loop that checks flock.
func bootstrapServerTools(t *testing.T) []string {
	t.Helper()
	var tools []string
	for _, m := range bootstrapToolLoop.FindAllStringSubmatch(readRepositoryFile(t, "scripts/deploy/bootstrap.sh"), -1) {
		fields := strings.Fields(m[1])
		for _, f := range fields {
			if f == "flock" {
				require.Nil(t, tools, "bootstrap.sh has two tool loops listing flock")
				tools = fields
			}
		}
	}
	require.NotEmpty(t, tools, "bootstrap.sh remote tool loop not found")
	return tools
}

var (
	sqlCreateDatabase = regexp.MustCompile("(?i)^CREATE DATABASE (?:IF NOT EXISTS )?`?([A-Za-z0-9_]+)`?(?:\\s|$)")
	sqlCreateUser     = regexp.MustCompile(`(?i)^CREATE USER (?:IF NOT EXISTS )?'([^']+)'@'([^']+)' IDENTIFIED BY '([^']*)'$`)
	sqlGrant          = regexp.MustCompile(`(?i)^GRANT .+ ON (\S+) TO '([^']+)'@'([^']+)'(?:\s.*)?$`)
)

func checkGuideSQL(t *testing.T, section string, env guideDotenv) {
	t.Helper()
	blocks := fencesWithInfo(parseGuideFences(t, section), "sql")
	require.Len(t, blocks, 1, "MySQL database has exactly one ```sql block")
	body := blocks[0].body
	assert.NotContains(t, body, "*.*", "no global grant")

	dbName, dbUser, dbPassword := env.active["DB_NAME"], env.active["DB_USER"], env.active["DB_PASSWORD"]
	var databases, users, grants int
	var account string
	var grantees []string
	for _, stmt := range strings.Split(body, ";") {
		stmt = strings.Join(strings.Fields(stmt), " ")
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		upper := strings.ToUpper(stmt)
		switch {
		case strings.HasPrefix(upper, "CREATE DATABASE"):
			databases++
			m := sqlCreateDatabase.FindStringSubmatch(stmt)
			if assert.NotNil(t, m, "CREATE DATABASE %q", stmt) {
				assert.Equal(t, dbName, m[1], "CREATE DATABASE names DB_NAME")
			}
			assert.Contains(t, stmt, "utf8mb4")
		case strings.HasPrefix(upper, "CREATE USER"):
			users++
			m := sqlCreateUser.FindStringSubmatch(stmt)
			if assert.NotNil(t, m, "CREATE USER %q", stmt) {
				assert.Equal(t, dbUser, m[1], "CREATE USER names DB_USER")
				assert.Contains(t, []string{"127.0.0.1", "localhost"}, m[2], "account host")
				assert.Equal(t, dbPassword, m[3], "IDENTIFIED BY equals DB_PASSWORD")
				assert.True(t, strings.HasPrefix(m[3], "CHANGE_THIS"))
				account = m[1] + "@" + m[2]
			}
		case strings.HasPrefix(upper, "GRANT"):
			grants++
			m := sqlGrant.FindStringSubmatch(stmt)
			if assert.NotNil(t, m, "GRANT %q", stmt) {
				assert.Equal(t, dbName+".*", strings.ReplaceAll(m[1], "`", ""), "GRANT target")
				grantees = append(grantees, m[2]+"@"+m[3])
			}
		}
	}
	assert.Equal(t, 1, databases, "one CREATE DATABASE")
	assert.Equal(t, 1, users, "one CREATE USER")
	assert.GreaterOrEqual(t, grants, 1, "at least one GRANT")
	for _, g := range grantees {
		assert.Equal(t, account, g, "GRANT grantee is the CREATE USER account")
	}
}

// TestServerProvisioningGuideSteps pins AC-5.
func TestServerProvisioningGuideSteps(t *testing.T) {
	d := parseDeployDefaults(t)
	guide := readProvisioningGuide(t)

	var headings []string
	for _, line := range strings.Split(guide, "\n") {
		if strings.HasPrefix(line, "## ") {
			headings = append(headings, line)
		}
	}
	next := 0
	for _, h := range headings {
		if next < len(provisioningGuideHeadings) && h == provisioningGuideHeadings[next] {
			next++
		}
	}
	require.Equal(t, len(provisioningGuideHeadings), next,
		"the step headings appear in order; missing from %q", provisioningGuideHeadings[min(next, len(provisioningGuideHeadings)-1)])

	section := func(h string) string { return guideSection(t, guide, h) }
	norm := func(h string) string { return normalizeDocText(section(h)) }

	packages := norm("## Packages")
	for _, tool := range append(bootstrapServerTools(t), "nginx", "certbot") {
		assert.Contains(t, packages, tool, "Packages names %s", tool)
	}

	assert.True(t, lineWithAll(section("## Service user"), "useradd --system", "--shell /usr/sbin/nologin", "gophercrm"),
		"Service user has one useradd line")

	dirs := norm("## Directories")
	assert.Contains(t, dirs, "install -d -o root -g root -m 755 "+d.Root)
	assert.Contains(t, dirs, "install -d -o root -g root -m 700 "+path.Dir(d.EnvFile))

	env := parseGuideDotenv(t, guide)
	envSection := norm("## Environment file")
	assert.Contains(t, envSection, normalizeDocText(env.body), "Environment file holds the dotenv block")
	assert.Contains(t, envSection, "chown root:root "+d.EnvFile)
	assert.Contains(t, envSection, "chmod 600 "+d.EnvFile)
	assert.Contains(t, envSection, "TRUSTED_PROXIES")

	units := fencesContaining(fencesWithInfo(parseGuideFences(t, guide), "ini"), "[Service]")
	require.Len(t, units, 1)
	unitSection := norm("## Systemd unit")
	assert.Contains(t, unitSection, normalizeDocText(units[0].body), "Systemd unit holds the ini block")
	assert.Contains(t, unitSection, "systemctl daemon-reload")
	assert.Contains(t, unitSection, "systemctl enable "+d.Service)

	checkGuideSQL(t, section("## MySQL database"), env)

	proxyFences := parseGuideFences(t, section("## Reverse proxy and TLS"))
	assert.Len(t, fencesWithInfo(proxyFences, "nginx"), 2, "Reverse proxy and TLS holds both nginx sites")
	assert.Len(t, fencesContaining(proxyFences, "certbot certonly --webroot"), 1, "Reverse proxy and TLS holds the certbot block")

	bootstrap := norm("## Bootstrap and first deploy")
	assert.Contains(t, bootstrap, "scripts/deploy/bootstrap.sh")
	assert.Contains(t, bootstrap, "not complete until", "the local health wait does not finish the deploy (#795)")

	verify := section("## Verify the deploy")
	var curlLines []string
	for _, line := range strings.Split(verify, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "curl -fsS https://crm.example.com/health") {
			curlLines = append(curlLines, line)
		}
	}
	require.Len(t, curlLines, 1, "Verify the deploy holds one external curl of /health")
	assert.Contains(t, curlLines[0], "/health")
	assert.Contains(t, verify, "revision", "Verify the deploy names the revision the JSON must carry")
	assert.Contains(t, normalizeDocText(verify), "complete", "Verify the deploy says when the deploy is complete")

	admin := section("## First admin")
	assert.Contains(t, admin, "https://crm.example.com/login", "First admin ends with the sign-in")
	assert.True(t, lineWithAll(admin, "systemd-run", "--pty", "--uid=gophercrm",
		"-p EnvironmentFile="+d.EnvFile, d.Root+"/bin/create-admin"), "First admin has one systemd-run line")
	assert.NotContains(t, admin, "--password")
	assert.NotContains(t, admin, "--non-interactive")

	backups := norm("## Backups")
	assert.Contains(t, backups, "mysqldump")
	assert.Contains(t, backups, d.Root+"/backups")

	assert.NotContains(t, guide, "enable --now")
}

// TestServerProvisioningGuideSchema pins AC-6.
func TestServerProvisioningGuideSchema(t *testing.T) {
	assertGuideSchemaSection(t)
}

// assertGuideSchemaSection holds the #231 assertions on the Schema migrations
// section, shared with the #305 migration-command pin.
func assertGuideSchemaSection(t *testing.T) {
	t.Helper()
	d := parseDeployDefaults(t)

	build := readRepositoryFile(t, "scripts/release/build.sh")
	require.Contains(t, build, "cp -R migrations release/migrations", "build.sh ships migrations/")
	targets := regexp.MustCompile(`(?m)^\s*for target in ([^;]+);\s*do`).FindStringSubmatch(build)
	require.NotNil(t, targets, "build.sh target loop")
	require.Contains(t, strings.Fields(targets[1]), "migrate", "build.sh builds migrate")

	receive := readRepositoryFile(t, "scripts/deploy/receive-release.sh")
	require.Regexp(t, regexp.MustCompile(`(?m)^CURRENT=\$ROOT/current$`), receive)
	require.NotContains(t, strings.ToLower(receive), "migrat", "receive-release runs no migration")

	guide := readProvisioningGuide(t)
	raw := guideSection(t, guide, "## Schema migrations")
	schema := normalizeDocText(raw)
	for _, phrase := range []string{
		"MigrateDatabase",
		d.Root + "/current/migrations",
		"bin/migrate",
		"golang-migrate",
		"receive-release does not run",
	} {
		assert.Contains(t, schema, phrase, "Schema migrations")
	}
	assert.Regexp(t, regexp.MustCompile(`\]\(upgrades\.md[)#]`), raw, "links upgrades.md (#795)")

	for _, wrong := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)(tarball|release)[^.\n]*(does not|doesn't|do not|no longer)[^.\n]*(ship|contain|include)[^.\n]*migrations`),
		regexp.MustCompile(`(?i)do not run[^.\n]*bin/migrate`),
	} {
		assert.NotRegexp(t, wrong, guide)
	}
}

// TestServerProvisioningGuideSQLiteUnsupported pins AC-7.
func TestServerProvisioningGuideSQLiteUnsupported(t *testing.T) {
	guide := readProvisioningGuide(t)
	text := normalizeDocText(guide)
	assert.Contains(t, text, "SQLite is not supported on this path")
	assert.Contains(t, text, "mysqldump")
	assert.Regexp(t, regexp.MustCompile(`\]\(docker\.md[)#]`), guide, "links docker.md")
	assert.NotContains(t, guide, "VACUUM INTO")
	assert.NotContains(t, guide, ".sqlite.gz")
}

// subsectionBetween returns the lines from the first line starting with start
// to the next line starting with stop.
// sinceReleaseStop ends the changelog text that tests read for entries: these
// changes came after 1.2.0, so they sit under Unreleased or a newer version.
const sinceReleaseStop = "## [1.2.0]"

func subsectionBetween(t *testing.T, doc, start, stop string) string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	from := -1
	for i, line := range lines {
		if strings.HasPrefix(line, start) {
			from = i
			break
		}
	}
	require.NotEqual(t, -1, from, "line starting with %q not found", start)
	to := len(lines)
	for i := from + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], stop) {
			to = i
			break
		}
	}
	return strings.Join(lines[from:to], "\n")
}

// TestServerProvisioningGuideLinked pins AC-8.
func TestServerProvisioningGuideLinked(t *testing.T) {
	forking := subsectionBetween(t, readRepositoryFile(t, "docs/development/forking.md"), "### 5.2", "### ")
	assert.Regexp(t, regexp.MustCompile(`\]\(\.\./operations/systemd\.md[)#]`), forking, "forking.md §5.2 links the guide")
	assert.NotContains(t, normalizeDocText(forking), "None of these ship in the repository (#231)")
	assert.Contains(t, normalizeDocText(forking), "not ready for SQLite as shipped")

	for _, rel := range []string{"README.md", "docs/development/local-setup.md"} {
		assert.Contains(t, readRepositoryFile(t, rel), "operations/systemd.md", rel)
	}

	unreleased := subsectionBetween(t, readRepositoryFile(t, "CHANGELOG.md"), "## [Unreleased]", sinceReleaseStop)
	assert.Contains(t, unreleased, "#231")
	assert.Contains(t, unreleased, serverProvisioningChangelogName)
}

// The tests below pin the #305 decisions (founder decisions of 13 Nov 2026):
// SQLite stays unsupported on the tarball path, and the SQL migration is a
// documented manual step after the deploy.

// TestServerProvisioningGuideSQLiteFounderDecision pins #305 AC-1.
func TestServerProvisioningGuideSQLiteFounderDecision(t *testing.T) {
	scope := normalizeDocText(guideSection(t, readProvisioningGuide(t), "## Scope"))
	for _, phrase := range []string{
		"SQLite is not supported on this path",
		"founder decision",
		"no SQLite backup",
	} {
		assert.Contains(t, scope, phrase, "Scope records the SQLite decision")
	}

	receive := readRepositoryFile(t, "scripts/deploy/receive-release.sh")
	assert.Contains(t, receive, "mysqldump", "receive-release keeps its mysqldump backup")
	assert.NotContains(t, strings.ToLower(receive), "sqlite", "receive-release has no SQLite branch")
}

// fieldAfter returns the field that follows flag on line, or "".
func fieldAfter(line, flag string) string {
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == flag {
			return fields[i+1]
		}
	}
	return ""
}

// TestServerProvisioningGuideMigrationCommand pins #305 AC-2.
func TestServerProvisioningGuideMigrationCommand(t *testing.T) {
	d := parseDeployDefaults(t)

	migrateSource := readRepositoryFile(t, "cmd/migrate/main.go")
	for _, decl := range []string{`flag.String("command"`, `flag.String("dir"`, `flag.Uint("version"`} {
		require.Contains(t, migrateSource, decl, "cmd/migrate declares %s", decl)
	}

	raw := guideSection(t, readProvisioningGuide(t), "## Schema migrations")
	blocks := fencesContaining(parseGuideFences(t, raw), "bin/migrate")
	require.Len(t, blocks, 1, "Schema migrations holds exactly one block running bin/migrate")

	needles := []string{
		"systemd-run",
		"--pty",
		"--uid=gophercrm",
		"--gid=gophercrm",
		"-p EnvironmentFile=" + d.EnvFile,
		d.Root + "/current/bin/migrate",
		"-command up",
		"-dir " + d.Root + "/current/migrations",
	}
	var matches []string
	for _, line := range strings.Split(blocks[0].body, "\n") {
		if lineWithAll(line, needles...) {
			matches = append(matches, line)
		}
	}
	require.Len(t, matches, 1, "exactly one line holds the whole migrate command")
	line := matches[0]
	dir := fieldAfter(line, "-dir")
	assert.True(t, strings.HasPrefix(dir, "/"), "-dir %q is absolute", dir)
	assert.Equal(t, d.Root+"/current/migrations", dir, "-dir names the shipped migrations")
	assert.NotContains(t, line, d.Root+"/bin/migrate", "bin/migrate is not linked into ROOT/bin")

	schema := normalizeDocText(raw)
	for _, phrase := range []string{
		"founder decision",
		"is live",
		"rolled back",
		"backup",
	} {
		assert.Contains(t, schema, phrase, "Schema migrations")
	}

	// The dirty-state recovery moved to the upgrade page (#795).
	upgrading := normalizeDocText(readRepositoryFile(t, "docs/operations/upgrades.md"))
	for _, phrase := range []string{
		"dirty",
		"-command force",
		"last version that applied cleanly",
	} {
		assert.Contains(t, upgrading, phrase, "docs/operations/upgrades.md")
	}

	assertGuideSchemaSection(t)
}

// changelogEntry returns the bullet of section whose first line holds marker,
// up to the next bullet or heading.
func changelogEntry(t *testing.T, section, marker string) string {
	t.Helper()
	lines := strings.Split(section, "\n")
	from := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, marker) {
			require.Equal(t, -1, from, "two entries mention %s on their first line", marker)
			from = i
		}
	}
	require.NotEqual(t, -1, from, "no entry mentions %s on its first line", marker)
	to := len(lines)
	for i := from + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- ") || strings.HasPrefix(lines[i], "#") {
			to = i
			break
		}
	}
	return strings.Join(lines[from:to], "\n")
}

// TestServerProvisioningGuideTarballDecisionsChangelog pins #305 AC-3.
func TestServerProvisioningGuideTarballDecisionsChangelog(t *testing.T) {
	unreleased := subsectionBetween(t, readRepositoryFile(t, "CHANGELOG.md"), "## [Unreleased]", sinceReleaseStop)
	assert.Contains(t, unreleased, "#305")
	assert.Contains(t, unreleased, serverProvisioningChangelogName)
	assert.NotContains(t, unreleased, "scripts/deploy/samples", "item 1 of #305 is not delivered")
	assert.Contains(t, unreleased, "nothing under `scripts/deploy` changes", "the #231 entry stays")

	entry := changelogEntry(t, unreleased, "#305")
	assert.Contains(t, entry, "SQLite")
	assert.Contains(t, entry, "bin/migrate")

	assert.Contains(t, normalizeDocText(readProvisioningGuide(t)), "Nothing in this guide ships as a file")
	forking := subsectionBetween(t, readRepositoryFile(t, "docs/development/forking.md"), "### 5.2", "### ")
	assert.Contains(t, normalizeDocText(forking), "no sample file ships under")
}
