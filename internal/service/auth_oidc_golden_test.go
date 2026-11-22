package service

// Issue #636: a recorded run of the single sign-on callback. One fixed,
// ordered sequence of 20 callbacks (3 sign-ins, then 17 refusals) runs against
// the in-process test issuer on one database, with no reset between steps,
// and every step's error class, auth_audit_log row, role_audit_log rows and
// linked account are compared with testdata/oidc_signin_golden.json.
// TestOIDCSignInGoldenSQLite and TestOIDCSignInGoldenMySQL compare their run
// with the same file, so every step has the same outcome on every engine;
// only the SQLite root writes the file (-update-oidc-golden).
// TestOIDCSignInGoldenCompare checks that the comparison reports a changed,
// a missing and an extra recorded value by its step.
//
// identity_taken is not a step: it is reached only through the unique-index
// collision of a concurrent link insert, whose outcome differs by engine
// (scim_oid_concurrent_link_before_insert in TestOIDCSignInMySQL).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateOIDCGolden = flag.Bool("update-oidc-golden", false, "rewrite testdata/oidc_signin_golden.json from the SQLite run")

const (
	oidcGoldenPath      = "testdata/oidc_signin_golden.json"
	oidcGoldenClientIP  = "203.0.113.7"
	oidcGoldenUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// oidcGoldenStepNames is the fixed order of the run.
var oidcGoldenStepNames = []string{
	"provision_sales", "resign_unchanged", "remap_support",
	"state_replayed", "binding_missing", "binding_mismatch", "id_token_invalid", "nonce_mismatch",
	"subject_invalid", "groups_malformed", "group_overage", "userinfo_subject_mismatch", "groups_unavailable",
	"no_mapping_match", "email_taken", "account_inactive", "account_erased", "last_admin",
	"token_exchange_failed", "issuer_changed",
}

// oidcGoldenReasons is the set of refusal reasons the run records.
var oidcGoldenReasons = []string{
	"state_invalid", "binding_missing", "binding_mismatch", "id_token_invalid", "nonce_mismatch",
	"subject_invalid", "groups_malformed", "group_overage", "userinfo_subject_mismatch", "groups_unavailable",
	"no_mapping_match", "email_taken", "account_inactive", "account_erased", "last_admin",
	"token_exchange_failed", "issuer_changed",
}

type oidcGolden struct {
	Steps []oidcGoldenStep `json:"steps"`
}

type oidcGoldenStep struct {
	Name               string                `json:"name"`
	Error              string                `json:"error"`
	Tokens             bool                  `json:"tokens"`
	RefreshTokensAdded int64                 `json:"refresh_tokens_added"`
	AuthAudit          []oidcGoldenAuthRow   `json:"auth_audit"`
	RoleAudit          []oidcGoldenRoleRow   `json:"role_audit"`
	Account            *oidcGoldenAccountRow `json:"account"`
}

type oidcGoldenAuthRow struct {
	Method          string  `json:"method"`
	Outcome         string  `json:"outcome"`
	Reason          string  `json:"reason"`
	User            *string `json:"user"`
	Attempt         *string `json:"attempt"`
	APIKeyID        *uint   `json:"api_key_id"`
	RequestID       string  `json:"request_id"`
	SubjectRef      *string `json:"subject_ref"`
	ClaimRoles      *string `json:"claim_roles"`
	ClaimGroups     *string `json:"claim_groups"`
	ClientNetwork   string  `json:"client_network"`
	UserAgentFamily string  `json:"user_agent_family"`
	Attempts        uint    `json:"attempts"`
	AggregateKey    *string `json:"aggregate_key"`
}

type oidcGoldenRoleRow struct {
	Action      string      `json:"action"`
	Actor       *string     `json:"actor"`
	Target      *string     `json:"target"`
	AuthMethod  string      `json:"auth_method"`
	Role        *string     `json:"role"`
	RoleName    string      `json:"role_name"`
	BeforeState interface{} `json:"before_state"`
	AfterState  interface{} `json:"after_state"`
}

type oidcGoldenAccountRow struct {
	Label      string `json:"label"`
	Role       string `json:"role"`
	IsActive   bool   `json:"is_active"`
	Identities int64  `json:"identities"`
}

// oidcGoldenRun is the state of one continuous run on one database.
type oidcGoldenRun struct {
	f *oidcFixture
	// labels names every user the run knows by id; roleNames names every
	// role by id.
	labels    map[uint]string
	roleNames map[uint]string
	// other is the second issuer of issuer_changed, nil before it.
	other *oidcTestIssuer
	// lastCode and lastState are the previous callback's, for the replay.
	lastCode, lastState string
}

// oidcGoldenCallback is what one step did: the callback's result, the state
// it posted and the subject of the ID token it carried ("" when the step
// issued no ID token of its own).
type oidcGoldenCallback struct {
	tokens  *AuthTokens
	err     error
	state   string
	subject string
}

func oidcGoldenContext(step string) context.Context {
	return utils.WithClientInfo(utils.WithRequestID(context.Background(), "golden-"+step), oidcGoldenClientIP, oidcGoldenUserAgent)
}

func oidcGoldenSpec(sub, email string) oidcTokenSpec {
	return oidcTokenSpec{sub: sub, email: email, groups: []string{"unrelated", oidcGroupSales}}
}

// signIn runs Start, the issuer's authorization and Callback under ctx.
func (r *oidcGoldenRun) signIn(t *testing.T, ctx context.Context, spec oidcTokenSpec) oidcGoldenCallback {
	t.Helper()
	authURL, err := oidcStartURL(r.f.svc.Start(ctx))
	require.NoError(t, err)
	code, state := r.f.issuer.authorize(t, authURL, spec)
	r.lastCode, r.lastState = code, state
	tokens, err := r.f.svc.Callback(ctx, code, state, state)
	return oidcGoldenCallback{tokens: tokens, err: err, state: state, subject: spec.sub}
}

// steps is the fixed sequence. A step that changes the settings or the
// issuer restores them before it returns.
func (r *oidcGoldenRun) steps() []struct {
	name string
	run  func(t *testing.T, ctx context.Context) oidcGoldenCallback
} {
	f := r.f
	const jitSub, jitEmail = "golden-jit-subject", "golden-jit@idp.example.test"
	return []struct {
		name string
		run  func(t *testing.T, ctx context.Context) oidcGoldenCallback
	}{
		{"provision_sales", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec(jitSub, jitEmail))
		}},
		{"resign_unchanged", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec(jitSub, jitEmail))
		}},
		{"remap_support", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcTokenSpec{sub: jitSub, email: jitEmail, roles: []string{oidcAppRole}})
		}},
		{"state_replayed", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			// The previous step's code and state again; no new ID token.
			tokens, err := f.svc.Callback(ctx, r.lastCode, r.lastState, r.lastState)
			return oidcGoldenCallback{tokens: tokens, err: err, state: r.lastState}
		}},
		{"binding_missing", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			authURL, err := oidcStartURL(f.svc.Start(ctx))
			require.NoError(t, err)
			spec := oidcGoldenSpec("golden-binding-missing", "golden-binding-missing@idp.example.test")
			code, state := f.issuer.authorize(t, authURL, spec)
			tokens, err := f.svc.Callback(ctx, code, state, "")
			return oidcGoldenCallback{tokens: tokens, err: err, state: state, subject: spec.sub}
		}},
		{"binding_mismatch", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			other, err := f.svc.Start(ctx)
			require.NoError(t, err)
			authURL, err := oidcStartURL(f.svc.Start(ctx))
			require.NoError(t, err)
			spec := oidcGoldenSpec("golden-binding-mismatch", "golden-binding-mismatch@idp.example.test")
			code, state := f.issuer.authorize(t, authURL, spec)
			require.NotEqual(t, state, other.State)
			tokens, err := f.svc.Callback(ctx, code, state, other.State)
			return oidcGoldenCallback{tokens: tokens, err: err, state: state, subject: spec.sub}
		}},
		{"id_token_invalid", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			spec := oidcGoldenSpec("golden-id-token", "golden-id-token@idp.example.test")
			spec.otherKey = true
			return r.signIn(t, ctx, spec)
		}},
		{"nonce_mismatch", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			spec := oidcGoldenSpec("golden-nonce", "golden-nonce@idp.example.test")
			spec.nonce = "not-the-nonce-of-the-request"
			return r.signIn(t, ctx, spec)
		}},
		{"subject_invalid", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec(" golden-subject", "golden-subject@idp.example.test"))
		}},
		{"groups_malformed", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			spec := oidcGoldenSpec("golden-groups-malformed", "golden-groups-malformed@idp.example.test")
			spec.groups = oidcGroupSales
			return r.signIn(t, ctx, spec)
		}},
		{"group_overage", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcOverage("golden-overage", "golden-overage@idp.example.test"))
		}},
		{"userinfo_subject_mismatch", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			other := "golden-userinfo-other"
			f.issuer.setUserInfo(oidcFakeUserInfo{sub: &other, groups: json.RawMessage(`["` + oidcGroupSales + `"]`)})
			defer f.issuer.resetUserInfo()
			return r.signIn(t, ctx, oidcTokenSpec{sub: "golden-userinfo-mismatch", email: "golden-userinfo-mismatch@idp.example.test"})
		}},
		{"groups_unavailable", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			f.issuer.setUserInfo(oidcFakeUserInfo{mode: userinfoModeStatus500})
			defer f.issuer.resetUserInfo()
			return r.signIn(t, ctx, oidcTokenSpec{sub: "golden-userinfo-down", email: "golden-userinfo-down@idp.example.test"})
		}},
		{"no_mapping_match", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			spec := oidcGoldenSpec("golden-no-match", "golden-no-match@idp.example.test")
			spec.groups = []string{"crm-SALES", "other"}
			return r.signIn(t, ctx, spec)
		}},
		{"email_taken", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec("golden-email-taken", "golden-password-owner@crm.example.test"))
		}},
		{"account_inactive", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec("golden-inactive", "golden-inactive@idp.example.test"))
		}},
		{"account_erased", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec("golden-erased", "golden-erased@idp.example.test"))
		}},
		{"last_admin", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			return r.signIn(t, ctx, oidcGoldenSpec("golden-admin-only", "golden-admin-only@idp.example.test"))
		}},
		{"token_exchange_failed", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			f.issuer.setTokenStatus(http.StatusInternalServerError)
			defer f.issuer.setTokenStatus(0)
			return r.signIn(t, ctx, oidcGoldenSpec("golden-token-exchange", "golden-token-exchange@idp.example.test"))
		}},
		{"issuer_changed", func(t *testing.T, ctx context.Context) oidcGoldenCallback {
			authURL, err := oidcStartURL(f.svc.Start(ctx))
			require.NoError(t, err)
			spec := oidcGoldenSpec("golden-issuer-moved", "golden-issuer-moved@idp.example.test")
			code, state := f.issuer.authorize(t, authURL, spec)
			r.other = newOIDCTestIssuer(t)
			// Accounts are linked to the fixture issuer, so the change is
			// confirmed; no account is linked to the other one, so the
			// restore needs no confirmation.
			enabled, secret := true, oidcTestClientSecret
			require.NoError(t, f.sso.Update(context.Background(), SSOSettingsUpdate{
				Enabled: &enabled, IssuerURL: r.other.URL(), ClientID: oidcTestClientID, ClientSecret: &secret, RoleMapping: f.mapping(),
				ConfirmIssuerChange: true,
			}))
			defer f.configure(t, f.mapping())
			tokens, err := f.svc.Callback(ctx, code, state, state)
			return oidcGoldenCallback{tokens: tokens, err: err, state: state, subject: spec.sub}
		}},
	}
}

// maxID is the highest id of table, 0 when it is empty.
func (r *oidcGoldenRun) maxID(t *testing.T, table string) uint {
	t.Helper()
	var id uint
	require.NoError(t, r.f.db.Raw("SELECT COALESCE(MAX(id), 0) FROM "+table).Scan(&id).Error, table)
	return id
}

// normalize replaces the test issuers' URLs, whose ports are random.
func (r *oidcGoldenRun) normalize(value string) string {
	value = strings.ReplaceAll(value, r.f.issuer.URL(), "<issuer>")
	if r.other != nil {
		value = strings.ReplaceAll(value, r.other.URL(), "<other-issuer>")
	}
	return value
}

func (r *oidcGoldenRun) normalizePtr(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := r.normalize(*value)
	return &normalized
}

func (r *oidcGoldenRun) label(t *testing.T, id *uint) *string {
	t.Helper()
	if id == nil {
		return nil
	}
	label, ok := r.labels[*id]
	require.True(t, ok, "user %d is not one the run knows", *id)
	return &label
}

func (r *oidcGoldenRun) roleName(t *testing.T, id *uint) *string {
	t.Helper()
	if id == nil {
		return nil
	}
	name, ok := r.roleNames[*id]
	require.True(t, ok, "role %d is not a stored role", *id)
	return &name
}

// state decodes a role audit state and replaces each role_id value with the
// role's name; everything else stays as stored.
func (r *oidcGoldenRun) state(t *testing.T, raw *string) interface{} {
	t.Helper()
	if raw == nil {
		return nil
	}
	var decoded interface{}
	require.NoError(t, json.Unmarshal([]byte(r.normalize(*raw)), &decoded), "role audit state %q", *raw)
	var walk func(value interface{}) interface{}
	walk = func(value interface{}) interface{} {
		switch typed := value.(type) {
		case map[string]interface{}:
			for key, inner := range typed {
				if key == "role_id" {
					if number, ok := inner.(float64); ok {
						id := uint(number)
						typed[key] = *r.roleName(t, &id)
						continue
					}
				}
				typed[key] = walk(inner)
			}
		case []interface{}:
			for i := range typed {
				typed[i] = walk(typed[i])
			}
		}
		return value
	}
	return walk(decoded)
}

// account is the user (issuer, subject) is linked to, nil when none is.
func (r *oidcGoldenRun) account(t *testing.T, subject string) *oidcGoldenAccountRow {
	t.Helper()
	if subject == "" {
		return nil
	}
	var identities []models.UserIdentity
	require.NoError(t, r.f.db.Where("issuer = ? AND subject = ?", r.f.issuer.Issuer(), subject).Find(&identities).Error)
	if len(identities) == 0 {
		return nil
	}
	require.Len(t, identities, 1)
	user := r.f.user(t, identities[0].UserID)
	return &oidcGoldenAccountRow{
		Label:      *r.label(t, &user.ID),
		Role:       string(user.Role),
		IsActive:   user.IsActive,
		Identities: r.f.count(t, "user_identities", "user_id = ?", user.ID),
	}
}

func (r *oidcGoldenRun) activeAdmins(t *testing.T) int64 {
	return r.f.count(t, "users", "role = ? AND is_active = ? AND deleted_at IS NULL", models.RoleAdmin, true)
}

// oidcGoldenRecord runs the whole sequence on db and returns what it
// recorded.
func oidcGoldenRecord(t *testing.T, f *oidcFixture) oidcGolden {
	t.Helper()
	f.configure(t, f.mapping())
	run := &oidcGoldenRun{f: f, labels: map[uint]string{}, roleNames: map[uint]string{}}
	var roles []models.Role
	require.NoError(t, f.db.Find(&roles).Error)
	for _, role := range roles {
		run.roleNames[role.ID] = role.Name
	}

	// The seeded accounts, before the first step: admin_only is the only
	// active admin.
	require.Zero(t, f.users(t), "the run starts without users")
	adminOnly := f.seedUser(t, "golden-admin-only@crm.example.test", models.RoleAdmin, true, "")
	inactive := f.seedUser(t, "golden-inactive@crm.example.test", models.RoleSales, false, "")
	erased := f.seedUser(t, "golden-erased@crm.example.test", models.RoleSales, true, "")
	passwordOwner := f.seedUser(t, "golden-password-owner@crm.example.test", models.RoleSales, true, "Golden-Passw0rd!-0636")
	f.link(t, adminOnly.ID, "golden-admin-only")
	f.link(t, inactive.ID, "golden-inactive")
	f.link(t, erased.ID, "golden-erased")
	require.NoError(t, repository.NewUserRepository(f.db).Delete(context.Background(), erased.ID))
	for label, id := range map[string]uint{"admin_only": adminOnly.ID, "inactive": inactive.ID, "erased": erased.ID, "password_owner": passwordOwner.ID} {
		run.labels[id] = label
	}

	var golden oidcGolden
	for _, step := range run.steps() {
		authBefore, roleBefore, refreshBefore := run.maxID(t, "auth_audit_log"), run.maxID(t, "role_audit_log"), run.maxID(t, "refresh_tokens")
		if step.name == "last_admin" {
			require.Equal(t, int64(1), run.activeAdmins(t), "admin_only is the only active admin")
		}
		result := step.run(t, oidcGoldenContext(step.name))
		recorded := oidcGoldenStep{Name: step.name, Tokens: result.tokens != nil, AuthAudit: []oidcGoldenAuthRow{}, RoleAudit: []oidcGoldenRoleRow{}}
		switch {
		case result.err == nil:
		case errors.Is(result.err, ErrSSOSignInFailed):
			recorded.Error = "sign_in_failed"
		case errors.Is(result.err, ErrSSOUnavailable):
			recorded.Error = "unavailable"
		default:
			require.FailNow(t, "an unexpected error class", "step %s: %v", step.name, result.err)
		}
		if step.name == "provision_sales" {
			require.NotNil(t, result.tokens, "step %s signs in", step.name)
			require.NotNil(t, result.tokens.User)
			run.labels[result.tokens.User.ID] = "jit"
		}

		var authRows []models.AuthAuditLog
		require.NoError(t, f.db.Where("id > ?", authBefore).Order("id").Find(&authRows).Error)
		require.Len(t, authRows, 1, "step %s writes exactly one auth_audit_log row", step.name)
		for _, row := range authRows {
			var attempt *string
			if row.AttemptID != nil {
				var claimed models.OIDCLoginAttempt
				require.NoError(t, f.db.Where("state_hash = ?", hashOpaqueToken(result.state, f.auth.sessionTokenSecret())).First(&claimed).Error,
					"step %s: the attempt of the state", step.name)
				require.Equal(t, claimed.ID, *row.AttemptID, "step %s: the audit row names the step's attempt", step.name)
				value := "claimed"
				attempt = &value
			}
			var subjectRef *string
			if row.SubjectRef != nil {
				require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), *row.SubjectRef, "step %s", step.name)
				value := "hmac"
				subjectRef = &value
			}
			recorded.AuthAudit = append(recorded.AuthAudit, oidcGoldenAuthRow{
				Method: run.normalize(row.Method), Outcome: run.normalize(row.Outcome), Reason: run.normalize(row.Reason),
				User: run.label(t, row.UserID), Attempt: attempt, APIKeyID: row.APIKeyID, RequestID: run.normalize(row.RequestID),
				SubjectRef: subjectRef, ClaimRoles: run.normalizePtr(row.ClaimRoles), ClaimGroups: run.normalizePtr(row.ClaimGroups),
				ClientNetwork: run.normalize(row.ClientNetwork), UserAgentFamily: run.normalize(row.UserAgentFamily),
				Attempts: row.Attempts, AggregateKey: run.normalizePtr(row.AggregateKey),
			})
		}

		var roleRows []models.RoleAuditEntry
		require.NoError(t, f.db.Where("id > ?", roleBefore).Order("id").Find(&roleRows).Error)
		for _, row := range roleRows {
			recorded.RoleAudit = append(recorded.RoleAudit, oidcGoldenRoleRow{
				Action: run.normalize(string(row.Action)), Actor: run.label(t, row.ActorUserID), Target: run.label(t, row.TargetUserID),
				AuthMethod: run.normalize(row.AuthMethod), Role: run.roleName(t, row.RoleID), RoleName: run.normalize(row.RoleName),
				BeforeState: run.state(t, row.BeforeState), AfterState: run.state(t, row.AfterState),
			})
		}
		recorded.RefreshTokensAdded = f.count(t, "refresh_tokens", "id > ?", refreshBefore)
		recorded.Account = run.account(t, result.subject)

		switch step.name {
		case "email_taken":
			owner := f.user(t, passwordOwner.ID)
			assert.Equal(t, models.RoleSales, owner.Role, "password_owner keeps its role")
			assert.True(t, owner.IsActive, "password_owner stays active")
			assert.Equal(t, int64(0), f.count(t, "user_identities", "user_id = ?", passwordOwner.ID), "password_owner gets no identity")
		case "last_admin":
			assert.Equal(t, models.RoleAdmin, f.user(t, adminOnly.ID).Role, "admin_only stays admin")
		}
		golden.Steps = append(golden.Steps, recorded)
	}
	return golden
}

func oidcGoldenMarshal(t *testing.T, golden oidcGolden) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(golden, "", "  ")
	require.NoError(t, err)
	return append(raw, '\n')
}

func oidcGoldenLoad(t *testing.T) oidcGolden {
	t.Helper()
	raw, err := os.ReadFile(oidcGoldenPath)
	require.NoError(t, err, "the committed golden; write it with -update-oidc-golden")
	var golden oidcGolden
	require.NoError(t, json.Unmarshal(raw, &golden))
	return golden
}

// oidcGoldenDiff compares two runs step by step and returns one line per
// difference, each naming the step and the field.
func oidcGoldenDiff(want, got oidcGolden) []string {
	var diffs []string
	for i := 0; i < len(want.Steps) || i < len(got.Steps); i++ {
		switch {
		case i >= len(got.Steps):
			diffs = append(diffs, fmt.Sprintf("step %s: missing from the run", want.Steps[i].Name))
			continue
		case i >= len(want.Steps):
			diffs = append(diffs, fmt.Sprintf("step %s: not in the golden", got.Steps[i].Name))
			continue
		}
		name := want.Steps[i].Name
		if got.Steps[i].Name != name {
			name = want.Steps[i].Name + "/" + got.Steps[i].Name
		}
		goldenJSONDiff("step "+name, "", goldenJSONValue(want.Steps[i]), goldenJSONValue(got.Steps[i]), &diffs)
	}
	return diffs
}

// goldenJSONValue is value as decoded JSON.
func goldenJSONValue(value interface{}) interface{} {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

// goldenJSONDiff appends "<prefix>: <path>: golden <want>, run <got>" for
// every leaf that differs.
func goldenJSONDiff(prefix, path string, want, got interface{}, diffs *[]string) {
	field := path
	if field == "" {
		field = "."
	}
	switch w := want.(type) {
	case map[string]interface{}:
		g, ok := got.(map[string]interface{})
		if !ok {
			break
		}
		keys := map[string]bool{}
		for key := range w {
			keys[key] = true
		}
		for key := range g {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			wv, wok := w[key]
			gv, gok := g[key]
			switch {
			case !gok:
				*diffs = append(*diffs, fmt.Sprintf("%s: %s.%s: golden %s, run has no field", prefix, path, key, goldenJSONText(wv)))
			case !wok:
				*diffs = append(*diffs, fmt.Sprintf("%s: %s.%s: golden has no field, run %s", prefix, path, key, goldenJSONText(gv)))
			default:
				goldenJSONDiff(prefix, path+"."+key, wv, gv, diffs)
			}
		}
		return
	case []interface{}:
		g, ok := got.([]interface{})
		if !ok {
			break
		}
		if len(w) != len(g) {
			*diffs = append(*diffs, fmt.Sprintf("%s: %s: golden %d entries, run %d: golden %s, run %s",
				prefix, field, len(w), len(g), goldenJSONText(w), goldenJSONText(g)))
			return
		}
		for i := range w {
			goldenJSONDiff(prefix, fmt.Sprintf("%s[%d]", path, i), w[i], g[i], diffs)
		}
		return
	}
	if !reflect.DeepEqual(want, got) {
		*diffs = append(*diffs, fmt.Sprintf("%s: %s: golden %s, run %s", prefix, field, goldenJSONText(want), goldenJSONText(got)))
	}
}

func goldenJSONText(value interface{}) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

// assertOIDCGoldenShape checks the fixed step list and the refusal reasons
// of a recorded run.
func assertOIDCGoldenShape(t *testing.T, golden oidcGolden) {
	t.Helper()
	names := make([]string, 0, len(golden.Steps))
	reasons := map[string]bool{}
	for _, step := range golden.Steps {
		names = append(names, step.Name)
		require.Len(t, step.AuthAudit, 1, "step %s has one auth_audit_log row", step.Name)
		for _, row := range step.AuthAudit {
			if row.Reason != "" {
				reasons[row.Reason] = true
			}
		}
	}
	require.Equal(t, oidcGoldenStepNames, names, "the fixed step list")
	got := make([]string, 0, len(reasons))
	for reason := range reasons {
		got = append(got, reason)
		assert.Contains(t, authAuditReasonList, reason, "a known reason code")
	}
	sort.Strings(got)
	want := append([]string(nil), oidcGoldenReasons...)
	sort.Strings(want)
	require.Equal(t, want, got, "the refusal reasons of the run")
}

// runOIDCSignInGolden records one run on f and compares it with the
// committed golden; write rewrites the file first.
func runOIDCSignInGolden(t *testing.T, f *oidcFixture, write bool) {
	t.Helper()
	got := oidcGoldenRecord(t, f)
	assertOIDCGoldenShape(t, got)
	if write {
		require.NoError(t, os.MkdirAll(filepath.Dir(oidcGoldenPath), 0o755))
		require.NoError(t, os.WriteFile(oidcGoldenPath, oidcGoldenMarshal(t, got), 0o644))
	}
	want := oidcGoldenLoad(t)
	assertOIDCGoldenShape(t, want)
	if diffs := oidcGoldenDiff(want, got); len(diffs) > 0 {
		t.Fatalf("the run differs from %s:\n%s", oidcGoldenPath, strings.Join(diffs, "\n"))
	}
}

func TestOIDCSignInGoldenSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runOIDCSignInGolden(t, newOIDCFixture(t, db, nil), *updateOIDCGolden)
}

func TestOIDCSignInGoldenMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runOIDCSignInGolden(t, newOIDCFixture(t, db, nil), false)
}

func TestOIDCSignInGoldenCompare(t *testing.T) {
	committed := oidcGoldenLoad(t)
	assertOIDCGoldenShape(t, committed)
	raw, err := os.ReadFile(oidcGoldenPath)
	require.NoError(t, err)
	assert.NotRegexp(t, regexp.MustCompile(`127\.0\.0\.1|https?://|@`), string(raw), "no issuer URL or email address in the golden")

	// clone is an independent deep copy of the committed golden.
	clone := func(t *testing.T) oidcGolden {
		t.Helper()
		var copied oidcGolden
		require.NoError(t, json.Unmarshal(oidcGoldenMarshal(t, committed), &copied))
		return copied
	}
	stepIndex := func(t *testing.T, golden oidcGolden, name string) int {
		t.Helper()
		for i, step := range golden.Steps {
			if step.Name == name {
				return i
			}
		}
		require.FailNow(t, "no such step", name)
		return -1
	}
	assertNamed := func(t *testing.T, diffs []string, step string) {
		t.Helper()
		require.NotEmpty(t, diffs, "the change is reported")
		for _, diff := range diffs {
			assert.Contains(t, diff, "step "+step+":", "the difference names the step")
		}
	}

	t.Run("changed_reason", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "nonce_mismatch")
		altered.Steps[i].AuthAudit[0].Reason = "id_token_invalid"
		assertNamed(t, oidcGoldenDiff(committed, altered), "nonce_mismatch")
	})
	t.Run("removed_auth_audit_row", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "last_admin")
		altered.Steps[i].AuthAudit = []oidcGoldenAuthRow{}
		assertNamed(t, oidcGoldenDiff(committed, altered), "last_admin")
	})
	t.Run("extra_role_audit_row", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "resign_unchanged")
		jit := "jit"
		altered.Steps[i].RoleAudit = append(altered.Steps[i].RoleAudit, oidcGoldenRoleRow{
			Action: string(models.RoleAuditUserRoleChanged), Actor: &jit, Target: &jit, AuthMethod: AuthMethodOIDC,
		})
		assertNamed(t, oidcGoldenDiff(committed, altered), "resign_unchanged")
	})
	t.Run("unaltered", func(t *testing.T) {
		assert.Empty(t, oidcGoldenDiff(committed, clone(t)))
	})
}
