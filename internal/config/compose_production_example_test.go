package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin docker-compose.production.example.yml (#222): its
// shape, the mapping of every key it forwards onto config.Load, and the
// documentation that explains it.

const composeProductionExampleFile = "docker-compose.production.example.yml"

// composeExampleRequiredKeys use ${NAME:?message}; every other forwarded key
// uses ${NAME:-}.
var composeExampleRequiredKeys = map[string]bool{
	"APP_BASE_URL":         true,
	"PUBLIC_BASE_URL":      true,
	"CORS_ALLOWED_ORIGINS": true,
}

var composeExampleForwardedKeys = []string{
	"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM",
	"APP_BASE_URL", "PUBLIC_BASE_URL", "CORS_ALLOWED_ORIGINS",
	"RECAPTCHA_SITE_KEY", "RECAPTCHA_SECRET_KEY", "RECAPTCHA_MIN_SCORE",
	"AUTOMATION_ENABLED", "AUTOMATION_POLL_INTERVAL_SECONDS",
	"AEO_SCHEDULE_ENABLED", "AEO_SCHEDULE_HOUR",
}

// composeNode is one key of the restricted YAML shape the example uses:
// a scalar value (tags kept verbatim), a block mapping (children) or a block
// sequence of scalars (items).
type composeNode struct {
	key      string
	value    string
	children []*composeNode
	items    []string
	line     int
}

func (n *composeNode) child(key string) *composeNode {
	for _, c := range n.children {
		if c.key == key {
			return c
		}
	}
	return nil
}

func (n *composeNode) childKeys() []string {
	keys := make([]string, 0, len(n.children))
	for _, c := range n.children {
		keys = append(keys, c.key)
	}
	return keys
}

var composeKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// parseComposeExample reads the restricted shape: two-space indentation,
// `key: value` / `key:` lines and `- item` block sequences. Full-line comments
// and blank lines are ignored. Anything else (tabs, odd indentation, flow
// mappings, anchors, multi-line scalars, duplicate keys) is an error, so a
// file this reader cannot model fails the tests instead of being misread.
func parseComposeExample(t *testing.T, data string) *composeNode {
	t.Helper()
	type frame struct {
		indent int
		node   *composeNode
	}
	root := &composeNode{}
	stack := []frame{{indent: -1, node: root}}

	scanner := bufio.NewScanner(strings.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimRight(scanner.Text(), " \r")
		if strings.Contains(raw, "\t") {
			t.Fatalf("%s:%d: tab character", composeProductionExampleFile, lineNo)
		}
		content := strings.TrimLeft(raw, " ")
		if content == "" || strings.HasPrefix(content, "#") {
			continue
		}
		indent := len(raw) - len(content)
		if indent%2 != 0 {
			t.Fatalf("%s:%d: indentation %d is not a multiple of two", composeProductionExampleFile, lineNo, indent)
		}
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].node

		if strings.HasPrefix(content, "- ") {
			if parent == root || parent.value != "" && !strings.HasPrefix(parent.value, "!") || len(parent.children) > 0 {
				t.Fatalf("%s:%d: sequence item outside a block sequence", composeProductionExampleFile, lineNo)
			}
			item := strings.TrimSpace(strings.TrimPrefix(content, "- "))
			if strings.ContainsAny(item, "&*{[|>") && !strings.Contains(item, "${") {
				t.Fatalf("%s:%d: unsupported sequence item %q", composeProductionExampleFile, lineNo, item)
			}
			parent.items = append(parent.items, item)
			continue
		}

		var key, value string
		if strings.HasSuffix(content, ":") {
			key = strings.TrimSuffix(content, ":")
		} else {
			idx := strings.Index(content, ": ")
			if idx < 0 {
				t.Fatalf("%s:%d: expected `key: value`, got %q", composeProductionExampleFile, lineNo, content)
			}
			key, value = content[:idx], strings.TrimSpace(content[idx+2:])
		}
		if !composeKeyPattern.MatchString(key) {
			t.Fatalf("%s:%d: unsupported key %q", composeProductionExampleFile, lineNo, key)
		}
		if strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") || strings.HasPrefix(value, "{") ||
			strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") || strings.HasPrefix(value, "<<") {
			t.Fatalf("%s:%d: unsupported value %q", composeProductionExampleFile, lineNo, value)
		}
		if len(parent.items) > 0 || (parent != root && parent.value != "") {
			t.Fatalf("%s:%d: mapping key under a scalar or sequence", composeProductionExampleFile, lineNo)
		}
		if parent.child(key) != nil {
			t.Fatalf("%s:%d: duplicate key %q", composeProductionExampleFile, lineNo, key)
		}
		node := &composeNode{key: key, value: value, line: lineNo}
		parent.children = append(parent.children, node)
		stack = append(stack, frame{indent: indent, node: node})
	}
	require.NoError(t, scanner.Err())
	return root
}

// repositoryRoot walks up from the package directory to the directory that
// holds go.mod.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func readRepositoryFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), rel))
	require.NoError(t, err, "read %s", rel)
	return string(data)
}

func loadComposeExample(t *testing.T) *composeNode {
	t.Helper()
	return parseComposeExample(t, readRepositoryFile(t, composeProductionExampleFile))
}

// composeService returns services.<name>, failing when it is absent.
func composeService(t *testing.T, root *composeNode, name string) *composeNode {
	t.Helper()
	services := root.child("services")
	require.NotNil(t, services, "top-level services key")
	svc := services.child(name)
	require.NotNil(t, svc, "services.%s", name)
	return svc
}

// composeBackendEnvironment returns the backend environment as key -> raw value.
func composeBackendEnvironment(t *testing.T, root *composeNode) (map[string]string, []string) {
	t.Helper()
	env := composeService(t, root, "backend").child("environment")
	require.NotNil(t, env, "services.backend.environment")
	require.Empty(t, env.value, "services.backend.environment must be a block mapping")
	require.Empty(t, env.items, "services.backend.environment must be a block mapping, not a list")
	values := make(map[string]string, len(env.children))
	for _, c := range env.children {
		require.Empty(t, c.children, "environment %s holds a nested mapping", c.key)
		require.Empty(t, c.items, "environment %s holds a sequence", c.key)
		values[c.key] = c.value
	}
	return values, env.childKeys()
}

// unquoteComposeScalar strips one pair of double quotes. Escapes are refused,
// because none of the example's values needs one.
func unquoteComposeScalar(t *testing.T, raw string) string {
	t.Helper()
	if len(raw) >= 2 && strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		inner := raw[1 : len(raw)-1]
		require.NotContains(t, inner, `\`, "escaped scalar %s", raw)
		require.NotContains(t, inner, `"`, "embedded quote in %s", raw)
		return inner
	}
	require.NotContains(t, raw, `"`, "unbalanced quotes in %s", raw)
	require.NotContains(t, raw, `'`, "single-quoted scalar %s", raw)
	return raw
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestComposeProductionExampleBackendEnvironment pins AC-1.
func TestComposeProductionExampleBackendEnvironment(t *testing.T) {
	root := loadComposeExample(t)

	assert.Equal(t, []string{"services"}, root.childKeys(), "the only top-level key is services")
	services := root.child("services")
	require.NotNil(t, services)
	assert.Empty(t, services.value)
	assert.Equal(t, []string{"backend", "ui"}, sortedCopy(services.childKeys()), "services holds exactly backend and ui")

	values, keys := composeBackendEnvironment(t, root)
	assert.Equal(t, sortedCopy(composeExampleForwardedKeys), sortedCopy(keys),
		"services.backend.environment holds exactly the 15 forwarded keys")

	for _, key := range keys {
		value := unquoteComposeScalar(t, values[key])
		if composeExampleRequiredKeys[key] {
			re := regexp.MustCompile(`^\$\{` + regexp.QuoteMeta(key) + `:\?([^${}]+)\}$`)
			m := re.FindStringSubmatch(value)
			if assert.NotNil(t, m, "%s must be ${%s:?<message>}, got %q", key, key, value) {
				assert.NotEmpty(t, strings.TrimSpace(m[1]), "%s needs a non-empty :? message", key)
			}
		} else {
			assert.Equal(t, fmt.Sprintf("${%s:-}", key), value, "%s must be exactly ${%s:-}", key, key)
		}
	}
}

// TestComposeProductionExampleNoStackSettings pins AC-2.
func TestComposeProductionExampleNoStackSettings(t *testing.T) {
	root := loadComposeExample(t)

	for _, key := range []string{"name", "networks", "volumes"} {
		assert.Nil(t, root.child(key), "no top-level %s", key)
	}
	services := root.child("services")
	require.NotNil(t, services)
	assert.Nil(t, services.child("db"), "no db service")

	for _, name := range []string{"backend", "ui"} {
		svc := composeService(t, root, name)
		for _, key := range []string{"build", "image", "volumes", "networks", "depends_on", "healthcheck"} {
			assert.Nil(t, svc.child(key), "services.%s must not set %s", name, key)
		}
	}

	values, _ := composeBackendEnvironment(t, root)
	for _, key := range []string{
		"TRUSTED_PROXIES", "JWT_SECRET", "API_KEY_SECRET",
		"DB_DRIVER", "DB_PATH", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD",
		"SERVER_PORT", "SERVER_MODE", "API_PREFIX", "LOG_LEVEL", "LOG_FORMAT", "FORM_RETENTION_DAYS",
	} {
		_, present := values[key]
		assert.False(t, present, "the example must not set %s", key)
	}
}

// TestComposeProductionExamplePorts pins AC-5.
func TestComposeProductionExamplePorts(t *testing.T) {
	root := loadComposeExample(t)

	backendPorts := composeService(t, root, "backend").child("ports")
	require.NotNil(t, backendPorts, "services.backend.ports")
	assert.Equal(t, "!reset []", backendPorts.value)
	assert.Empty(t, backendPorts.items)
	assert.Empty(t, backendPorts.children)

	uiPorts := composeService(t, root, "ui").child("ports")
	require.NotNil(t, uiPorts, "services.ui.ports")
	assert.Equal(t, "!override", uiPorts.value)
	assert.Empty(t, uiPorts.children)
	require.Len(t, uiPorts.items, 1, "services.ui.ports holds exactly one entry")
	assert.Equal(t, "127.0.0.1:${UI_PORT:-3000}:80", unquoteComposeScalar(t, uiPorts.items[0]))
}

// prepareComposeExampleLoad isolates Load from settings outside the example.
func prepareComposeExampleLoad(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", validSecret())
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_PATH", "")
}

// TestComposeProductionExampleValuesReachConfig pins AC-3.
func TestComposeProductionExampleValuesReachConfig(t *testing.T) {
	type expectation struct {
		sample string
		got    func(*Config) any
		want   any
	}
	table := map[string]expectation{
		"SMTP_HOST":     {"smtp.example.test", func(c *Config) any { return c.SMTP.Host }, "smtp.example.test"},
		"SMTP_PORT":     {"2525", func(c *Config) any { return c.SMTP.Port }, 2525},
		"SMTP_USER":     {"mailer-user", func(c *Config) any { return c.SMTP.User }, "mailer-user"},
		"SMTP_PASSWORD": {"mailer-password", func(c *Config) any { return c.SMTP.Password }, "mailer-password"},
		"SMTP_FROM":     {"crm@example.test", func(c *Config) any { return c.SMTP.From }, "crm@example.test"},
		"APP_BASE_URL": {"https://crm.example.test/",
			func(c *Config) any { return c.App.BaseURL }, "https://crm.example.test"},
		"PUBLIC_BASE_URL": {"https://forms.example.test/",
			func(c *Config) any { return c.Forms.PublicBaseURL }, "https://forms.example.test"},
		"CORS_ALLOWED_ORIGINS": {"https://crm.example.test,https://www.example.test",
			func(c *Config) any { return c.Server.CORSExtraOrigins },
			[]string{"https://crm.example.test", "https://www.example.test"}},
		"RECAPTCHA_SITE_KEY": {"site-key-sample",
			func(c *Config) any { return c.Forms.RecaptchaSiteKey }, "site-key-sample"},
		"RECAPTCHA_SECRET_KEY": {"secret-key-sample",
			func(c *Config) any { return c.Forms.RecaptchaSecret }, "secret-key-sample"},
		"RECAPTCHA_MIN_SCORE": {"0.7",
			func(c *Config) any { return c.Forms.RecaptchaMinScore }, 0.7},
		"AUTOMATION_ENABLED": {"false",
			func(c *Config) any { return c.Automation.Enabled }, false},
		"AUTOMATION_POLL_INTERVAL_SECONDS": {"45",
			func(c *Config) any { return c.Automation.PollIntervalSeconds }, 45},
		"AEO_SCHEDULE_ENABLED": {"0",
			func(c *Config) any { return c.AEO.ScheduleEnabled }, false},
		"AEO_SCHEDULE_HOUR": {"13",
			func(c *Config) any { return c.AEO.ScheduleHour }, 13},
	}

	_, keys := composeBackendEnvironment(t, loadComposeExample(t))
	require.NotEmpty(t, keys)
	inFile := make(map[string]bool, len(keys))
	for _, key := range keys {
		inFile[key] = true
		if _, ok := table[key]; !ok {
			t.Fatalf("%s forwards %s, which has no expectation in this table", composeProductionExampleFile, key)
		}
	}
	for key := range table {
		assert.True(t, inFile[key], "table key %s is not forwarded by %s", key, composeProductionExampleFile)
	}

	samples := make(map[string]bool, len(keys))
	prepareComposeExampleLoad(t)
	for _, key := range keys {
		sample := table[key].sample
		require.False(t, samples[sample], "sample value %q is not distinct", sample)
		samples[sample] = true
		t.Setenv(key, sample)
	}

	cfg, err := Load()
	require.NoError(t, err)
	for _, key := range keys {
		exp := table[key]
		assert.Equal(t, exp.want, exp.got(cfg), "%s=%q", key, exp.sample)
	}
}

// TestComposeProductionExampleBlankValuesKeepDefaults pins AC-4.
func TestComposeProductionExampleBlankValuesKeepDefaults(t *testing.T) {
	values, keys := composeBackendEnvironment(t, loadComposeExample(t))

	var optional []string
	for _, key := range keys {
		if unquoteComposeScalar(t, values[key]) == fmt.Sprintf("${%s:-}", key) {
			optional = append(optional, key)
		}
	}
	require.NotEmpty(t, optional)

	prepareComposeExampleLoad(t)
	t.Setenv("APP_BASE_URL", "https://crm.example.test")
	t.Setenv("PUBLIC_BASE_URL", "https://crm.example.test")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://crm.example.test")

	// Blank: what Compose passes for an unset host variable.
	for _, key := range optional {
		t.Setenv(key, "")
	}
	blank, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "", blank.SMTP.Host)
	assert.Equal(t, 587, blank.SMTP.Port)
	assert.Equal(t, "no-reply@localhost", blank.SMTP.From)
	assert.Equal(t, 0.5, blank.Forms.RecaptchaMinScore)
	assert.True(t, blank.Automation.Enabled)
	assert.Equal(t, AutomationPollIntervalDefault, blank.Automation.PollIntervalSeconds)
	assert.True(t, blank.AEO.ScheduleEnabled)
	assert.Equal(t, 6, blank.AEO.ScheduleHour)

	// Unset: the same names absent from the environment.
	for _, key := range optional {
		require.NoError(t, os.Unsetenv(key)) // t.Setenv above restores the original value
	}
	unset, err := Load()
	require.NoError(t, err)

	assert.Equal(t, unset.SMTP, blank.SMTP)
	assert.Equal(t, unset.Forms.RecaptchaSiteKey, blank.Forms.RecaptchaSiteKey)
	assert.Equal(t, unset.Forms.RecaptchaSecret, blank.Forms.RecaptchaSecret)
	assert.Equal(t, unset.Forms.RecaptchaMinScore, blank.Forms.RecaptchaMinScore)
	assert.Equal(t, unset.Automation, blank.Automation)
	assert.Equal(t, unset.AEO.ScheduleEnabled, blank.AEO.ScheduleEnabled)
	assert.Equal(t, unset.AEO.ScheduleHour, blank.AEO.ScheduleHour)
}

// normalizeDocText removes backticks and collapses whitespace runs.
func normalizeDocText(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
}

// dockerProductionOverrideSubsection returns the lines from the heading to the
// next line starting with # outside a ``` fence, or to the end of the file.
func dockerProductionOverrideSubsection(t *testing.T, doc string) string {
	t.Helper()
	const heading = "### Production override example"
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if line == heading {
			require.Equal(t, -1, start, "docs/DOCKER.md has the heading %q twice", heading)
			start = i
		}
	}
	require.NotEqual(t, -1, start, "docs/DOCKER.md lacks the heading %q", heading)

	inFence := false
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(lines[i], "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestComposeProductionExampleDocumented pins AC-6.
func TestComposeProductionExampleDocumented(t *testing.T) {
	section := normalizeDocText(dockerProductionOverrideSubsection(t, readRepositoryFile(t, "docs/DOCKER.md")))
	for _, phrase := range []string{
		"docker-compose.production.example.yml",
		"docker compose -f docker-compose.yml -f docker-compose.production.example.yml config -q",
		"docker compose -f docker-compose.sqlite.yml -f docker-compose.production.example.yml config -q",
		"2.24.4",
		"/data/.env",
		"PUBLIC_BASE_URL is the public origin of the UI proxy",
		".env.example sets PUBLIC_BASE_URL",
		"keep every value as a ${...} interpolation",
	} {
		assert.Contains(t, section, phrase, "docs/DOCKER.md Production override example")
	}

	docs := []string{"docs/DOCKER.md", "docs/FORKING.md", "docs/USE_CASES.md", "CHANGELOG.md"}
	for _, rel := range docs[1:] {
		assert.Contains(t, normalizeDocText(readRepositoryFile(t, rel)), composeProductionExampleFile, rel)
	}
	for _, rel := range docs {
		text := normalizeDocText(readRepositoryFile(t, rel))
		for _, stale := range []string{
			"docker-compose.production.yml",
			"production override is tracked in #222",
			"A shipped production override is #222",
			"until #222 ships one",
			"build argument is tracked in #222",
			"build argument (#222)",
		} {
			assert.NotContains(t, text, stale, rel)
		}
	}
}
