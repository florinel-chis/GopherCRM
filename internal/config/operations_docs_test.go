package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin the operations docs of #795: one owner page per
// topic (configuration, backup and restore, upgrades), the deployment decision
// table, and the procedures written in the order they are run.

var deploymentTableHeader = regexp.MustCompile(`^\| *Deployment *\| *When to choose it *\| *Prerequisites *\| *Guide *\|$`)

var markdownLinkTarget = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// deploymentTableRow is one data row of a decision table, split into cells.
type deploymentTableRow struct {
	cells []string
}

// docLines returns the document's lines without carriage returns.
func docLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// deploymentTableRows returns the rows of the decision table that sits before
// the first level-2 heading of doc.
func deploymentTableRows(t *testing.T, rel string) []deploymentTableRow {
	t.Helper()
	lines := docLines(readRepositoryFile(t, rel))
	firstHeading := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			firstHeading = i
			break
		}
	}
	header := -1
	for i := 0; i < firstHeading; i++ {
		if deploymentTableHeader.MatchString(lines[i]) {
			require.Equal(t, -1, header, "%s has two deployment tables before its first ## heading", rel)
			header = i
		}
	}
	require.NotEqual(t, -1, header, "%s has no deployment table before its first ## heading", rel)
	require.Less(t, header+1, firstHeading, "%s: the deployment table has no delimiter row", rel)
	require.Regexp(t, regexp.MustCompile(`^\|( *:?-+:? *\|){4}$`), lines[header+1], "%s: delimiter row", rel)

	var rows []deploymentTableRow
	for i := header + 2; i < firstHeading && strings.HasPrefix(lines[i], "|"); i++ {
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(lines[i]), "|"), "|")
		var cells []string
		for _, cell := range strings.Split(inner, "|") {
			cells = append(cells, strings.TrimSpace(cell))
		}
		rows = append(rows, deploymentTableRow{cells: cells})
	}
	return rows
}

// TestDeploymentDecisionTables pins #795 AC-1.
func TestDeploymentDecisionTables(t *testing.T) {
	wantFirst := []string{
		"Local evaluation",
		"Production Compose",
		"Systemd server from a published release",
		"Fork's automated release pipeline",
	}
	for _, rel := range []string{"docs/README.md", serverProvisioningGuideFile} {
		rows := deploymentTableRows(t, rel)
		require.Len(t, rows, len(wantFirst), "%s: the deployment table has four data rows", rel)
		var guides []string
		for i, row := range rows {
			require.Len(t, row.cells, 4, "%s row %d has four cells", rel, i+1)
			assert.Equal(t, wantFirst[i], row.cells[0], "%s row %d", rel, i+1)
			assert.NotEmpty(t, row.cells[2], "%s row %q: Prerequisites", rel, row.cells[0])
			links := markdownLinkTarget.FindAllStringSubmatch(row.cells[3], -1)
			require.NotEmpty(t, links, "%s row %q: the Guide cell holds a link", rel, row.cells[0])
			for _, link := range links {
				target := link[1]
				assert.NotContains(t, target, "://", "%s row %q: relative link", rel, row.cells[0])
				assert.False(t, strings.HasPrefix(target, "/"), "%s row %q: relative link %s", rel, row.cells[0], target)
			}
			guides = append(guides, row.cells[3])
		}
		assert.Contains(t, guides[0], "DOCKER.md#sqlite-flavor-no-database-server", rel)
		assert.Contains(t, guides[0], "SETUP.md", rel)
		assert.Contains(t, guides[1], "DOCKER.md#production-override-example", rel)
		assert.True(t, strings.Contains(guides[2], "SERVER_PROVISIONING.md") || strings.Contains(guides[2], "](#scope)"),
			"%s: the systemd row links SERVER_PROVISIONING.md", rel)
		assert.Contains(t, guides[3], "FORKING.md#52-release-tarball-and-systemd-through-the-shipped-workflows", rel)
	}
}

// operationsOwnerPages are the three owner pages of #795.
var operationsOwnerPages = []string{"CONFIGURATION", "BACKUP_RESTORE", "UPGRADING"}

// operationsDocFiles are the pages among which each topic has one owner.
var operationsDocFiles = []string{
	"README.md",
	"docs/README.md",
	"docs/SETUP.md",
	"docs/DOCKER.md",
	"docs/FORKING.md",
	"docs/SERVER_PROVISIONING.md",
	"docs/DEVELOPMENT.md",
	"docs/CONFIGURATION.md",
	"docs/BACKUP_RESTORE.md",
	"docs/UPGRADING.md",
}

// configurationVariables are the variables docs/CONFIGURATION.md must name.
var configurationVariables = strings.Fields(`
AEO_EGRESS_ALLOWED_ORIGINS AEO_EGRESS_LOCAL_ORIGINS AEO_SCHEDULE_ENABLED AEO_SCHEDULE_HOUR
API_KEY_SECRET API_PREFIX APP_BASE_URL AUTH_AUDIT_RETENTION_DAYS AUTOMATION_ENABLED
AUTOMATION_POLL_INTERVAL_SECONDS CORS_ALLOWED_ORIGINS DB_DRIVER DB_PATH DB_HOST DB_PORT DB_NAME
DB_USER DB_PASSWORD DISABLE_RATE_LIMIT FORM_RETENTION_DAYS JWT_SECRET JWT_EXPIRY_HOURS
REFRESH_TOKEN_EXPIRY_DAYS LOG_FORMAT LOG_LEVEL OIDC_ALLOW_PASSWORD OIDC_CLIENT_ID
OIDC_CLIENT_SECRET OIDC_ENABLED OIDC_ENTRA_APP_SIGNING_KEYS OIDC_GRAPH_MAX_PAGES OIDC_ISSUER_URL
OIDC_ROLE_MAPPING OIDC_SCOPES PUBLIC_BASE_URL QUOTE_PDF_CHROME_PATH RECAPTCHA_SECRET_KEY
RECAPTCHA_SITE_KEY SCIM_RATE_LIMIT_BURST SCIM_RATE_LIMIT_PER_MINUTE SERVER_HOST SERVER_MODE
SERVER_PORT SMTP_FROM SMTP_HOST SMTP_PASSWORD SMTP_PORT SMTP_USER TRUSTED_PROXIES
VITE_API_BASE_URL VITE_APP_NAME`)

// operationsTopicMarkers maps a phrase to the one page that may hold it; an
// empty owner means no page of operationsDocFiles may.
var operationsTopicMarkers = map[string]string{
	"VACUUM INTO":                 "docs/BACKUP_RESTORE.md",
	"chown 10001":                 "docs/BACKUP_RESTORE.md",
	"gunzip -c":                   "docs/BACKUP_RESTORE.md",
	"secrets it was written with": "docs/BACKUP_RESTORE.md",
	"invalidates every API key":   "docs/CONFIGURATION.md",
	"| `TRUSTED_PROXIES` |":       "docs/CONFIGURATION.md",
	"clamped to 1..3600":          "docs/CONFIGURATION.md",
	"-command force":              "docs/UPGRADING.md",
	"fk_tasks_deal":               "docs/UPGRADING.md",
	"section 4.2":                 "",
}

// TestOperationsOwnerPages pins #795 AC-2 and AC-3.
func TestOperationsOwnerPages(t *testing.T) {
	index := docLines(readRepositoryFile(t, "docs/README.md"))
	for _, name := range operationsOwnerPages {
		rel := "docs/" + name + ".md"
		text := readRepositoryFile(t, rel)
		forLines := 0
		for _, line := range docLines(text) {
			if strings.HasPrefix(line, "> **For:**") {
				forLines++
				assert.Contains(t, line, "Part of the [documentation index](README.md).", rel)
			}
		}
		assert.Equal(t, 1, forLines, "%s has one > **For:** header line", rel)

		bullet := "- [" + name + ".md](" + name + ".md)"
		bullets := 0
		for _, line := range index {
			if strings.HasPrefix(line, bullet) {
				bullets++
			}
		}
		assert.Equal(t, 1, bullets, "docs/README.md has exactly one bullet starting %q", bullet)
	}

	configuration := readRepositoryFile(t, "docs/CONFIGURATION.md")
	for _, name := range configurationVariables {
		assert.Contains(t, configuration, "`"+name+"`", "docs/CONFIGURATION.md names %s in backticks", name)
	}

	texts := map[string]string{}
	for _, rel := range operationsDocFiles {
		data, err := os.ReadFile(filepath.Join(repositoryRoot(t), rel))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err, rel)
		texts[rel] = string(data)
	}
	for phrase, owner := range operationsTopicMarkers {
		var holders []string
		for _, rel := range operationsDocFiles {
			if strings.Contains(texts[rel], phrase) {
				holders = append(holders, rel)
			}
		}
		if owner == "" {
			assert.Empty(t, holders, "no operations page holds %q", phrase)
			continue
		}
		assert.Equal(t, []string{owner}, holders, "only %s holds %q", owner, phrase)
	}
}

// TestOperationsProcedureOrder pins #795 AC-4.
func TestOperationsProcedureOrder(t *testing.T) {
	t.Run("SETUP runs in execution order with labelled terminals", func(t *testing.T) {
		lines := docLines(readRepositoryFile(t, "docs/SETUP.md"))
		last := -1
		for _, heading := range []string{
			"## Prerequisites", "## Configuration", "## Start",
			"## Create the first admin", "## Sign in", "## Verify",
		} {
			at := -1
			for i, line := range lines {
				if line == heading {
					require.Equal(t, -1, at, "docs/SETUP.md has %q twice", heading)
					at = i
				}
			}
			require.NotEqual(t, -1, at, "docs/SETUP.md lacks %q", heading)
			assert.Greater(t, at, last, "docs/SETUP.md: %q is out of order", heading)
			last = at
		}

		starters := []string{"make run", "go run ./cmd", "npm run dev", "create-admin"}
		labelled := 0
		for i := 0; i < len(lines); i++ {
			if !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				continue
			}
			open := i
			end := open + 1
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
				end++
			}
			require.Less(t, end, len(lines), "docs/SETUP.md: unterminated fence at line %d", open+1)
			body := strings.Join(lines[open+1:end], "\n")
			runs := false
			for _, s := range starters {
				if strings.Contains(body, s) {
					runs = true
				}
			}
			if runs {
				above := ""
				for j := open - 1; j >= 0; j-- {
					if strings.TrimSpace(lines[j]) != "" {
						above = lines[j]
						break
					}
				}
				assert.Contains(t, above, "Terminal", "docs/SETUP.md: the fence at line %d needs a terminal label", open+1)
				labelled++
			}
			i = end
		}
		assert.GreaterOrEqual(t, labelled, 3, "docs/SETUP.md: backend, frontend and create-admin blocks")
	})

	docker := readRepositoryFile(t, "docs/DOCKER.md")

	t.Run("DOCKER quick start creates the admin, opens the UI, then checks health", func(t *testing.T) {
		lines := docLines(guideSection(t, strings.ReplaceAll(docker, "\r\n", "\n"), "## Quick start"))
		first := func(needle string) int {
			for i, line := range lines {
				if strings.Contains(line, needle) {
					return i
				}
			}
			return -1
		}
		admin, open := first("create-admin"), first("localhost:3000")
		require.NotEqual(t, -1, admin, "Quick start runs create-admin")
		require.NotEqual(t, -1, open, "Quick start opens localhost:3000")
		assert.Less(t, admin, open, "create-admin comes before opening the UI")
		health := false
		for i := admin + 1; i < len(lines); i++ {
			if strings.Contains(lines[i], "/health") {
				health = true
			}
		}
		assert.True(t, health, "a /health check follows create-admin")
	})

	t.Run("the production override example opens with its requirements", func(t *testing.T) {
		lines := docLines(docker)
		start := -1
		for i, line := range lines {
			if line == "### Production override example" {
				start = i
			}
		}
		require.NotEqual(t, -1, start, "docs/DOCKER.md lacks the production override heading")
		i := start + 1
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		var block []string
		for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			block = append(block, lines[i])
		}
		text := strings.Join(block, "\n")
		for _, word := range []string{"2.24.4", "TLS", "APP_BASE_URL", "PUBLIC_BASE_URL", "CORS_ALLOWED_ORIGINS"} {
			assert.Contains(t, text, word, "the first block of the production override example")
		}
		assert.NotContains(t, text, "```", "the requirements come before any command")
	})
}
