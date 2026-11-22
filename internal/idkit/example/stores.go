package example

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/server"
	"github.com/florinel-chis/gophercrm/internal/idkit/sso"
)

// Refusal codes of the example's AccountStore, the ones sso.CodeStateInvalid
// and the rest leave to the host.
const (
	CodeAccountInactive = "account_inactive"
	CodeEmailMissing    = "email_missing"
	CodeEmailTaken      = "email_taken"
)

// maxEmailBytes bounds an account email.
const maxEmailBytes = 254

// Account is one account of the example host.
type Account struct {
	ID         string
	Email      string
	GivenName  string
	FamilyName string
	RoleID     uint
	Active     bool
	Created    time.Time
	Modified   time.Time
}

// identityRow is a stored identity with its creation order.
type identityRow struct {
	identity idkit.Identity
	seq      int
}

// state is everything a transaction may write. clone copies it deeply, so a
// failed transaction can put the copy back.
type state struct {
	nextAccount  int
	nextIdentity int
	accounts     map[string]Account
	identities   map[[2]string]identityRow
	// members maps a group value to the ids of its members.
	members map[string]map[string]bool
	// sessions maps an issued access token to its account id.
	sessions map[string]string
}

func newState() state {
	return state{
		accounts:   map[string]Account{},
		identities: map[[2]string]identityRow{},
		members:    map[string]map[string]bool{},
		sessions:   map[string]string{},
	}
}

func (s state) clone() state {
	c := s
	c.accounts = make(map[string]Account, len(s.accounts))
	for k, v := range s.accounts {
		c.accounts[k] = v
	}
	c.identities = make(map[[2]string]identityRow, len(s.identities))
	for k, v := range s.identities {
		c.identities[k] = v
	}
	c.members = make(map[string]map[string]bool, len(s.members))
	for group, ids := range s.members {
		set := make(map[string]bool, len(ids))
		for id := range ids {
			set[id] = true
		}
		c.members[group] = set
	}
	c.sessions = make(map[string]string, len(s.sessions))
	for k, v := range s.sessions {
		c.sessions[k] = v
	}
	return c
}

// Store is the in-memory state behind every host interface of the example.
// Its methods return the implementations; all of them share the state and
// are safe for concurrent use.
type Store struct {
	// issuer is the configured issuer: the issuer of the identities SCIM
	// creates and the one server.Manages judges against.
	issuer        string
	mapping       []rolemap.Rule
	defaultRoleID uint
	config        idkit.Config
	clock         idkit.Clock

	// txMu is held for a whole root transaction and a whole SCIM call, so
	// a rollback never undoes a write it did not make.
	txMu sync.Mutex
	// mu guards everything below.
	mu sync.Mutex
	st state

	nextAttempt int
	attempts    map[string]idkit.Attempt
	audit       []idkit.AuditEvent
	scimCalls   int
}

func newStore(config idkit.Config, defaultRoleID uint) *Store {
	return &Store{
		issuer:        config.IssuerURL,
		mapping:       append([]rolemap.Rule(nil), config.RoleMapping...),
		defaultRoleID: defaultRoleID,
		config:        config,
		clock:         idkit.SystemClock{},
		st:            newState(),
		attempts:      map[string]idkit.Attempt{},
	}
}

// Snapshot is a copy of the accounts and identities, for tests.
type Snapshot struct {
	Accounts map[string]Account
	// Identities are in creation order.
	Identities []idkit.Identity
}

// Snapshot copies the accounts and identities as they are now.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{Accounts: map[string]Account{}, Identities: []idkit.Identity{}}
	for id, account := range s.st.accounts {
		snap.Accounts[id] = account
	}
	rows := make([]identityRow, 0, len(s.st.identities))
	for _, row := range s.st.identities {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	for _, row := range rows {
		snap.Identities = append(snap.Identities, row.identity)
	}
	return snap
}

// SCIMCalls counts the calls the SCIM server made to Users and Groups.
func (s *Store) SCIMCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scimCalls
}

// account returns a copy of the account id names.
func (s *Store) account(id string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.st.accounts[id]
	return account, ok
}

// The implementations, one per interface.

// IdentityStore is the idkit.IdentityStore.
func (s *Store) IdentityStore() idkit.IdentityStore { return identityStore{s} }

// AccountStore is the idkit.AccountStore.
func (s *Store) AccountStore() idkit.AccountStore { return accountStore{s} }

// AttemptStore is the idkit.AttemptStore.
func (s *Store) AttemptStore() idkit.AttemptStore { return attemptStore{s} }

// SessionIssuer is the idkit.SessionIssuer.
func (s *Store) SessionIssuer() idkit.SessionIssuer { return sessionIssuer{s} }

// AuditSink is the idkit.AuditSink.
func (s *Store) AuditSink() idkit.AuditSink { return auditSink{s} }

// ConfigSource is the idkit.ConfigSource.
func (s *Store) ConfigSource() idkit.ConfigSource { return configSource{s} }

// TxRunner is the idkit.TxRunner.
func (s *Store) TxRunner() idkit.TxRunner { return txRunner{s} }

// Users is the server.Users of the SCIM server.
func (s *Store) Users() server.Users { return userStore{s} }

// Groups is the server.Groups of the SCIM server.
func (s *Store) Groups() server.Groups { return groupStore{s} }

var (
	_ idkit.IdentityStore = identityStore{}
	_ idkit.AccountStore  = accountStore{}
	_ idkit.AttemptStore  = attemptStore{}
	_ idkit.SessionIssuer = sessionIssuer{}
	_ idkit.AuditSink     = auditSink{}
	_ idkit.ConfigSource  = configSource{}
	_ idkit.TxRunner      = txRunner{}
	_ idkit.Logger        = (*Logger)(nil)
	_ server.Users        = userStore{}
	_ server.Groups       = groupStore{}
	_ server.Logger       = (*Logger)(nil)
)

// Transactions.

// txKey marks a ctx that carries a transaction.
type txKey struct{}

func inTransaction(ctx context.Context) bool {
	marked, _ := ctx.Value(txKey{}).(bool)
	return marked
}

// txRunner runs fn once over a copy of the state it can restore: when fn
// returns an error, every write fn made is undone. A call inside a
// transaction joins it and undoes only its own writes on an error, like a
// savepoint.
type txRunner struct{ s *Store }

func (t txRunner) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if !inTransaction(ctx) {
		t.s.txMu.Lock()
		defer t.s.txMu.Unlock()
		ctx = context.WithValue(ctx, txKey{}, true)
	}
	t.s.mu.Lock()
	saved := t.s.st.clone()
	t.s.mu.Unlock()
	if err := fn(ctx); err != nil {
		t.s.mu.Lock()
		t.s.st = saved
		t.s.mu.Unlock()
		return err
	}
	return nil
}

// Identities.

type identityStore struct{ s *Store }

func (i identityStore) Get(_ context.Context, issuer, subject string) (*idkit.Identity, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	row, ok := i.s.st.identities[[2]string{issuer, subject}]
	if !ok {
		return nil, fmt.Errorf("example identity: %w", idkit.ErrNotFound)
	}
	identity := row.identity
	return &identity, nil
}

func (i identityStore) Create(_ context.Context, identity idkit.Identity) error {
	if !canonicalID(identity.AccountID) {
		return fmt.Errorf("example identity: %w", idkit.ErrInvalidAccountID)
	}
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	return i.s.addIdentityLocked(identity)
}

// addIdentityLocked stores identity; the caller holds mu.
func (s *Store) addIdentityLocked(identity idkit.Identity) error {
	key := [2]string{identity.Issuer, identity.Subject}
	if _, taken := s.st.identities[key]; taken {
		return fmt.Errorf("example identity: %w", idkit.ErrIdentityTaken)
	}
	s.st.nextIdentity++
	s.st.identities[key] = identityRow{identity: identity, seq: s.st.nextIdentity}
	return nil
}

// identitiesLocked returns the identities of an account in creation order;
// the caller holds mu.
func (s *Store) identitiesLocked(accountID string) []idkit.Identity {
	var rows []identityRow
	for _, row := range s.st.identities {
		if row.identity.AccountID == accountID {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	identities := make([]idkit.Identity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, row.identity)
	}
	return identities
}

// canonicalID reports whether id is a canonical, non-zero decimal.
func canonicalID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == id
}

// Accounts.

type accountStore struct{ s *Store }

func (a accountStore) SignIn(_ context.Context, accountID string, roleID uint) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	account, ok := a.s.st.accounts[accountID]
	if !ok {
		return fmt.Errorf("example account: %w", idkit.ErrNotFound)
	}
	if !account.Active {
		return sso.RefuseAccount(&oidc.RefusalError{Code: CodeAccountInactive}, accountID)
	}
	account.RoleID = roleID
	account.Modified = a.s.clock.Now().UTC()
	a.s.st.accounts[accountID] = account
	return nil
}

func (a accountStore) Provision(_ context.Context, profile idkit.Profile, roleID uint) (string, error) {
	if profile.Email == "" || len(profile.Email) > maxEmailBytes {
		return "", &oidc.RefusalError{Code: CodeEmailMissing}
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if a.s.emailTakenLocked(profile.Email, "") {
		return "", &oidc.RefusalError{Code: CodeEmailTaken}
	}
	account := a.s.newAccountLocked(profile.Email, profile.GivenName, profile.FamilyName, roleID, true)
	return account.ID, nil
}

// newAccountLocked stores a new account with the next id; the caller holds
// mu.
func (s *Store) newAccountLocked(email, givenName, familyName string, roleID uint, active bool) Account {
	s.st.nextAccount++
	now := s.clock.Now().UTC()
	account := Account{
		ID:         strconv.Itoa(s.st.nextAccount),
		Email:      email,
		GivenName:  givenName,
		FamilyName: familyName,
		RoleID:     roleID,
		Active:     active,
		Created:    now,
		Modified:   now,
	}
	s.st.accounts[account.ID] = account
	return account
}

// emailTakenLocked reports whether an account other than except uses email,
// compared without case; the caller holds mu.
func (s *Store) emailTakenLocked(email, except string) bool {
	for id, account := range s.st.accounts {
		if id != except && strings.EqualFold(account.Email, email) {
			return true
		}
	}
	return false
}

// Attempts.

type attemptStore struct{ s *Store }

func (a attemptStore) Create(ctx context.Context, attempt idkit.Attempt) (string, error) {
	if inTransaction(ctx) {
		return "", idkit.ErrInTransaction
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	a.s.nextAttempt++
	attempt.ID = strconv.Itoa(a.s.nextAttempt)
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = a.s.clock.Now().UTC()
	}
	a.s.attempts[attempt.StateHash] = attempt
	return attempt.ID, nil
}

// Claim removes the attempt whatever its expiry, so a state is spent by its
// first callback.
func (a attemptStore) Claim(ctx context.Context, stateHash string, now time.Time) (*idkit.Attempt, error) {
	if inTransaction(ctx) {
		return nil, idkit.ErrInTransaction
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	attempt, ok := a.s.attempts[stateHash]
	if !ok {
		return nil, fmt.Errorf("example attempt: %w", idkit.ErrNotFound)
	}
	delete(a.s.attempts, stateHash)
	if !now.Before(attempt.ExpiresAt) {
		return nil, fmt.Errorf("example attempt: %w", idkit.ErrNotFound)
	}
	return &attempt, nil
}

func (a attemptStore) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if inTransaction(ctx) {
		return 0, idkit.ErrInTransaction
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	var deleted int64
	for hash, attempt := range a.s.attempts {
		if deleted >= int64(limit) {
			break
		}
		if attempt.CreatedAt.Before(before) {
			delete(a.s.attempts, hash)
			deleted++
		}
	}
	return deleted, nil
}

// Sessions.

type sessionIssuer struct{ s *Store }

func (i sessionIssuer) Issue(_ context.Context, request idkit.SessionRequest) (idkit.Session, error) {
	if request.SignedInAt.IsZero() {
		return idkit.Session{}, errors.New("example session: the sign-in time is required")
	}
	access, err := randomToken()
	if err != nil {
		return idkit.Session{}, err
	}
	refresh, err := randomToken()
	if err != nil {
		return idkit.Session{}, err
	}
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	i.s.st.sessions[access] = request.AccountID
	return idkit.Session{AccountID: request.AccountID, AccessToken: access, RefreshToken: refresh}, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("example session: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Audit.

type auditSink struct{ s *Store }

func (a auditSink) Record(ctx context.Context, event idkit.AuditEvent) string {
	if inTransaction(ctx) {
		return ""
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	a.s.audit = append(a.s.audit, event)
	return strconv.Itoa(len(a.s.audit))
}

// Configuration.

type configSource struct{ s *Store }

func (c configSource) Config(ctx context.Context) (idkit.Config, error) {
	if inTransaction(ctx) {
		return idkit.Config{}, idkit.ErrInTransaction
	}
	cfg := c.s.config
	cfg.Scopes = append([]string(nil), cfg.Scopes...)
	cfg.RoleMapping = append([]rolemap.Rule(nil), cfg.RoleMapping...)
	return cfg, nil
}

// Logger writes one line per call to its writer: the level, the fixed
// message and the fields as given. The identity layer passes only fixed
// messages, codes and ids; the error the SCIM server hands to Error is not
// written.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewLogger writes to w (io.Discard when nil).
func NewLogger(w io.Writer) *Logger {
	if w == nil {
		w = io.Discard
	}
	return &Logger{w: w}
}

// Info writes an info line.
func (l *Logger) Info(_ context.Context, msg string, fields ...idkit.Field) {
	l.write("info", msg, fields)
}

// Warn writes a warn line.
func (l *Logger) Warn(_ context.Context, msg string, fields ...idkit.Field) {
	l.write("warn", msg, fields)
}

// Error writes an error line without err.
func (l *Logger) Error(_ context.Context, msg string, _ error, fields ...idkit.Field) {
	l.write("error", msg, fields)
}

func (l *Logger) write(level, msg string, fields []idkit.Field) {
	var line strings.Builder
	fmt.Fprintf(&line, "level=%s msg=%q", level, msg)
	for _, field := range fields {
		fmt.Fprintf(&line, " %s=%v", field.Key, field.Value)
	}
	line.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, line.String())
}

// SCIM Users.

// userStore serves the accounts server.Manages accepts under the configured
// issuer. A create needs a userName (the email) and an externalId, which
// becomes the subject of the account's scim identity; replace and patch do
// not change the externalId. roles are ignored: a role comes from group
// membership.
type userStore struct{ s *Store }

// scimLock counts a SCIM call and holds txMu and mu until the returned func
// runs.
func (s *Store) scimLock() func() {
	s.txMu.Lock()
	s.mu.Lock()
	s.scimCalls++
	return func() {
		s.mu.Unlock()
		s.txMu.Unlock()
	}
}

// managedLocked returns the account id names when the provider manages it;
// the caller holds mu.
func (s *Store) managedLocked(id string) (Account, []idkit.Identity, bool) {
	account, ok := s.st.accounts[id]
	if !ok {
		return Account{}, nil, false
	}
	identities := s.identitiesLocked(id)
	if !server.Manages(s.issuer, identities) {
		return Account{}, nil, false
	}
	return account, identities, true
}

func (s *Store) userData(account Account, identities []idkit.Identity) scim.UserData {
	return scim.UserData{
		ID:           account.ID,
		ExternalID:   server.ExternalID(s.issuer, identities),
		Email:        account.Email,
		GivenName:    account.GivenName,
		FamilyName:   account.FamilyName,
		Active:       account.Active,
		Created:      account.Created,
		LastModified: account.Modified,
	}
}

func (u userStore) Get(_ context.Context, id string) (scim.UserData, error) {
	defer u.s.scimLock()()
	account, identities, ok := u.s.managedLocked(id)
	if !ok {
		return scim.UserData{}, server.ErrNotFound
	}
	return u.s.userData(account, identities), nil
}

func (u userStore) List(_ context.Context, userName *string, startIndex, count int) (server.UserPage, error) {
	defer u.s.scimLock()()
	var users []scim.UserData
	for _, id := range sortedIDs(u.s.st.accounts) {
		account, identities, ok := u.s.managedLocked(id)
		if !ok || (userName != nil && !scim.UserNameMatches(account.Email, *userName)) {
			continue
		}
		users = append(users, u.s.userData(account, identities))
	}
	from, to := pageBounds(len(users), startIndex, count)
	return server.UserPage{Total: len(users), StartIndex: startIndex, Users: users[from:to]}, nil
}

func (u userStore) Create(_ context.Context, in server.UserInput) (scim.UserData, error) {
	if !validEmail(in.UserName) || !oidc.ValidSubject(in.ExternalID) {
		return scim.UserData{}, server.ErrInvalidValue
	}
	defer u.s.scimLock()()
	if u.s.issuer == "" {
		return scim.UserData{}, server.ErrInvalidValue
	}
	if u.s.emailTakenLocked(in.UserName, "") {
		return scim.UserData{}, server.ErrUniqueness
	}
	if _, taken := u.s.st.identities[[2]string{u.s.issuer, in.ExternalID}]; taken {
		return scim.UserData{}, server.ErrUniqueness
	}
	active := in.Active == nil || *in.Active
	account := u.s.newAccountLocked(in.UserName, in.GivenName, in.FamilyName, u.s.defaultRoleID, active)
	if err := u.s.addIdentityLocked(idkit.Identity{
		AccountID: account.ID, Issuer: u.s.issuer, Subject: in.ExternalID, Source: idkit.IdentitySourceSCIM,
	}); err != nil {
		return scim.UserData{}, err
	}
	return u.s.userData(account, u.s.identitiesLocked(account.ID)), nil
}

func (u userStore) Replace(_ context.Context, id string, in server.UserInput) (scim.UserData, error) {
	defer u.s.scimLock()()
	account, identities, ok := u.s.managedLocked(id)
	if !ok {
		return scim.UserData{}, server.ErrNotFound
	}
	if !validEmail(in.UserName) {
		return scim.UserData{}, server.ErrInvalidValue
	}
	if u.s.emailTakenLocked(in.UserName, id) {
		return scim.UserData{}, server.ErrUniqueness
	}
	account.Email, account.GivenName, account.FamilyName = in.UserName, in.GivenName, in.FamilyName
	if in.Active != nil {
		account.Active = *in.Active
	}
	account.Modified = u.s.clock.Now().UTC()
	u.s.st.accounts[id] = account
	return u.s.userData(account, identities), nil
}

func (u userStore) Patch(_ context.Context, id string, p scim.UserPatch) (scim.UserData, error) {
	defer u.s.scimLock()()
	account, identities, ok := u.s.managedLocked(id)
	if !ok {
		return scim.UserData{}, server.ErrNotFound
	}
	email := p.UserName
	if email == nil {
		email = p.Email
	}
	if email != nil {
		if !validEmail(*email) {
			return scim.UserData{}, server.ErrInvalidValue
		}
		if u.s.emailTakenLocked(*email, id) {
			return scim.UserData{}, server.ErrUniqueness
		}
		account.Email = *email
	}
	if p.GivenName != nil {
		account.GivenName = *p.GivenName
	}
	if p.FamilyName != nil {
		account.FamilyName = *p.FamilyName
	}
	if p.Active != nil {
		account.Active = *p.Active
	}
	account.Modified = u.s.clock.Now().UTC()
	u.s.st.accounts[id] = account
	return u.s.userData(account, identities), nil
}

// Delete deactivates the account; the account and its identities stay.
func (u userStore) Delete(_ context.Context, id string) error {
	defer u.s.scimLock()()
	account, _, ok := u.s.managedLocked(id)
	if !ok {
		return server.ErrNotFound
	}
	if account.Active {
		account.Active = false
		account.Modified = u.s.clock.Now().UTC()
		u.s.st.accounts[id] = account
	}
	return nil
}

func validEmail(email string) bool {
	return email != "" && len(email) <= maxEmailBytes
}

// sortedIDs returns the keys of accounts in ascending numeric order.
func sortedIDs[V any](accounts map[string]V) []string {
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sortNumeric(ids)
	return ids
}

func sortNumeric(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		if len(ids[i]) != len(ids[j]) {
			return len(ids[i]) < len(ids[j])
		}
		return ids[i] < ids[j]
	})
}

// pageBounds is the slice [from:to] of a 1-based page of count items out of
// total.
func pageBounds(total, startIndex, count int) (int, int) {
	from := startIndex - 1
	if from < 0 {
		from = 0
	}
	if from > total {
		from = total
	}
	to := from + count
	if count < 0 || to > total {
		to = total
	}
	return from, to
}

// SCIM Groups.

// groupStore serves one group per distinct groups-claim value of the role
// mapping. A member's role is rolemap.Match over the groups it belongs to,
// or the default role when no rule matches.
type groupStore struct{ s *Store }

// groupValues are the distinct groups-claim values of the mapping, in
// mapping order.
func (s *Store) groupValues() []string {
	seen := map[string]bool{}
	var values []string
	for _, rule := range s.mapping {
		if rule.Claim == rolemap.ClaimGroups && !seen[rule.Value] {
			seen[rule.Value] = true
			values = append(values, rule.Value)
		}
	}
	return values
}

func (s *Store) isGroup(value string) bool {
	for _, candidate := range s.groupValues() {
		if candidate == value {
			return true
		}
	}
	return false
}

// groupDataLocked builds the group of value; the caller holds mu.
func (s *Store) groupDataLocked(value string) server.GroupData {
	ids := make([]string, 0, len(s.st.members[value]))
	for id := range s.st.members[value] {
		ids = append(ids, id)
	}
	sortNumeric(ids)
	return server.GroupData{Value: value, MemberIDs: ids}
}

// roleLocked is the role of the account from its groups; the caller holds
// mu.
func (s *Store) roleLocked(accountID string) uint {
	var groups []string
	for _, value := range s.groupValues() {
		if s.st.members[value][accountID] {
			groups = append(groups, value)
		}
	}
	if roleID, ok := rolemap.Match(s.mapping, groups, nil); ok {
		return roleID
	}
	return s.defaultRoleID
}

func (g groupStore) List(_ context.Context, displayName *string, startIndex, count int) (server.GroupPage, error) {
	defer g.s.scimLock()()
	var groups []server.GroupData
	for _, value := range g.s.groupValues() {
		if displayName == nil || *displayName == value {
			groups = append(groups, g.s.groupDataLocked(value))
		}
	}
	from, to := pageBounds(len(groups), startIndex, count)
	return server.GroupPage{Total: len(groups), StartIndex: startIndex, Groups: groups[from:to]}, nil
}

func (g groupStore) Get(_ context.Context, id string) (server.GroupData, error) {
	defer g.s.scimLock()()
	value, err := scim.ParseGroupID(id)
	if err != nil || !g.s.isGroup(value) {
		return server.GroupData{}, server.ErrNotFound
	}
	return g.s.groupDataLocked(value), nil
}

func (g groupStore) Create(_ context.Context, externalID, displayName *string) (server.GroupData, error) {
	defer g.s.scimLock()()
	for _, candidate := range []*string{externalID, displayName} {
		if candidate != nil && g.s.isGroup(*candidate) {
			return g.s.groupDataLocked(*candidate), nil
		}
	}
	return server.GroupData{}, server.ErrInvalidValue
}

// Patch applies every operation to a copy of the members and keeps it only
// when every named member is an account the provider manages; then it
// recomputes the role of every member that joined or left.
func (g groupStore) Patch(_ context.Context, id string, p scim.GroupPatch) error {
	defer g.s.scimLock()()
	value, err := scim.ParseGroupID(id)
	if err != nil || !g.s.isGroup(value) {
		return server.ErrNotFound
	}
	before := g.s.st.members[value]
	members := make(map[string]bool, len(before))
	for member := range before {
		members[member] = true
	}
	for _, op := range p.Ops {
		switch op.Op {
		case scim.GroupPatchAdd, scim.GroupPatchReplace:
			for _, member := range op.Values {
				if _, _, ok := g.s.managedLocked(member); !ok {
					return server.ErrInvalidValue
				}
			}
			if op.Op == scim.GroupPatchReplace {
				members = map[string]bool{}
			}
			for _, member := range op.Values {
				members[member] = true
			}
		case scim.GroupPatchRemove:
			if op.All {
				members = map[string]bool{}
			}
			for _, member := range op.Values {
				delete(members, member)
			}
		default:
			return server.ErrInvalidValue
		}
	}
	g.s.st.members[value] = members
	affected := map[string]bool{}
	for member := range before {
		affected[member] = true
	}
	for member := range members {
		affected[member] = true
	}
	now := g.s.clock.Now().UTC()
	for member := range affected {
		account, ok := g.s.st.accounts[member]
		if !ok {
			continue
		}
		if role := g.s.roleLocked(member); role != account.RoleID {
			account.RoleID, account.Modified = role, now
			g.s.st.accounts[member] = account
		}
	}
	return nil
}
