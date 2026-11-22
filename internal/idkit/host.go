package idkit

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
)

// The host interfaces: what the identity layer needs from the application
// that embeds it. Account and attempt ids cross this boundary as decimal
// strings; the host converts them to its own key type.
//
// A ctx handed to a host method may carry a transaction started through
// TxRunner. IdentityStore, AccountStore and SessionIssuer join it.
// AttemptStore, AuditSink and ConfigSource run only outside a transaction and
// refuse a ctx that carries one.
//
// The sign-in itself (package sso) runs over these interfaces: it decides
// which account an identity belongs to, and the host's AccountStore signs
// that account in or provisions a new one.

// Errors the host implementations return, classified with errors.Is.
var (
	// ErrNotFound reports that the requested row does not exist (or, for an
	// attempt, is used or expired).
	ErrNotFound = errors.New("idkit: not found")
	// ErrIdentityTaken reports that the issuer and subject pair is already
	// linked to an account.
	ErrIdentityTaken = errors.New("idkit: identity already linked")
	// ErrInTransaction reports a call that must run outside a transaction
	// made with a ctx that carries one. Nothing was read or written.
	ErrInTransaction = errors.New("idkit: called inside a transaction")
	// ErrInvalidAccountID reports an account or attempt id that is not a
	// canonical, non-zero decimal the host can store. Nothing was written.
	ErrInvalidAccountID = errors.New("idkit: invalid account id")
)

// Clock is the source of the current time.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

// Now calls f.
func (f ClockFunc) Now() time.Time { return f() }

// Field is one structured log field.
type Field struct {
	Key   string
	Value any
}

// Logger writes structured log lines. Callers pass no code, state, cookie
// value, nonce, verifier, token, secret, email, subject, name, claim value,
// URL or body, neither in the message nor in a field: only fixed messages
// and codes, counts and ids.
type Logger interface {
	Info(ctx context.Context, msg string, fields ...Field)
	Warn(ctx context.Context, msg string, fields ...Field)
}

// TxRunner runs fn inside a transaction: it commits when fn returns nil and
// rolls back when fn returns an error. fn's ctx carries the transaction.
// IdentityStore, AccountStore and SessionIssuer join a transaction carried by
// ctx; AttemptStore, AuditSink and ConfigSource run only outside one and
// refuse a ctx that carries one. A runner may call fn more than once (a
// replay after a deadlock); every call starts from nothing.
type TxRunner interface {
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// Identity sources.
const (
	IdentitySourceOIDC = "oidc"
	IdentitySourceSCIM = "scim"
)

// Identity links an issuer and subject pair to an account.
type Identity struct {
	AccountID string
	Issuer    string
	Subject   string
	// Source is IdentitySourceOIDC or IdentitySourceSCIM.
	Source string
}

// IdentityStore holds the links between identities and accounts. Both
// methods join a transaction carried by ctx.
type IdentityStore interface {
	// Get returns the identity stored for issuer and subject, or ErrNotFound
	// when no row exists. The row is returned as stored: the caller compares
	// Issuer and Subject byte for byte with what it asked for, because a
	// case- or pad-insensitive collation can return a near match.
	Get(ctx context.Context, issuer, subject string) (*Identity, error)
	// Create stores the identity. It returns ErrIdentityTaken when the
	// issuer and subject pair is already used, and ErrInvalidAccountID for
	// an AccountID the host cannot store.
	Create(ctx context.Context, identity Identity) error
}

// Profile is what a first sign-in knows about the person, for a new
// account.
type Profile struct {
	Email      string
	GivenName  string
	FamilyName string
}

// AccountStore holds the accounts identities sign in as. Both methods join
// the transaction carried by ctx, and the caller decides which account an
// identity belongs to before it calls them.
//
// A refusal is an oidc.RefusalError whose Code is the reason; a refusal that
// concerns an existing account carries its id (sso.RefuseAccount). Any other
// error is a failure of the host.
type AccountStore interface {
	// SignIn signs in as the account: it locks it, refuses an erased
	// (account_erased) or deactivated (account_inactive) one, a custom role
	// for a customer account (customer_login_role), a role that no longer
	// exists (role_missing) and a change that would leave no active
	// administrator (last_admin), and then applies roleID to the account
	// with its role audit record.
	SignIn(ctx context.Context, accountID string, roleID uint) error
	// Provision creates an active account with roleID and its role audit
	// record and returns its id. It refuses a missing or too long email
	// (email_missing), a role that no longer exists (role_missing) and an
	// email that any account, erased ones included, already uses
	// (email_taken). Email never links to an existing account.
	Provision(ctx context.Context, profile Profile, roleID uint) (string, error)
}

// Attempt is one started sign-in: the hash of its state, the nonce and PKCE
// verifier of its authorization request and the issuer it was started
// against.
type Attempt struct {
	ID        string
	StateHash string
	Nonce     string
	Verifier  string
	Issuer    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// AttemptStore holds started sign-ins. No method runs inside a transaction
// carried by ctx: each returns ErrInTransaction instead, without a statement,
// so a spent attempt is never made usable again by a rollback.
type AttemptStore interface {
	// Create stores the attempt and returns its id.
	Create(ctx context.Context, attempt Attempt) (string, error)
	// Claim spends the attempt with the given state hash once and returns
	// it. An unknown, used or expired state is ErrNotFound.
	Claim(ctx context.Context, stateHash string, now time.Time) (*Attempt, error)
	// Prune deletes at most limit attempts created before before and
	// returns how many it deleted.
	Prune(ctx context.Context, before time.Time, limit int) (int64, error)
}

// SignInMethodOIDC is the sign-in method of an OpenID Connect session.
const SignInMethodOIDC = "oidc"

// SessionRequest asks for a session for an account.
type SessionRequest struct {
	AccountID string
	// Method is SignInMethodOIDC.
	Method string
	// SignedInAt is when the user signed in.
	SignedInAt time.Time
}

// Session is an issued session.
type Session struct {
	AccountID    string
	AccessToken  string
	RefreshToken string
}

// SessionIssuer issues sessions. Issue joins a transaction carried by ctx.
//
// SignedInAt sets the session's start and its lifetime cap, while the access
// token's sign-in time (auth_time) is the time of issue, so a caller must not
// assume the two are equal. The caller passes the real sign-in time and never
// a future one: Issue refuses only a zero SignedInAt, and a time far enough
// in the past yields a session that has already ended.
//
// Issue performs no account checks: the caller has checked the account under
// its lock in the same transaction before asking for a session.
type SessionIssuer interface {
	Issue(ctx context.Context, request SessionRequest) (Session, error)
}

// AuditEvent is one sign-in outcome to record.
type AuditEvent struct {
	Method  string
	Outcome string
	// Reason is empty on success.
	Reason string
	// AccountID and AttemptID are "" when unknown.
	AccountID string
	AttemptID string
	// SubjectRef is the host's pseudonymous reference of a refused identity,
	// or "".
	SubjectRef  string
	ClaimGroups []string
	ClaimRoles  []string
}

// AuditSink records sign-in outcomes. It runs only outside a transaction:
// with a ctx that carries one it writes nothing and returns "".
type AuditSink interface {
	// Record writes the event and returns the row id, or "" when nothing was
	// written. It never fails the caller.
	Record(ctx context.Context, event AuditEvent) string
}

// Config is the sign-in configuration in force.
type Config struct {
	Enabled           bool
	IssuerURL         string
	ClientID          string
	ClientSecret      string
	Scopes            []string
	RoleMapping       []rolemap.Rule
	ClientCertificate *oidc.ClientCertificate
	// GraphMaxPages bounds the Microsoft Graph pages one Entra ID sign-in
	// reads.
	GraphMaxPages int
	// EntraAppSigningKeys verifies an Entra ID issuer's ID tokens against
	// the application-specific signing keys.
	EntraAppSigningKeys bool
}

// ConfigSource returns the sign-in configuration. Config runs only outside
// a transaction and returns ErrInTransaction for a ctx that carries one.
type ConfigSource interface {
	Config(ctx context.Context) (Config, error)
}
