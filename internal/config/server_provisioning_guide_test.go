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

// The tests in this file pin docs/SERVER_PROVISIONING.md (#231) to the deploy
// scripts it describes (scripts/deploy/receive-release.sh, bootstrap.sh,
// scripts/release/build.sh), to .env.example and to config.Load. Every value
// the guide must repeat is parsed from those files at test time, so a change
// to a script default fails here until the guide follows.

const serverProvisioningGuideFile = "docs/SERVER_PROVISIONING.md"

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
	for _, opener := range []string{"location /api/v1/ {", "location = /health {"} {
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

	assert.Contains(t, norm("## Bootstrap and first deploy"), "scripts/deploy/bootstrap.sh")

	admin := section("## First admin")
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
	assert.Regexp(t, regexp.MustCompile(`\]\(FORKING\.md#74-migration-versions\)`), raw, "links FORKING §7.4")

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
	assert.Regexp(t, regexp.MustCompile(`\]\(DOCKER\.md[)#]`), guide, "links DOCKER.md")
	assert.NotContains(t, guide, "VACUUM INTO")
	assert.NotContains(t, guide, ".sqlite.gz")
}

// subsectionBetween returns the lines from the first line starting with start
// to the next line starting with stop.
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
	forking := subsectionBetween(t, readRepositoryFile(t, "docs/FORKING.md"), "### 5.2", "### ")
	assert.Regexp(t, regexp.MustCompile(`\]\(SERVER_PROVISIONING\.md[)#]`), forking, "FORKING §5.2 links the guide")
	assert.NotContains(t, normalizeDocText(forking), "None of these ship in the repository (#231)")
	assert.Contains(t, normalizeDocText(forking), "not ready for SQLite as shipped")

	for _, rel := range []string{"README.md", "docs/SETUP.md"} {
		assert.Contains(t, readRepositoryFile(t, rel), "SERVER_PROVISIONING.md", rel)
	}

	unreleased := subsectionBetween(t, readRepositoryFile(t, "CHANGELOG.md"), "## [Unreleased]", "## [")
	assert.Contains(t, unreleased, "#231")
	assert.Contains(t, unreleased, "SERVER_PROVISIONING.md")
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
		"dirty",
		"-command force",
		"last version that applied cleanly",
		"backup",
	} {
		assert.Contains(t, schema, phrase, "Schema migrations")
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
	unreleased := subsectionBetween(t, readRepositoryFile(t, "CHANGELOG.md"), "## [Unreleased]", "## [")
	assert.Contains(t, unreleased, "#305")
	assert.Contains(t, unreleased, "SERVER_PROVISIONING.md")
	assert.NotContains(t, unreleased, "scripts/deploy/samples", "item 1 of #305 is not delivered")
	assert.Contains(t, unreleased, "nothing under `scripts/deploy` changes", "the #231 entry stays")

	entry := changelogEntry(t, unreleased, "#305")
	assert.Contains(t, entry, "SQLite")
	assert.Contains(t, entry, "bin/migrate")

	assert.Contains(t, normalizeDocText(readProvisioningGuide(t)), "Nothing in this guide ships as a file")
	forking := subsectionBetween(t, readRepositoryFile(t, "docs/FORKING.md"), "### 5.2", "### ")
	assert.Contains(t, normalizeDocText(forking), "no sample file ships under")
}
