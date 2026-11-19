package config

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dockerfileRuntimeStage returns the lines of the last stage of the backend
// Dockerfile, with line continuations joined, comments and blank lines
// dropped.
func dockerfileRuntimeStage(t *testing.T) []string {
	t.Helper()
	text := strings.ReplaceAll(readRepositoryFile(t, "Dockerfile"), "\\\n", " ")
	var stage []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			stage = nil
		}
		stage = append(stage, line)
	}
	require.NotEmpty(t, stage, "the Dockerfile has a runtime stage")
	return stage
}

// composeBackendBlock returns the text of services.backend of a compose file.
func composeBackendBlock(t *testing.T, file string) string {
	t.Helper()
	data := readRepositoryFile(t, file)
	start := strings.Index(data, "\n  backend:\n")
	require.GreaterOrEqual(t, start, 0, "%s has a backend service", file)
	rest := data[start+1:]
	// The block ends at the next key with the same or a smaller indent.
	end := regexp.MustCompile(`\n {0,2}[A-Za-z_-]+:`).FindStringIndex(rest[len("  backend:\n"):])
	if end != nil {
		rest = rest[:len("  backend:\n")+end[0]]
	}
	return rest
}

// dockerfileAptPackages returns the names of the packages the stage installs
// with apt-get install: options are skipped, quotes dropped and a =version
// suffix is cut off. The second result holds the fields as written.
func dockerfileAptPackages(stage []string) (names []string, fields []string) {
	for _, line := range stage {
		if !strings.HasPrefix(line, "RUN ") {
			continue
		}
		for _, command := range strings.Split(strings.TrimPrefix(line, "RUN "), "&&") {
			words := strings.Fields(command)
			if len(words) < 2 || words[0] != "apt-get" {
				continue
			}
			install := -1
			for i, word := range words[1:] {
				if word == "install" {
					install = i + 1
					break
				}
				if !strings.HasPrefix(word, "-") {
					break
				}
			}
			if install < 0 {
				continue
			}
			for _, word := range words[install+1:] {
				if strings.HasPrefix(word, "-") {
					continue
				}
				word = strings.Trim(word, `"'`)
				fields = append(fields, word)
				name, _, _ := strings.Cut(word, "=")
				names = append(names, name)
			}
		}
	}
	return names, fields
}

// headlessShellName matches the browser by either spelling of its name: the
// package and the Debian binary use a hyphen, upstream builds an underscore.
var headlessShellName = regexp.MustCompile(`headless[-_]shell`)

// TestDockerfileShipsHeadlessShell pins what quote PDF output (#384, #393)
// needs from the deployment files: the runtime image is Debian, installs
// chrome-headless-shell at one pinned version instead of the full Chromium,
// a font, an init and the command of the compose health checks, and points
// QUOTE_PDF_CHROME_PATH at the browser or at a launcher that execs it; the Go
// code is not the place the sandbox is switched off; and the two development
// compose files, whose APP_BASE_URL the backend container cannot reach,
// switch PDF output off.
func TestDockerfileShipsHeadlessShell(t *testing.T) {
	stage := dockerfileRuntimeStage(t)
	require.True(t, strings.HasPrefix(stage[0], "FROM debian:"), "the runtime stage is debian: %s", stage[0])

	installed, fields := dockerfileAptPackages(stage)
	require.NotEmpty(t, installed, "the runtime stage installs packages with apt-get install")
	for _, pkg := range []string{"chromium", "chromium-browser"} {
		assert.NotContains(t, installed, pkg, "the runtime stage does not install the full browser")
	}

	// One literal for the browser version, used by the line that brings it.
	var version string
	versionLines := 0
	for _, line := range stage {
		if match := regexp.MustCompile(`^ARG\s+HEADLESS_SHELL_VERSION=(.*)$`).FindStringSubmatch(line); match != nil {
			versionLines++
			version = strings.Trim(strings.TrimSpace(match[1]), `"`)
		}
	}
	require.Equal(t, 1, versionLines, "the runtime stage declares ARG HEADLESS_SHELL_VERSION=<version> once")
	assert.Regexp(t, `^\d+\.\d+\.\d+\.\d+$`, version, "the pinned browser version is a.b.c.d")
	usesVersion := regexp.MustCompile(`\$\{HEADLESS_SHELL_VERSION\}|\$HEADLESS_SHELL_VERSION\b`)
	brought := false
	for _, field := range fields {
		if headlessShellName.MatchString(field) && usesVersion.MatchString(field) {
			brought = true
		}
	}
	for _, line := range stage {
		if strings.HasPrefix(line, "COPY ") && headlessShellName.MatchString(line) && usesVersion.MatchString(line) {
			brought = true
		}
	}
	assert.True(t, brought, "a COPY line or an apt-get install package names headless-shell and uses HEADLESS_SHELL_VERSION: %v", fields)

	hasFont := false
	for _, pkg := range installed {
		if strings.HasPrefix(pkg, "fonts-") {
			hasFont = true
		}
	}
	assert.True(t, hasFont, "the runtime stage installs a fonts- package (a PDF without a font has no readable text): %v", installed)

	// A render kills the browser's process group; the orphaned helpers go to
	// PID 1, which must reap them. The server does not, an init does.
	assert.Contains(t, installed, "tini", "the runtime stage installs an init")
	assert.Contains(t, stage, `ENTRYPOINT ["/usr/bin/tini", "--", "gophercrm"]`, "the server runs under the init")

	var chromePath string
	for _, line := range stage {
		if match := regexp.MustCompile(`^ENV\s+QUOTE_PDF_CHROME_PATH[= ]\s*(\S.*)$`).FindStringSubmatch(line); match != nil {
			chromePath = strings.Trim(strings.TrimSpace(match[1]), `"`)
		}
	}
	require.NotEmpty(t, chromePath, "the runtime stage sets QUOTE_PDF_CHROME_PATH")
	assert.True(t, strings.HasPrefix(chromePath, "/"), "QUOTE_PDF_CHROME_PATH is an absolute path: %q", chromePath)
	assert.NotEqual(t, "/usr/bin/chromium-browser", chromePath, "the launcher of the full Alpine Chromium is gone")
	// The path is the browser itself or a launcher the Dockerfile writes. A
	// launcher must exec the browser: the server kills the process group of
	// the pid it started, and a shell that waits on the browser instead
	// would be that pid.
	if !headlessShellName.MatchString(chromePath) {
		var creating []string
		for _, line := range stage {
			if strings.HasPrefix(line, "RUN ") && regexp.MustCompile(`>\s*`+regexp.QuoteMeta(chromePath)+`(\s|$)`).MatchString(line) {
				creating = append(creating, line)
			}
		}
		require.Len(t, creating, 1, "one RUN instruction writes the launcher %s", chromePath)
		assert.Contains(t, creating[0], "exec ", "the launcher execs the browser")
		assert.Regexp(t, `exec \S*`+headlessShellName.String(), creating[0], "and what it execs is the headless shell")
	}

	// The image, not the server, decides about the browser's sandbox.
	for _, file := range []string{"internal/service/quote_pdf_chrome.go", "internal/service/quote_pdf_chrome_pipe.go", "internal/service/quote_pdf_chrome_unix.go", "internal/service/quote_pdf_chrome_other.go", "internal/service/quote_pdf.go", "cmd/main.go"} {
		for i, line := range strings.Split(readRepositoryFile(t, file), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			assert.NotContains(t, line, "--no-sandbox", "%s:%d passes --no-sandbox from Go", file, i+1)
		}
	}

	for _, file := range []string{composeMySQLFile, composeSQLiteFile} {
		t.Run(file, func(t *testing.T) {
			backend := composeBackendBlock(t, file)
			matches := regexp.MustCompile(`(?m)^      QUOTE_PDF_CHROME_PATH:(.*)$`).FindAllStringSubmatch(backend, -1)
			require.Len(t, matches, 1, "services.backend.environment sets QUOTE_PDF_CHROME_PATH once")
			assert.Equal(t, `""`, strings.TrimSpace(matches[0][1]), "and sets it empty: PDF output is off in the development stack")
			assert.Contains(t, backend, "APP_BASE_URL: http://localhost:${UI_PORT:-3000}",
				"the reason: this APP_BASE_URL is not reachable from the backend container")
		})
	}

	// Both compose files health-check the backend with a command run inside
	// this image, and the ui service waits for that check: a command the
	// image lacks keeps the whole stack from coming up.
	for _, file := range []string{composeMySQLFile, composeSQLiteFile} {
		t.Run("healthcheck command of "+file, func(t *testing.T) {
			backend := composeBackendBlock(t, file)
			match := regexp.MustCompile(`(?m)^    healthcheck:\n      test: \["CMD", "([^"]+)"`).FindStringSubmatch(backend)
			require.NotNil(t, match, "services.backend.healthcheck.test is a CMD list")
			assert.Contains(t, installed, match[1], "the runtime stage installs the health check's command")
		})
	}

	t.Run(composeProductionExampleFile, func(t *testing.T) {
		assert.NotContains(t, readRepositoryFile(t, composeProductionExampleFile), "QUOTE_PDF_CHROME_PATH",
			"the production example does not forward the variable: the stack it overrides decides")
	})
}
