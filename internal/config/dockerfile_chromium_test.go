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

// TestDockerfileShipsChromium pins what quote PDF output (#384) needs from
// the deployment files: the runtime image installs Chromium and a font and
// points QUOTE_PDF_CHROME_PATH at the browser, the Go code is not the place
// the sandbox is switched off, and the two development compose files, whose
// APP_BASE_URL the backend container cannot reach, switch PDF output off.
func TestDockerfileShipsChromium(t *testing.T) {
	stage := dockerfileRuntimeStage(t)
	require.True(t, strings.HasPrefix(stage[0], "FROM alpine:"), "the runtime stage is alpine: %s", stage[0])

	var installed []string
	var chromePath string
	for _, line := range stage {
		if strings.HasPrefix(line, "RUN ") {
			for _, command := range strings.Split(strings.TrimPrefix(line, "RUN "), "&&") {
				fields := strings.Fields(command)
				if len(fields) >= 2 && fields[0] == "apk" && fields[1] == "add" {
					for _, field := range fields[2:] {
						if !strings.HasPrefix(field, "-") {
							installed = append(installed, field)
						}
					}
				}
			}
		}
		if match := regexp.MustCompile(`^ENV\s+QUOTE_PDF_CHROME_PATH[= ]\s*(\S.*)$`).FindStringSubmatch(line); match != nil {
			chromePath = strings.Trim(strings.TrimSpace(match[1]), `"`)
		}
	}
	assert.Contains(t, installed, "chromium", "the runtime stage installs chromium")
	hasFont := false
	for _, pkg := range installed {
		if strings.HasPrefix(pkg, "font-") {
			hasFont = true
		}
	}
	assert.True(t, hasFont, "the runtime stage installs a font- package (a PDF without a font has no readable text): %v", installed)
	require.NotEmpty(t, chromePath, "the runtime stage sets QUOTE_PDF_CHROME_PATH")

	// A render kills the browser's process group; the orphaned helpers go to
	// PID 1, which must reap them. The server does not, an init does.
	assert.Contains(t, installed, "tini", "the runtime stage installs an init")
	assert.Contains(t, stage, `ENTRYPOINT ["/sbin/tini", "--", "gophercrm"]`, "the server runs under the init")
	assert.True(t, strings.HasPrefix(chromePath, "/"), "QUOTE_PDF_CHROME_PATH is an absolute path: %q", chromePath)

	// The image, not the server, decides about the browser's sandbox.
	for _, file := range []string{"internal/service/quote_pdf_chrome.go", "internal/service/quote_pdf_chrome_unix.go", "internal/service/quote_pdf_chrome_other.go", "internal/service/quote_pdf.go", "cmd/main.go"} {
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

	t.Run(composeProductionExampleFile, func(t *testing.T) {
		assert.NotContains(t, readRepositoryFile(t, composeProductionExampleFile), "QUOTE_PDF_CHROME_PATH",
			"the production example does not forward the variable: the stack it overrides decides")
	})
}
