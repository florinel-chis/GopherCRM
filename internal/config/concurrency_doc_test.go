package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin docs/CONCURRENCY.md (#323) to the code it
// describes: its structure, the status and the key facts of every flow, the
// lock chains, the retry rule, the #175 failure modes, every cited path and
// line, the flow -> ticket map, and the links that point to it from
// EXTENDING, DEVELOPMENT, ROADMAP, README and CHANGELOG. Every citation of
// the auth service files (internal/service/auth_service.go and the per-flow
// files split from it, #639) is pinned to its file, its enclosing function
// and a text fragment of the cited line (#581); citations of other files are
// only checked to lie inside their file. Where a fact is a property of the code (a
// lock that is or is not taken, a helper that is or is not used) the code is
// read too, so the document and the code cannot drift apart silently.

const concurrencyDocFile = "docs/CONCURRENCY.md"

var concurrencyDocSectionsInOrder = []string{
	"Global lock order",
	"Retry rule",
	"Flows",
	"How the #175 failure modes are avoided",
	"Flow to ticket map",
}

var concurrencyDocFlowsInOrder = []string{
	"User-rooted erasure",
	"Customer-rooted erasure",
	"Lead-rooted erasure",
	"Lead conversion",
	"Lead update",
	"Company delete",
	"Bulk status",
	"Login",
	"Refresh rotation",
	"Password-reset request",
	"Password-reset confirm",
	"API-key create",
	"RBAC user-role and role writes",
	"OIDC sign-in",
	"SCIM user create and replace",
	"SCIM user patch",
	"SCIM group patch",
}

var concurrencyDocFieldLabels = []string{"Rows locked", "Order", "Transaction", "Retry", "Status"}

// concurrencyDocExpectedStatus is the status of every flow at a415cf1, with
// Login implemented by #336 (C2), the password-reset request and API-key
// create by #342 (C3) and the three erasures by #385 (C4).
var concurrencyDocExpectedStatus = map[string]string{
	"User-rooted erasure":            "implemented",
	"Customer-rooted erasure":        "implemented",
	"Lead-rooted erasure":            "implemented",
	"Lead conversion":                "implemented",
	"Lead update":                    "implemented",
	"Company delete":                 "implemented",
	"Bulk status":                    "implemented",
	"Login":                          "implemented",
	"Refresh rotation":               "implemented",
	"Password-reset request":         "implemented",
	"Password-reset confirm":         "implemented",
	"API-key create":                 "implemented",
	"RBAC user-role and role writes": "implemented",
	"OIDC sign-in":                   "implemented",
	"SCIM user create and replace":   "implemented",
	"SCIM user patch":                "implemented",
	"SCIM group patch":               "implemented",
}

// concurrencyDocRetryFlows are the flows whose Retry line names
// WithTransactionAndRetry, with the retry bound constant each cites, the file
// that defines it and, when the constant is not a `const name = 3` line of its
// own, the declaration as the file writes it.
var concurrencyDocRetryFlows = map[string]struct{ constant, file, declaration string }{
	"Lead conversion":        {"", "", ""},
	"Login":                  {"loginMaxRetries", "internal/service/auth_login.go", ""},
	"Password-reset request": {"resetRequestMaxRetries", "internal/service/auth_password_reset.go", ""},
	// #407: the confirm is one replayed root as well.
	"Password-reset confirm": {"resetConfirmMaxRetries", "internal/service/auth_password_reset.go", ""},
	"API-key create":         {"apiKeyCreateMaxRetries", "internal/service/apikey_service.go", ""},
	// #348: every governed user write and role write is one replayed root.
	"RBAC user-role and role writes": {"roleWriteMaxRetries", "internal/service/role_governance.go", ""},
	// #435: the OIDC sign-in is a governed write.
	"OIDC sign-in": {"roleWriteMaxRetries", "internal/service/role_governance.go", ""},
	// #470: the SCIM user create and replace are governed writes.
	"SCIM user create and replace": {"roleWriteMaxRetries", "internal/service/role_governance.go", ""},
	// #471: the SCIM user patch is a governed write.
	"SCIM user patch": {"roleWriteMaxRetries", "internal/service/role_governance.go", ""},
	// #506: the SCIM group patch is a governed write.
	"SCIM group patch": {"roleWriteMaxRetries", "internal/service/role_governance.go", ""},
	// #385: every erasure root is one replayed transaction (a const block).
	"User-rooted erasure":     {"erasureMaxRetries", concurrencyDocErasureFile, concurrencyDocErasureRetryDecl},
	"Customer-rooted erasure": {"erasureMaxRetries", concurrencyDocErasureFile, concurrencyDocErasureRetryDecl},
	"Lead-rooted erasure":     {"erasureMaxRetries", concurrencyDocErasureFile, concurrencyDocErasureRetryDecl},
}

const (
	concurrencyDocErasureFile      = "internal/repository/erasure.go"
	concurrencyDocErasureRetryDecl = "\terasureMaxRetries       = 3\n"
	// concurrencyDocErasureOrder is the one lock order of every erasure (#385).
	concurrencyDocErasureOrder = "automation runs → leads → customers → users → refresh_tokens → password_reset_tokens → api_keys"
)

var concurrencyDocExpectedTicket = map[string]string{
	"User-rooted erasure":            "#385",
	"Customer-rooted erasure":        "#385",
	"Lead-rooted erasure":            "#385",
	"Lead conversion":                "#182",
	"Lead update":                    "#182",
	"Company delete":                 "#182",
	"Bulk status":                    "#182, #242, #558",
	"Login":                          "C2",
	"Refresh rotation":               "C2",
	"Password-reset request":         "C3",
	"Password-reset confirm":         "C3",
	"API-key create":                 "C3",
	"RBAC user-role and role writes": "#348",
	"OIDC sign-in":                   "#435",
	"SCIM user create and replace":   "#470",
	"SCIM user patch":                "#471",
	"SCIM group patch":               "#506",
}

type concurrencyDocSection struct {
	heading string
	body    string
}

// concurrencyDocNormalize collapses every whitespace run to one space, so a
// phrase wrapped across lines still matches.
func concurrencyDocNormalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// concurrencyDocSplit splits text at lines that start with prefix ("## " or
// "### "). Text before the first heading is dropped.
func concurrencyDocSplit(text, prefix string) []concurrencyDocSection {
	var sections []concurrencyDocSection
	var current *concurrencyDocSection
	var body []string
	flush := func() {
		if current != nil {
			current.body = strings.Join(body, "\n")
			sections = append(sections, *current)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			flush()
			current = &concurrencyDocSection{heading: strings.TrimSpace(strings.TrimPrefix(line, prefix))}
			body = nil
			continue
		}
		if current != nil {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

func concurrencyDocText(t *testing.T) string {
	t.Helper()
	return readRepositoryFile(t, concurrencyDocFile)
}

func concurrencyDocSectionBody(t *testing.T, heading string) string {
	t.Helper()
	for _, s := range concurrencyDocSplit(concurrencyDocText(t), "## ") {
		if s.heading == heading {
			return s.body
		}
	}
	t.Fatalf("%s has no section %q", concurrencyDocFile, heading)
	return ""
}

// concurrencyDocFlowBodies returns the body of every level-3 subsection of
// ## Flows, keyed by its heading.
func concurrencyDocFlowBodies(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range concurrencyDocSplit(concurrencyDocSectionBody(t, "Flows"), "### ") {
		out[s.heading] = s.body
	}
	return out
}

// concurrencyDocBullets returns every top-level bullet of text: a line that
// starts with "- " and the lines indented under it.
func concurrencyDocBullets(text string) []string {
	var bullets []string
	var current []string
	flush := func() {
		if current != nil {
			bullets = append(bullets, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "- "):
			flush()
			current = []string{line}
		case current != nil && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			current = append(current, line)
		default:
			flush()
		}
	}
	flush()
	return bullets
}

// concurrencyDocFields parses the labelled bullets of a flow subsection
// ("- **Label:** value"), each value normalized. A label that appears twice
// is reported through dup.
func concurrencyDocFields(body string) (fields map[string]string, dup []string) {
	fields = map[string]string{}
	for _, bullet := range concurrencyDocBullets(body) {
		for _, label := range concurrencyDocFieldLabels {
			prefix := "- **" + label + ":**"
			if !strings.HasPrefix(bullet, prefix) {
				continue
			}
			if _, seen := fields[label]; seen {
				dup = append(dup, label)
			}
			fields[label] = concurrencyDocNormalize(strings.TrimPrefix(bullet, prefix))
		}
	}
	return fields, dup
}

func concurrencyDocFlowFields(t *testing.T) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for flow, body := range concurrencyDocFlowBodies(t) {
		fields, _ := concurrencyDocFields(body)
		out[flow] = fields
	}
	return out
}

// concurrencyDocParagraph returns the lines from the first line starting with
// start up to the next blank line.
func concurrencyDocParagraph(t *testing.T, text, start, file string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, start) {
			continue
		}
		j := i
		for j < len(lines) && strings.TrimSpace(lines[j]) != "" {
			j++
		}
		return strings.Join(lines[i:j], "\n")
	}
	t.Fatalf("%s has no paragraph starting %q", file, start)
	return ""
}

// concurrencyDocSentences splits normalized text into sentences at '.', '!'
// or '?' followed by a space or the end.
func concurrencyDocSentences(text string) []string {
	text = concurrencyDocNormalize(text)
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if i+1 == len(text) || text[i+1] == ' ' {
			out = append(out, strings.TrimSpace(text[start:i+1]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(text[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// concurrencyDocFuncBody returns the source of the function whose declaration
// starts with signature, up to its closing brace at column 0.
func concurrencyDocFuncBody(t *testing.T, rel, signature string) string {
	t.Helper()
	src := readRepositoryFile(t, rel)
	idx := strings.Index(src, signature)
	require.GreaterOrEqual(t, idx, 0, "%s declares %q", rel, signature)
	end := strings.Index(src[idx:], "\n}\n")
	require.GreaterOrEqual(t, end, 0, "%s: end of %q", rel, signature)
	return src[idx : idx+end]
}

func TestConcurrencyDocSections(t *testing.T) {
	text := concurrencyDocText(t)

	var headings []string
	for _, s := range concurrencyDocSplit(text, "## ") {
		headings = append(headings, s.heading)
	}
	assert.Equal(t, concurrencyDocSectionsInOrder, headings, "level-2 headings of %s", concurrencyDocFile)

	var flows []string
	for _, s := range concurrencyDocSplit(concurrencyDocSectionBody(t, "Flows"), "### ") {
		flows = append(flows, s.heading)
	}
	assert.Equal(t, concurrencyDocFlowsInOrder, flows, "level-3 headings under ## Flows")
}

func TestConcurrencyDocFlowFields(t *testing.T) {
	bodies := concurrencyDocFlowBodies(t)
	for _, flow := range concurrencyDocFlowsInOrder {
		body, ok := bodies[flow]
		require.True(t, ok, "flow %q is missing", flow)
		fields, dup := concurrencyDocFields(body)
		assert.Empty(t, dup, "%s repeats a labelled line", flow)
		for _, label := range concurrencyDocFieldLabels {
			value, ok := fields[label]
			if assert.True(t, ok, "%s has no `- **%s:**` line", flow, label) {
				assert.NotEmpty(t, value, "%s: %s is empty", flow, label)
			}
		}
		assert.Contains(t, []string{"implemented", "partial", "not implemented"}, fields["Status"],
			"%s: Status must be implemented, partial or not implemented", flow)
	}
}

func TestConcurrencyDocFlowFacts(t *testing.T) {
	bodies := concurrencyDocFlowBodies(t)
	fields := concurrencyDocFlowFields(t)
	flow := func(name string) string { return concurrencyDocNormalize(bodies[name]) }

	for name, want := range concurrencyDocExpectedStatus {
		assert.Equal(t, want, fields[name]["Status"], "status of %s", name)
	}

	// Lead writes: the restart bound, which the code still defines.
	for _, name := range []string{"Lead conversion", "Lead update"} {
		assert.Contains(t, flow(name), "leadCompanyMaxRestarts", name)
	}
	assert.Contains(t, readRepositoryFile(t, "internal/service/lead_company_lock.go"), "const leadCompanyMaxRestarts = 3")
	var leadService strings.Builder
	for _, file := range []string{
		"internal/service/lead_service.go",
		"internal/service/lead_company_lock.go",
		"internal/service/lead_conversion.go",
		"internal/service/lead_queries.go",
		"internal/service/lead_stats.go",
		"internal/service/lead_export.go",
	} {
		leadService.WriteString(readRepositoryFile(t, file))
	}
	assert.Equal(t, 1, strings.Count(leadService.String(), "WithTransactionAndRetry("),
		"only lead conversion uses WithTransactionAndRetry in the lead service files")

	// Bulk status: tasks are locked in id order (#558), and the single task
	// update locks the same row before its completed-task re-check.
	bulk := flow("Bulk status")
	for _, want := range []string{
		"tasks, `FOR UPDATE` in id ascending order (`GetTasksByIDs`",
		"#558",
		"its own `bulk_operations` row (`CompletePending`)",
		"so it can invert no cross-table order",
		"The found, assignee and completed-task checks",
		"judge the locked rows",
		"The single task update (`taskService.UpdateWithLabels`, `PUT /tasks/{id}`) is one transaction that locks the same row (`GetByIDForUpdate`",
		"before its completed-task re-check and the write",
	} {
		assert.Contains(t, bulk, want, "Bulk status")
	}
	for _, gone := range []string{"reads the tasks without a lock", "tasks only at the write", "not protected by a row lock"} {
		assert.NotContains(t, bulk, gone, "Bulk status")
	}
	const bulkRepo = "internal/repository/bulk_status_repository.go"
	assert.Contains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetTasksByIDs("), "clause.Locking",
		"GetTasksByIDs is documented as a locking read")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/repository/task_repository.go", "func (r *taskRepository) GetByIDForUpdate("), "clause.Locking")
	taskUpdate := concurrencyDocFuncBody(t, "internal/service/task_service.go", "func (s *taskService) writeLocked(")
	assert.Contains(t, taskUpdate, "GetByIDForUpdate(")
	assert.Contains(t, taskUpdate, "TaskStatusCompleted")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/service/task_service.go", "func (s *taskService) UpdateWithLabels("), "s.writeLocked(")
	assert.Contains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetLeadsByIDs("), "clause.Locking")
	assert.Contains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetTicketsByIDs("), "clause.Locking")
	taskBatch := concurrencyDocFuncBody(t, "internal/service/bulk_status_service.go", "func (s *bulkStatusService) BulkSetTaskStatus(")
	assert.Contains(t, taskBatch, "txRepo.GetTasksByIDs(")
	assert.Contains(t, taskBatch, "TaskStatusCompleted")
	assert.Contains(t, taskBatch, "txRepo.SetTaskStatus(")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/service/bulk_status_service.go", "func (s *bulkStatusService) runBulkStatusUpdate("),
		"CompletePending(")

	const authService = "internal/service/auth_service.go"
	const authLogin = "internal/service/auth_login.go"
	const authRefresh = "internal/service/auth_refresh.go"
	const authPasswordReset = "internal/service/auth_password_reset.go"
	const apiKeyService = "internal/service/apikey_service.go"

	// Password-reset request and API-key create (#342, C3): one issuing
	// transaction under the retry rule that locks the owner row first,
	// users → password_reset_tokens and users → api_keys.
	for _, c := range []struct{ flow, chain string }{
		{"Password-reset request", "users → password_reset_tokens"},
		{"API-key create", "users → api_keys"},
	} {
		body := flow(c.flow)
		for _, want := range []string{c.chain, "WithTransactionAndRetry", "FOR UPDATE", "lockLiveCredentialOwner",
			"erased", "inactive", "context.Background()"} {
			assert.Contains(t, body, want, c.flow)
		}
		assert.NotContains(t, body, "no transaction", c.flow)
		assert.NotContains(t, body, "no lock held across statements", c.flow)
	}
	request := flow("Password-reset request")
	for _, want := range []string{
		"`GetByEmail`",
		"`InvalidateAllForUser`",
		"after the commit",
		"password_reset_tokens → users",
		"`ErrInvalidResetToken`",
		"1213",
		"a request victim is replayed",
		"a confirm victim is replayed by the same rule (`resetConfirmMaxRetries`) and refused with `ErrInvalidResetToken`",
	} {
		assert.Contains(t, request, want, "Password-reset request")
	}
	apiKey := flow("API-key create")
	for _, want := range []string{"apiKeyService.Generate", "`ErrAccountUnavailable`", "401",
		"`ErrAPIKeyIssuanceUnavailable`", "`ValidateAPIKey`"} {
		assert.Contains(t, apiKey, want, "API-key create")
	}

	owner := concurrencyDocFuncBody(t, authService, "func lockLiveCredentialOwner(")
	assert.Contains(t, owner, "userRepo.GetByID(userID)")
	assert.Contains(t, owner, "!owner.IsActive")
	assert.Contains(t, owner, "apperrors.IsNotFound(err)")

	// Every locking owner read goes through the one helper, which carries the
	// clause on a fresh Session.
	lockHelper := concurrencyDocFuncBody(t, "internal/repository/locking.go", "func LockingRead(")
	assert.Contains(t, lockHelper, `clause.Locking{Strength: "UPDATE"}`)
	assert.Contains(t, lockHelper, ".Session(&gorm.Session{})")

	reset := concurrencyDocFuncBody(t, authPasswordReset, "func (s *authService) IssuePasswordReset(")
	assert.Contains(t, reset, "s.txManager.WithTransactionAndRetry(context.Background(),")
	assert.Contains(t, reset, "repository.LockingRead(tx)")
	assert.Contains(t, reset, "resetRequestMaxRetries)")
	assert.Contains(t, reset, "s.resetTokenRepo.WithTx(tx)")
	resetLock := strings.Index(reset, "lockLiveCredentialOwner(")
	resetSpend := strings.Index(reset, "resetRepo.InvalidateAllForUser(")
	resetInsert := strings.Index(reset, "resetRepo.Create(")
	resetMail := strings.Index(reset, "s.mailer.SendPasswordReset(")
	assert.True(t, resetLock >= 0 && resetSpend > resetLock && resetInsert > resetSpend && resetMail > resetInsert,
		"RequestPasswordReset locks the owner, spends the old tokens, inserts, then mails")
	assert.NotContains(t, reset, "s.resetTokenRepo.Create(", "no insert outside the transaction")
	assert.NotContains(t, reset, "s.resetTokenRepo.InvalidateAllForUser(", "no invalidation outside the transaction")
	assert.Contains(t, readRepositoryFile(t, authPasswordReset), "const resetRequestMaxRetries = 3")

	generate := concurrencyDocFuncBody(t, apiKeyService, "func (s *apiKeyService) Generate(")
	assert.Contains(t, generate, "s.txManager.WithTransactionAndRetry(context.Background(),")
	assert.Contains(t, generate, "repository.LockingRead(tx)")
	assert.Contains(t, generate, "apiKeyCreateMaxRetries)")
	assert.Contains(t, generate, "ErrAPIKeyIssuanceUnavailable")
	keyLock := strings.Index(generate, "lockLiveCredentialOwner(")
	keyInsert := strings.Index(generate, "s.apiKeyRepo.WithTx(tx).Create(")
	assert.True(t, keyLock >= 0 && keyInsert > keyLock, "Generate locks the owner, then inserts through WithTx")
	assert.NotContains(t, generate, "s.apiKeyRepo.Create(", "no insert outside the transaction")
	assert.Contains(t, readRepositoryFile(t, apiKeyService), "const apiKeyCreateMaxRetries = 3")
	assert.Contains(t, readRepositoryFile(t, "cmd/main.go"),
		"service.NewAPIKeyService(apiKeyRepo, userRepo, txManager, cfg.API.APIKeySecret)")

	// Login (#336, C2): one issuing transaction under the retry rule that locks
	// the owner row first, users → refresh_tokens.
	login := flow("Login")
	for _, want := range []string{
		"users → refresh_tokens",
		"WithTransactionAndRetry",
		"FOR UPDATE",
		"`GetByEmail`",
		"bcrypt",
		"savepoint",
		// #457: while security.sso.allow_password is off the login reads
		// user_identities (plain, no lock) after the owner lock.
		"users → (read) user_identities → refresh_tokens",
		"`ExistsForUser`",
	} {
		assert.Contains(t, login, want, "Login")
	}
	assert.NotContains(t, login, "no transaction", "Login")
	assert.NotContains(t, login, "no lock held across statements", "Login")
	authenticate := concurrencyDocFuncBody(t, authLogin, "func (s *authService) authenticate(")
	assert.Contains(t, authenticate, "s.txManager.WithTransactionAndRetry(")
	assert.Contains(t, authenticate, "repository.LockingRead(tx)")
	assert.Contains(t, authenticate, "s.loginInTransaction(")
	assert.Contains(t, concurrencyDocFuncBody(t, authLogin, "func (s *authService) LoginWithTokens("), "s.authenticate(")
	assert.Contains(t, concurrencyDocFuncBody(t, authLogin, "func (s *authService) Login("), "s.authenticate(")
	issuing := concurrencyDocFuncBody(t, authLogin, "func (s *authService) loginInTransaction(")
	lockAt := strings.Index(issuing, "userRepo.GetByID(")
	insertAt := strings.Index(issuing, "s.issueRefreshToken(")
	signAt := strings.Index(issuing, "s.accessTokenFor(")
	assert.True(t, lockAt >= 0 && insertAt > lockAt && signAt > insertAt,
		"loginInTransaction locks the owner, then inserts the refresh token, then signs")
	// The rotation is one plain root; the confirm is replayed (#407).
	for _, c := range []struct{ file, sig, root string }{
		{authRefresh, "func (s *authService) RefreshAccessToken(", "s.txManager.WithTransaction("},
		{authPasswordReset, "func (s *authService) CompletePasswordReset(", "s.txManager.WithTransactionAndRetry("},
	} {
		sig := c.sig
		body := concurrencyDocFuncBody(t, c.file, sig)
		assert.Contains(t, body, c.root, sig)
		assert.Contains(t, body, "repository.LockingRead(tx)", sig)
	}
	assert.Contains(t, concurrencyDocFuncBody(t, authPasswordReset, "func (s *authService) CompletePasswordReset("),
		"}, resetConfirmMaxRetries)")
	// Child inserts under the owner lock: the foreign-key check's shared lock
	// falls on an owner row the issuing transaction already holds.
	for _, fk := range []struct{ flow, constraint, migration string }{
		{"Password-reset request", "fk_password_reset_tokens_user", "migrations/20261001120000_v1_2_0_schema_parity.up.sql"},
		{"API-key create", "fk_api_keys_user", "migrations/20250806120000_initial_schema.up.sql"},
	} {
		body := flow(fk.flow)
		assert.Contains(t, body, "takes its shared lock on an owner row this transaction already holds", fk.flow)
		assert.Contains(t, body, "`"+fk.constraint+"`", fk.flow)
		assert.NotContains(t, body, "for the insert statement only", fk.flow)
		assert.Contains(t, readRepositoryFile(t, fk.migration),
			"CONSTRAINT "+fk.constraint+" FOREIGN KEY (user_id) REFERENCES users(id)", fk.flow)
	}

	assert.Contains(t, flow("Refresh rotation"), "users → refresh_tokens")
	// #444, #457: both reset flows read user_identities (plain, no lock)
	// after the owner lock and, while security.sso.allow_password is off,
	// refuse an account linked to single sign-on.
	// #526: the confirm ends every credential, refresh tokens then API keys.
	assert.Contains(t, flow("Password-reset confirm"), "password_reset_tokens → users → (read) user_identities → refresh_tokens → api_keys")
	assert.Contains(t, flow("Password-reset request"), "users → (read) user_identities → password_reset_tokens")
	assert.Contains(t, flow("Password-reset request"), "`ExistsForUser`")
	assert.Contains(t, flow("Password-reset confirm"), "`ExistsForUser`")

	// RBAC (#348): every governed write is one replayed root that takes the
	// admin set, then users, then roles, and decides after the locks.
	rbac := flow("RBAC user-role and role writes")
	for _, want := range []string{
		"`LockActiveAdmins`", "`LockLiveByIDs`", "`RoleRepository.LockByIDs`", "`LockLiveRoleHolders`",
		"admin set → users (id ascending) → roles (id ascending)",
		"users before roles",
		"decided after the locks",
		"`role_audit_log`",
	} {
		assert.Contains(t, rbac, want, "RBAC flow")
	}
	assert.NotContains(t, rbac, "has no caller")
	assert.NotContains(t, rbac, "Target")

	// The root: WithTransactionAndRetry with the bound the Retry line cites,
	// inside the restart loop of a nested erasure whose graph changed (#385).
	const governance = "internal/service/role_governance.go"
	runOnce := concurrencyDocFuncBody(t, governance, "func (g *roleGovernance) runOnce(")
	assert.Contains(t, runOnce, "g.txManager.WithTransactionAndRetry(")
	assert.Contains(t, runOnce, "roleWriteMaxRetries")
	run := concurrencyDocFuncBody(t, governance, "func (g *roleGovernance) run(")
	assert.Contains(t, run, "g.runOnce(ctx, fn)")
	assert.Contains(t, run, "shouldRestartGovernedWrite(ctx, err)")
	assert.Contains(t, run, "restarts >= roleWriteGraphMaxRestarts")
	assert.NotContains(t, run, "WithTransactionAndRetry(", "the deadlock replay stays WithTransactionAndRetry's own, in runOnce")
	restartGoverned := concurrencyDocFuncBody(t, governance, "func shouldRestartGovernedWrite(")
	for _, want := range []string{"repository.ErrErasureGraphChanged", "utils.ErrRollbackFailed", "utils.ErrTransactionPoisoned",
		"utils.ErrCommitFailed", "utils.ErrBeginFailed", "ctx.Err() == nil"} {
		assert.Contains(t, restartGoverned, want, "shouldRestartGovernedWrite")
	}
	assert.Contains(t, readRepositoryFile(t, governance), "const roleWriteGraphMaxRestarts = 3")
	for _, want := range []string{"`ErrErasureGraphChanged`", "`roleWriteGraphMaxRestarts`", "`shouldRestartGovernedWrite`",
		"the one inversion of the erasure order (#385)", "`governedWrite` is not reordered"} {
		assert.Contains(t, rbac, want, "RBAC flow")
	}

	// The governed user writes: admin set, then users, then roles, inside it.
	inOrder := func(body string, statements ...string) {
		t.Helper()
		last := -1
		for _, statement := range statements {
			at := strings.Index(body, statement)
			if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
				assert.Greater(t, at, last, "%q comes after the statement before it", statement)
				last = at
			}
		}
	}
	for _, signature := range []string{"func (s *userService) governedWrite(", "func (s *userService) CreateAs("} {
		body := concurrencyDocFuncBody(t, "internal/service/user_governed_writes.go", signature)
		inOrder(body, "s.governance.run(ctx,", "lockAdminSet(tx)", "tx.lockUsers(", "tx.lockRoles(", "tx.actor(",
			"acting.requireKeys(", "checkEscalation(")
	}
	// #638: every governed write takes the admin set through the one guard.
	inOrder(concurrencyDocFuncBody(t, "internal/service/last_admin_guard.go", "func lockAdminSet("),
		"tx.userRoles.LockActiveAdmins()")
	governed := concurrencyDocFuncBody(t, "internal/service/user_governed_writes.go", "func (s *userService) governedWrite(")
	inOrder(governed, "checkEscalation(", "apperrors.ErrCustomerLoginRole", "apperrors.ErrLastAdmin", "tx.users.Delete(ctx, id)",
		"tx.users.UpdateFields(id, fields)", "tx.record(")
	// #526: a deactivation ends every credential after the users write and
	// before the role audit row.
	inOrder(governed, "tx.users.UpdateFields(id, fields)", "revokeAllCredentials(", "tx.record(")
	assert.Contains(t, rbac, "refresh_tokens → api_keys", "RBAC flow: the deactivation's credential tables")
	// The helper writes in the documented order users → refresh_tokens →
	// api_keys: endSessions writes users then refresh_tokens (#530), and
	// revokeAllCredentials runs it before the API keys.
	sessions := concurrencyDocFuncBody(t, "internal/service/credential_revocation.go", "func endSessions(")
	inOrder(sessions, "UpdateFields(", "refreshTokens.RevokeAllForUser(")
	helper := concurrencyDocFuncBody(t, "internal/service/credential_revocation.go", "func revokeAllCredentials(")
	inOrder(helper, "endSessions(", "apiKeys.RevokeAllForUser(")
	// The plain update stays plain: it is the non-HTTP writer.
	update := concurrencyDocFuncBody(t, "internal/service/user_service.go", "func (s *userService) Update(")
	assert.NotContains(t, update, "WithTransaction")
	assert.Contains(t, update, "s.userRepo.UpdateFields(")

	// The role writes lock users before roles.
	roleService := "internal/service/role_service.go"
	for _, signature := range []string{
		"func (s *roleService) Create(", "func (s *roleService) Update(", "func (s *roleService) Delete(", "func (s *roleService) Clone(",
	} {
		body := concurrencyDocFuncBody(t, roleService, signature)
		inOrder(body, "s.governance.run(ctx,", "lockActor(tx, actor)", "tx.lockRoles(", "tx.actor(", "acting.requireKeys(", "checkEscalation(")
	}
	assert.Contains(t, concurrencyDocFuncBody(t, roleService, "func lockActor("), "tx.lockUsers(actor.UserID)")
	remove := concurrencyDocFuncBody(t, roleService, "func (s *roleService) Delete(")
	inOrder(remove, "lockActor(tx, actor)", "tx.userRoles.LockLiveRoleHolders(id)", "tx.lockRoles(", "checkEscalation(")
	assert.Equal(t, 3, strings.Count(remove, "tx.userRoles.LockLiveRoleHolders(id)"),
		"the holders are read before the role lock, after it, and once more after a foreign-key refusal")
	inOrder(remove[strings.Index(remove, "checkEscalation("):], "tx.userRoles.LockLiveRoleHolders(id)",
		"tx.userRoles.ClearRoleIDOfErased(id)", "tx.roles.Delete(id)", "tx.record(")

	// The locking reads the flow names.
	users := readRepositoryFile(t, "internal/repository/user_repository.go")
	for _, signature := range []string{
		"func (r *userRepository) LockActiveAdmins() ([]models.User, error)",
		"func (r *userRepository) LockLiveByIDs(ids []uint) ([]models.User, error)",
		"func (r *userRepository) LockLiveRoleHolders(roleID uint) ([]uint, error)",
	} {
		assert.Contains(t, users, signature)
	}
	lockByIDs := concurrencyDocFuncBody(t, "internal/repository/role_repository.go", "func (r *roleRepository) LockByIDs(")
	assert.Equal(t, 2, strings.Count(lockByIDs, `clause.Locking{Strength: "UPDATE"}`),
		"the roles and their permissions are both read with a locking read")
	inOrder(lockByIDs, `Where("id IN ?", ids).Order("id")`, `Where("role_id IN ?", found)`)
}

// concurrencyDocChainGroup classifies a table word of a lock chain. "set" is
// the last word of "admin set".
func concurrencyDocChainGroup(word string) (lead, session, rbac bool) {
	switch word {
	case "companies", "leads", "customers", "deals":
		return true, false, false
	case "users":
		return false, true, true
	case "refresh_tokens", "password_reset_tokens", "api_keys":
		return false, true, false
	case "set", "roles":
		return false, false, true
	}
	return false, false, false
}

func TestConcurrencyDocGlobalOrder(t *testing.T) {
	section := concurrencyDocSectionBody(t, "Global lock order")
	lines := strings.Split(section, "\n")

	lineWith := func(needle string) string {
		for _, line := range lines {
			if strings.Contains(line, needle) {
				return line
			}
		}
		t.Errorf("Global lock order has no line containing %q", needle)
		return ""
	}
	assert.Contains(t, lineWith("companies → leads → customers → deals"), "#182")
	lineWith("users → refresh_tokens")
	// #526: every credential-ending flow continues with the API keys.
	lineWith("users → refresh_tokens → api_keys")
	// #342: the credential-issuing edges of the users chain, and the reset
	// confirm's inversion of the request's order.
	lineWith("users → password_reset_tokens")
	lineWith("users → api_keys")
	lineWith("password_reset_tokens → users")
	rbac := lineWith("admin set → users (id ascending) → roles (id ascending)")
	assert.Contains(t, rbac, "#259")
	assert.Contains(t, rbac, "#348")
	assert.NotContains(t, rbac, "target")
	assert.NotContains(t, rbac, "not implemented")

	// #385: the cross-chain order is decided for the erasures and stated once.
	assert.NotContains(t, section, "(undecided)")
	assert.NotContains(t, section, "This document pins no combined sequence across the chains")
	erasureParagraph := concurrencyDocParagraph(t, section, "**Erasure order (#385).**", concurrencyDocFile)
	cross := concurrencyDocNormalize(erasureParagraph)
	for _, want := range []string{
		"One order spans the chains for every erasure, whichever row it starts from",
		"`" + concurrencyDocErasureOrder + "`",
		"ids ascending inside each table",
		"a locking read by primary key only",
		"`lockGraph`", "`lockCredentials`",
		"Two flows still lock against this order",
		"The governed user write of `DELETE /users/{id}` (#348) holds the target's `users` row",
		"whichever side is the victim is replayed",
		"The password-reset confirm claims its `password_reset_tokens` row before the owner's `users` row and is replayed when it is the victim (`resetConfirmMaxRetries`)",
	} {
		assert.Contains(t, cross, want, "Erasure order paragraph")
	}
	erasureLines := map[string]bool{}
	for _, line := range strings.Split(erasureParagraph, "\n") {
		erasureLines[line] = true
	}

	// Outside the erasure order paragraph no arrow in the section joins tables
	// of different chains; inside it the only cross-chain step is customers →
	// users, the step of the erasure order.
	parenthetical := regexp.MustCompile(`\([^)]*\)`)
	word := regexp.MustCompile(`[A-Za-z_]+`)
	pairs, crossPairs := 0, 0
	for _, line := range lines {
		clean := strings.NewReplacer("`", "", "*", "").Replace(line)
		parts := strings.Split(clean, "→")
		for i := 0; i+1 < len(parts); i++ {
			left := word.FindAllString(parenthetical.ReplaceAllString(parts[i], ""), -1)
			right := word.FindAllString(parts[i+1], -1)
			if len(left) == 0 || len(right) == 0 {
				continue
			}
			a, b := left[len(left)-1], right[0]
			aLead, aSession, aRBAC := concurrencyDocChainGroup(a)
			bLead, bSession, bRBAC := concurrencyDocChainGroup(b)
			if !(aLead || aSession || aRBAC) || !(bLead || bSession || bRBAC) {
				continue
			}
			pairs++
			sameChain := (aLead && bLead) || (aSession && bSession) || (aRBAC && bRBAC)
			if erasureLines[line] && !sameChain {
				crossPairs++
				assert.Equal(t, "customers → users", a+" → "+b, "the erasure order crosses the chains at customers → users only: %q", line)
				continue
			}
			assert.True(t, sameChain, "Global lock order joins %s → %s across chains: %q", a, b, line)
		}
	}
	assert.GreaterOrEqual(t, pairs, 6, "the chains themselves were not recognised")
	assert.Equal(t, 1, crossPairs, "the erasure order states leads → customers → users once")
}

func TestConcurrencyDocRetryRule(t *testing.T) {
	section := concurrencyDocSectionBody(t, "Retry rule")
	norm := concurrencyDocNormalize(section)
	for _, want := range []string{"WithTransactionAndRetry", "internal/utils/transaction.go", "ErrNestedRetryDeferred", "maxRetries"} {
		assert.Contains(t, norm, want)
	}
	lower := strings.ToLower(norm)
	assert.Contains(t, lower, "only a root transaction is replayed, and only after a clean rollback")

	var current, target bool
	for _, s := range concurrencyDocSentences(section) {
		if strings.Contains(s, "current") && strings.Contains(s, "lead conversion") {
			current = true
		}
		if strings.Contains(s, "target") && strings.Contains(s, "#183") {
			target = true
		}
	}
	assert.True(t, current, "a sentence says the rule is current for lead conversion")
	var issuance bool
	for _, s := range concurrencyDocSentences(section) {
		if strings.Contains(s, "current") && strings.Contains(s, "the password-reset request") &&
			strings.Contains(s, "API-key create") {
			issuance = true
		}
	}
	assert.True(t, issuance, "the same sentence names the password-reset request and API-key create (#342)")
	var confirm bool
	for _, s := range concurrencyDocSentences(section) {
		if strings.Contains(s, "current") && strings.Contains(s, "the password-reset request") &&
			strings.Contains(s, "the password-reset confirm") {
			confirm = true
		}
	}
	assert.True(t, confirm, "the sentence on current behaviour names the password-reset confirm (#407)")
	assert.True(t, target, "a sentence says the rule is the #183 target")
	// #385: the erasures are under the rule; the target sentence names the
	// three flows that are not, and neither an erasure nor, since #407, the
	// password-reset confirm.
	var erasures, remaining bool
	for _, s := range concurrencyDocSentences(section) {
		if strings.Contains(s, "current") && strings.Contains(s, "the three erasures") && strings.Contains(s, "`runErasureUnit`") {
			erasures = true
		}
		if strings.Contains(s, "target") && strings.Contains(s, "#183") {
			for _, flow := range []string{"company delete", "bulk status", "refresh rotation"} {
				assert.Contains(t, s, flow, "the #183 target sentence")
			}
			assert.NotContains(t, s, "password-reset confirm", "the #183 target sentence")
			assert.NotContains(t, s, "erasure", "the #183 target sentence")
			remaining = true
		}
	}
	assert.True(t, erasures, "the sentence on current behaviour names the three erasures and runErasureUnit")
	assert.True(t, remaining)
	assert.Contains(t, norm, "the erasures restart theirs on `ErrErasureGraphChanged`")

	// The rule as the code implements it.
	tx := readRepositoryFile(t, "internal/utils/transaction.go")
	assert.Contains(t, tx, "func (tm *TransactionManager) WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error")
	assert.Contains(t, tx, "ErrNestedRetryDeferred = errors.New(")
	retry := concurrencyDocFuncBody(t, "internal/utils/transaction.go", "func (tm *TransactionManager) WithTransactionAndRetry(")
	assert.Contains(t, retry, "tm.runRoot(ctx, fn)")
	assert.Contains(t, retry, "!replayable || !IsRetryable(err)")
	assert.Contains(t, retry, "attempts > maxRetries")
}

func TestConcurrencyDocRetryLines(t *testing.T) {
	fields := concurrencyDocFlowFields(t)
	for _, name := range concurrencyDocFlowsInOrder {
		retry := fields[name]["Retry"]
		if bound, ok := concurrencyDocRetryFlows[name]; ok {
			assert.Contains(t, retry, "WithTransactionAndRetry", name)
			if bound.constant != "" {
				assert.Contains(t, retry, bound.constant, "%s Retry", name)
				declaration := bound.declaration
				if declaration == "" {
					declaration = "const " + bound.constant + " = 3"
				}
				assert.Contains(t, readRepositoryFile(t, bound.file), declaration)
			}
		} else {
			assert.NotContains(t, retry, "WithTransactionAndRetry",
				"only the erasure, Lead conversion, Login, Password-reset request and confirm, API-key create and RBAC Retry lines name it; %s does", name)
		}
	}
	for _, name := range []string{"Lead conversion", "Lead update"} {
		retry := fields[name]["Retry"]
		for _, want := range []string{"runLeadCompanyAttempts", "leadCompanyMaxRestarts", "errLeadCompanyMoved"} {
			assert.Contains(t, retry, want, "%s Retry", name)
		}
	}
	assert.Contains(t, fields["Lead update"]["Retry"], "no deadlock retry")
	for _, name := range []string{"Company delete", "Bulk status", "Refresh rotation"} {
		assert.Contains(t, fields[name]["Retry"], "none today", "%s Retry", name)
	}
	// #407: the confirm is retried, carries no target, and says what an
	// exhausted bound is.
	confirmRetry := fields["Password-reset confirm"]["Retry"]
	for _, gone := range []string{"none today", "Target"} {
		assert.NotContains(t, confirmRetry, gone, "Password-reset confirm Retry")
	}
	assert.Contains(t, concurrencyDocNormalize(confirmRetry), "An exhausted bound returns the last error with nothing written")
	assert.Contains(t, fields["Password-reset confirm"]["Transaction"], "`WithTransactionAndRetry`")
	// #385: the erasures are retried and restarted, and carry no target.
	for _, name := range []string{"User-rooted erasure", "Customer-rooted erasure", "Lead-rooted erasure"} {
		retry := fields[name]["Retry"]
		for _, want := range []string{"`ErrErasureGraphChanged`", "`erasureGraphMaxRestarts`"} {
			assert.Contains(t, retry, want, "%s Retry", name)
		}
		for _, gone := range []string{"none today", "Target"} {
			assert.NotContains(t, retry, gone, "%s Retry", name)
		}
	}
	assert.Contains(t, readRepositoryFile(t, concurrencyDocErasureFile), "\terasureGraphMaxRestarts = 3\n")

	// The code behind the lead Retry lines.
	update := concurrencyDocFuncBody(t, "internal/service/lead_service.go", "func (s *leadService) Update(")
	assert.Contains(t, update, "runLeadCompanyAttempts(")
	assert.Contains(t, update, "s.txManager.WithTransaction(")
	assert.NotContains(t, update, "WithTransactionAndRetry")
	convert := concurrencyDocFuncBody(t, "internal/service/lead_conversion.go", "func (s *leadService) ConvertToCustomer(")
	assert.Contains(t, convert, "runLeadCompanyAttempts(")
	assert.Contains(t, convert, "s.txManager.WithTransactionAndRetry(")
	restart := concurrencyDocFuncBody(t, "internal/service/lead_company_lock.go", "func shouldRestartLeadCompanyAttempt(")
	assert.Contains(t, restart, "errLeadCompanyMoved")
}

func TestConcurrencyDocFailureModes175(t *testing.T) {
	norm := concurrencyDocNormalize(concurrencyDocSectionBody(t, "How the #175 failure modes are avoided"))
	for _, want := range []string{
		"foreign key",
		"data_lock_waits",
		"MariaDB 10.11",
		"information_schema.PROCESSLIST",
		"two consecutive polls",
		"`internal/service/lead_company_lock_order_mysql_test.go`",
		"the #182 MySQL roots run on an `AutoMigrate` schema without foreign keys",
	} {
		assert.Contains(t, norm, want)
	}

	probe := readRepositoryFile(t, "internal/service/lead_company_lock_order_mysql_test.go")
	assert.Contains(t, probe, "information_schema.PROCESSLIST")
	assert.NotContains(t, probe, "data_lock_waits")
	assert.Contains(t, readRepositoryFile(t, "internal/service/automation_test_helpers_test.go"),
		"cfg.DisableForeignKeyConstraintWhenMigrating = true")
}

var concurrencyDocCitation = regexp.MustCompile("`((?:internal|cmd|migrations|scripts|tests|test)/[^`\\s:]*)(?::([0-9][0-9,\\-]*))?`")

func TestConcurrencyDocCitationsResolve(t *testing.T) {
	root := repositoryRoot(t)
	matches := concurrencyDocCitation.FindAllStringSubmatch(concurrencyDocText(t), -1)
	require.GreaterOrEqual(t, len(matches), 40, "expected the document to cite its code paths")
	for _, m := range matches {
		rel, lines := m[1], m[2]
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if !assert.NoError(t, err, "cited path %s", rel) || lines == "" {
			continue
		}
		if !assert.False(t, info.IsDir(), "%s:%s cites lines of a directory", rel, lines) {
			continue
		}
		data := readRepositoryFile(t, rel)
		count := strings.Count(data, "\n")
		if !strings.HasSuffix(data, "\n") {
			count++
		}
		for _, part := range strings.Split(lines, ",") {
			bounds := strings.SplitN(part, "-", 2)
			start, err := strconv.Atoi(bounds[0])
			if !assert.NoError(t, err, "%s:%s", rel, lines) {
				continue
			}
			end := start
			if len(bounds) == 2 {
				end, err = strconv.Atoi(bounds[1])
				if !assert.NoError(t, err, "%s:%s", rel, lines) {
					continue
				}
			}
			assert.True(t, start >= 1 && start <= end && end <= count,
				"%s:%s lies outside the file's %d lines", rel, lines, count)
		}
	}
}

// concurrencyDocAuthServiceFiles are the auth service files: auth_service.go
// and the per-flow files split from it (#639).
var concurrencyDocAuthServiceFiles = []string{
	"internal/service/auth_service.go",
	"internal/service/auth_sso_policy.go",
	"internal/service/auth_login.go",
	"internal/service/auth_tokens.go",
	"internal/service/auth_apikey.go",
	"internal/service/auth_refresh.go",
	"internal/service/auth_logout.go",
	"internal/service/auth_password_change.go",
	"internal/service/auth_password_reset.go",
}

// concurrencyDocAuthServiceCitation matches a `internal/service/<auth service
// file>:N` span and, when it is followed by "`, `:M` in the same file", that
// bare continuation too. Bare spans after other files are not matched.
var concurrencyDocAuthServiceCitation = regexp.MustCompile(
	"`(" + concurrencyDocAuthServiceFilePattern() + "):([0-9]+)`(?:,\\s+`:([0-9]+)`\\s+in\\s+the\\s+same\\s+file)?")

func concurrencyDocAuthServiceFilePattern() string {
	quoted := make([]string, len(concurrencyDocAuthServiceFiles))
	for i, file := range concurrencyDocAuthServiceFiles {
		quoted[i] = regexp.QuoteMeta(file)
	}
	return strings.Join(quoted, "|")
}

// concurrencyDocAuthServicePin pins one auth service citation of the
// document: the citation must name file, and the cited line must contain
// fragment and lie inside the function of that file whose declaration starts
// with signature. An empty signature marks a top-level declaration, whose
// fragment must occur exactly once in the file.
type concurrencyDocAuthServicePin struct {
	file      string
	signature string
	fragment  string
}

const (
	pinAuthenticate          = "func (s *authService) authenticate("
	pinLoginInTransaction    = "func (s *authService) loginInTransaction("
	pinRecordLoginFailure    = "func recordLoginFailureInTransaction("
	pinRefreshAccessToken    = "func (s *authService) RefreshAccessToken("
	pinRotateInTransaction   = "func (s *authService) rotateRefreshTokenInTransaction("
	pinRefuseRevoked         = "func (s *authService) refuseRevokedRefreshToken("
	pinLogout                = "func (s *authService) Logout("
	pinChangePassword        = "func (s *authService) ChangePassword("
	pinChangePasswordInTx    = "func (s *authService) changePasswordInTransaction("
	pinLockLiveOwner         = "func lockLiveCredentialOwner("
	pinIssuePasswordReset    = "func (s *authService) IssuePasswordReset("
	pinCompletePasswordReset = "func (s *authService) CompletePasswordReset("
	pinConfirmResetInTx      = "func (s *authService) confirmPasswordResetInTransaction("
)

// concurrencyDocAuthServicePins lists, in document order, one pin per
// auth service citation of docs/CONCURRENCY.md (#581, #639).
var concurrencyDocAuthServicePins = []concurrencyDocAuthServicePin{
	// Retry rule: the login, request and confirm roots.
	{"internal/service/auth_login.go", pinAuthenticate, "WithTransactionAndRetry("},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "WithTransactionAndRetry("},
	{"internal/service/auth_password_reset.go", pinCompletePasswordReset, "WithTransactionAndRetry("},
	// Login.
	{"internal/service/auth_login.go", pinAuthenticate, `s.userRepo.WithTx(repository.LockingRead(tx))`},
	{"internal/service/auth_login.go", pinLoginInTransaction, "userRepo.GetByID(userID)"},
	{"internal/service/auth_login.go", pinLoginInTransaction, "userRepo.UpdateFields(owner.ID, fields)"},
	{"internal/service/auth_login.go", pinLoginInTransaction, "identities.ExistsForUser(owner.ID)"},
	{"internal/service/auth_login.go", pinLoginInTransaction, "s.issueRefreshToken("},
	{"internal/service/auth_login.go", pinAuthenticate, "s.userRepo.GetByEmail(email)"},
	{"internal/service/auth_login.go", pinAuthenticate, "checkIdentity := s.checkSSOIdentity()"},
	{"internal/service/auth_login.go", pinLoginInTransaction, "WithTransactionAndRetry(ctx, func(spCtx context.Context) error {"},
	{"internal/service/auth_login.go", pinLoginInTransaction, "s.accessTokenFor(owner)"},
	{"internal/service/auth_login.go", pinAuthenticate, "WithTransactionAndRetry("},
	{"internal/service/auth_login.go", pinRecordLoginFailure, pinRecordLoginFailure},
	{"internal/service/auth_login.go", pinLoginInTransaction, pinLoginInTransaction},
	{"internal/service/auth_login.go", pinAuthenticate, "WithTransactionAndRetry("},
	{"internal/service/auth_login.go", "", "const loginMaxRetries = 3"},
	// Refresh rotation.
	{"internal/service/auth_refresh.go", pinRefreshAccessToken, `s.userRepo.WithTx(repository.LockingRead(tx))`},
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "userRepo.GetByID(stored.UserID)"},
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "refreshRepo.ClaimForRotation(stored.ID)"},
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "s.issueRefreshToken(refreshRepo, user.ID, session)"},
	{"internal/service/auth_refresh.go", pinRefuseRevoked, "userRepo.GetByID(stored.UserID)"},
	{"internal/service/auth_refresh.go", pinRefuseRevoked, "refreshRepo.RevokeFamily(stored.UserID, stored.FamilyID)"},
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "refreshRepo.GetByTokenHash(hash)"},
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "// Lock the owner before claiming the token."},
	{"internal/service/auth_refresh.go", pinRefuseRevoked, "refreshRepo.GetRevokedByTokenHash(hash)"},
	{"internal/service/auth_refresh.go", pinRefuseRevoked, "refreshRepo.HasLaterInFamily(stored)"},
	{"internal/service/auth_refresh.go", pinRefreshAccessToken, "s.txManager.WithTransaction(context.Background(),"},
	// Logout and change password.
	{"internal/service/auth_logout.go", pinLogout, pinLogout},
	{"internal/service/auth_password_change.go", pinChangePassword, pinChangePassword},
	{"internal/service/auth_password_change.go", pinChangePasswordInTx, "revokeAllCredentials(credentialStores{"},
	// Password-reset request.
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, `s.userRepo.WithTx(repository.LockingRead(tx))`},
	{"internal/service/auth_service.go", pinLockLiveOwner, "userRepo.GetByID(userID)"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "s.identities.WithTx(tx).ExistsForUser(owner.ID)"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "checkIdentity := s.checkSSOIdentity()"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "resetRepo.InvalidateAllForUser(owner.ID)"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "resetRepo.Create(token)"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "s.userRepo.GetByEmail(email)"},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "s.mailer.SendPasswordReset("},
	{"internal/service/auth_password_reset.go", pinIssuePasswordReset, "WithTransactionAndRetry("},
	{"internal/service/auth_password_reset.go", "", "const resetRequestMaxRetries = 3"},
	// Password-reset confirm.
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "resetRepo.MarkUsed(stored.ID)"},
	{"internal/service/auth_password_reset.go", pinCompletePasswordReset, `s.userRepo.WithTx(repository.LockingRead(tx))`},
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "userRepo.GetByID(stored.UserID)"},
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "userRepo.UpdateFields(user.ID,"},
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "revokeAllCredentials(credentialStores{"},
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "identities.ExistsForUser(user.ID)"},
	{"internal/service/auth_password_reset.go", pinCompletePasswordReset, "checkIdentity := s.checkSSOIdentity()"},
	{"internal/service/auth_password_reset.go", pinConfirmResetInTx, "resetRepo.GetByTokenHash(hash)"},
	{"internal/service/auth_password_reset.go", pinCompletePasswordReset, "WithTransactionAndRetry("},
	{"internal/service/auth_password_reset.go", "", "const resetConfirmMaxRetries = 3"},
	// How the #175 failure modes are avoided.
	{"internal/service/auth_refresh.go", pinRotateInTransaction, "// Lock the owner before claiming the token."},
}

func TestConcurrencyDocAuthServiceCitationsPinned(t *testing.T) {
	text := concurrencyDocText(t)
	sources := map[string]string{}
	for _, rel := range concurrencyDocAuthServiceFiles {
		sources[rel] = readRepositoryFile(t, rel)
	}

	type citation struct {
		docLine int
		file    string
		label   string
		line    int
	}
	var citations []citation
	for _, loc := range concurrencyDocAuthServiceCitation.FindAllStringSubmatchIndex(text, -1) {
		file := text[loc[2]:loc[3]]
		for _, g := range []int{4, 6} {
			if loc[g] < 0 {
				continue
			}
			n, err := strconv.Atoi(text[loc[g]:loc[g+1]])
			require.NoError(t, err)
			label := file + ":" + text[loc[g]:loc[g+1]]
			if g == 6 {
				label = "`:" + text[loc[g]:loc[g+1]] + "` (same file, " + file + ")"
			}
			citations = append(citations, citation{
				docLine: strings.Count(text[:loc[g]], "\n") + 1,
				file:    file,
				label:   label,
				line:    n,
			})
		}
	}
	require.Len(t, citations, len(concurrencyDocAuthServicePins),
		"%s cites the auth service files %d times; pin every citation in concurrencyDocAuthServicePins",
		concurrencyDocFile, len(citations))

	for i, c := range citations {
		pin := concurrencyDocAuthServicePins[i]
		where := fmt.Sprintf("%s line %d cites %s", concurrencyDocFile, c.docLine, c.label)
		if !assert.Equal(t, pin.file, c.file, "%s: the pinned statement lives in %s", where, pin.file) {
			continue
		}
		src := sources[c.file]
		lines := strings.Split(src, "\n")
		if !assert.True(t, c.line >= 1 && c.line <= len(lines), "%s, outside the file", where) {
			continue
		}
		actual := lines[c.line-1]
		assert.Contains(t, actual, pin.fragment,
			"%s: expected a line containing %q, got %q", where, pin.fragment, strings.TrimSpace(actual))
		if pin.signature == "" {
			assert.Equal(t, 1, strings.Count(src, pin.fragment),
				"%s: top-level fragment %q must occur exactly once in %s", where, pin.fragment, c.file)
			continue
		}
		idx := strings.Index(src, pin.signature)
		if !assert.GreaterOrEqual(t, idx, 0, "%s: %s declares %q", where, c.file, pin.signature) {
			continue
		}
		end := strings.Index(src[idx:], "\n}\n")
		if !assert.GreaterOrEqual(t, end, 0, "%s: end of %q", where, pin.signature) {
			continue
		}
		first := strings.Count(src[:idx], "\n") + 1
		last := strings.Count(src[:idx+end+1], "\n") + 1
		assert.True(t, c.line >= first && c.line <= last,
			"%s: expected inside %q (lines %d-%d), got line %d %q",
			where, pin.signature, first, last, c.line, strings.TrimSpace(actual))
	}
}

func TestConcurrencyDocTicketMap(t *testing.T) {
	section := concurrencyDocSectionBody(t, "Flow to ticket map")
	lines := strings.Split(section, "\n")
	header := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "| Flow | Ticket | Status |" {
			header = i
			break
		}
	}
	require.GreaterOrEqual(t, header, 0, "the ticket map header | Flow | Ticket | Status |")
	require.Greater(t, len(lines), header+1)
	require.True(t, strings.HasPrefix(strings.TrimSpace(lines[header+1]), "|---"), "table separator row")

	fields := concurrencyDocFlowFields(t)
	seen := map[string]bool{}
	end := header + 2
	for ; end < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end]), "|"); end++ {
		cells := strings.Split(strings.Trim(strings.TrimSpace(lines[end]), "|"), "|")
		require.Len(t, cells, 3, "row %q", lines[end])
		flow, ticket, status := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2])
		assert.False(t, seen[flow], "flow %q is listed twice", flow)
		seen[flow] = true
		want, known := concurrencyDocExpectedTicket[flow]
		if !assert.True(t, known, "unknown flow %q in the ticket map", flow) {
			continue
		}
		assert.Equal(t, want, ticket, "ticket of %s", flow)
		assert.Equal(t, fields[flow]["Status"], status, "status cell of %s", flow)
	}
	assert.Len(t, seen, len(concurrencyDocFlowsInOrder), "one row per flow")

	after := concurrencyDocNormalize(strings.Join(lines[end:], "\n"))
	assert.Contains(t, after, "C2 and C3 are Paperclip ids with no GitHub issue yet")
	assert.NotContains(t, after, "C2–C4 are Paperclip ids")
	assert.Contains(t, after, "C4 is implemented by #385")
	assert.Contains(t, after, "The RBAC lock order of #259 is implemented by #348")
	assert.NotContains(t, concurrencyDocText(t), "R6")
	assert.NotContains(t, concurrencyDocText(t), "R7")
	assert.Contains(t, after, "C2–C4 follow the family order of the #183 comment of 14 Nov 2026")
}

func TestConcurrencyDocExtendingLinks(t *testing.T) {
	text := readRepositoryFile(t, "docs/EXTENDING.md")
	const link = "[CONCURRENCY.md](CONCURRENCY.md)"

	var rules, recipe string
	for _, s := range concurrencyDocSplit(text, "## ") {
		if strings.HasPrefix(s.heading, "1. ") {
			rules = s.body
		}
	}
	require.NotEmpty(t, rules, "EXTENDING.md section 1")
	rows := 0
	for _, line := range strings.Split(rules, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if strings.TrimSpace(cells[len(cells)-1]) == link {
			rows++
		}
	}
	assert.Equal(t, 1, rows, "rows of the section 1 rules table whose Long form is %s", link)

	for _, s := range concurrencyDocSplit(text, "### ") {
		if strings.HasPrefix(s.heading, "3.10 ") {
			recipe = s.body
		}
	}
	require.NotEmpty(t, recipe, "EXTENDING.md section 3.10")
	named := 0
	for _, sentence := range concurrencyDocSentences(recipe) {
		if strings.Contains(sentence, "#183") {
			named++
			assert.Contains(t, sentence, "](CONCURRENCY.md)", "3.10 sentence naming #183: %q", sentence)
		}
	}
	assert.Greater(t, named, 0, "section 3.10 names #183")
}

func TestConcurrencyDocCrossLinks(t *testing.T) {
	development := readRepositoryFile(t, "docs/DEVELOPMENT.md")
	for _, start := range []string{"**Lock order for session writes**", "**Lead and company lock order (#182)**"} {
		assert.Contains(t, concurrencyDocParagraph(t, development, start, "docs/DEVELOPMENT.md"), "](CONCURRENCY.md)", start)
	}

	bullets := concurrencyDocBullets(readRepositoryFile(t, "docs/ROADMAP.md"))
	bulletStarting := func(start string) string {
		for _, b := range bullets {
			if strings.HasPrefix(b, start) {
				return b
			}
		}
		t.Errorf("docs/ROADMAP.md has no bullet starting %q", start)
		return ""
	}
	for _, start := range []string{
		"- **Erasure vs conversion/authentication concurrency",
	} {
		b := bulletStarting(start)
		assert.Contains(t, b, "#183", start)
		assert.Contains(t, b, "](CONCURRENCY.md)", start)
	}
	// Login issues its session under the owner lock since #336 (C2): the gap
	// bullet is gone.
	for _, b := range bullets {
		assert.False(t, strings.HasPrefix(b, "- **Login issued from an obsolete verification**"),
			"docs/ROADMAP.md still lists the closed login gap")
	}

	// The bulk task batch locks its tasks since #558: the gap bullet is gone.
	for _, b := range bullets {
		assert.False(t, strings.HasPrefix(b, "- **Unlocked completed-task check in the bulk task status batch**"),
			"docs/ROADMAP.md still lists the closed bulk task gap")
		assert.NotContains(t, b, "GetTasksByIDs", "no ROADMAP bullet names GetTasksByIDs")
	}
}

// TestConcurrencyDocLeadErasureVsConversion pins the order of the lead-rooted
// erasure (#337, #385) — automation runs, then a locking read of the lead by
// id, then the customer and the login by id, with a plain converted-lead
// pluck — to the document and to the code.
func TestConcurrencyDocLeadErasureVsConversion(t *testing.T) {
	const cascade = "internal/repository/erasure_cascade.go"
	bodies := concurrencyDocFlowBodies(t)
	fields := concurrencyDocFlowFields(t)
	lead := concurrencyDocNormalize(bodies["Lead-rooted erasure"])
	order := fields["Lead-rooted erasure"]["Order"]

	for _, want := range []string{
		"automation runs (`cancelAutomationInTables`) → leads (locking read by id",
		"→ customers (locking read by id",
		"→ users (locking read by id",
		"→ the credential rows → deals and quotes text",
		"`DeleteUnconverted` discovers and locks the root lead only and never reads or locks a customer or a user",
		"The converted-lead pluck (`WHERE customer_id = ?`",
		"stays a plain read",
		"a conversion always inserts a new customer",
	} {
		assert.Contains(t, order, want, "Lead-rooted erasure Order")
	}
	for _, gone := range []string{"non-locking first read", "deliberately non-locking"} {
		assert.NotContains(t, lead, gone, "the unlocked first read is gone")
	}
	assert.Contains(t, lead, "`ErrConvertedLeadNeedsAdmin`")
	assert.Contains(t, lead, "Neither ordering ends in error 1213")
	assert.Contains(t, lead, "rolls back with `ErrErasureGraphChanged`, discovers again and erases the new customer too")

	// The locks the Order line cites are the locking reads of lockGraph.
	lines := strings.Split(readRepositoryFile(t, cascade), "\n")
	cited := func(after, statement string) {
		t.Helper()
		at := strings.Index(order, after)
		require.GreaterOrEqual(t, at, 0, after)
		m := regexp.MustCompile("`(?:" + regexp.QuoteMeta(cascade) + ")?:([0-9]+)`").FindStringSubmatch(order[at:])
		require.NotNil(t, m, "%q is cited with a line", after)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		require.LessOrEqual(t, n, len(lines))
		assert.Contains(t, lines[n-1], statement, "%s:%d", cascade, n)
	}
	cited("leads (locking read by id", `LockingRead(tx).Unscoped().Select("id", "status", "customer_id", "deleted_at")`)
	cited("customers (locking read by id", `LockingRead(tx).Unscoped().Select("id", "user_id", "deleted_at")`)
	cited("users (locking read by id", `LockingRead(tx).Unscoped().Select("id", "role")`)

	// Lead conversion queued behind an erasure: not found, 404.
	conversion := concurrencyDocNormalize(bodies["Lead conversion"])
	assert.Contains(t, conversion, "A conversion queued behind a lead erasure finds the lead gone")
	assert.Contains(t, conversion, "404")

	// The erasures share the direction of lead conversion (#385).
	flowsSection := concurrencyDocNormalize(concurrencyDocSectionBody(t, "Flows"))
	assert.NotContains(t, flowsSection, "All three erasures are one transaction each, but they share no order with lead conversion")
	assert.NotContains(t, flowsSection, "Customer- and user-rooted erasures still share no order with lead conversion")
	assert.Contains(t, flowsSection, "All three erasures take the lead before the customer, the direction lead conversion takes")

	// The code behind it: the plan still names the cancel, and the address
	// read and the plain reverse reads are what the document says they are.
	assert.Contains(t, leadErasurePlanBody(t, cascade), "BeforeScrub: cancelLeadAutomation")
	assert.Contains(t, concurrencyDocFuncBody(t, concurrencyDocErasureFile, "func readErasedEmail("),
		"LockingRead(tx).Unscoped()")
	pluck := concurrencyDocFuncBody(t, cascade, "func convertedLeadsOf(")
	assert.Contains(t, pluck, "db.Unscoped().Model(&models.Lead{}).\n\t\tWhere(\"customer_id = ?\", customerID)",
		"the converted-lead pluck is a plain read")
	assert.NotContains(t, pluck, "LockingRead")
	assert.NotContains(t, concurrencyDocFuncBody(t, cascade, "func customersOfLogin("), "LockingRead")
}

// TestConcurrencyDocErasureOrder pins the one erasure protocol of #385 to the
// three erasure flows and to the code: the order, the snapshot rule, the
// re-check, the retry and the restart, and what stays open.
func TestConcurrencyDocErasureOrder(t *testing.T) {
	const cascade = "internal/repository/erasure_cascade.go"
	bodies := concurrencyDocFlowBodies(t)
	fields := concurrencyDocFlowFields(t)
	erasures := []string{"User-rooted erasure", "Customer-rooted erasure", "Lead-rooted erasure"}

	for _, name := range erasures {
		body := concurrencyDocNormalize(bodies[name])
		order := fields[name]["Order"]
		assert.Contains(t, order, concurrencyDocErasureOrder, "%s Order", name)
		assert.Contains(t, order, "ids ascending inside each table", "%s Order", name)
		assert.Contains(t, strings.ToLower(order), "snapshot rule", "%s Order", name)
		assert.Contains(t, body, "`readErasedEmail`", name)
		for _, gone := range []string{"Target (C4)", "(undecided)", "customers → automation runs → leads", "db.Transaction"} {
			assert.NotContains(t, body, gone, name)
		}
		assert.Contains(t, fields[name]["Transaction"], "`eraseGraph`", "%s Transaction", name)
		assert.Contains(t, fields[name]["Transaction"], "`runErasureUnit`", "%s Transaction", name)
	}

	// The rule is stated in full once, in the user-rooted flow.
	user := concurrencyDocNormalize(bodies["User-rooted erasure"])
	for _, want := range []string{
		"`discoverErasureGraph`",
		"No locking read filters on `leads.customer_id`, `customers.user_id`, `deleted_at` or a credential table's `user_id`",
		"`ErrErasureGraphChanged`",
		"the first plain read of a transaction fixes its snapshot",
		"run on the root handle before the transaction begins",
		"no plain read of any kind runs on the transaction before the `users` lock, the last graph lock",
		"on MySQL 8 a select on `information_schema` fixes the snapshot (on MariaDB 10.11 it does not)",
		"`probeAutomationTables`",
		"`purgeCredentials`",
		"`shouldRestartErasure`",
		"Two inversions remain",
		"The governed user write holds the target's `users` row before its nested erasure locks leads and customers",
		"The password-reset confirm takes `password_reset_tokens` before `users` and is replayed by `resetConfirmMaxRetries`",
		"Inside a caller's transaction the reverse re-read answers from the caller's snapshot",
		"No shipped code path writes `customers.user_id`",
		"such a write takes no lock on the `users` row",
		"`scrubUnlinkedSubmissions`",
	} {
		assert.Contains(t, user, want, "User-rooted erasure")
	}

	// The code: discovery before the unit, one attempt per discovery, the
	// restart on the sentinel only.
	graph := concurrencyDocFuncBody(t, cascade, "func eraseGraph(")
	inOrder := func(body string, statements ...string) {
		t.Helper()
		last := -1
		for _, statement := range statements {
			at := strings.Index(body, statement)
			if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
				assert.Greater(t, at, last, "%q comes after the statement before it", statement)
				last = at
			}
		}
	}
	inOrder(graph, "db = db.WithContext(ctx)", "discoverErasureGraph(db, root, allowCustomers)", "seams.runAfterDiscovery()", "runErasureUnit(ctx, db,",
		"cascade.erase(tx, root, discovery)", "if inTransaction(db) {", "shouldRestartErasure(err)", "restarts >= erasureGraphMaxRestarts")
	restart := concurrencyDocFuncBody(t, cascade, "func shouldRestartErasure(")
	for _, want := range []string{"ErrErasureGraphChanged", "utils.ErrRollbackFailed", "utils.ErrTransactionPoisoned",
		"utils.ErrCommitFailed", "utils.ErrBeginFailed"} {
		assert.Contains(t, restart, want, "shouldRestartErasure")
	}
	assert.Contains(t, readRepositoryFile(t, cascade), "var ErrErasureGraphChanged = errors.New(")

	// The discovery reads plainly and resolves the schema probes itself.
	discover := concurrencyDocFuncBody(t, cascade, "func discoverErasureGraph(")
	assert.NotContains(t, discover, "LockingRead")
	assert.Contains(t, discover, "probeAutomationTables(db)")

	// One attempt: cancel (writes only), lock, re-read, credential locks, scrub.
	erase := concurrencyDocFuncBody(t, cascade, "func (c *erasureCascade) erase(")
	inOrder(erase, "cancelAutomationInTables(tx, models.AutomationEntityLead, leadID, discovery.tables)",
		"c.lockGraph(tx, graph)", "walkErasureGraph(root, c.allowCustomers, locked.links(tx, graph))",
		"return ErrErasureGraphChanged", "c.lockCredentials(tx, graph.users)",
		"eraseRecord(tx, leadID, plan)", "eraseRecord(tx, customerID, customerErasurePlan())",
		"eraseRecord(tx, userID, userErasurePlan())", "c.scrubBusinessText(tx)")
	assert.NotContains(t, erase, "HasTable", "no schema probe on the transaction before the locks")
	assert.NotContains(t, concurrencyDocFuncBody(t, concurrencyDocErasureFile, "func cancelAutomationInTables("), "HasTable",
		"the cancel of an erasure issues writes only")
	assert.Contains(t, concurrencyDocFuncBody(t, concurrencyDocErasureFile, "func probeAutomationTables("), "db.Migrator().HasTable(")

	// The graph locks: leads, customers, users, by primary key, the refusal
	// decided on the locked lead.
	lock := concurrencyDocFuncBody(t, cascade, "func (c *erasureCascade) lockGraph(")
	inOrder(lock, "c.lockStage(erasureStageLeads,", `LockingRead(tx).Unscoped().Select("id", "status", "customer_id", "deleted_at").First(&lead, id)`,
		"return ErrConvertedLeadNeedsAdmin", "c.lockStage(erasureStageCustomers,",
		`LockingRead(tx).Unscoped().Select("id", "user_id", "deleted_at").First(&customer, id)`, "c.lockStage(erasureStageUsers,",
		`LockingRead(tx).Unscoped().Select("id", "role").First(&user, id)`)
	assert.Equal(t, 3, strings.Count(lock, "First(&"), "each graph row is read once, under its lock")
	// deleted_at is selected (the erasure listener's live-row rule, #437) but
	// never filtered on.
	for _, filter := range []string{`"customer_id = ?"`, `"user_id = ?"`, "deleted_at IS", "deleted_at =", "Where("} {
		assert.NotContains(t, lock, filter, "lockGraph filters on the primary key only")
	}

	// The credential locks: refresh_tokens, password_reset_tokens, api_keys,
	// ids by a plain read, locks by primary key.
	credentials := concurrencyDocFuncBody(t, cascade, "func (c *erasureCascade) lockCredentials(")
	inOrder(credentials, "erasureStageRefreshTokens", "erasureStagePasswordResets", "erasureStageAPIKeys",
		`Where("user_id IN ?", userIDs).Order("id").Pluck("id", &ids)`, "LockingRead(tx).Unscoped().Model(stage.model())",
		`Where("id IN ?", chunk).Order("id")`)
	assert.Equal(t, 1, strings.Count(credentials, "LockingRead("), "only the read by primary key locks")
	stages := readRepositoryFile(t, cascade)
	for _, stage := range []string{
		`erasureStageLeads          = "leads"`, `erasureStageCustomers      = "customers"`, `erasureStageUsers          = "users"`,
		`erasureStageRefreshTokens  = "refresh_tokens"`, `erasureStagePasswordResets = "password_reset_tokens"`,
		`erasureStageAPIKeys        = "api_keys"`,
	} {
		assert.Contains(t, stages, stage)
	}

	// The unit: WithTransactionAndRetry on the database, a savepoint in a
	// caller's transaction, and no db.Transaction any more.
	unit := concurrencyDocFuncBody(t, concurrencyDocErasureFile, "func runErasureUnit(")
	assert.Contains(t, unit, "utils.NewTransactionManager(db).WithTransactionAndRetry(ctx,")
	assert.NotContains(t, unit, "context.Background()")
	assert.Contains(t, unit, "}, erasureMaxRetries)")
	assert.Contains(t, unit, `"SAVEPOINT "+name`)
	assert.NotContains(t, unit, "db.Transaction(")

	// Every root enters through eraseGraph.
	for file, calls := range map[string]int{
		"internal/repository/user_repository.go":     1,
		"internal/repository/customer_repository.go": 1,
		"internal/repository/lead_repository.go":     2,
	} {
		assert.Equal(t, calls, strings.Count(readRepositoryFile(t, file), "return eraseGraph(ctx, r.db, "), file)
	}
}

func leadErasurePlanBody(t *testing.T, rel string) string {
	t.Helper()
	return concurrencyDocFuncBody(t, rel, "func leadErasurePlan(")
}

func TestConcurrencyDocIndexAndChangelog(t *testing.T) {
	var listed bool
	for _, b := range concurrencyDocBullets(readRepositoryFile(t, "README.md")) {
		if strings.HasPrefix(b, "- [docs/CONCURRENCY.md](docs/CONCURRENCY.md)") {
			listed = true
		}
	}
	assert.True(t, listed, "README.md lists [docs/CONCURRENCY.md](docs/CONCURRENCY.md)")

	changelog := readRepositoryFile(t, "CHANGELOG.md")
	start := strings.Index(changelog, "## [Unreleased]")
	require.GreaterOrEqual(t, start, 0, "CHANGELOG.md has ## [Unreleased]")
	unreleased := changelog[start+len("## [Unreleased]"):]
	if next := strings.Index(unreleased, "\n"+sinceReleaseStop); next >= 0 {
		unreleased = unreleased[:next]
	}
	var entry bool
	for _, b := range concurrencyDocBullets(unreleased) {
		if strings.Contains(b, "docs/CONCURRENCY.md") && strings.Contains(b, "#323") {
			entry = true
		}
	}
	assert.True(t, entry, "an [Unreleased] entry names docs/CONCURRENCY.md and #323")
	// #385: the erasure lock order has its own entry.
	var erasure bool
	for _, b := range concurrencyDocBullets(unreleased) {
		if strings.Contains(b, "#385") && strings.Contains(b, "docs/CONCURRENCY.md") &&
			strings.Contains(b, "ErrErasureGraphChanged") && strings.Contains(b, "check-go.py transaction") {
			erasure = true
		}
	}
	assert.True(t, erasure, "an [Unreleased] entry names #385, docs/CONCURRENCY.md, the sentinel and the transaction gate")
}

// TestConcurrencyDocOIDCSignIn pins the OIDC sign-in flow (#435) to the code:
// the attempt claim is its own statement before the code exchange and the
// governed transaction, which takes admin set → users → roles before it writes
// the identity and the refresh token. The steps are split across the sign-in
// (internal/idkit/sso: the callback and the link rule) and GopherCRM's
// transaction runner and account store (internal/service/auth_oidc_host.go).
func TestConcurrencyDocOIDCSignIn(t *testing.T) {
	fields := concurrencyDocFlowFields(t)
	assert.Contains(t, concurrencyDocNormalize(fields["OIDC sign-in"]["Order"]),
		"admin set → users → roles → user_identities → refresh_tokens")
	assert.Contains(t, concurrencyDocNormalize(fields["OIDC sign-in"]["Rows locked"]), "its own committed statement")

	inOrder := func(body string, statements ...string) {
		t.Helper()
		last := -1
		for _, statement := range statements {
			at := strings.Index(body, statement)
			if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
				assert.Greater(t, at, last, "%q comes after the statement before it", statement)
				last = at
			}
		}
	}
	callback := concurrencyDocFuncBody(t, "internal/idkit/sso/callback.go", "func (s *Service) callback(")
	inOrder(callback, "s.Attempts.Claim(", "s.exchangeCode(", "s.Tx.WithTransaction(", "s.signInAccount(ctx")
	// The code exchange itself lives in exchangeCode, which adds the client
	// assertion when a certificate is in force.
	exchange := concurrencyDocFuncBody(t, "internal/idkit/sso/service.go", "func (s *Service) exchangeCode(")
	inOrder(exchange, ".Exchange(")
	// The transaction: the governed root, whose first statement locks the
	// admin set, before the sign-in's steps run.
	runner := concurrencyDocFuncBody(t, "internal/service/auth_oidc_host.go", "func (r *oidcSignInRunner) WithTransaction(")
	inOrder(runner, "r.governance.runWithContext(ctx,", "lockAdminSet(tx)", "return fn(")
	// The link rule: the plain identity reads, then the account, then the
	// identity link, and the session last.
	link := concurrencyDocFuncBody(t, "internal/idkit/sso/link.go", "func (s *Service) signInAccount(")
	inOrder(link, "s.Identities.Get(", "s.Accounts.SignIn(", "s.Accounts.Provision(", "s.Identities.Create(", "s.Sessions.Issue(")
	// The account: users, then roles, the last-admin rule and the role audit.
	signIn := concurrencyDocFuncBody(t, "internal/service/auth_oidc_host.go", "func (s *oidcAccountStore) SignIn(")
	inOrder(signIn, "tx.lockUsers(", "tx.lockRoles(", "apperrors.ErrLastAdmin", "tx.record(")
	provision := concurrencyDocFuncBody(t, "internal/service/auth_oidc_host.go", "func (s *oidcAccountStore) Provision(")
	inOrder(provision, "tx.lockRoles(", "tx.users.EmailExistsFoldedUnscoped(", "tx.users.Create(", "tx.record(")
	// bcrypt runs before the transaction, never under the admin-set and
	// role locks: the outer runner hashes, then starts the governed one.
	for name, body := range map[string]string{"runner": runner, "Provision": provision, "SignIn": signIn} {
		assert.NotContains(t, body, "oidcUnusablePasswordHash(", "%s hashes inside the transaction", name)
	}
	first := concurrencyDocFuncBody(t, "internal/service/auth_oidc_host.go", "func (r *oidcPasswordFirstRunner) WithTransaction(")
	inOrder(first, "oidcUnusablePasswordHash(", "r.next.WithTransaction(")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/service/auth_oidc.go", "func NewOIDCService("),
		"&oidcPasswordFirstRunner{next: &oidcSignInRunner{")
}

// TestConcurrencyDocSCIMUserWrites pins the SCIM user create and replace flow
// (#470) to the code: both take admin set → users → roles inside one governed
// transaction, the create before it reads and writes the identity.
func TestConcurrencyDocSCIMUserWrites(t *testing.T) {
	fields := concurrencyDocFlowFields(t)
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM user create and replace"]["Order"]),
		"admin set → users → roles → user_identities")

	inOrder := func(body string, statements ...string) {
		t.Helper()
		last := -1
		for _, statement := range statements {
			at := strings.Index(body, statement)
			if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
				assert.Greater(t, at, last, "%q comes after the statement before it", statement)
				last = at
			}
		}
	}
	const file = "internal/service/scim_user_writes.go"
	create := concurrencyDocFuncBody(t, file, "func (s *scimUserService) Create(")
	inOrder(create, "governance.run(ctx,", "lockAdminSet(tx)", "tx.lockRoles(",
		"tx.users.EmailExistsFoldedUnscoped(", "identities.GetByIssuerSubject(", "tx.users.Create(",
		"identities.Create(", "tx.record(", "s.writes.recordSuccess(")
	replace := concurrencyDocFuncBody(t, file, "func (s *scimUserService) Replace(")
	inOrder(replace, "governance.run(ctx,", "lockAdminSet(tx)", "tx.lockUsers(",
		"tx.lockRoles(", "EmailTakenByOther(", "tx.users.UpdateFields(", "tx.record(", "s.writes.recordSuccess(")
}

// TestConcurrencyDocSCIMUserPatch pins the SCIM user patch flow (#471): the
// documented order and, in Patch, the locks before the users write, the
// revocation of the refresh tokens after it and the sign-in audit row after
// the commit.
func TestConcurrencyDocSCIMUserPatch(t *testing.T) {
	fields := concurrencyDocFlowFields(t)
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM user patch"]["Order"]),
		"admin set → users → roles → refresh_tokens → api_keys")

	body := concurrencyDocFuncBody(t, "internal/service/scim_user_writes.go", "func (s *scimUserService) Patch(")
	last := -1
	for _, statement := range []string{"governance.run(ctx,", "lockAdminSet(tx)", "tx.lockUsers(", "tx.lockRoles(",
		"tx.users.UpdateFields(", "revokeAllCredentials(", "s.writes.recordSuccess("} {
		at := strings.Index(body, statement)
		if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
			assert.Greater(t, at, last, "%q comes after the statement before it", statement)
			last = at
		}
	}

	// The SCIM delete (#504) is the same deactivation without a role write:
	// admin set → users → refresh_tokens → api_keys → role_audit_log, a
	// prefix-compatible subsequence; its user_deactivated row (#552) comes
	// after the revocation.
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM user patch"]["Rows locked"]), "scimUserService.Delete")
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM user patch"]["Order"]),
		"admin set → users → refresh_tokens → api_keys → role_audit_log")
	body = concurrencyDocFuncBody(t, "internal/service/scim_user_writes.go", "func (s *scimUserService) Delete(")
	last = -1
	for _, statement := range []string{"governance.run(ctx,", "lockAdminSet(tx)", "tx.lockUsers(", ".checkAfter(",
		"tx.users.UpdateFields(", "revokeAllCredentials(", "tx.record(", "s.writes.recordSuccess("} {
		at := strings.Index(body, statement)
		if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
			assert.Greater(t, at, last, "%q comes after the statement before it", statement)
			last = at
		}
	}
}

// TestConcurrencyDocSCIMGroupPatch pins the SCIM group patch flow (#506): the
// documented order and, in Patch, the admin set first, the users and roles
// locks, the membership write, the role audit rows and the sign-in audit rows
// after the commit.
func TestConcurrencyDocSCIMGroupPatch(t *testing.T) {
	fields := concurrencyDocFlowFields(t)
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM group patch"]["Order"]),
		"admin set → users → roles → scim_group_members")
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM group patch"]["Order"]),
		"scim_group_members → refresh_tokens → api_keys")
	assert.Contains(t, concurrencyDocNormalize(fields["SCIM group patch"]["Rows locked"]), "scimGroupService.Patch")

	body := concurrencyDocFuncBody(t, "internal/service/scim_group_writes.go", "func (s *scimGroupService) Patch(")
	last := -1
	for _, statement := range []string{"governance.run(ctx,", "lockAdminSet(tx)", "tx.lockUsers(", "tx.lockRoles(",
		"members.Remove(", "members.Add(", "revokeAllCredentials(", "endSessions(", "tx.record(", "s.writes.audit.Record("} {
		at := strings.Index(body, statement)
		if assert.GreaterOrEqual(t, at, 0, "%q is called", statement) {
			assert.Greater(t, at, last, "%q comes after the statement before it", statement)
			last = at
		}
	}
}
