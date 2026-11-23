package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In-memory fakes of every host interface the sign-in uses.

const (
	testRedirectURL = "https://crm.example.test/auth/callback"
	testGroupSales  = "crm-sales-group"
	testRoleSales   = uint(7)
	testTenant      = "tenant-0648"
)

// memIdentities is an identity store. near holds rows Get returns for a key
// although they differ from it (a collation near match).
type memIdentities struct {
	mu        sync.Mutex
	rows      map[[2]string]idkit.Identity
	near      map[[2]string]idkit.Identity
	gets      [][2]string
	created   []idkit.Identity
	createErr error
}

func newMemIdentities() *memIdentities {
	return &memIdentities{rows: map[[2]string]idkit.Identity{}, near: map[[2]string]idkit.Identity{}}
}

func (m *memIdentities) Get(_ context.Context, issuer, subject string) (*idkit.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]string{issuer, subject}
	m.gets = append(m.gets, key)
	if row, ok := m.near[key]; ok {
		return &row, nil
	}
	if row, ok := m.rows[key]; ok {
		return &row, nil
	}
	return nil, fmt.Errorf("identity: %w", idkit.ErrNotFound)
}

func (m *memIdentities) Create(_ context.Context, identity idkit.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	key := [2]string{identity.Issuer, identity.Subject}
	if _, ok := m.rows[key]; ok {
		return fmt.Errorf("identity: %w", idkit.ErrIdentityTaken)
	}
	m.rows[key] = identity
	m.created = append(m.created, identity)
	return nil
}

func (m *memIdentities) add(identity idkit.Identity) {
	m.rows[[2]string{identity.Issuer, identity.Subject}] = identity
}

// memAccounts is an account store that records its calls.
type memAccounts struct {
	mu           sync.Mutex
	next         int
	signIns      []string
	roles        []uint
	provisioned  []idkit.Profile
	signInErr    error
	provisionErr error
}

func (m *memAccounts) SignIn(_ context.Context, accountID string, roleID uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signIns = append(m.signIns, accountID)
	m.roles = append(m.roles, roleID)
	return m.signInErr
}

func (m *memAccounts) Provision(_ context.Context, profile idkit.Profile, roleID uint) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provisioned = append(m.provisioned, profile)
	m.roles = append(m.roles, roleID)
	if m.provisionErr != nil {
		return "", m.provisionErr
	}
	m.next++
	return strconv.Itoa(100 + m.next), nil
}

// memSessions issues opaque sessions and records the requests.
type memSessions struct {
	mu       sync.Mutex
	requests []idkit.SessionRequest
}

func (m *memSessions) Issue(_ context.Context, req idkit.SessionRequest) (idkit.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	return idkit.Session{AccountID: req.AccountID, AccessToken: "session-access-" + req.AccountID, RefreshToken: "session-refresh"}, nil
}

// memAttempts keeps attempts by state hash.
type memAttempts struct {
	mu       sync.Mutex
	seq      int
	byHash   map[string]idkit.Attempt
	created  []idkit.Attempt
	claimErr error
}

func newMemAttempts() *memAttempts { return &memAttempts{byHash: map[string]idkit.Attempt{}} }

func (m *memAttempts) Create(_ context.Context, attempt idkit.Attempt) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	attempt.ID = strconv.Itoa(m.seq)
	m.byHash[attempt.StateHash] = attempt
	m.created = append(m.created, attempt)
	return attempt.ID, nil
}

func (m *memAttempts) Claim(_ context.Context, stateHash string, now time.Time) (*idkit.Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	attempt, ok := m.byHash[stateHash]
	if !ok || !now.Before(attempt.ExpiresAt) {
		return nil, fmt.Errorf("attempt: %w", idkit.ErrNotFound)
	}
	delete(m.byHash, stateHash)
	return &attempt, nil
}

func (m *memAttempts) Prune(context.Context, time.Time, int) (int64, error) { return 0, nil }

// memAudit records the events.
type memAudit struct {
	mu     sync.Mutex
	events []idkit.AuditEvent
}

func (m *memAudit) Record(_ context.Context, event idkit.AuditEvent) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return strconv.Itoa(500 + len(m.events))
}

func (m *memAudit) last(t *testing.T) idkit.AuditEvent {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.events, "an audit event was recorded")
	return m.events[len(m.events)-1]
}

// memConfig returns a fixed configuration.
type memConfig struct{ cfg idkit.Config }

func (m *memConfig) Config(context.Context) (idkit.Config, error) { return m.cfg, nil }

// txKey marks the ctx memTx hands to fn.
type txKey struct{}

// memTx runs fn once, or twice when replay is set (the first run's error is
// discarded, as a deadlock replay would).
type memTx struct {
	runs   int
	replay bool
}

func (m *memTx) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx = context.WithValue(ctx, txKey{}, true)
	m.runs++
	err := fn(ctx)
	if m.replay {
		m.runs++
		err = fn(ctx)
	}
	return err
}

// logEntry is one recorded Logger call.
type logEntry struct {
	level, msg string
	fields     []idkit.Field
}

type memLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (m *memLogger) Info(_ context.Context, msg string, fields ...idkit.Field) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, logEntry{"info", msg, fields})
}

func (m *memLogger) Warn(_ context.Context, msg string, fields ...idkit.Field) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, logEntry{"warn", msg, fields})
}

// fixture is a Service over the fakes and an oidctest issuer.
type fixture struct {
	issuer     *oidctest.Issuer
	svc        *Service
	config     *memConfig
	identities *memIdentities
	accounts   *memAccounts
	sessions   *memSessions
	attempts   *memAttempts
	audit      *memAudit
	tx         *memTx
	logger     *memLogger
}

func stateHash(state string) string {
	sum := sha256.Sum256([]byte("state-key:" + state))
	return hex.EncodeToString(sum[:])
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		issuer:     oidctest.New(t),
		identities: newMemIdentities(),
		accounts:   &memAccounts{},
		sessions:   &memSessions{},
		attempts:   newMemAttempts(),
		audit:      &memAudit{},
		tx:         &memTx{},
		logger:     &memLogger{},
	}
	f.config = &memConfig{cfg: idkit.Config{
		Enabled:      true,
		IssuerURL:    f.issuer.Issuer(),
		ClientID:     f.issuer.ClientID(),
		ClientSecret: f.issuer.ClientSecret(),
		Scopes:       []string{"openid", "email", "profile"},
		RoleMapping:  []rolemap.Rule{{Claim: rolemap.ClaimGroups, Value: testGroupSales, RoleID: testRoleSales}},
	}}
	f.svc = New(Options{
		Config:      f.config,
		Attempts:    f.attempts,
		Identities:  f.identities,
		Accounts:    f.accounts,
		Sessions:    f.sessions,
		Audit:       f.audit,
		Tx:          f.tx,
		Logger:      f.logger,
		StateHash:   stateHash,
		SubjectRef:  func(issuer, subject string) string { return "ref-" + stateHash(issuer + "|" + subject)[:16] },
		RedirectURL: testRedirectURL,
		RootCAs:     f.issuer.RootCAs(),
	})
	return f
}

// entra makes the issuer an Entra ID issuer of testTenant.
func (f *fixture) entra(t *testing.T) {
	t.Helper()
	parsed, err := url.Parse(f.issuer.URL())
	require.NoError(t, err)
	f.issuer.SetPath("/" + testTenant + "/v2.0")
	f.svc.EntraIssuerHost = parsed.Hostname()
	f.config.cfg.IssuerURL = f.issuer.Issuer()
	f.svc.Providers().Clear()
}

func (f *fixture) start(t *testing.T) string {
	t.Helper()
	authz, err := f.svc.Start(context.Background())
	require.NoError(t, err)
	return authz.URL
}

func (f *fixture) signIn(t *testing.T, claims oidctest.Claims) (idkit.Session, error) {
	t.Helper()
	code, state := f.issuer.Authorize(t, f.start(t), claims)
	return f.svc.Callback(context.Background(), code, state, state)
}

// testSID is the provider session id every salesClaims sign-in carries.
const testSID = "provider-session-0634"

func salesClaims(subject, email string) oidctest.Claims {
	return oidctest.Claims{Subject: subject, Email: email, Groups: []string{testGroupSales}, SID: testSID}
}

// TestSignInAccountLinkRule is the account link rule against in-memory
// stores.
func TestSignInAccountLinkRule(t *testing.T) {
	const iss = "https://issuer.example.test/" + testTenant + "/v2.0"
	const sub, hint, email = "subject-0648", "oid-0648", "person-0648@example.test"
	profile := idkit.Profile{Email: email, GivenName: "Ada", FamilyName: "Lovelace"}
	run := func(t *testing.T, setup func(f *fixture), linkHint string) (*fixture, idkit.Session, error) {
		t.Helper()
		f := newFixture(t)
		if setup != nil {
			setup(f)
		}
		session, err := f.svc.signInAccount(context.Background(), signIn{
			issuer: iss, subject: sub, linkHint: linkHint, roleID: testRoleSales, profile: profile,
		})
		return f, session, err
	}

	t.Run("known_identity", func(t *testing.T) {
		f, session, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "11", Issuer: iss, Subject: sub, Source: idkit.IdentitySourceOIDC})
		}, hint)
		require.NoError(t, err)
		assert.Equal(t, []string{"11"}, f.accounts.signIns)
		assert.Equal(t, []uint{testRoleSales}, f.accounts.roles)
		assert.Empty(t, f.accounts.provisioned)
		assert.Empty(t, f.identities.created, "a known pair writes no identity")
		assert.Equal(t, [][2]string{{iss, sub}}, f.identities.gets, "the hint is not read for a known pair")
		require.Len(t, f.sessions.requests, 1)
		assert.Equal(t, "11", f.sessions.requests[0].AccountID)
		assert.Equal(t, idkit.SignInMethodOIDC, f.sessions.requests[0].Method)
		assert.Equal(t, "11", session.AccountID)
	})

	t.Run("near_match_refused", func(t *testing.T) {
		f, _, err := run(t, func(f *fixture) {
			f.identities.near[[2]string{iss, sub}] = idkit.Identity{AccountID: "12", Issuer: iss, Subject: sub + " ", Source: idkit.IdentitySourceOIDC}
		}, hint)
		require.ErrorIs(t, err, idkitoidc.ErrSignInFailed)
		assert.Equal(t, CodeIdentityMismatch, idkitoidc.RefusalCode(err))
		assert.Empty(t, f.accounts.signIns, "no account call")
		assert.Empty(t, f.accounts.provisioned, "no account call")
		assert.Empty(t, f.identities.created)
		assert.Empty(t, f.sessions.requests)
	})

	t.Run("provider_created_same_tenant", func(t *testing.T) {
		hooked := false
		f, session, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "13", Issuer: iss, Subject: hint, Source: idkit.IdentitySourceSCIM})
			f.svc.BeforeSCIMLink = func() {
				hooked = true
				assert.Empty(t, f.accounts.signIns, "the hook runs before the account sign-in")
			}
		}, hint)
		require.NoError(t, err)
		assert.True(t, hooked)
		assert.Equal(t, []string{"13"}, f.accounts.signIns, "SignIn on the SCIM account")
		assert.Empty(t, f.accounts.provisioned)
		assert.Equal(t, []idkit.Identity{{AccountID: "13", Issuer: iss, Subject: sub, Source: idkit.IdentitySourceOIDC}},
			f.identities.created, "then Create of (iss, sub, oidc)")
		assert.Equal(t, "13", session.AccountID)
	})

	t.Run("provider_created_link_taken", func(t *testing.T) {
		f, _, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "14", Issuer: iss, Subject: hint, Source: idkit.IdentitySourceSCIM})
			f.identities.createErr = fmt.Errorf("unique index: %w", idkit.ErrIdentityTaken)
		}, hint)
		require.ErrorIs(t, err, idkitoidc.ErrSignInFailed)
		require.ErrorIs(t, err, idkit.ErrIdentityTaken, "the refusal wraps its cause")
		assert.Equal(t, CodeIdentityTaken, idkitoidc.RefusalCode(err))
		assert.Equal(t, "14", RefusedAccount(err), "the refusal names the SCIM account")
		assert.Empty(t, f.sessions.requests)
	})

	t.Run("hint_row_source_oidc", func(t *testing.T) {
		f, session, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "15", Issuer: iss, Subject: hint, Source: idkit.IdentitySourceOIDC})
		}, hint)
		require.NoError(t, err)
		assert.Empty(t, f.accounts.signIns, "an identity the provider did not create never links")
		assert.Equal(t, []idkit.Profile{profile}, f.accounts.provisioned, "Provision")
		require.Len(t, f.identities.created, 1)
		assert.Equal(t, idkit.Identity{AccountID: "101", Issuer: iss, Subject: sub, Source: idkit.IdentitySourceOIDC}, f.identities.created[0])
		assert.Equal(t, "101", session.AccountID)
	})

	t.Run("hint_row_near_match", func(t *testing.T) {
		f, _, err := run(t, func(f *fixture) {
			f.identities.near[[2]string{iss, hint}] = idkit.Identity{AccountID: "16", Issuer: iss, Subject: hint + " ", Source: idkit.IdentitySourceSCIM}
		}, hint)
		require.NoError(t, err)
		assert.Empty(t, f.accounts.signIns, "a near match never links")
		assert.Equal(t, []idkit.Profile{profile}, f.accounts.provisioned, "Provision")
	})

	t.Run("no_hint", func(t *testing.T) {
		f, _, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "17", Issuer: iss, Subject: hint, Source: idkit.IdentitySourceSCIM})
		}, "")
		require.NoError(t, err)
		assert.Empty(t, f.accounts.signIns, "without a hint nothing links")
		assert.Equal(t, []idkit.Profile{profile}, f.accounts.provisioned, "Provision")
		assert.Equal(t, [][2]string{{iss, sub}}, f.identities.gets, "only the pair is read")
	})

	t.Run("no_hint_link_taken", func(t *testing.T) {
		_, _, err := run(t, func(f *fixture) {
			f.identities.createErr = fmt.Errorf("unique index: %w", idkit.ErrIdentityTaken)
		}, "")
		assert.Equal(t, CodeIdentityTaken, idkitoidc.RefusalCode(err))
		assert.Empty(t, RefusedAccount(err), "a provisioning refusal names no account")
	})

	t.Run("email_never_links", func(t *testing.T) {
		f, _, err := run(t, func(f *fixture) {
			// Rows stored under the email as a subject, under either issuer
			// form, are never consulted.
			f.identities.add(idkit.Identity{AccountID: "18", Issuer: iss, Subject: email, Source: idkit.IdentitySourceSCIM})
		}, hint)
		require.NoError(t, err)
		assert.Empty(t, f.accounts.signIns)
		assert.Equal(t, [][2]string{{iss, sub}, {iss, hint}}, f.identities.gets, "the lookups are (iss, sub) and (iss, hint) only")
		for _, get := range f.identities.gets {
			assert.NotEqual(t, email, get[1], "the email is never looked up")
		}
	})

	t.Run("account_refusal_passes_through", func(t *testing.T) {
		refusal := RefuseAccount(&idkitoidc.RefusalError{Code: "last_admin"}, "19")
		f, _, err := run(t, func(f *fixture) {
			f.identities.add(idkit.Identity{AccountID: "19", Issuer: iss, Subject: sub, Source: idkit.IdentitySourceOIDC})
			f.accounts.signInErr = refusal
		}, "")
		assert.Equal(t, "last_admin", idkitoidc.RefusalCode(err))
		assert.Equal(t, "19", RefusedAccount(err))
		assert.Empty(t, f.sessions.requests)
	})
}

// TestCallbackEntraLink signs in against an Entra ID shaped test issuer: the
// oid links to an identity the provider created only when the token's tid is
// the configured tenant and the oid is a string.
func TestCallbackEntraLink(t *testing.T) {
	const oid = "entra-oid-0648"
	cases := []struct {
		name  string
		extra map[string]interface{}
		links bool
	}{
		{"tid_equal_links", map[string]interface{}{"tid": testTenant, "oid": oid}, true},
		{"other_tenant_provisions", map[string]interface{}{"tid": "other-tenant", "oid": oid}, false},
		{"absent_oid_provisions", map[string]interface{}{"tid": testTenant}, false},
		{"non_string_oid_provisions", map[string]interface{}{"tid": testTenant, "oid": 648}, false},
		{"non_string_tid_provisions", map[string]interface{}{"tid": []string{testTenant}, "oid": oid}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.entra(t)
			iss := f.issuer.Issuer()
			f.identities.add(idkit.Identity{AccountID: "21", Issuer: iss, Subject: oid, Source: idkit.IdentitySourceSCIM})
			claims := salesClaims("subject-entra-0648", "entra-0648@example.test")
			claims.Extra = tc.extra
			session, err := f.signIn(t, claims)
			require.NoError(t, err)
			if tc.links {
				assert.Equal(t, []string{"21"}, f.accounts.signIns)
				assert.Empty(t, f.accounts.provisioned)
				assert.Equal(t, "21", session.AccountID)
			} else {
				assert.Empty(t, f.accounts.signIns)
				require.Len(t, f.accounts.provisioned, 1)
				assert.Equal(t, "entra-0648@example.test", f.accounts.provisioned[0].Email)
				assert.Equal(t, "101", session.AccountID)
			}
			require.Len(t, f.identities.created, 1)
			assert.Equal(t, idkit.Identity{AccountID: session.AccountID, Issuer: iss, Subject: "subject-entra-0648", Source: idkit.IdentitySourceOIDC},
				f.identities.created[0])
			assert.Equal(t, testRoleSales, f.accounts.roles[0])
		})
	}
}

// TestCallbackOutcome checks the session, the audit event and the outcome
// line of a sign-in and of refusals decided before and after the token
// verified.
func TestCallbackOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := newFixture(t)
		before := time.Now()
		session, err := f.signIn(t, oidctest.Claims{Subject: "subject-ok-0648", Email: "ok-0648@example.test",
			Groups: []string{testGroupSales}, Extra: map[string]interface{}{"given_name": "Grace", "family_name": "Hopper"}})
		require.NoError(t, err)
		assert.Equal(t, "101", session.AccountID)
		assert.Equal(t, []idkit.Profile{{Email: "ok-0648@example.test", GivenName: "Grace", FamilyName: "Hopper"}}, f.accounts.provisioned)
		assert.Equal(t, 1, f.tx.runs, "one transaction")
		require.Len(t, f.sessions.requests, 1)
		assert.False(t, f.sessions.requests[0].SignedInAt.Before(before), "the sign-in time is the wall clock")
		event := f.audit.last(t)
		assert.Equal(t, idkit.AuditEvent{Method: "oidc", Outcome: "success", AccountID: "101", AttemptID: "1",
			ClaimGroups: []string{testGroupSales}, ClaimRoles: []string{}}, event)
		line := f.logger.entries[len(f.logger.entries)-1]
		assert.Equal(t, "Signed in through single sign-on", line.msg)
		assert.Equal(t, []idkit.Field{{Key: "attempt_id", Value: uint64(1)}, {Key: "user_id", Value: uint64(101)}, {Key: "audit_id", Value: uint64(501)}}, line.fields)
	})

	t.Run("refused_before_the_token", func(t *testing.T) {
		f := newFixture(t)
		code, state := f.issuer.Authorize(t, f.start(t), salesClaims("subject-binding-0648", "binding-0648@example.test"))
		_, err := f.svc.Callback(context.Background(), code, state, "")
		assert.Equal(t, CodeBindingMissing, idkitoidc.RefusalCode(err))
		event := f.audit.last(t)
		assert.Equal(t, idkit.AuditEvent{Method: "oidc", Outcome: "refused", Reason: CodeBindingMissing, AttemptID: "1"}, event,
			"no subject reference before the token verified")
		line := f.logger.entries[len(f.logger.entries)-1]
		assert.Equal(t, "Single sign-on refused", line.msg)
		assert.Equal(t, []idkit.Field{{Key: "attempt_id", Value: uint64(1)}, {Key: "error_code", Value: CodeBindingMissing}, {Key: "audit_id", Value: uint64(501)}}, line.fields)
	})

	t.Run("account_refusal_names_the_account", func(t *testing.T) {
		f := newFixture(t)
		f.identities.add(idkit.Identity{AccountID: "31", Issuer: f.issuer.Issuer(), Subject: "subject-inactive-0648", Source: idkit.IdentitySourceOIDC})
		f.accounts.signInErr = RefuseAccount(&idkitoidc.RefusalError{Code: "account_inactive"}, "31")
		_, err := f.signIn(t, salesClaims("subject-inactive-0648", "inactive-0648@example.test"))
		require.ErrorIs(t, err, idkitoidc.ErrSignInFailed)
		event := f.audit.last(t)
		assert.Equal(t, "account_inactive", event.Reason)
		assert.Equal(t, "31", event.AccountID)
		assert.NotEmpty(t, event.SubjectRef, "a refusal after the token verified carries the subject reference")
	})

	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t)
		f.config.cfg.Enabled = false
		_, err := f.svc.Callback(context.Background(), "code", "state", "state")
		require.ErrorIs(t, err, idkitoidc.ErrDisabled)
		assert.Equal(t, idkit.AuditEvent{Method: "oidc", Outcome: "refused", Reason: "sso_disabled"}, f.audit.last(t))
		assert.Empty(t, f.logger.entries, "a switched-off sign-in logs nothing")
	})

	t.Run("internal_error", func(t *testing.T) {
		f := newFixture(t)
		f.attempts.claimErr = errors.New("connection refused")
		_, err := f.svc.Callback(context.Background(), "code", "state", "state")
		require.Error(t, err)
		assert.NotErrorIs(t, err, idkitoidc.ErrSignInFailed)
		assert.Equal(t, idkit.AuditEvent{Method: "oidc", Outcome: "refused", Reason: "internal_error"}, f.audit.last(t))
	})

	t.Run("without_audit_sink", func(t *testing.T) {
		f := newFixture(t)
		f.svc.Audit = nil
		_, err := f.signIn(t, salesClaims("subject-noaudit-0648", "noaudit-0648@example.test"))
		require.NoError(t, err)
		line := f.logger.entries[len(f.logger.entries)-1]
		assert.Equal(t, idkit.Field{Key: "audit_id", Value: uint64(0)}, line.fields[2], "audit_id is 0 without a sink")
	})
}

// TestCallbackReplay: a transaction runner that runs fn twice gets a fresh
// decision each time; nothing of the first run is kept.
func TestCallbackReplay(t *testing.T) {
	f := newFixture(t)
	f.tx.replay = true
	session, err := f.signIn(t, salesClaims("subject-replay-0648", "replay-0648@example.test"))
	require.NoError(t, err)
	assert.Equal(t, 2, f.tx.runs)
	// The in-memory stores keep the first run's writes (a real runner rolls
	// them back): the second run finds the linked pair and signs in.
	assert.Equal(t, []string{"101"}, f.accounts.signIns, "the second run decided again")
	require.Len(t, f.sessions.requests, 2)
	assert.Equal(t, "101", session.AccountID, "the session is the last run's")
}

// TestCallbackLogPrivacy records every Logger call over one sign-in and the
// refusals the package can reach: the fields are only attempt_id,
// error_code, audit_id and user_id, and no message or value carries the
// code, state, nonce, verifier, access token, email, subject, group value or
// issuer URL.
func TestCallbackLogPrivacy(t *testing.T) {
	type secrets struct{ values []string }
	allowed := map[string]bool{"attempt_id": true, "error_code": true, "audit_id": true, "user_id": true}
	const email, subject, group = "privacy-0648@example.test", "subject-privacy-0648", testGroupSales

	scenarios := []struct {
		name string
		run  func(t *testing.T, f *fixture) (code, state string, err error)
	}{
		{"success", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state)
			require.NoError(t, err)
			return code, state, nil
		}},
		{"state_invalid", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state+"x", state+"x")
			return code, state, err
		}},
		{"binding_mismatch", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state+"x")
			return code, state, err
		}},
		{"issuer_changed", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			f.config.cfg.IssuerURL = f.issuer.Issuer() + "/moved"
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"not_configured", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			f.config.cfg.ClientID = ""
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"discovery_failed", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			f.svc.Providers().Clear()
			f.issuer.SetDiscoveryRedirect(true)
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"token_exchange_failed", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			f.issuer.SetTokenStatus(500)
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"id_token_invalid", func(t *testing.T, f *fixture) (string, string, error) {
			claims := salesClaims(subject, email)
			claims.UnpublishedKey = true
			code, state := f.issuer.Authorize(t, f.start(t), claims)
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"nonce_mismatch", func(t *testing.T, f *fixture) (string, string, error) {
			claims := salesClaims(subject, email)
			claims.Nonce = "another-nonce-0648"
			code, state := f.issuer.Authorize(t, f.start(t), claims)
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"groups_malformed", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), oidctest.Claims{Subject: subject, Email: email, Groups: group})
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"group_overage", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), oidctest.Claims{Subject: subject, Email: email,
				Extra: map[string]interface{}{
					"_claim_names":   map[string]string{"groups": "src1"},
					"_claim_sources": map[string]interface{}{"src1": map[string]string{"endpoint": "https://graph.example.test/getMemberObjects"}},
				}})
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"subject_invalid", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(" "+subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"userinfo_subject_mismatch", func(t *testing.T, f *fixture) (string, string, error) {
			other := "subject-other-0648"
			f.issuer.SetUserInfo(oidctest.UserInfo{Sub: &other, Groups: []byte(`["` + group + `"]`)})
			code, state := f.issuer.Authorize(t, f.start(t), oidctest.Claims{Subject: subject, Email: email})
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"no_mapping_match", func(t *testing.T, f *fixture) (string, string, error) {
			code, state := f.issuer.Authorize(t, f.start(t), oidctest.Claims{Subject: subject, Email: email, Groups: []string{"nobody-0648"}})
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"identity_mismatch", func(t *testing.T, f *fixture) (string, string, error) {
			f.identities.near[[2]string{f.issuer.Issuer(), subject}] = idkit.Identity{AccountID: "41", Issuer: f.issuer.Issuer(), Subject: subject + " "}
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"identity_taken", func(t *testing.T, f *fixture) (string, string, error) {
			f.identities.createErr = fmt.Errorf("unique index: %w", idkit.ErrIdentityTaken)
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
		{"account_refused", func(t *testing.T, f *fixture) (string, string, error) {
			f.accounts.provisionErr = &idkitoidc.RefusalError{Code: "email_taken"}
			code, state := f.issuer.Authorize(t, f.start(t), salesClaims(subject, email))
			_, err := f.svc.Callback(context.Background(), code, state, state)
			return code, state, err
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newFixture(t)
			code, state, err := sc.run(t, f)
			if sc.name != "success" {
				require.Error(t, err)
				if want := sc.name; want != "account_refused" {
					assert.Equal(t, want, idkitoidc.RefusalCode(err)+idkitoidc.UnavailableCode(err), "the scenario reaches its code")
				}
			}
			s := secrets{values: []string{code, state, email, subject, group, f.issuer.URL(), testRedirectURL, testSID}}
			require.NotEmpty(t, f.attempts.created)
			for _, attempt := range f.attempts.created {
				s.values = append(s.values, attempt.Nonce, attempt.Verifier, attempt.StateHash)
			}
			if access := f.issuer.LastAccessToken(); access != "" {
				s.values = append(s.values, access)
			}
			if id := f.issuer.LastIDToken(); id != "" {
				s.values = append(s.values, id)
			}
			require.NotEmpty(t, f.logger.entries)
			for _, entry := range f.logger.entries {
				for _, secret := range s.values {
					assert.NotContains(t, entry.msg, secret, "message %q", entry.msg)
				}
				for _, field := range entry.fields {
					assert.True(t, allowed[field.Key], "field %q in %q", field.Key, entry.msg)
					value := fmt.Sprint(field.Value)
					for _, secret := range s.values {
						assert.NotContains(t, value, secret, "field %s of %q", field.Key, entry.msg)
					}
				}
			}
		})
	}
}

// TestRefuseAccount: the account id rides on a refusal and only on one.
func TestRefuseAccount(t *testing.T) {
	refusal := refuse(CodeIdentityTaken)
	withAccount := RefuseAccount(refusal, "42")
	assert.Equal(t, "42", RefusedAccount(withAccount))
	assert.Equal(t, refusal.Error(), withAccount.Error(), "it reads as the refusal")
	assert.ErrorIs(t, withAccount, idkitoidc.ErrSignInFailed)
	assert.Equal(t, CodeIdentityTaken, idkitoidc.RefusalCode(withAccount))
	wrapped := fmt.Errorf("transaction: %w", withAccount)
	assert.Equal(t, "42", RefusedAccount(wrapped), "it survives wrapping")
	reason, account := auditReason(wrapped)
	assert.Equal(t, CodeIdentityTaken, reason)
	assert.Equal(t, "42", account)

	plain := errors.New("boom")
	assert.Same(t, plain, RefuseAccount(plain, "42"), "a non-refusal is returned as it is")
	assert.Equal(t, refusal, RefuseAccount(refusal, ""), "no account, no wrapper")
	assert.Empty(t, RefusedAccount(refusal))

	reason, account = auditReason(unavailable(CodeDiscoveryFailed))
	assert.Equal(t, CodeDiscoveryFailed, reason)
	assert.Empty(t, account)
	reason, _ = auditReason(idkitoidc.ErrDisabled)
	assert.Equal(t, "sso_disabled", reason)
	reason, _ = auditReason(plain)
	assert.Equal(t, "internal_error", reason)
}

// TestNewDefaults: the zero options take the production values.
func TestNewDefaults(t *testing.T) {
	s := New(Options{})
	assert.Equal(t, "login.microsoftonline.com", s.EntraIssuerHost)
	assert.Equal(t, "https://graph.microsoft.com", s.GraphBaseURL)
	assert.Equal(t, 5*time.Second, s.GraphTimeout)
	assert.Equal(t, 5*time.Second, s.UserInfoTimeout)
	assert.Equal(t, 10*time.Minute, s.AttemptTTL)
	assert.Equal(t, 512, s.IssuerMaxBytes)
	assert.NotNil(t, s.Providers())
}
