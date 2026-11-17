package config

import (
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
// EXTENDING, DEVELOPMENT, ROADMAP, README and CHANGELOG. Where a fact is a
// property of the code (a lock that is or is not taken, a helper that is or is
// not used) the code is read too, so the document and the code cannot drift
// apart silently.

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
}

var concurrencyDocFieldLabels = []string{"Rows locked", "Order", "Transaction", "Retry", "Status"}

// concurrencyDocExpectedStatus is the status of every flow at a415cf1, with
// Login implemented by #336 (C2) and the password-reset request and API-key
// create by #342 (C3).
var concurrencyDocExpectedStatus = map[string]string{
	"User-rooted erasure":            "partial",
	"Customer-rooted erasure":        "partial",
	"Lead-rooted erasure":            "partial",
	"Lead conversion":                "implemented",
	"Lead update":                    "implemented",
	"Company delete":                 "implemented",
	"Bulk status":                    "implemented",
	"Login":                          "implemented",
	"Refresh rotation":               "implemented",
	"Password-reset request":         "implemented",
	"Password-reset confirm":         "implemented",
	"API-key create":                 "implemented",
	"RBAC user-role and role writes": "not implemented",
}

// concurrencyDocRetryFlows are the flows whose Retry line names
// WithTransactionAndRetry, with the retry bound constant each cites and the
// file that defines it.
var concurrencyDocRetryFlows = map[string]struct{ constant, file string }{
	"Lead conversion":        {"", ""},
	"Login":                  {"loginMaxRetries", "internal/service/auth_service.go"},
	"Password-reset request": {"resetRequestMaxRetries", "internal/service/auth_service.go"},
	"API-key create":         {"apiKeyCreateMaxRetries", "internal/service/apikey_service.go"},
}

var concurrencyDocExpectedTicket = map[string]string{
	"User-rooted erasure":            "C4",
	"Customer-rooted erasure":        "C4",
	"Lead-rooted erasure":            "C4",
	"Lead conversion":                "#182",
	"Lead update":                    "#182",
	"Company delete":                 "#182",
	"Bulk status":                    "#182, #242",
	"Login":                          "C2",
	"Refresh rotation":               "C2",
	"Password-reset request":         "C3",
	"Password-reset confirm":         "C3",
	"API-key create":                 "C3",
	"RBAC user-role and role writes": "R6, R7",
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
	leadService := readRepositoryFile(t, "internal/service/lead_service.go")
	assert.Contains(t, leadService, "const leadCompanyMaxRestarts = 3")
	assert.Equal(t, 1, strings.Count(leadService, "WithTransactionAndRetry("),
		"only lead conversion uses WithTransactionAndRetry in lead_service.go")

	// Bulk status: tasks are read without a lock, locked at the UPDATE, and
	// the completed-task check is not protected.
	bulk := flow("Bulk status")
	for _, want := range []string{
		"`GetTasksByIDs`",
		"reads the tasks without a lock",
		"the task batch locks only its task rows (at the `UPDATE` in `SetTaskStatus`",
		"its own `bulk_operations` row (`CompletePending`)",
		"so it can invert no cross-table order",
		"The completed-task check",
		"is a read before that `UPDATE` and is not protected by a row lock",
	} {
		assert.Contains(t, bulk, want, "Bulk status")
	}
	const bulkRepo = "internal/repository/bulk_status_repository.go"
	assert.NotContains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetTasksByIDs("), "Locking",
		"GetTasksByIDs is documented as a read without a lock")
	assert.Contains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetLeadsByIDs("), "clause.Locking")
	assert.Contains(t, concurrencyDocFuncBody(t, bulkRepo, "func (r *bulkStatusRepository) GetTicketsByIDs("), "clause.Locking")
	taskBatch := concurrencyDocFuncBody(t, "internal/service/bulk_status_service.go", "func (s *bulkStatusService) BulkSetTaskStatus(")
	assert.Contains(t, taskBatch, "txRepo.GetTasksByIDs(")
	assert.Contains(t, taskBatch, "TaskStatusCompleted")
	assert.Contains(t, taskBatch, "txRepo.SetTaskStatus(")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/service/bulk_status_service.go", "func (s *bulkStatusService) runBulkStatusUpdate("),
		"CompletePending(")

	const authService = "internal/service/auth_service.go"
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
		"a confirm victim (plain `WithTransaction`, no retry) returns the error with nothing written",
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

	reset := concurrencyDocFuncBody(t, authService, "func (s *authService) RequestPasswordReset(")
	assert.Contains(t, reset, "s.txManager.WithTransactionAndRetry(context.Background(),")
	assert.Contains(t, reset, "clause.Locking{Strength: \"UPDATE\"}")
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
	assert.Contains(t, readRepositoryFile(t, authService), "const resetRequestMaxRetries = 3")

	generate := concurrencyDocFuncBody(t, apiKeyService, "func (s *apiKeyService) Generate(")
	assert.Contains(t, generate, "s.txManager.WithTransactionAndRetry(context.Background(),")
	assert.Contains(t, generate, "clause.Locking{Strength: \"UPDATE\"}")
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
	} {
		assert.Contains(t, login, want, "Login")
	}
	assert.NotContains(t, login, "no transaction", "Login")
	assert.NotContains(t, login, "no lock held across statements", "Login")
	authenticate := concurrencyDocFuncBody(t, authService, "func (s *authService) authenticate(")
	assert.Contains(t, authenticate, "s.txManager.WithTransactionAndRetry(")
	assert.Contains(t, authenticate, "clause.Locking{Strength: \"UPDATE\"}")
	assert.Contains(t, authenticate, "s.loginInTransaction(")
	assert.Contains(t, concurrencyDocFuncBody(t, authService, "func (s *authService) LoginWithTokens("), "s.authenticate(")
	assert.Contains(t, concurrencyDocFuncBody(t, authService, "func (s *authService) Login("), "s.authenticate(")
	issuing := concurrencyDocFuncBody(t, authService, "func (s *authService) loginInTransaction(")
	lockAt := strings.Index(issuing, "userRepo.GetByID(")
	insertAt := strings.Index(issuing, "s.issueRefreshToken(")
	signAt := strings.Index(issuing, "s.accessTokenFor(")
	assert.True(t, lockAt >= 0 && insertAt > lockAt && signAt > insertAt,
		"loginInTransaction locks the owner, then inserts the refresh token, then signs")
	for _, sig := range []string{
		"func (s *authService) RefreshAccessToken(",
		"func (s *authService) ConfirmPasswordReset(",
	} {
		body := concurrencyDocFuncBody(t, authService, sig)
		assert.Contains(t, body, "s.txManager.WithTransaction(", sig)
		assert.Contains(t, body, "clause.Locking{Strength: \"UPDATE\"}", sig)
	}
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
	assert.Contains(t, flow("Password-reset confirm"), "password_reset_tokens → users → refresh_tokens")

	// RBAC: one UpdateFields in UserService.Update, and no roles table.
	rbac := flow("RBAC user-role and role writes")
	assert.Contains(t, rbac, "UserService.Update")
	assert.Contains(t, rbac, "no `roles` table")
	update := concurrencyDocFuncBody(t, "internal/service/user_service.go", "func (s *userService) Update(")
	assert.NotContains(t, update, "WithTransaction")
	assert.Contains(t, update, "s.userRepo.UpdateFields(")
	modelsDir := filepath.Join(repositoryRoot(t), "internal", "models")
	entries, err := os.ReadDir(modelsDir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src := readRepositoryFile(t, "internal/models/"+e.Name())
		assert.NotContains(t, src, `"roles"`, "internal/models/%s names a roles table", e.Name())
	}
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
	// #342: the credential-issuing edges of the users chain, and the reset
	// confirm's inversion of the request's order.
	lineWith("users → password_reset_tokens")
	lineWith("users → api_keys")
	lineWith("password_reset_tokens → users")
	rbac := lineWith("admin set → users (id ascending) → roles (id ascending)")
	assert.Contains(t, rbac, "#259")
	assert.Contains(t, rbac, "target")

	cross := concurrencyDocNormalize(concurrencyDocParagraph(t, section, "**Cross-chain order (undecided).**", concurrencyDocFile))
	for _, want := range []string{
		"No source specifies the order for flows spanning several chains",
		"`C4` (#183) owns that decision",
		"the current erasure order depends on the root",
		"the user-rooted erasure locks the user before its customers and leads",
	} {
		assert.Contains(t, cross, want, "Cross-chain paragraph")
	}

	// No arrow in the section joins tables of different chains: the section
	// states no combined cross-chain sequence.
	parenthetical := regexp.MustCompile(`\([^)]*\)`)
	word := regexp.MustCompile(`[A-Za-z_]+`)
	pairs := 0
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
			assert.True(t, sameChain, "Global lock order joins %s → %s across chains: %q", a, b, line)
		}
	}
	assert.GreaterOrEqual(t, pairs, 6, "the chains themselves were not recognised")
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
	assert.True(t, target, "a sentence says the rule is the #183 target")

	// The rule as the code implements it.
	tx := readRepositoryFile(t, "internal/utils/transaction.go")
	assert.Contains(t, tx, "func (tm *TransactionManager) WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error")
	assert.Contains(t, tx, "ErrNestedRetryDeferred = errors.New(")
	retry := concurrencyDocFuncBody(t, "internal/utils/transaction.go", "func (tm *TransactionManager) WithTransactionAndRetry(")
	assert.Contains(t, retry, "tm.runRoot(ctx, fn)")
	assert.Contains(t, retry, "!replayable || !isRetryableError(err)")
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
				assert.Contains(t, readRepositoryFile(t, bound.file), "const "+bound.constant+" = 3")
			}
		} else {
			assert.NotContains(t, retry, "WithTransactionAndRetry",
				"only the Lead conversion, Login, Password-reset request and API-key create Retry lines name it; %s does", name)
		}
	}
	for _, name := range []string{"Lead conversion", "Lead update"} {
		retry := fields[name]["Retry"]
		for _, want := range []string{"runLeadCompanyAttempts", "leadCompanyMaxRestarts", "errLeadCompanyMoved"} {
			assert.Contains(t, retry, want, "%s Retry", name)
		}
	}
	assert.Contains(t, fields["Lead update"]["Retry"], "no deadlock retry")
	for _, name := range []string{
		"User-rooted erasure", "Customer-rooted erasure", "Lead-rooted erasure",
		"Company delete", "Bulk status", "Refresh rotation", "Password-reset confirm",
	} {
		assert.Contains(t, fields[name]["Retry"], "none today", "%s Retry", name)
	}

	// The code behind the lead Retry lines.
	update := concurrencyDocFuncBody(t, "internal/service/lead_service.go", "func (s *leadService) Update(")
	assert.Contains(t, update, "runLeadCompanyAttempts(")
	assert.Contains(t, update, "s.txManager.WithTransaction(")
	assert.NotContains(t, update, "WithTransactionAndRetry")
	convert := concurrencyDocFuncBody(t, "internal/service/lead_service.go", "func (s *leadService) ConvertToCustomer(")
	assert.Contains(t, convert, "runLeadCompanyAttempts(")
	assert.Contains(t, convert, "s.txManager.WithTransactionAndRetry(")
	restart := concurrencyDocFuncBody(t, "internal/service/lead_service.go", "func shouldRestartLeadCompanyAttempt(")
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
	assert.Contains(t, after, "C2–C4 and R6–R7 are Paperclip ids with no GitHub issue yet")
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

	var gaps []string
	for _, b := range bullets {
		if strings.Contains(b, "GetTasksByIDs") {
			gaps = append(gaps, concurrencyDocNormalize(b))
		}
	}
	if assert.Len(t, gaps, 1, "one ROADMAP gap bullet names GetTasksByIDs") {
		assert.Contains(t, gaps[0], "](CONCURRENCY.md)")
		assert.Contains(t, gaps[0], "completed-task check")
		assert.Contains(t, gaps[0], "not protected by a row lock")
	}
}

// TestConcurrencyDocLeadErasureVsConversion pins the #337 order of the
// lead-rooted erasure — automation runs, then a locking read of the lead by
// id, then the customer by id, with a plain converted-lead pluck — to the
// document and to the code, and the inherited per-lead order of the customer-
// and user-rooted erasures.
func TestConcurrencyDocLeadErasureVsConversion(t *testing.T) {
	const cascade = "internal/repository/erasure_cascade.go"
	bodies := concurrencyDocFlowBodies(t)
	fields := concurrencyDocFlowFields(t)
	lead := concurrencyDocNormalize(bodies["Lead-rooted erasure"])
	order := fields["Lead-rooted erasure"]["Order"]

	for _, want := range []string{
		"automation runs (`cancelLeadAutomation`) → leads (locking read by id",
		"→ customers (locking read by id",
		"→ users → deals and quotes text",
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

	// The lead lock the Order line cites is the locking read in eraseLead.
	cite := regexp.MustCompile("`" + regexp.QuoteMeta(cascade) + `:([0-9]+)` + "`")
	leadAt := strings.Index(order, "leads (locking read by id")
	require.GreaterOrEqual(t, leadAt, 0)
	m := cite.FindStringSubmatch(order[leadAt:])
	require.NotNil(t, m, "the lead lock is cited with file:line")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	lines := strings.Split(readRepositoryFile(t, cascade), "\n")
	require.LessOrEqual(t, n, len(lines))
	assert.Contains(t, lines[n-1], `lockingRead(tx).Unscoped().Select("id", "status", "customer_id")`,
		"%s:%d is the locking read of the lead", cascade, n)

	// Customer- and user-rooted erasures inherit the per-lead order.
	for _, flow := range []string{"Customer-rooted erasure", "User-rooted erasure"} {
		assert.Contains(t, concurrencyDocNormalize(bodies[flow]), "customers → automation runs → leads", flow)
		assert.Contains(t, concurrencyDocNormalize(bodies[flow]), "`readErasedEmail`", flow)
	}

	// Lead conversion queued behind an erasure: not found, 404.
	conversion := concurrencyDocNormalize(bodies["Lead conversion"])
	assert.Contains(t, conversion, "A conversion queued behind a lead erasure finds the lead gone")
	assert.Contains(t, conversion, "404")

	// The "no shared order with lead conversion" statement is narrowed.
	flowsSection := concurrencyDocNormalize(concurrencyDocSectionBody(t, "Flows"))
	assert.NotContains(t, flowsSection, "All three erasures are one transaction each, but they share no order with lead conversion")
	assert.Contains(t, flowsSection, "Customer- and user-rooted erasures still share no order with lead conversion")

	// The code behind it.
	eraseLead := concurrencyDocFuncBody(t, cascade, "func (c *erasureCascade) eraseLead(")
	cancelAt := strings.Index(eraseLead, "plan.BeforeScrub(tx, id)")
	lockAt := strings.Index(eraseLead, `lockingRead(tx).Unscoped().Select("id", "status", "customer_id")`)
	refuseAt := strings.Index(eraseLead, "return ErrConvertedLeadNeedsAdmin")
	scrubAt := strings.Index(eraseLead, "eraseRecord(tx, id, plan)")
	assert.True(t, cancelAt >= 0 && lockAt > cancelAt && refuseAt > lockAt && scrubAt > refuseAt,
		"eraseLead cancels the runs, locks the lead, decides, then scrubs")
	assert.Equal(t, 1, strings.Count(eraseLead, "First(&"), "eraseLead reads the lead once, under the lock")
	assert.Contains(t, leadErasurePlanBody(t, cascade), "BeforeScrub: cancelLeadAutomation")

	eraseCustomer := concurrencyDocFuncBody(t, cascade, "func (c *erasureCascade) eraseCustomer(")
	assert.Contains(t, eraseCustomer, `lockingRead(tx).Unscoped().Select("id", "user_id")`)
	assert.Contains(t, eraseCustomer, "tx.Unscoped().Model(&models.Lead{}).\n\t\tWhere(\"customer_id = ?\", id)",
		"the converted-lead pluck is a plain read")
	assert.Contains(t, concurrencyDocFuncBody(t, "internal/repository/erasure.go", "func readErasedEmail("),
		"lockingRead(tx).Unscoped()")
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
	if next := strings.Index(unreleased, "\n## ["); next >= 0 {
		unreleased = unreleased[:next]
	}
	var entry bool
	for _, b := range concurrencyDocBullets(unreleased) {
		if strings.Contains(b, "docs/CONCURRENCY.md") && strings.Contains(b, "#323") {
			entry = true
		}
	}
	assert.True(t, entry, "an [Unreleased] entry names docs/CONCURRENCY.md and #323")
}
