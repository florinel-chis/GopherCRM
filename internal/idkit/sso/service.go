package sso

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"strconv"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/entra"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"golang.org/x/oauth2"
)

// Defaults of the Options a host leaves zero.
const (
	// DefaultAttemptTTL is how long a started sign-in can be finished.
	DefaultAttemptTTL = 10 * time.Minute
	// DefaultIssuerMaxBytes bounds the issuer of the configuration and of an
	// ID token.
	DefaultIssuerMaxBytes = 512
	// UserInfoTimeout is the default deadline of the userinfo request.
	UserInfoTimeout = 5 * time.Second
	// UserInfoMaxBytes bounds the userinfo response body.
	UserInfoMaxBytes = 1 << 20
	// EndSessionTimeout is the default deadline of EndSessionURL.
	EndSessionTimeout = 5 * time.Second
)

// Options is what a Service is built from.
type Options struct {
	// Config is re-read on every call. A Config error is returned as it is.
	Config idkit.ConfigSource
	// Attempts holds the started sign-ins.
	Attempts idkit.AttemptStore
	// Identities, Accounts and Sessions join the one transaction Tx runs
	// for a callback.
	Identities idkit.IdentityStore
	Accounts   idkit.AccountStore
	Sessions   idkit.SessionIssuer
	// Audit, when set, records every callback after its transaction.
	Audit idkit.AuditSink
	// SessionEnder ends the host's sessions of a provider session for
	// FrontChannelLogout. It runs outside a transaction.
	SessionEnder idkit.SessionEnder
	// Tx runs the transaction of a callback.
	Tx idkit.TxRunner
	// Clock drives the attempt expiry and the time of a client assertion
	// (idkit.SystemClock when nil). A session's sign-in time is always the
	// wall clock.
	Clock idkit.Clock
	// Logger receives every log line (none when nil).
	Logger idkit.Logger
	// StateHash is the keyed hash of a state under which its attempt is
	// stored.
	StateHash func(state string) string
	// SubjectRef, when set, is the pseudonymous reference of a refused
	// identity for the audit event.
	SubjectRef func(issuer, subject string) string

	// RedirectURL is the redirect URI of the authorization request.
	RedirectURL string
	// PostLogoutRedirectURL is the post_logout_redirect_uri of the URL
	// EndSessionURL returns (left out when empty).
	PostLogoutRedirectURL string
	// RootCAs, when set, replaces the system roots of the issuer client. New
	// reads it once.
	RootCAs *x509.CertPool
	// EntraIssuerHost is the host of an Entra ID issuer (entra.IssuerHost
	// when empty).
	EntraIssuerHost string
	// GraphBaseURL and GraphTimeout drive the Microsoft Graph resolution of
	// an Entra ID group overage (entra.GraphBaseURL and entra.GraphTimeout
	// when zero).
	GraphBaseURL string
	GraphTimeout time.Duration
	// UserInfoTimeout is the deadline of the userinfo request
	// (UserInfoTimeout when zero).
	UserInfoTimeout time.Duration
	// EndSessionTimeout is the deadline of one EndSessionURL call, its
	// discovery and key fetch included (EndSessionTimeout when zero).
	EndSessionTimeout time.Duration
	// AttemptTTL is how long a started sign-in can be finished
	// (DefaultAttemptTTL when zero).
	AttemptTTL time.Duration
	// IssuerMaxBytes bounds the configured issuer and the iss of an ID token
	// (DefaultIssuerMaxBytes when zero).
	IssuerMaxBytes int

	// BeforeSCIMLink is a test hook that runs when an identity the provider
	// created matched the link hint, after the identity reads and before the
	// account sign-in; nil in production.
	BeforeSCIMLink func()
}

// Service is the OpenID Connect sign-in: the authorization code flow with
// PKCE against the configured issuer. Its Options may be replaced between
// calls (tests do), except RootCAs, which New reads once.
type Service struct {
	Options
	// client is the issuer client, built once by New.
	client *http.Client
	// providers keeps the last discovered issuer (see discover).
	providers *idkitoidc.ProviderCache
}

// New builds the sign-in. It fills in the defaults of the zero options and
// builds the issuer client and the provider cache once.
func New(opts Options) *Service {
	if opts.EntraIssuerHost == "" {
		opts.EntraIssuerHost = entra.IssuerHost
	}
	if opts.GraphBaseURL == "" {
		opts.GraphBaseURL = entra.GraphBaseURL
	}
	if opts.GraphTimeout == 0 {
		opts.GraphTimeout = entra.GraphTimeout
	}
	if opts.UserInfoTimeout == 0 {
		opts.UserInfoTimeout = UserInfoTimeout
	}
	if opts.EndSessionTimeout == 0 {
		opts.EndSessionTimeout = EndSessionTimeout
	}
	if opts.AttemptTTL == 0 {
		opts.AttemptTTL = DefaultAttemptTTL
	}
	if opts.IssuerMaxBytes == 0 {
		opts.IssuerMaxBytes = DefaultIssuerMaxBytes
	}
	s := &Service{Options: opts}
	s.client = idkitoidc.NewHTTPClient(s.RootCAs)
	s.providers = idkitoidc.NewProviderCache(s.client, func() time.Time { return s.now() })
	return s
}

// Providers is the cache of the discovered issuer, for tests that clear or
// inspect it.
func (s *Service) Providers() *idkitoidc.ProviderCache { return s.providers }

// Authorization is what Start returns: the issuer's authorization URL and
// the raw state it carries, which the browser must send back as its binding.
type Authorization struct {
	URL   string
	State string
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return idkit.SystemClock{}.Now()
	}
	return s.Clock.Now()
}

// logID is the log form of a boundary id: a number, 0 when unknown.
func logID(id string) uint64 {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// info and warn write one line with the attempt id (0 before the attempt
// exists or is claimed) and the given fields.
func (s *Service) info(ctx context.Context, attemptID, msg string, fields ...idkit.Field) {
	if s.Logger != nil {
		s.Logger.Info(ctx, msg, append([]idkit.Field{{Key: "attempt_id", Value: logID(attemptID)}}, fields...)...)
	}
}

func (s *Service) warn(ctx context.Context, attemptID, msg string, fields ...idkit.Field) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, append([]idkit.Field{{Key: "attempt_id", Value: logID(attemptID)}}, fields...)...)
	}
}

// usableConfig reads the configuration and refuses one that is switched
// off.
func (s *Service) usableConfig(ctx context.Context) (idkit.Config, error) {
	cfg, err := s.Config.Config(ctx)
	if err != nil {
		return idkit.Config{}, err
	}
	if !cfg.Enabled {
		return idkit.Config{}, idkitoidc.ErrDisabled
	}
	return cfg, nil
}

// ssoConfigured reports whether cfg names an issuer and a client.
func (s *Service) ssoConfigured(cfg idkit.Config) bool {
	return cfg.IssuerURL != "" && cfg.ClientID != "" && len(cfg.IssuerURL) <= s.IssuerMaxBytes
}

// providerKey is the cache key of cfg. The application signing keys are used
// only when EntraAppSigningKeys is on and the issuer is Entra ID.
func (s *Service) providerKey(cfg idkit.Config) idkitoidc.ProviderKey {
	key := idkitoidc.ProviderKey{Issuer: cfg.IssuerURL}
	if cfg.EntraAppSigningKeys && entra.IsIssuer(cfg.IssuerURL, s.EntraIssuerHost) {
		key.AppKeys, key.ClientID = true, cfg.ClientID
	}
	return key
}

// discover returns the configured issuer's provider and the context that
// carries the issuer client for the code exchange and the userinfo call.
// The discovery is cached by idkitoidc.ProviderCache for
// idkitoidc.ProviderCacheTTL per (issuer, app-key mode, client id in app-key
// mode); a different key replaces the entry, and go-oidc fetches the keys
// again when a token names an unknown key id. A failed discovery
// (unreachable, redirect, timeout, an issuer mismatch) is not kept, leaves
// the cached entry as it was and is ErrUnavailable.
func (s *Service) discover(ctx context.Context, cfg idkit.Config, attemptID string) (context.Context, *idkitoidc.Discovery, error) {
	ctx = gooidc.ClientContext(ctx, s.client)
	entry, err := s.providers.Get(ctx, s.providerKey(cfg))
	if err != nil {
		s.warn(ctx, attemptID, "Single sign-on issuer discovery failed", idkit.Field{Key: "error_code", Value: CodeDiscoveryFailed})
		return nil, nil, unavailable(CodeDiscoveryFailed)
	}
	return ctx, entry, nil
}

// oidcClient is the OAuth 2.0 client of the configuration: the client id,
// the secret or, when one is in force, the client certificate, the redirect
// URL and the request scopes.
func (s *Service) oidcClient(cfg idkit.Config) idkitoidc.Client {
	client := idkitoidc.Client{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  s.RedirectURL,
		Scopes:       s.RequestScopes(cfg),
	}
	if cfg.ClientCertificate != nil {
		client.Certificate = &idkitoidc.ClientCertificate{
			Certificate: cfg.ClientCertificate.Certificate,
			PrivateKey:  cfg.ClientCertificate.PrivateKey,
		}
	}
	return client
}

// exchangeCode redeems the authorization code with the PKCE verifier. With a
// client certificate in force the request carries a fresh client assertion;
// a signing failure fails the exchange.
func (s *Service) exchangeCode(ctx context.Context, provider *gooidc.Provider, cfg idkit.Config, code, verifier string) (*oauth2.Token, error) {
	return s.oidcClient(cfg).Exchange(ctx, provider, code, verifier, s.now())
}

// RequestScopes are the configured scopes, plus the Microsoft Graph
// User.Read scope (once) when the issuer is Entra ID and the role mapping
// uses groups, so a group overage can be resolved. The added scope is never
// stored.
func (s *Service) RequestScopes(cfg idkit.Config) []string {
	scopes := append([]string(nil), cfg.Scopes...)
	if !entra.IsIssuer(cfg.IssuerURL, s.EntraIssuerHost) || !rolemap.UsesGroups(cfg.RoleMapping) {
		return scopes
	}
	for _, scope := range scopes {
		if scope == entra.GraphScope {
			return scopes
		}
	}
	return append(scopes, entra.GraphScope)
}

// Start begins a sign-in: it stores one single-use attempt (the hash of the
// state, the nonce, the PKCE verifier and the issuer) and returns the
// authorization URL with the raw state.
func (s *Service) Start(ctx context.Context) (*Authorization, error) {
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !s.ssoConfigured(cfg) {
		s.warn(ctx, "", "Single sign-on is enabled but not configured", idkit.Field{Key: "error_code", Value: CodeNotConfigured})
		return nil, unavailable(CodeNotConfigured)
	}
	ctx, discovered, err := s.discover(ctx, cfg, "")
	if err != nil {
		return nil, err
	}
	provider := discovered.Provider

	request, err := idkitoidc.NewAuthRequest()
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	attemptID, err := s.Attempts.Create(ctx, idkit.Attempt{
		StateHash: s.StateHash(request.State),
		Nonce:     request.Nonce,
		Verifier:  request.Verifier,
		Issuer:    cfg.IssuerURL,
		ExpiresAt: now.Add(s.AttemptTTL),
	})
	if err != nil {
		return nil, fmt.Errorf("store the single sign-on attempt: %w", err)
	}
	s.info(ctx, attemptID, "Single sign-on started")
	return &Authorization{
		URL:   idkitoidc.AuthCodeURL(s.oidcClient(cfg).OAuth2Config(provider), request),
		State: request.State,
	}, nil
}

// Callback finishes a sign-in with the code and state the issuer redirected
// back with and the binding the browser sent ("" when none), and returns the
// session. The attempt is spent first; a binding that is empty or differs
// from the state is then refused. The audit event is recorded after the
// transaction, and the outcome is logged with its id.
func (s *Service) Callback(ctx context.Context, code, state, binding string) (idkit.Session, error) {
	session, facts, err := s.callback(ctx, code, state, binding)
	return s.finishCallback(ctx, session, facts, err)
}

// finishCallback records the audit event of a callback and then logs its
// outcome with the event id (audit_id, 0 when nothing was recorded).
func (s *Service) finishCallback(ctx context.Context, session idkit.Session, facts callbackFacts, err error) (idkit.Session, error) {
	auditID := logID(s.recordCallback(ctx, session, facts, err))
	if err != nil {
		if errorCode := idkitoidc.RefusalCode(err); errorCode != "" {
			s.warn(ctx, facts.attemptID, "Single sign-on refused",
				idkit.Field{Key: "error_code", Value: errorCode}, idkit.Field{Key: "audit_id", Value: auditID})
		} else if errorCode := idkitoidc.UnavailableCode(err); errorCode != "" {
			s.warn(ctx, facts.attemptID, "Single sign-on unavailable",
				idkit.Field{Key: "error_code", Value: errorCode}, idkit.Field{Key: "audit_id", Value: auditID})
		}
		return idkit.Session{}, err
	}
	s.info(ctx, facts.attemptID, "Signed in through single sign-on",
		idkit.Field{Key: "user_id", Value: logID(session.AccountID)}, idkit.Field{Key: "audit_id", Value: auditID})
	return session, nil
}

// recordCallback records the audit event of one callback and returns its id,
// "" without an audit sink or when nothing was recorded. The subject
// reference is set only on a refusal decided after the ID token verified.
func (s *Service) recordCallback(ctx context.Context, session idkit.Session, facts callbackFacts, err error) string {
	if s.Audit == nil {
		return ""
	}
	var event idkit.AuditEvent
	if err == nil {
		event = idkit.AuditEvent{Method: auditMethodOIDC, Outcome: auditOutcomeSuccess, AccountID: session.AccountID}
	} else {
		reason, accountID := auditReason(err)
		event = idkit.AuditEvent{Method: auditMethodOIDC, Outcome: auditOutcomeRefused, Reason: reason, AccountID: accountID}
		if facts.verified && s.SubjectRef != nil {
			event.SubjectRef = s.SubjectRef(facts.issuer, facts.subject)
		}
	}
	event.AttemptID = facts.attemptID
	event.ClaimGroups = facts.groups
	event.ClaimRoles = facts.roles
	return s.Audit.Record(ctx, event)
}
