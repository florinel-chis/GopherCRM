package config

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dockerfileLines returns the instructions of the backend Dockerfile, with
// line continuations joined, comments and blank lines dropped.
func dockerfileLines(t *testing.T) []string {
	t.Helper()
	text := strings.ReplaceAll(readRepositoryFile(t, "Dockerfile"), "\\\n", " ")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// dockerfileStages returns the stages of the backend Dockerfile in order,
// each starting with its FROM line. Instructions before the first FROM (the
// global ARGs) belong to no stage.
func dockerfileStages(t *testing.T) [][]string {
	t.Helper()
	var stages [][]string
	for _, line := range dockerfileLines(t) {
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			stages = append(stages, nil)
		}
		if len(stages) > 0 {
			stages[len(stages)-1] = append(stages[len(stages)-1], line)
		}
	}
	return stages
}

// dockerfileRuntimeStage returns the lines of the last stage of the backend
// Dockerfile.
func dockerfileRuntimeStage(t *testing.T) []string {
	t.Helper()
	stages := dockerfileStages(t)
	require.NotEmpty(t, stages, "the Dockerfile has a runtime stage")
	return stages[len(stages)-1]
}

// dockerfileStageNamed returns the stage declared with FROM ... AS name.
func dockerfileStageNamed(t *testing.T, name string) []string {
	t.Helper()
	as := regexp.MustCompile(`(?i)^FROM\s.*\sAS\s+` + regexp.QuoteMeta(name) + `$`)
	for _, stage := range dockerfileStages(t) {
		if as.MatchString(stage[0]) {
			return stage
		}
	}
	t.Fatalf("the Dockerfile has no stage named %s", name)
	return nil
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

// TestDockerfileShipsHeadlessShell pins what quote PDF output (#384, #393,
// #420) needs from the deployment files: the image takes chrome-headless-shell
// from Chrome for Testing at one pinned version, checked against a SHA-256 per
// architecture, instead of a Debian package whose pinned version the security
// repository drops; the runtime image is Debian, installs no Chromium package,
// fails its build on a missing library, and brings a font, an init and the
// command of the compose health checks; QUOTE_PDF_CHROME_PATH points at the
// browser or at a launcher that execs it; the Go code is not the place the
// sandbox is switched off; and the two development compose files, whose
// APP_BASE_URL the backend container cannot reach, switch PDF output off.
func TestDockerfileShipsHeadlessShell(t *testing.T) {
	stage := dockerfileRuntimeStage(t)
	require.True(t, strings.HasPrefix(stage[0], "FROM debian:"), "the runtime stage is debian: %s", stage[0])

	installed, fields := dockerfileAptPackages(stage)
	require.NotEmpty(t, installed, "the runtime stage installs packages with apt-get install")
	for _, pkg := range []string{"chromium", "chromium-browser", "chromium-headless-shell"} {
		assert.NotContains(t, installed, pkg, "the runtime stage installs no Chromium package")
	}
	for _, field := range fields {
		assert.NotContains(t, field, "deb12u1", "the runtime stage pins no Debian package revision")
		assert.NotContains(t, field, "=", "the runtime stage pins no apt package version: %s", field)
	}

	// One literal for the browser version in the whole file: a global ARG
	// that the stages re-declare without a value.
	whole := dockerfileLines(t)
	var version string
	versionLines := 0
	for _, line := range whole {
		if match := regexp.MustCompile(`^ARG\s+HEADLESS_SHELL_VERSION=(.*)$`).FindStringSubmatch(line); match != nil {
			versionLines++
			version = strings.Trim(strings.TrimSpace(match[1]), `"`)
		}
	}
	require.Equal(t, 1, versionLines, "the Dockerfile declares ARG HEADLESS_SHELL_VERSION=<version> once")
	require.Regexp(t, `^\d+\.\d+\.\d+\.\d+$`, version, "the pinned browser version is a.b.c.d")
	holding := 0
	for _, line := range strings.Split(readRepositoryFile(t, "Dockerfile"), "\n") {
		if strings.Contains(line, version) {
			holding++
		}
	}
	assert.Equal(t, 1, holding, "no other line of the Dockerfile writes the version %s", version)

	// A checksum per architecture, 64 lower-case hex characters each.
	for _, arch := range []string{"AMD64", "ARM64"} {
		var sums []string
		for _, line := range whole {
			if match := regexp.MustCompile(`^ARG\s+HEADLESS_SHELL_SHA256_` + arch + `=(.*)$`).FindStringSubmatch(line); match != nil {
				sums = append(sums, strings.Trim(strings.TrimSpace(match[1]), `"`))
			}
		}
		require.Len(t, sums, 1, "the Dockerfile declares ARG HEADLESS_SHELL_SHA256_%s=<sha256> once", arch)
		assert.Regexp(t, `^[0-9a-f]{64}$`, sums[0], "HEADLESS_SHELL_SHA256_%s is a SHA-256", arch)
	}

	// The runtime stage copies the browser from the download stage, which
	// fetches the pinned version from Chrome for Testing for both
	// architectures and checks it.
	copyFrom := regexp.MustCompile(`^COPY\s+--from=(\S+)\s+(\S+)`)
	var source string
	for _, line := range stage {
		if match := copyFrom.FindStringSubmatch(line); match != nil && headlessShellName.MatchString(match[2]) {
			source = match[1]
		}
	}
	require.NotEmpty(t, source, "the runtime stage copies the headless shell from another stage")
	download := dockerfileStageNamed(t, source)
	var fetches []string
	for _, line := range download {
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, "chrome-for-testing-public/${HEADLESS_SHELL_VERSION}/") {
			fetches = append(fetches, line)
		}
	}
	require.Len(t, fetches, 1, "one RUN of stage %s downloads from chrome-for-testing-public/${HEADLESS_SHELL_VERSION}/", source)
	for _, needle := range []string{"https://", "linux64", "linux-arm64", "HEADLESS_SHELL_SHA256_AMD64", "HEADLESS_SHELL_SHA256_ARM64", "sha256sum -c"} {
		assert.Contains(t, fetches[0], needle, "the download maps both architectures and checks the zip")
	}
	for _, arg := range []string{"HEADLESS_SHELL_VERSION", "HEADLESS_SHELL_SHA256_AMD64", "HEADLESS_SHELL_SHA256_ARM64", "TARGETARCH"} {
		assert.Contains(t, download, "ARG "+arg, "stage %s re-declares %s without a value", source, arg)
	}

	// A library the binary links against but the image lacks breaks the
	// build, not the first render.
	lddCheck := false
	for _, line := range stage {
		if strings.HasPrefix(line, "RUN ") && regexp.MustCompile(`ldd \S*`+headlessShellName.String()).MatchString(line) &&
			strings.Contains(line, "not found") && strings.Contains(line, "exit 1") {
			lddCheck = true
		}
	}
	assert.True(t, lddCheck, "the runtime stage fails the build when ldd reports a library as not found")

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

// workflowBlock returns the lines indented deeper than the line that is
// exactly key (with its indent), up to the next line at that indent or less.
func workflowBlock(text, key string) (string, bool) {
	lines := strings.Split(text, "\n")
	indent := len(key) - len(strings.TrimLeft(key, " "))
	for i, line := range lines {
		if line != key {
			continue
		}
		var block []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) != "" && len(next)-len(strings.TrimLeft(next, " ")) <= indent {
				break
			}
			block = append(block, next)
		}
		return strings.Join(block, "\n"), true
	}
	return "", false
}

// TestDockerImageWorkflow pins the workflow that keeps the image's browser
// pin honest (#420): it runs weekly, on demand and on pull requests that
// change the Dockerfile or the workflow, reads nothing but the repository,
// uses actions pinned to a commit, builds the download stage for both
// architectures and the whole image for linux/amd64, and prints a PDF with
// the image's own launcher as the image's user, checking the file header and
// that the browser is the version the Dockerfile pins.
func TestDockerImageWorkflow(t *testing.T) {
	const file = ".github/workflows/docker-image.yml"
	text := readRepositoryFile(t, file)

	on, ok := workflowBlock(text, "on:")
	require.True(t, ok, "%s has a top-level on:", file)
	schedule, ok := workflowBlock(on, "  schedule:")
	require.True(t, ok, "the workflow runs on a schedule")
	assert.Len(t, regexp.MustCompile(`(?m)^\s+- cron: "[^"]+"\s*$`).FindAllString(schedule, -1), 1, "with one cron")
	assert.Regexp(t, `(?m)^  workflow_dispatch:\s*$`, on, "the workflow can be started by hand")
	pullRequest, ok := workflowBlock(on, "  pull_request:")
	require.True(t, ok, "the workflow runs on pull requests")
	paths, ok := workflowBlock(pullRequest, "    paths:")
	require.True(t, ok, "limited to paths")
	for _, path := range []string{"Dockerfile", file} {
		assert.Regexp(t, `(?m)^\s+- "?`+regexp.QuoteMeta(path)+`"?\s*$`, paths, "the pull_request paths include %s", path)
	}

	permissions, ok := workflowBlock(text, "permissions:")
	require.True(t, ok, "the workflow sets top-level permissions")
	assert.Equal(t, "  contents: read", strings.TrimRight(permissions, "\n "), "and reads the repository only")
	assert.Equal(t, 1, strings.Count(text, "permissions:"), "no job widens the permissions")

	uses := regexp.MustCompile(`(?m)^\s*(?:- )?uses:\s*(\S+)`).FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, uses, "the workflow checks out the repository with an action")
	for _, use := range uses {
		assert.Regexp(t, `@[0-9a-f]{40}$`, use[1], "the action is pinned to a commit")
	}

	// The commands in the order they run.
	position := func(pattern string) int {
		t.Helper()
		loc := regexp.MustCompile(pattern).FindStringIndex(text)
		require.NotNil(t, loc, "the workflow runs %s", pattern)
		return loc[0]
	}
	amd64Stage := position(`docker buildx build --platform linux/amd64 --target headless-shell \.`)
	arm64Stage := position(`docker buildx build --platform linux/arm64 --target headless-shell \.`)
	fullBuild := regexp.MustCompile(`docker buildx build --platform linux/amd64 --load -t (\S+) \.`).FindStringSubmatchIndex(text)
	require.NotNil(t, fullBuild, "the workflow builds and loads the whole image for linux/amd64")
	tag := text[fullBuild[2]:fullBuild[3]]
	render := regexp.MustCompile(`docker run --rm --entrypoint sh ` + regexp.QuoteMeta(tag) + ` -c '([^']*)'`).FindStringSubmatchIndex(text)
	require.NotNil(t, render, "the workflow runs a shell in the image it built")
	script := text[render[2]:render[3]]
	assert.NotRegexp(t, `--user|\s-u\s`, text[render[0]:render[2]], "the render runs as the image's own user")
	assert.Contains(t, script, "quote-pdf-browser --version", "it prints the browser version through the launcher")
	assert.Regexp(t, `quote-pdf-browser --print-to-pdf=(\S+) "data:text/html,`, script, "it prints a page to PDF through the launcher")
	assert.Contains(t, script, "head -c 4", "it reads the file header")
	assert.Contains(t, script, `"%PDF"`, "and wants %PDF")
	compare := position(`sed -n 's/\^ARG HEADLESS_SHELL_VERSION=//p' Dockerfile`)
	assert.Less(t, amd64Stage, arm64Stage, "the amd64 download stage builds first")
	assert.Less(t, arm64Stage, fullBuild[0], "then the arm64 download stage")
	assert.Less(t, fullBuild[0], render[0], "then the whole image")
	assert.Less(t, render[0], compare, "then the render, then the version comparison")
	assert.Regexp(t, `case "\$reported" in\s+\*" \$pinned"\*\) ;;\s+\*\) .*exit 1 ;;`, text[compare:],
		"the reported browser version must contain the pinned one")
}
