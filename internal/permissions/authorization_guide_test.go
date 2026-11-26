package permissions

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// The guide tests pin docs/AUTHORIZATION.md to this package: the catalogue
// and matrix tables are compared with Leaves and ForSystemRole, the phase 2
// design note with the seam list of #259, and the pointers in the
// docs/README.md index, docs/FEATURES.md and CHANGELOG with the guide's path.

const (
	guideRepoRoot = "../.."
	guidePath     = "docs/AUTHORIZATION.md"
)

func readGuideFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(guideRepoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// guideSection returns the lines below the H2 heading up to the next H2, and
// whether the heading exists exactly once.
func guideSection(doc, heading string) ([]string, bool) {
	var out []string
	found, inside := 0, false
	for _, line := range strings.Split(doc, "\n") {
		switch {
		case line == heading:
			found++
			inside = true
		case strings.HasPrefix(line, "## "):
			inside = false
		case inside:
			out = append(out, line)
		}
	}
	return out, found == 1
}

func mustGuideSection(t *testing.T, doc, heading string) []string {
	t.Helper()
	lines, ok := guideSection(doc, heading)
	if !ok {
		t.Fatalf("%s must have the heading %q exactly once", guidePath, heading)
	}
	return lines
}

// guideKeyRows returns the table rows whose first cell is a backticked
// permission key, as the trimmed cells of each row.
func guideKeyRows(lines []string) [][]string {
	var rows [][]string
	for _, line := range lines {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	return rows
}

// guideRowsByKey indexes rows by their key and reports duplicated keys and
// keys the catalogue does not have.
func guideRowsByKey(t *testing.T, table string, rows [][]string) map[string][]string {
	t.Helper()
	byKey := map[string][]string{}
	for _, cells := range rows {
		key := strings.Trim(cells[0], "`")
		if _, dup := byKey[key]; dup {
			t.Errorf("%s: key %s has more than one row", table, key)
			continue
		}
		byKey[key] = cells
		if _, known := Leaf(key); !known {
			t.Errorf("%s: key %s is not in the catalogue", table, key)
		}
	}
	for _, node := range Leaves() {
		if _, ok := byKey[node.Key]; !ok {
			t.Errorf("%s: catalogue key %s has no row", table, node.Key)
		}
	}
	return byKey
}

func TestAuthorizationGuideCoversPhase2Seams(t *testing.T) {
	doc := readGuideFile(t, guidePath)

	for _, heading := range []string{
		"## Phased plan",
		"## Permission catalogue",
		"## System-role matrix",
		"## Phase 2 design note",
	} {
		if _, ok := guideSection(doc, heading); !ok {
			t.Errorf("%s must have the heading %q exactly once", guidePath, heading)
		}
	}

	plan := strings.Join(mustGuideSection(t, doc, "## Phased plan"), "\n")
	for _, ticket := range []string{"322", "344", "347", "348", "349", "260", "261"} {
		if !regexp.MustCompile(`#` + ticket + `\b`).MatchString(plan) {
			t.Errorf("the Phased plan section must name #%s", ticket)
		}
	}

	note := strings.Join(mustGuideSection(t, doc, "## Phase 2 design note"), "\n")
	for _, token := range []string{
		"OIDC",
		"PKCE",
		"user_identities",
		"(iss, sub)",
		"JIT provisioning",
		"group",
		"app role",
		"SCIM 2.0",
		"break-glass",
		"session-bound CSRF",
		"not implemented",
	} {
		t.Run(token, func(t *testing.T) {
			if !strings.Contains(note, token) {
				t.Errorf("the Phase 2 design note must contain %q", token)
			}
		})
	}
}

func TestAuthorizationGuideMatchesCatalogue(t *testing.T) {
	doc := readGuideFile(t, guidePath)

	t.Run("catalogue", func(t *testing.T) {
		rows := guideKeyRows(mustGuideSection(t, doc, "## Permission catalogue"))
		byKey := guideRowsByKey(t, "Permission catalogue", rows)
		for _, node := range Leaves() {
			cells, ok := byKey[node.Key]
			if !ok {
				continue
			}
			if len(cells) != 3 {
				t.Errorf("catalogue row %s has %d cells, want key, title and scopes", node.Key, len(cells))
				continue
			}
			if cells[1] != node.Title {
				t.Errorf("catalogue row %s: title %q, the catalogue says %q", node.Key, cells[1], node.Title)
			}
			want := "all"
			if node.AllowsOwn {
				want = "all, own"
			}
			if node.SystemOnly {
				want += " (system-only)"
			}
			if node.AdminOnly {
				want += " (admin-only)"
			}
			if cells[2] != want {
				t.Errorf("catalogue row %s: scopes %q, want %q", node.Key, cells[2], want)
			}
		}
	})

	t.Run("matrix", func(t *testing.T) {
		lines := mustGuideSection(t, doc, "## System-role matrix")
		const header = "| Permission | admin | sales | support | customer |"
		hasHeader := false
		for _, line := range lines {
			if line == header {
				hasHeader = true
			}
		}
		if !hasHeader {
			t.Errorf("the System-role matrix must have the header %q", header)
		}
		roles := []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer}
		byKey := guideRowsByKey(t, "System-role matrix", guideKeyRows(lines))
		for _, node := range Leaves() {
			cells, ok := byKey[node.Key]
			if !ok {
				continue
			}
			if len(cells) != 1+len(roles) {
				t.Errorf("matrix row %s has %d cells, want %d", node.Key, len(cells), 1+len(roles))
				continue
			}
			for i, role := range roles {
				want := string(ForSystemRole(string(role)).Scope(node.Key))
				if want == "" {
					want = "-"
				}
				if cells[i+1] != want {
					t.Errorf("matrix row %s, role %s: %q, the code says %q", node.Key, role, cells[i+1], want)
				}
			}
		}
	})
}

func TestAuthorizationGuideIsLinked(t *testing.T) {
	hasLine := func(doc string, match func(string) bool) bool {
		for _, line := range strings.Split(doc, "\n") {
			if match(line) {
				return true
			}
		}
		return false
	}

	// docs/README.md is the only docs index: the guide has exactly one bullet
	// there, and README.md lists it neither in a table row nor in a bullet,
	// which would make a second index.
	const indexLink = "[AUTHORIZATION.md](AUTHORIZATION.md)"
	indexBullets := 0
	for _, line := range strings.Split(readGuideFile(t, "docs/README.md"), "\n") {
		if strings.HasPrefix(line, "- "+indexLink) {
			indexBullets++
		}
	}
	if indexBullets != 1 {
		t.Errorf("docs/README.md must have exactly one bullet starting %q, found %d", "- "+indexLink, indexBullets)
	}
	readme := readGuideFile(t, "README.md")
	const link = "[docs/AUTHORIZATION.md](docs/AUTHORIZATION.md)"
	if hasLine(readme, func(l string) bool { return strings.HasPrefix(l, "- "+link) }) {
		t.Errorf("README.md must not have a bullet starting %q: docs/README.md is the index", "- "+link)
	}
	if hasLine(readme, func(l string) bool { return strings.HasPrefix(l, "| "+link) }) {
		t.Errorf("README.md must not have a table row starting %q: docs/README.md is the index", "| "+link)
	}

	features := readGuideFile(t, "docs/FEATURES.md")
	if !hasLine(features, func(l string) bool {
		return strings.HasPrefix(l, "| 11.1 |") && strings.Contains(l, "](AUTHORIZATION.md)")
	}) {
		t.Error("row 11.1 of docs/FEATURES.md must link ](AUTHORIZATION.md)")
	}

	// The entries since 1.2.0 (Unreleased and newer versions), cut into
	// bullets: a top-level "- " line with its continuation lines.
	var bullets []string
	inside := false
	for _, line := range strings.Split(readGuideFile(t, "CHANGELOG.md"), "\n") {
		switch {
		case strings.HasPrefix(line, "## [Unreleased]"):
			inside = true
		case strings.HasPrefix(line, "## [1.2.0]"):
			inside = false
		case !inside:
		case strings.HasPrefix(line, "- "):
			bullets = append(bullets, line)
		case strings.HasPrefix(line, "#"):
			bullets = append(bullets, "")
		case len(bullets) > 0:
			bullets[len(bullets)-1] += "\n" + line
		}
	}
	named := false
	for _, bullet := range bullets {
		if strings.Contains(bullet, guidePath) && regexp.MustCompile(`#349\b`).MatchString(bullet) {
			named = true
		}
	}
	if !named {
		t.Errorf("CHANGELOG.md needs a bullet since 1.2.0 naming %s and #349", guidePath)
	}

	guide := readGuideFile(t, guidePath)
	targets := regexp.MustCompile(`\]\(([A-Za-z0-9_./-]+\.md)`).FindAllStringSubmatch(guide, -1)
	if len(targets) == 0 {
		t.Errorf("%s links no other guide", guidePath)
	}
	for _, target := range targets {
		rel := "docs/" + target[1]
		if _, err := os.Stat(filepath.Join(guideRepoRoot, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s links %s, which does not exist: %v", guidePath, target[1], err)
		}
	}
}

// The integration-user section (#211) names the setup calls, the keys of the
// two intake cases, rotation, revocation and the suite that proves it. Every
// permission key it names in backticks must be a leaf of the catalogue.
func TestAuthorizationGuideCoversIntegrationUser(t *testing.T) {
	doc := readGuideFile(t, guidePath)
	section := strings.Join(mustGuideSection(t, doc, "## Least-privilege integration user"), "\n")

	for _, token := range []string{
		"customers.create",
		"tickets.create",
		"customers.view",
		"assigned_to_id",
		"assignment.ticket_assignee",
		"leads.create",
		"assignment.form_owner",
		"leads.assign_owner",
		"POST /roles",
		"POST /users",
		"POST /api-keys",
		"DELETE /api-keys/{id}",
		"/deactivate",
		"expires_at",
		"TestLeastPrivilegeIntegrationUser",
	} {
		t.Run(token, func(t *testing.T) {
			if !strings.Contains(section, token) {
				t.Errorf("the Least-privilege integration user section must contain %q", token)
			}
		})
	}

	keys := regexp.MustCompile("`([a-z_]+\\.[a-z_]+)`").FindAllStringSubmatch(section, -1)
	if len(keys) == 0 {
		t.Error("the Least-privilege integration user section names no permission key in backticks")
	}
	for _, key := range keys {
		if _, known := Leaf(key[1]); !known {
			t.Errorf("the Least-privilege integration user section names %s, which is not in the catalogue", key[1])
		}
	}

	// The section sits directly before the design note.
	order := regexp.MustCompile(`(?m)^## .*$`).FindAllString(doc, -1)
	for i, heading := range order {
		if heading == "## Phase 2 design note" && (i == 0 || order[i-1] != "## Least-privilege integration user") {
			t.Error("the Least-privilege integration user section must come directly before the Phase 2 design note")
		}
	}
}

// TestAuthorizationGuideStatesPermissionModel pins the guide's statements
// about where the system role still decides, the own-account rule on
// /users/{id}, the POST /api-keys rules, the registration routes, the role
// routes and codes, the sign-in audit aggregation and the SCIM and single
// sign-on deliveries since #506.
func TestAuthorizationGuideStatesPermissionModel(t *testing.T) {
	doc := readGuideFile(t, guidePath)
	spaces := regexp.MustCompile(`\s+`)
	section := func(heading string) string {
		return spaces.ReplaceAllString(strings.Join(mustGuideSection(t, doc, heading), "\n"), " ")
	}
	contains := func(heading, text string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s %q must contain %q", guidePath, heading, w)
			}
		}
	}
	lacks := func(heading, text string, unwanted ...string) {
		t.Helper()
		for _, u := range unwanted {
			if strings.Contains(text, u) {
				t.Errorf("%s %q must not contain %q", guidePath, heading, u)
			}
		}
	}

	scope := section("## Service-side scope")
	contains("## Service-side scope", scope, "aggregateScope", "quoteCreateRole")
	lacks("## Service-side scope", scope, "no system role is passed through")

	contains("## Deny by default", section("## Deny by default"),
		"GET /users/{id}", "PUT /users/{id}", "REAUTHENTICATION_REQUIRED",
		"#least-privilege-integration-user", "POST /auth/register", "GET /auth/registration",
		"RequireRegistrationEnabled")

	contains("## Custom roles and scopes", section("## Custom roles and scopes"),
		"GET /roles/{id}", "PUT /roles/{id}", "DELETE /roles/{id}", "ROLE_NOT_FOUND",
		"ROLE_NAME_TAKEN", "ROLE_IN_USE", "INVALID_PERMISSION", "INVALID_ROLE_ASSIGNMENT")

	// The migration-rows paragraph stands on its own, not inside the list
	// bullet before it.
	audit := mustGuideSection(t, doc, "## Audit trail")
	found := false
	for i, line := range audit {
		if !strings.Contains(line, "The `migration` rows of #524") {
			continue
		}
		found = true
		if !strings.HasPrefix(line, "The ") {
			t.Errorf("%s: the line naming the migration rows of #524 must start a paragraph, got %q", guidePath, line)
		}
		if i == 0 || strings.TrimSpace(audit[i-1]) != "" {
			t.Errorf("%s: the paragraph naming the migration rows of #524 must follow an empty line", guidePath)
		}
	}
	if !found {
		t.Errorf("%s \"## Audit trail\" must name the migration rows of #524", guidePath)
	}

	signIns := section("## Sign-in audit log")
	lacks("## Sign-in audit log", signIns,
		"on an `api_key` row, NULL on every other row",
		"for an `api_key` row the number of refusals it counts")
	columns := "Each row carries exactly the stored columns"
	start := strings.Index(signIns, columns)
	if start < 0 {
		t.Errorf("%s \"## Sign-in audit log\" must contain %q", guidePath, columns)
	} else {
		rest := signIns[start:]
		if end := strings.Index(rest, "`aggregate_key`"); end >= 0 {
			rest = rest[:end]
		}
		if !strings.Contains(rest, "`api_key_id`") {
			t.Errorf("%s: the columns GET /audit/sign-ins returns must include `api_key_id`", guidePath)
		}
	}

	plan := mustGuideSection(t, doc, "## Phased plan")
	contains("## Phased plan", spaces.ReplaceAllString(strings.Join(plan, "\n"), " "),
		"#630", "#633", "#634", "#719")
	phase2 := false
	for _, line := range plan {
		if !strings.HasPrefix(line, "| Phase 2 |") {
			continue
		}
		phase2 = true
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if last := strings.TrimSpace(cells[len(cells)-1]); strings.HasPrefix(last, "Open") {
			t.Errorf("%s: the Phase 2 row's status must not start with Open, got %q", guidePath, last)
		}
	}
	if !phase2 {
		t.Errorf("%s \"## Phased plan\" must have a Phase 2 row", guidePath)
	}

	contains("## SCIM provisioning", section("## SCIM provisioning"), "#630")

	note := section("## Phase 2 design note")
	lacks("## Phase 2 design note", note,
		"creating, renaming or deleting a group through SCIM are not implemented")
	if i := strings.Index(note, "### Design conditions"); i < 0 {
		t.Errorf("%s \"## Phase 2 design note\" must have \"### Design conditions\"", guidePath)
	} else {
		conditions := note[i:]
		lacks("### Design conditions", conditions,
			"one or more configured issuers", "in a table an administrator edits")
		contains("### Design conditions", conditions,
			"security.sso.issuer_url", "security.sso.role_mapping")
	}
}
