package config

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin #308: both dev Compose files forward
// VITE_APP_NAME to the UI image build through build.args, the Dockerfile
// declares it in the build stage only, the documentation describes the
// pass-through as delivered, and the Compose line citations follow the file.

const (
	composeMySQLFile  = "docker-compose.yml"
	composeSQLiteFile = "docker-compose.sqlite.yml"
)

// composeLineIndent returns the number of leading spaces and whether the line
// holds content (not blank, not a full-line comment).
func composeLineIndent(line string) (int, bool) {
	trimmed := strings.TrimLeft(line, " ")
	content := strings.TrimSpace(trimmed)
	return len(line) - len(trimmed), content != "" && !strings.HasPrefix(content, "#")
}

func composeSplitLines(data string) []string {
	return strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
}

// maskComposeBlocks keeps the selected blocks of a Compose file and replaces
// every other line with an empty line, so the line count (and therefore every
// node's line number after parseComposeExample) is the file's own.
//
// A selector `services.<svc>` keeps the `services:` line plus the `  <svc>:`
// line and every following line up to the next content line indented by two
// or less. Any other selector names a top-level key and keeps its line and
// every following line up to the next content line at column 0. Blocks that
// are not kept (the db service's multi-line flow sequence, for one) are never
// parsed.
func maskComposeBlocks(t *testing.T, data string, keep ...string) string {
	t.Helper()
	lines := composeSplitLines(data)
	kept := make([]bool, len(lines))

	topLevel := func(key string) int {
		for i, line := range lines {
			if line == key+":" || strings.HasPrefix(line, key+": ") {
				return i
			}
		}
		t.Fatalf("top-level key %q not found", key)
		return -1
	}
	keepUntil := func(start, maxIndent int) {
		kept[start] = true
		for i := start + 1; i < len(lines); i++ {
			indent, content := composeLineIndent(lines[i])
			if content && indent <= maxIndent {
				return
			}
			kept[i] = true
		}
	}

	for _, sel := range keep {
		if svc, ok := strings.CutPrefix(sel, "services."); ok {
			services := topLevel("services")
			kept[services] = true
			start := -1
			for i := services + 1; i < len(lines); i++ {
				indent, content := composeLineIndent(lines[i])
				if content && indent == 0 {
					break
				}
				if lines[i] == "  "+svc+":" {
					start = i
					break
				}
			}
			require.NotEqual(t, -1, start, "services.%s not found", svc)
			keepUntil(start, 2)
			continue
		}
		keepUntil(topLevel(sel), 0)
	}

	out := make([]string, len(lines))
	for i, line := range lines {
		if kept[i] {
			out[i] = line
		}
	}
	return strings.Join(out, "\n")
}

// composeServiceNames lists the keys indented by two under `services:`,
// textually, without parsing the service blocks.
func composeServiceNames(t *testing.T, data string) []string {
	t.Helper()
	lines := composeSplitLines(data)
	start := -1
	for i, line := range lines {
		if line == "services:" {
			start = i
			break
		}
	}
	require.NotEqual(t, -1, start, "top-level services key")
	var names []string
	for i := start + 1; i < len(lines); i++ {
		indent, content := composeLineIndent(lines[i])
		if !content {
			continue
		}
		if indent == 0 {
			break
		}
		if indent == 2 {
			key := strings.TrimSpace(lines[i])
			require.True(t, strings.HasSuffix(key, ":"), "service line %d is not `name:`: %q", i+1, key)
			names = append(names, strings.TrimSuffix(key, ":"))
		}
	}
	return names
}

// composeItemLines returns the 1-based line numbers of the `- item` lines of a
// block sequence node.
func composeItemLines(t *testing.T, data string, node *composeNode) []int {
	t.Helper()
	lines := composeSplitLines(data)
	require.Greater(t, node.line, 0)
	nodeIndent, _ := composeLineIndent(lines[node.line-1])
	var out []int
	for i := node.line; i < len(lines); i++ {
		indent, content := composeLineIndent(lines[i])
		if !content {
			continue
		}
		if indent <= nodeIndent {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
			out = append(out, i+1)
		}
	}
	require.Len(t, out, len(node.items), "item lines of %s", node.key)
	return out
}

func composeLastChildLine(t *testing.T, node *composeNode) int {
	t.Helper()
	require.NotEmpty(t, node.children, "%s has children", node.key)
	return node.children[len(node.children)-1].line
}

// composeUIBuildArgs returns services.ui.build.args as key -> unquoted value.
func composeUIBuildArgs(t *testing.T, rel string) map[string]string {
	t.Helper()
	root := parseComposeExample(t, maskComposeBlocks(t, readRepositoryFile(t, rel), "services.ui"))
	build := composeService(t, root, "ui").child("build")
	require.NotNil(t, build, "%s services.ui.build", rel)
	args := build.child("args")
	require.NotNil(t, args, "%s services.ui.build.args", rel)
	out := make(map[string]string, len(args.children))
	for _, c := range args.children {
		out[c.key] = unquoteComposeScalar(t, c.value)
	}
	return out
}

// TestComposeUIBuildArgs pins #308 AC-1.
func TestComposeUIBuildArgs(t *testing.T) {
	for _, tc := range []struct {
		file     string
		services []string
	}{
		{composeMySQLFile, []string{"db", "backend", "ui"}},
		{composeSQLiteFile, []string{"backend", "ui"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data := readRepositoryFile(t, tc.file)
			assert.Equal(t, tc.services, composeServiceNames(t, data), "service names")

			root := parseComposeExample(t, maskComposeBlocks(t, data, "services.ui"))
			ui := composeService(t, root, "ui")
			require.Equal(t, []string{"build", "depends_on", "ports"}, ui.childKeys(), "services.ui keys")

			build := ui.child("build")
			require.NotNil(t, build, "services.ui.build")
			require.Empty(t, build.value, "services.ui.build must be a block mapping, not the scalar %q", build.value)
			require.Empty(t, build.items, "services.ui.build must be a block mapping, not a list")
			require.Equal(t, []string{"context", "args"}, build.childKeys(),
				"services.ui.build holds exactly context and args (no dockerfile)")

			context := build.child("context")
			assert.Empty(t, context.children)
			assert.Empty(t, context.items)
			assert.Equal(t, "./gocrm-ui", unquoteComposeScalar(t, context.value))

			args := build.child("args")
			require.Empty(t, args.value, "services.ui.build.args must be a block mapping")
			require.Empty(t, args.items, "services.ui.build.args must be a block mapping, not a list")
			require.Equal(t, []string{"VITE_APP_NAME"}, args.childKeys(), "build.args holds only VITE_APP_NAME")
			name := args.child("VITE_APP_NAME")
			assert.Empty(t, name.children)
			assert.Empty(t, name.items)
			assert.Equal(t, "${VITE_APP_NAME:-}", unquoteComposeScalar(t, name.value))

			// The long form replaces one line with four: +3 lines.
			assert.Equal(t, build.line+4, ui.child("depends_on").line, "the build block spans exactly four lines")

			ports := ui.child("ports")
			require.NotNil(t, ports, "services.ui.ports")
			assert.Empty(t, ports.value)
			assert.Empty(t, ports.children)
			assert.Equal(t, []string{`"${UI_PORT:-3000}:80"`}, ports.items, "services.ui.ports")
		})
	}
}

// TestComposeUIBuildArgMatchesDockerfileBuildStage pins #308 AC-2.
func TestComposeUIBuildArgMatchesDockerfileBuildStage(t *testing.T) {
	lines := composeSplitLines(readRepositoryFile(t, "gocrm-ui/Dockerfile"))
	last := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "FROM ") {
			last = i
		}
	}
	require.Greater(t, last, 0, "gocrm-ui/Dockerfile has a build stage before the last FROM")

	for _, file := range []string{composeMySQLFile, composeSQLiteFile} {
		args := composeUIBuildArgs(t, file)
		require.NotEmpty(t, args, file)
		for key := range args {
			declared := false
			for _, line := range lines[:last] {
				if strings.TrimSpace(line) == "ARG "+key+"=" {
					declared = true
				}
			}
			assert.True(t, declared, "%s forwards %s; gocrm-ui/Dockerfile must declare `ARG %s=` before the last FROM", file, key, key)
		}
	}

	for i, line := range lines[last:] {
		assert.NotContains(t, line, "VITE_APP_NAME", "gocrm-ui/Dockerfile:%d is in the nginx stage", last+i+1)
	}
}

// dockerViteAppNameBullet returns the DOCKER.md bullet that documents the
// VITE_APP_NAME build argument, up to the next top-level bullet or heading.
func dockerViteAppNameBullet(t *testing.T, doc string) string {
	t.Helper()
	lines := composeSplitLines(doc)
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "`gocrm-ui/Dockerfile` also accepts") {
			require.Equal(t, -1, start, "docs/DOCKER.md has the VITE_APP_NAME bullet twice")
			start = i
		}
	}
	require.NotEqual(t, -1, start, "docs/DOCKER.md lacks the VITE_APP_NAME bullet")
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- ") || strings.HasPrefix(lines[i], "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestComposeUIBuildArgsDocumented pins #308 AC-3.
func TestComposeUIBuildArgsDocumented(t *testing.T) {
	docker := readRepositoryFile(t, "docs/DOCKER.md")
	docs := map[string]string{"docs/DOCKER.md": normalizeDocText(docker)}
	for _, rel := range []string{"docs/FORKING.md", "docs/USE_CASES.md", "docs/ROADMAP.md"} {
		docs[rel] = normalizeDocText(readRepositoryFile(t, rel))
	}
	unreleased := subsectionBetween(t, readRepositoryFile(t, "CHANGELOG.md"), "## [Unreleased]", "## [")
	docs["CHANGELOG.md [Unreleased]"] = normalizeDocText(unreleased)

	bullet := normalizeDocText(dockerViteAppNameBullet(t, docker))
	for _, phrase := range []string{
		"${VITE_APP_NAME:-}", ".env", "docker compose up --build", "GoCRM", "not a secret", "JavaScript",
		"docker build --build-arg VITE_APP_NAME=",
	} {
		assert.Contains(t, bullet, normalizeDocText(phrase), "docs/DOCKER.md VITE_APP_NAME bullet")
	}

	stale := []string{
		"declare no `build.args`",
		"declares no `build.args`",
		"does not pass the variable on its own",
		"do not forward `VITE_APP_NAME`",
		"do not forward it yet",
		"pass-through is #308",
		"pass-through is tracked in #308",
		"tracked in #308",
		"Still open: the Compose `build.args` pass-through",
		"repo-root `.env` is #308",
	}
	stillOpen := regexp.MustCompile(`(?i)still open[^|.]*#308`)
	for rel, text := range docs {
		for _, phrase := range stale {
			assert.NotContains(t, text, normalizeDocText(phrase), rel)
		}
		assert.NotRegexp(t, stillOpen, text, rel)
		assert.Contains(t, text, "#308", rel)
		assert.Contains(t, text, "build.args", rel)
	}

	assert.Contains(t, docs["docs/ROADMAP.md"], "VITE_APP_NAME")
	assert.Contains(t, docs["docs/FORKING.md"], "--build-arg VITE_APP_NAME")
	changelog := docs["CHANGELOG.md [Unreleased]"]
	for _, phrase := range []string{"#303", "VITE_APP_NAME", "#231", "SERVER_PROVISIONING.md"} {
		assert.Contains(t, changelog, phrase, "CHANGELOG.md [Unreleased]")
	}
	assert.NotContains(t, changelog, "build argument remain open")

	// The Vitest pins of gocrm-ui/src/hooks/productNameBuildArg.test.ts,
	// restated so a doc edit that would break them fails this backend check.
	for _, phrase := range []string{
		"is the only build argument", "not shipped", "never reaches the UI image",
		"add `ARG VITE_APP_NAME`", "docker history",
	} {
		assert.NotContains(t, docs["docs/DOCKER.md"], normalizeDocText(phrase), "docs/DOCKER.md")
	}
	for _, phrase := range []string{
		"ignores `VITE_APP_NAME`",
		"(not shipped; see [DOCKER.md](DOCKER.md))",
		"and nothing else (`gocrm-ui/Dockerfile:16-17`)",
	} {
		assert.NotContains(t, docs["docs/FORKING.md"], normalizeDocText(phrase), "docs/FORKING.md")
	}
	for _, phrase := range []string{"takes no `VITE_APP_NAME` build argument", "build argument (no issue yet)"} {
		assert.NotContains(t, docs["docs/USE_CASES.md"], normalizeDocText(phrase), "docs/USE_CASES.md")
	}

	// The gaps-table row of #221.
	var rows []string
	for _, line := range composeSplitLines(readRepositoryFile(t, "docs/USE_CASES.md")) {
		if strings.HasSuffix(strings.TrimSpace(line), "| #221 |") {
			rows = append(rows, line)
		}
	}
	require.Len(t, rows, 1, "docs/USE_CASES.md has one gaps-table row for #221")
	row := normalizeDocText(rows[0])
	for _, phrase := range []string{"Delivered", "build.args", "#308"} {
		assert.Contains(t, row, phrase, "docs/USE_CASES.md #221 row")
	}
	if _, open, ok := strings.Cut(row, "Still open:"); ok {
		assert.NotContains(t, open, "#308", "docs/USE_CASES.md #221 row: Still open part")
	}
}

// composeCitationLines holds the line numbers the documentation cites.
type composeCitationLines struct {
	envFrom, envTo          int
	backendPorts, backendIt int
	uiPorts, uiItem         int
	name                    int
	networks, subnet        int
}

func readComposeCitationLines(t *testing.T, rel string, withName bool) composeCitationLines {
	t.Helper()
	data := readRepositoryFile(t, rel)
	keep := []string{"services.backend", "services.ui", "networks"}
	if withName {
		keep = append(keep, "name")
	}
	masked := maskComposeBlocks(t, data, keep...)
	root := parseComposeExample(t, masked)

	var out composeCitationLines
	env := composeService(t, root, "backend").child("environment")
	require.NotNil(t, env, "%s services.backend.environment", rel)
	out.envFrom, out.envTo = env.line, composeLastChildLine(t, env)

	backendPorts := composeService(t, root, "backend").child("ports")
	require.NotNil(t, backendPorts, "%s services.backend.ports", rel)
	items := composeItemLines(t, masked, backendPorts)
	require.Len(t, items, 1, "%s services.backend.ports holds one item", rel)
	out.backendPorts, out.backendIt = backendPorts.line, items[0]

	uiPorts := composeService(t, root, "ui").child("ports")
	require.NotNil(t, uiPorts, "%s services.ui.ports", rel)
	items = composeItemLines(t, masked, uiPorts)
	require.Len(t, items, 1, "%s services.ui.ports holds one item", rel)
	out.uiPorts, out.uiItem = uiPorts.line, items[0]

	networks := root.child("networks")
	require.NotNil(t, networks, "%s networks", rel)
	out.networks = networks.line
	def := networks.child("default")
	require.NotNil(t, def, "%s networks.default", rel)
	ipam := def.child("ipam")
	require.NotNil(t, ipam, "%s networks.default.ipam", rel)
	config := ipam.child("config")
	require.NotNil(t, config, "%s networks.default.ipam.config", rel)
	items = composeItemLines(t, masked, config)
	require.NotEmpty(t, items)
	out.subnet = items[len(items)-1]
	require.True(t, strings.HasPrefix(config.items[len(config.items)-1], "subnet: "), "%s last ipam item is the subnet", rel)

	if withName {
		name := root.child("name")
		require.NotNil(t, name, "%s name", rel)
		out.name = name.line
	}
	return out
}

// TestComposeUIDocCitations pins #308 AC-4.
func TestComposeUIDocCitations(t *testing.T) {
	mysql := readComposeCitationLines(t, composeMySQLFile, false)
	sqlite := readComposeCitationLines(t, composeSQLiteFile, true)

	// The expected lines after the +3-line ui build block.
	assert.Equal(t, composeCitationLines{
		envFrom: 41, envTo: 60, backendPorts: 61, backendIt: 62, uiPorts: 78, uiItem: 79,
		networks: 84, subnet: 88,
	}, mysql, composeMySQLFile)
	assert.Equal(t, composeCitationLines{
		envFrom: 25, envTo: 43, backendPorts: 46, backendIt: 47, uiPorts: 63, uiItem: 64,
		name: 20, networks: 69, subnet: 76,
	}, sqlite, composeSQLiteFile)

	portsMySQL := fmt.Sprintf("%s:%d-%d,%d-%d", composeMySQLFile, mysql.backendPorts, mysql.backendIt, mysql.uiPorts, mysql.uiItem)
	portsSQLite := fmt.Sprintf("%s:%d-%d,%d-%d", composeSQLiteFile, sqlite.backendPorts, sqlite.backendIt, sqlite.uiPorts, sqlite.uiItem)
	envMySQL := fmt.Sprintf("%s:%d-%d", composeMySQLFile, mysql.envFrom, mysql.envTo)
	envSQLite := fmt.Sprintf("%s:%d-%d", composeSQLiteFile, sqlite.envFrom, sqlite.envTo)

	docker := normalizeDocText(readRepositoryFile(t, "docs/DOCKER.md"))
	forking := normalizeDocText(readRepositoryFile(t, "docs/FORKING.md"))

	for _, citation := range []string{portsMySQL, portsSQLite, envMySQL, envSQLite} {
		assert.Contains(t, docker, citation, "docs/DOCKER.md")
		assert.Contains(t, forking, citation, "docs/FORKING.md")
	}
	assert.Contains(t, docker, fmt.Sprintf("%s:%d,%d,%d", composeSQLiteFile, sqlite.name, sqlite.backendIt, sqlite.subnet), "docs/DOCKER.md")
	assert.Contains(t, docker, fmt.Sprintf("%s:%d,%d", composeMySQLFile, mysql.backendIt, mysql.subnet), "docs/DOCKER.md")
	assert.Contains(t, forking, fmt.Sprintf("%s:%d,%d-%d,%d-%d", composeSQLiteFile,
		sqlite.name, sqlite.backendPorts, sqlite.backendIt, sqlite.networks, sqlite.subnet), "docs/FORKING.md")

	// The citations as they read before the ui build block grew.
	for _, old := range []string{
		"docker-compose.yml:61-62,75-76", "docker-compose.sqlite.yml:46-47,60-61",
		"docker-compose.sqlite.yml:20,47,73", "docker-compose.yml:62,85",
		"docker-compose.sqlite.yml:20,46-47,66-73",
	} {
		assert.NotContains(t, docker, old, "docs/DOCKER.md")
		assert.NotContains(t, forking, old, "docs/FORKING.md")
	}
}
