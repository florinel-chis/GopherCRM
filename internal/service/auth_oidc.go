package service

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/entra"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/sso"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"golang.org/x/crypto/bcrypt"
)

// OpenID Connect sign-in (#435): the host side of the sign-in in
// internal/idkit/sso, which runs the authorization code flow with PKCE
// against the issuer, client and role mapping of the single sign-on settings
// (#434), re-read on every call, over the host interfaces of internal/idkit.
// NewOIDCService wires GopherCRM's adapters (auth_oidc_host.go) into it: the
// settings, the oidc_login_attempts rows, user_identities, the accounts and
// the governed role write (idkit.AccountStore), the session issuing, the
// sign-in audit and the logger.
//
// Start stores one single-use oidc_login_attempts row (the HMAC of the state,
// the nonce, the PKCE verifier and the issuer) and returns the authorization
// URL together with the raw state, which the handler also sets as the
// short-lived sign-in cookie of the browser that called it (#528). Callback
// spends that row (a plain read by the state's HMAC, then one conditional
// UPDATE by id committed on its own that also blanks the nonce and the
// verifier, #453), then requires the sign-in cookie sent with the callback to
// equal the posted state, so a sign-in finishes only in the browser that
// started it (binding_missing / binding_mismatch, #528), exchanges the code,
// verifies the ID token (signature, iss, aud, exp and the nonce), maps its
// groups or roles claim to a role (the first matching entry, in stored order;
// an Entra ID group overage is read from Microsoft Graph, #450; when a groups
// mapping meets an ID token without a groups claim and no overage, the groups
// are read from the issuer's userinfo endpoint, whose sub must equal the
// token's, #451) and then runs one governed transaction (oidcSignInRunner):
//
//	admin set → users → roles → user_identities → refresh_tokens
//
// An identity is found by its exact (iss, sub) pair. When that pair is
// unknown and the issuer is Entra ID, a token whose tid equals the configured
// issuer's tenant segment byte for byte and whose oid is the subject of the
// identity SCIM created under that issuer signs in as that account and links
// its (iss, sub) pair (#505). Any other first sign-in provisions a user (JIT)
// unless any user, erased ones included, already holds its email address in
// any letter case; email never links to an existing account. Every sign-in
// re-applies the mapped role through the governed role write
// (oidcAccountStore), so the last-admin rule and the role audit (auth_method
// oidc) apply. Success returns the same token pair as a password login.
//
// Every log line carries component oidc_sign_in, the request_id of the HTTP
// request and the attempt_id of the row (0 before the row exists or is
// claimed), and never a code, state, sign-in cookie value, nonce, verifier,
// token, email, subject, name or claim. Attempts older than
// OIDCLoginAttemptRetention are pruned by the stale bulk operation sweep
// (PruneOIDCLoginAttempts).

// Errors of the OIDC sign-in, classified with errors.Is.
var (
	// ErrSSODisabled: single sign-on is switched off (404).
	ErrSSODisabled = idkitoidc.ErrDisabled
	// ErrSSOUnavailable: single sign-on is on but the issuer or client is not
	// configured, the issuer cannot be discovered, or the token endpoint
	// failed (503).
	ErrSSOUnavailable = idkitoidc.ErrUnavailable
	// ErrSSOSignInFailed covers every refusal of a callback (401): an
	// unknown, used or expired state, a missing sign-in cookie or one that
	// names another sign-in, a token that does not verify, a claim that does
	// not map, a refused account. The reason is logged as an
	// error_code and never disclosed.
	ErrSSOSignInFailed = idkitoidc.ErrSignInFailed
)

// OIDCService is the OpenID Connect sign-in.
type OIDCService interface {
	// Start begins a sign-in and returns the issuer's authorization URL and
	// the state the browser must hold as its sign-in cookie.
	Start(ctx context.Context) (*OIDCAuthorization, error)
	// Callback finishes a sign-in with the code and state the issuer
	// redirected back with and the value of the sign-in cookie the browser
	// sent ("" when it sent none), and returns the session, as a password
	// login does. The attempt is spent first; a binding that is empty or
	// differs from the state is then refused.
	Callback(ctx context.Context, code, state, binding string) (*AuthTokens, error)
}

// OIDCAuthorization is what Start returns: the issuer's authorization URL,
// the raw state it carries (the value of the sign-in cookie, #528) and
// whether that cookie is sent only over HTTPS (APP_BASE_URL is https).
type OIDCAuthorization struct {
	URL          string
	State        string
	SecureCookie bool
}

// OIDCAttemptTTL is how long a started sign-in can be finished; the sign-in
// cookie lives as long.
const OIDCAttemptTTL = oidcAttemptTTL

// Limits and lifetimes of the OIDC sign-in.
const (
	oidcAttemptTTL         = sso.DefaultAttemptTTL
	oidcIssuerMaxBytes     = models.UserIdentityIssuerMaxLength
	oidcEmailMaxBytes      = 255
	oidcCallbackPath       = "/auth/callback"
	oidcRandomPasswordSize = 32
)

// oidcRefuse and oidcRefuseWith build a refusal with the error_code it is
// logged under (an idkitoidc.RefusalError): ErrSSOSignInFailed to errors.Is,
// and also the cause it wraps, if any.
func oidcRefuse(code string) error { return &idkitoidc.RefusalError{Code: code} }

func oidcRefuseWith(code string, cause error) error {
	return &idkitoidc.RefusalError{Code: code, Cause: cause}
}

// OIDCServiceOption configures NewOIDCService. Production passes none.
type OIDCServiceOption func(*oidcService)

// WithOIDCClock replaces the clock of the attempt expiry; tests only.
func WithOIDCClock(now func() time.Time) OIDCServiceOption {
	return func(s *oidcService) { s.now = now }
}

// WithOIDCAudit makes Callback write one auth_audit_log row per call (#454).
func WithOIDCAudit(audit AuthAuditService) OIDCServiceOption {
	return func(s *oidcService) { s.audit = audit }
}

// WithOIDCRootCAs makes the issuer client trust these roots instead of the
// system roots; tests only.
func WithOIDCRootCAs(pool *x509.CertPool) OIDCServiceOption {
	return func(s *oidcService) { s.rootCAs = pool }
}

type oidcService struct {
	// auth is the session-issuing auth service, nil when NewOIDCService got
	// another one: Start and Callback then answer errOIDCNotWired.
	auth       *authService
	appBaseURL string
	now        func() time.Time
	rootCAs    *x509.CertPool
	// audit, when set, records every callback (#454).
	audit AuthAuditService
	// configs is the settings source of the sign-in.
	configs *oidcConfigSource
	// accounts is the account store of the sign-in.
	accounts *oidcAccountStore
	// signin is the sign-in itself, built once by NewOIDCService after the
	// options.
	signin *sso.Service
}

// NewOIDCService builds the sign-in from the single sign-on settings, the
// auth service built by NewAuthServiceWithSessions (its token-issuing half),
// the user, role, role-audit, refresh-token, identity and attempt
// repositories, the transaction manager they share and APP_BASE_URL.
func NewOIDCService(
	ssoSettings SSOSettingsService,
	auth AuthService,
	userRepo repository.UserRepository,
	roleRepo repository.RoleRepository,
	auditRepo repository.RoleAuditRepository,
	refreshTokens repository.RefreshTokenRepository,
	identities repository.UserIdentityRepository,
	attempts repository.OIDCLoginAttemptRepository,
	txManager *utils.TransactionManager,
	appBaseURL string,
	opts ...OIDCServiceOption,
) OIDCService {
	issuer, _ := auth.(*authService)
	s := &oidcService{
		auth:       issuer,
		appBaseURL: strings.TrimRight(appBaseURL, "/"),
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	governance := &roleGovernance{users: userRepo, roles: roleRepo, audit: auditRepo, txManager: txManager}
	s.configs = &oidcConfigSource{wired: s.auth != nil, inner: NewIDKitConfigSource(ssoSettings)}
	s.accounts = &oidcAccountStore{}
	options := sso.Options{
		Config:          s.configs,
		Attempts:        NewIDKitAttemptStore(attempts),
		Identities:      NewIDKitIdentityStore(identities),
		Accounts:        s.accounts,
		Tx:              &oidcPasswordFirstRunner{next: &oidcSignInRunner{governance: governance}},
		Clock:           idkit.ClockFunc(func() time.Time { return s.now() }),
		Logger:          NewIDKitLogger("oidc_sign_in"),
		RedirectURL:     s.redirectURI(),
		RootCAs:         s.rootCAs,
		EntraIssuerHost: entra.IssuerHost,
		GraphBaseURL:    entra.GraphBaseURL,
		GraphTimeout:    entra.GraphTimeout,
		UserInfoTimeout: sso.UserInfoTimeout,
		AttemptTTL:      oidcAttemptTTL,
		IssuerMaxBytes:  oidcIssuerMaxBytes,
	}
	if s.audit != nil {
		options.Audit = NewIDKitAuditSink(s.audit)
	}
	if s.auth != nil {
		// The secret is read on every call, from the auth service in force.
		options.StateHash = func(state string) string { return hashOpaqueToken(state, s.auth.sessionTokenSecret()) }
		options.SubjectRef = func(issuer, subject string) string {
			return AuthAuditSubjectRef(s.auth.sessionTokenSecret(), issuer, subject)
		}
		if sessions, err := NewIDKitSessionIssuer(s.auth, userRepo, refreshTokens); err == nil {
			options.Sessions = &oidcSessionIssuer{inner: sessions.(*idkitSessionIssuer)}
		}
	}
	s.signin = sso.New(options)
	return s
}

// errOIDCNotWired is a wiring fault (500): the auth service is not the one
// NewAuthServiceWithSessions builds.
var errOIDCNotWired = errors.New("oidc sign-in needs the session-issuing auth service")

// OIDCLoginAttemptRetention is how long an oidc_login_attempts row is kept
// after its creation; the stale bulk operation sweep prunes older rows.
const OIDCLoginAttemptRetention = 24 * time.Hour

// oidcAttemptPruneChunk is how many rows one prune statement deletes.
const oidcAttemptPruneChunk = 500

// PruneOIDCLoginAttempts deletes every attempt created before
// now - OIDCLoginAttemptRetention, in chunks of oidcAttemptPruneChunk, until a
// chunk deletes fewer rows or ctx ends, and returns how many it deleted.
func PruneOIDCLoginAttempts(ctx context.Context, repo repository.OIDCLoginAttemptRepository, now time.Time) (int64, error) {
	cutoff := now.Add(-OIDCLoginAttemptRetention)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := repo.PruneCreatedBefore(ctx, cutoff, oidcAttemptPruneChunk)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < oidcAttemptPruneChunk {
			return total, nil
		}
	}
}

// redirectURI is the SPA page the issuer sends the browser back to.
func (s *oidcService) redirectURI() string {
	return s.appBaseURL + oidcCallbackPath
}

func (s *oidcService) Start(ctx context.Context) (*OIDCAuthorization, error) {
	authz, err := s.signin.Start(ctx)
	if err != nil {
		return nil, err
	}
	return &OIDCAuthorization{URL: authz.URL, State: authz.State, SecureCookie: s.httpsOnlyCookie()}, nil
}

// httpsOnlyCookie reports whether APP_BASE_URL is https (the scheme compared
// in any letter case), in which case the sign-in cookie is sent only over HTTPS.
func (s *oidcService) httpsOnlyCookie() bool {
	scheme, _, found := strings.Cut(s.appBaseURL, "://")
	return found && strings.EqualFold(scheme, "https")
}

// Callback runs the sign-in and returns its session as a token pair whose
// User is the row the session issuing read inside the sign-in transaction.
func (s *oidcService) Callback(ctx context.Context, code, state, binding string) (*AuthTokens, error) {
	result := &oidcSignInResult{}
	session, err := s.signin.Callback(context.WithValue(ctx, oidcSignInResultKey{}, result), code, state, binding)
	if err != nil {
		return nil, err
	}
	if result.user == nil || formatIDKitID(result.user.ID) != session.AccountID {
		return nil, errors.New("single sign-on session without its account")
	}
	return &AuthTokens{AccessToken: session.AccessToken, RefreshToken: session.RefreshToken, User: result.user}, nil
}

// oidcUnusablePasswordHash is the bcrypt hash of random bytes that are
// discarded at once, so a provisioned account cannot log in with a password.
func oidcUnusablePasswordHash() (string, error) {
	secret := make([]byte, oidcRandomPasswordSize)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate the single sign-on account password: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword(secret, bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
