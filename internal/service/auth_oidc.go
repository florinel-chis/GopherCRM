package service

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// OpenID Connect sign-in (#435): the authorization code flow with PKCE
// against the issuer, client and role mapping of the single sign-on settings
// (#434), which are re-read on every call.
//
// Start writes one single-use oidc_login_attempts row (the HMAC of the state,
// the nonce, the PKCE verifier and the issuer) and returns the authorization
// URL together with the raw state, which the handler also sets as the
// short-lived sign-in cookie of the browser that called it (#528). Callback
// spends that row (a plain read by the state's HMAC, then one conditional
// UPDATE by id committed on its own that also blanks the nonce and the
// verifier, #453), then requires the sign-in cookie sent with the callback to
// equal the posted state, so a sign-in finishes only in the browser that
// started it (binding_missing / binding_mismatch, #528), exchanges the code, verifies the ID token (signature, iss, aud, exp and
// the nonce), maps its groups or roles claim to a role (the first matching
// entry, in stored order; an Entra ID group overage is read from Microsoft
// Graph, #450; when a groups mapping meets an ID token without a groups claim
// and no overage, the groups are read from the issuer's userinfo endpoint,
// whose sub must equal the token's, #451) and then runs one governed
// transaction
// (userService.ApplyOIDCSignIn):
//
//	admin set → users → roles → user_identities → refresh_tokens
//
// An identity is found by its exact (iss, sub) pair. When that pair is
// unknown and the issuer is Entra ID, a token whose tid equals the configured
// issuer's tenant segment byte for byte and whose oid is the subject of the
// identity SCIM created under that issuer signs in as that account and links
// its (iss, sub) pair (#505). Any other first sign-in provisions a user (JIT)
// unless any user, erased ones included, already holds its email address in
// any letter case; email never links to an existing account. Every sign-in re-applies the mapped role through the governed role
// write, so the last-admin rule and the role audit (auth_method oidc) apply.
// Success returns the same token pair as a password login.
//
// Every log line carries component oidc_sign_in, the request_id of the HTTP
// request and the attempt_id of the row (0 before the row exists or is
// claimed), and never a code, state, sign-in cookie value, nonce, verifier,
// token, email, subject, name or claim. Attempts older than OIDCLoginAttemptRetention are pruned by
// the stale bulk operation sweep (PruneOIDCLoginAttempts).

// Errors of the OIDC sign-in, classified with errors.Is.
var (
	// ErrSSODisabled: single sign-on is switched off (404).
	ErrSSODisabled = errors.New("single sign-on is disabled")
	// ErrSSOUnavailable: single sign-on is on but the issuer or client is not
	// configured, the issuer cannot be discovered, or the token endpoint
	// failed (503).
	ErrSSOUnavailable = errors.New("single sign-on is unavailable")
	// ErrSSOSignInFailed covers every refusal of a callback (401): an
	// unknown, used or expired state, a missing sign-in cookie or one that
	// names another sign-in, a token that does not verify, a claim that does
	// not map, a refused account. The reason is logged as an
	// error_code and never disclosed.
	ErrSSOSignInFailed = errors.New("single sign-on failed")
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
	oidcAttemptTTL         = 10 * time.Minute
	oidcHTTPTimeout        = 10 * time.Second
	oidcProviderCacheTTL   = 24 * time.Hour
	oidcIssuerMaxBytes     = models.UserIdentityIssuerMaxLength
	oidcSubjectMaxBytes    = models.UserIdentitySubjectMaxLength
	oidcEmailMaxBytes      = 255
	oidcNameMaxBytes       = 100
	oidcCallbackPath       = "/auth/callback"
	oidcRandomPasswordSize = 32
)

// Microsoft Graph group resolution of an Entra ID group overage (#450).
const (
	// oidcEntraIssuerHost is the host of the Entra ID global cloud. Sovereign
	// clouds (login.microsoftonline.us, login.chinacloudapi.cn) are left out
	// because their Graph host differs.
	oidcEntraIssuerHost = "login.microsoftonline.com"
	oidcGraphBaseURL    = "https://graph.microsoft.com"
	// oidcGraphTimeout is ONE deadline over every Graph page of a sign-in.
	oidcGraphTimeout = 5 * time.Second
	// oidcGraphScope is the delegated permission Start adds for an Entra ID
	// issuer whose role mapping uses groups: User.Read, the least-privileged
	// permission for /me/getMemberObjects.
	oidcGraphScope           = "https://graph.microsoft.com/User.Read"
	oidcGraphMemberObjects   = "/v1.0/me/getMemberObjects"
	oidcGraphPageMaxBytes    = 1 << 20
	oidcGraphMemberObjectsIn = `{"securityEnabledOnly":true}`
)

// Userinfo group resolution of an ID token without a groups claim (#451).
const (
	// oidcUserInfoTimeout is the one deadline of the userinfo request.
	oidcUserInfoTimeout = 5 * time.Second
	// oidcUserInfoMaxBytes bounds the userinfo response body.
	oidcUserInfoMaxBytes = 1 << 20
)

// oidcRefusalError is a refusal with the error_code it is logged under. It is
// ErrSSOSignInFailed to errors.Is, and also the cause it wraps, if any. userID
// names the account an identity row linked to, when the refusal concerns one
// (the audit row's user_id, #454).
type oidcRefusalError struct {
	code   string
	cause  error
	userID uint
}

func (e *oidcRefusalError) Error() string { return "single sign-on failed: " + e.code }

func (e *oidcRefusalError) Is(target error) bool { return target == ErrSSOSignInFailed }

func (e *oidcRefusalError) Unwrap() error { return e.cause }

func oidcRefuse(code string) error { return &oidcRefusalError{code: code} }

func oidcRefuseWith(code string, cause error) error {
	return &oidcRefusalError{code: code, cause: cause}
}

// oidcRefusalForUser sets the user a refusal concerns; err is returned as it
// is when it is not a refusal.
func oidcRefusalForUser(err error, userID uint) error {
	var refusal *oidcRefusalError
	if errors.As(err, &refusal) {
		refusal.userID = userID
	}
	return err
}

// oidcErrorCode is the error_code a refusal is logged under.
func oidcErrorCode(err error) string {
	var refusal *oidcRefusalError
	if errors.As(err, &refusal) {
		return refusal.code
	}
	return ""
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
	sso           SSOSettingsService
	auth          *authService
	users         *userService
	refreshTokens repository.RefreshTokenRepository
	identities    repository.UserIdentityRepository
	attempts      repository.OIDCLoginAttemptRepository
	appBaseURL    string
	now           func() time.Time
	rootCAs       *x509.CertPool
	// client is the issuer client (newOIDCHTTPClient), built once by
	// NewOIDCService after the options.
	client *http.Client
	// providers keeps the last discovered issuer (see discover).
	providers oidcProviderCache
	// beforeJITInsert is a test seam of the first sign-in (see
	// oidcSignIn.beforeInsert); nil in production.
	beforeJITInsert func()
	// beforeSCIMLink is a test seam of the first sign-in that matched a
	// SCIM-created identity by its oid (see oidcSignIn.beforeSCIMLink); nil
	// in production.
	beforeSCIMLink func()
	// entraIssuerHost, graphBaseURL and graphTimeout drive the Microsoft
	// Graph resolution of an Entra ID group overage. NewOIDCService sets the
	// production values; tests in this package replace them directly.
	entraIssuerHost string
	graphBaseURL    string
	graphTimeout    time.Duration
	// userinfoTimeout is the deadline of the userinfo request (#451).
	userinfoTimeout time.Duration
	// audit, when set, records every callback (#454).
	audit AuthAuditService
}

// NewOIDCService builds the sign-in from the single sign-on settings, the
// auth service built by NewAuthServiceWithSessions (its token-issuing half),
// the user, role, role-audit, refresh-token, identity and attempt
// repositories, the transaction manager they share and APP_BASE_URL.
func NewOIDCService(
	sso SSOSettingsService,
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
		sso:           sso,
		auth:          issuer,
		users:         &userService{userRepo: userRepo, governance: &roleGovernance{users: userRepo, roles: roleRepo, audit: auditRepo, txManager: txManager}},
		refreshTokens: refreshTokens,
		identities:    identities,
		attempts:      attempts,
		appBaseURL:    strings.TrimRight(appBaseURL, "/"),
		now:           time.Now,

		entraIssuerHost: oidcEntraIssuerHost,
		graphBaseURL:    oidcGraphBaseURL,
		graphTimeout:    oidcGraphTimeout,
		userinfoTimeout: oidcUserInfoTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.client = newOIDCHTTPClient(s.rootCAs)
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

// oidcLogger is the entry every sign-in log line starts from: the component,
// the request id the handler put on ctx (a field only, it is client input)
// and the attempt row id, 0 when there is none yet.
func oidcLogger(ctx context.Context, attemptID uint) *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithFields(logrus.Fields{
		"component":  "oidc_sign_in",
		"request_id": utils.RequestIDFromContext(ctx),
		"attempt_id": attemptID,
	})
}

// newOIDCHTTPClient builds the client that talks to an issuer: discovery,
// JWKS and the code exchange. NewOIDCService builds it once and every
// sign-in shares it, including the cached key sets. It has a timeout, never
// follows a redirect and ignores HTTP(S)_PROXY. rootCAs nil means the system
// roots.
func newOIDCHTTPClient(rootCAs *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if rootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:   oidcHTTPTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// redirectURI is the SPA page the issuer sends the browser back to.
func (s *oidcService) redirectURI() string {
	return s.appBaseURL + oidcCallbackPath
}

// usableConfig reads the settings and refuses a configuration that cannot
// sign anyone in.
func (s *oidcService) usableConfig(ctx context.Context) (EffectiveSSOConfig, error) {
	if s.auth == nil {
		return EffectiveSSOConfig{}, errOIDCNotWired
	}
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return EffectiveSSOConfig{}, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	if !cfg.Enabled {
		return EffectiveSSOConfig{}, ErrSSODisabled
	}
	return cfg, nil
}

func ssoConfigured(cfg EffectiveSSOConfig) bool {
	return cfg.IssuerURL != "" && cfg.ClientID != "" && len(cfg.IssuerURL) <= oidcIssuerMaxBytes
}

// oidcProviderKey identifies a cached discovery: the configured issuer URL,
// whether the Entra ID application signing keys are used and, only then, the
// client id they are read for.
type oidcProviderKey struct {
	issuer   string
	appKeys  bool
	clientID string
}

// oidcProvider is one discovered issuer: the go-oidc provider and, in
// app-key mode, the key set of jwks_uri?appid=<client id> with the signing
// algorithms the discovery document allows.
type oidcProvider struct {
	key       oidcProviderKey
	provider  *oidc.Provider
	appKeySet oidc.KeySet
	appAlgs   []string
	fetchedAt time.Time
}

// oidcProviderCache holds at most one discovered issuer. mu is held while a
// miss fetches, so concurrent misses wait for one discovery.
type oidcProviderCache struct {
	mu    sync.Mutex
	entry *oidcProvider
}

// oidcSigningAlgs are the ID token algorithms go-oidc verifies; a
// discovery document's list is filtered to them the way go-oidc filters it
// for provider.Verifier.
var oidcSigningAlgs = map[string]bool{
	oidc.RS256: true, oidc.RS384: true, oidc.RS512: true,
	oidc.ES256: true, oidc.ES384: true, oidc.ES512: true,
	oidc.PS256: true, oidc.PS384: true, oidc.PS512: true,
	oidc.EdDSA: true,
}

// providerKey is the cache key of cfg. The application signing keys are used
// only when OIDC_ENTRA_APP_SIGNING_KEYS is on and the issuer is Entra ID.
func (s *oidcService) providerKey(cfg EffectiveSSOConfig) oidcProviderKey {
	key := oidcProviderKey{issuer: cfg.IssuerURL}
	if cfg.EntraAppSigningKeys && oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) {
		key.appKeys, key.clientID = true, cfg.ClientID
	}
	return key
}

// discover returns the configured issuer's provider and the context that
// carries the issuer client for the code exchange and the userinfo call.
// The discovery document and the signing keys are kept for
// oidcProviderCacheTTL per (issuer, app-key mode, client id in app-key
// mode); a different key replaces the entry, and go-oidc fetches the keys
// again when a token names an unknown key id. A failed discovery
// (unreachable, redirect, timeout, an issuer mismatch) is not kept, leaves
// the cached entry as it was and is ErrSSOUnavailable.
func (s *oidcService) discover(ctx context.Context, cfg EffectiveSSOConfig, attemptID uint) (context.Context, *oidcProvider, error) {
	ctx = oidc.ClientContext(ctx, s.client)
	key := s.providerKey(cfg)

	s.providers.mu.Lock()
	defer s.providers.mu.Unlock()
	if entry := s.providers.entry; entry != nil && entry.key == key && s.now().Before(entry.fetchedAt.Add(oidcProviderCacheTTL)) {
		return ctx, entry, nil
	}
	entry, err := s.fetchProvider(ctx, key)
	if err != nil {
		oidcLogger(ctx, attemptID).WithField("error_code", "discovery_failed").Warn("Single sign-on issuer discovery failed")
		return nil, nil, ssoUnavailable(AuthAuditReasonDiscoveryFailed)
	}
	s.providers.entry = entry
	return ctx, entry, nil
}

// fetchProvider runs the discovery of key.issuer and, in app-key mode,
// prepares the key set of jwks_uri?appid=<client id>.
func (s *oidcService) fetchProvider(ctx context.Context, key oidcProviderKey) (*oidcProvider, error) {
	provider, err := oidc.NewProvider(ctx, key.issuer)
	if err != nil {
		return nil, err
	}
	entry := &oidcProvider{key: key, provider: provider, fetchedAt: s.now()}
	if !key.appKeys {
		return entry, nil
	}
	var document struct {
		JWKSURI    string   `json:"jwks_uri"`
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := provider.Claims(&document); err != nil {
		return nil, err
	}
	jwksURL, err := url.Parse(document.JWKSURI)
	if err != nil || document.JWKSURI == "" {
		return nil, errors.New("the discovery document has no usable jwks_uri")
	}
	query := jwksURL.Query()
	query.Set("appid", key.clientID)
	jwksURL.RawQuery = query.Encode()
	entry.appKeySet = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), s.client), jwksURL.String())
	for _, alg := range document.Algorithms {
		if oidcSigningAlgs[alg] {
			entry.appAlgs = append(entry.appAlgs, alg)
		}
	}
	return entry, nil
}

// verifier checks ID tokens for the current client id against the entry's
// keys: the provider's own key set, or the application signing keys in
// app-key mode. It is built per call, never cached.
func (p *oidcProvider) verifier(clientID string) *oidc.IDTokenVerifier {
	if p.appKeySet == nil {
		return p.provider.Verifier(&oidc.Config{ClientID: clientID})
	}
	return oidc.NewVerifier(p.key.issuer, p.appKeySet, &oidc.Config{ClientID: clientID, SupportedSigningAlgs: p.appAlgs})
}

// oauthConfig is the OAuth 2.0 client of the configuration. With a client
// certificate in force it carries no secret and sends client_id in the form,
// so the token request authenticates only with the client assertion.
func (s *oidcService) oauthConfig(provider *oidc.Provider, cfg EffectiveSSOConfig) *oauth2.Config {
	out := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  s.redirectURI(),
		Scopes:       s.requestScopes(cfg),
	}
	if cfg.ClientCertificate != nil {
		out.ClientSecret = ""
		out.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	return out
}

// exchangeCode redeems the authorization code with the PKCE verifier. With a
// client certificate in force the request carries a fresh client assertion;
// a signing failure fails the exchange.
func (s *oidcService) exchangeCode(ctx context.Context, provider *oidc.Provider, cfg EffectiveSSOConfig, code, verifier string) (*oauth2.Token, error) {
	options := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if cfg.ClientCertificate != nil {
		assertion, err := s.clientAssertion(cfg.ClientCertificate, cfg.ClientID, provider.Endpoint().TokenURL)
		if err != nil {
			return nil, err
		}
		options = append(options,
			oauth2.SetAuthURLParam("client_assertion_type", oidcClientAssertionType),
			oauth2.SetAuthURLParam("client_assertion", assertion))
	}
	return s.oauthConfig(provider, cfg).Exchange(ctx, code, options...)
}

// The client assertion of RFC 7523 (private_key_jwt).
const (
	oidcClientAssertionType     = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	oidcClientAssertionLifetime = 5 * time.Minute
)

// clientAssertion signs the client assertion of one code exchange: PS256 over
// the certificate's key, the certificate named by its x5t and x5t#S256
// thumbprints, aud the token endpoint, iss and sub the client id, a fresh jti,
// valid from now for five minutes.
func (s *oidcService) clientAssertion(cert *SSOClientCertificate, clientID, tokenURL string) (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("generate the client assertion id: %w", err)
	}
	now := s.now().UTC()
	token := jwt.NewWithClaims(jwt.SigningMethodPS256, jwt.RegisteredClaims{
		Issuer:    clientID,
		Subject:   clientID,
		Audience:  jwt.ClaimStrings{tokenURL},
		ID:        hex.EncodeToString(jti),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(oidcClientAssertionLifetime)),
	})
	sha1Sum := sha1.Sum(cert.Certificate.Raw)
	sha256Sum := sha256.Sum256(cert.Certificate.Raw)
	token.Header["typ"] = "JWT"
	token.Header["x5t"] = base64.RawURLEncoding.EncodeToString(sha1Sum[:])
	token.Header["x5t#S256"] = base64.RawURLEncoding.EncodeToString(sha256Sum[:])
	signed, err := token.SignedString(cert.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("sign the client assertion: %w", err)
	}
	return signed, nil
}

// requestScopes are the configured scopes, plus the Microsoft Graph
// User.Read scope (once) when the issuer is Entra ID and the role
// mapping uses groups, so a group overage can be resolved. The added scope is
// never stored.
func (s *oidcService) requestScopes(cfg EffectiveSSOConfig) []string {
	scopes := append([]string(nil), cfg.Scopes...)
	if !oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) || !rolemap.UsesGroups(cfg.RoleMapping) {
		return scopes
	}
	for _, scope := range scopes {
		if scope == oidcGraphScope {
			return scopes
		}
	}
	return append(scopes, oidcGraphScope)
}

// oidcIsEntraIssuer reports whether the issuer URL's host (without port,
// any letter case) is exactly entraHost. A host that merely starts with it
// or a path that names it does not count.
func oidcIsEntraIssuer(issuerURL, entraHost string) bool {
	if entraHost == "" {
		return false
	}
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), entraHost)
}

// oidcEntraTenant is the first path segment of the issuer URL, the tenant of
// an Entra ID issuer (https://login.microsoftonline.com/<tenant>/v2.0), or ""
// when the URL does not parse or that segment is empty.
func oidcEntraTenant(issuerURL string) string {
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return ""
	}
	segment, _, _ := strings.Cut(strings.TrimPrefix(parsed.Path, "/"), "/")
	return segment
}

func (s *oidcService) Start(ctx context.Context) (*OIDCAuthorization, error) {
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !ssoConfigured(cfg) {
		oidcLogger(ctx, 0).WithField("error_code", "not_configured").Warn("Single sign-on is enabled but not configured")
		return nil, ssoUnavailable(AuthAuditReasonNotConfigured)
	}
	ctx, discovered, err := s.discover(ctx, cfg, 0)
	if err != nil {
		return nil, err
	}
	provider := discovered.provider

	state, err := generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	nonce, err := generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	now := s.now().UTC()
	attempt := &models.OIDCLoginAttempt{
		StateHash:    hashOpaqueToken(state, s.auth.sessionTokenSecret()),
		Nonce:        nonce,
		CodeVerifier: verifier,
		Issuer:       cfg.IssuerURL,
		ExpiresAt:    now.Add(oidcAttemptTTL),
	}
	if err := s.attempts.Create(attempt); err != nil {
		return nil, fmt.Errorf("store the single sign-on attempt: %w", err)
	}
	oidcLogger(ctx, attempt.ID).Info("Single sign-on started")
	return &OIDCAuthorization{
		URL:          s.oauthConfig(provider, cfg).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)),
		State:        state,
		SecureCookie: s.httpsOnlyCookie(),
	}, nil
}

// httpsOnlyCookie reports whether APP_BASE_URL is https (the scheme compared
// in any letter case), in which case the sign-in cookie is sent only over HTTPS.
func (s *oidcService) httpsOnlyCookie() bool {
	scheme, _, found := strings.Cut(s.appBaseURL, "://")
	return found && strings.EqualFold(scheme, "https")
}

// oidcClaims are the ID-token claims the sign-in reads. Groups and roles are
// kept raw so their shape can be checked.
type oidcClaims struct {
	Groups     json.RawMessage `json:"groups"`
	Roles      json.RawMessage `json:"roles"`
	ClaimNames json.RawMessage `json:"_claim_names"`
	Email      json.RawMessage `json:"email"`
	GivenName  json.RawMessage `json:"given_name"`
	FamilyName json.RawMessage `json:"family_name"`
	Name       json.RawMessage `json:"name"`
	// OID and TID are the Entra ID object id and tenant id (#505).
	OID json.RawMessage `json:"oid"`
	TID json.RawMessage `json:"tid"`
}

func (s *oidcService) Callback(ctx context.Context, code, state, binding string) (*AuthTokens, error) {
	tokens, facts, err := s.callback(ctx, code, state, binding)
	return s.finishCallback(ctx, tokens, facts, err)
}

// finishCallback writes the audit row of a callback and then logs its
// outcome with the row id (audit_id, 0 when the write failed or there is no
// audit service).
func (s *oidcService) finishCallback(ctx context.Context, tokens *AuthTokens, facts oidcCallbackFacts, err error) (*AuthTokens, error) {
	auditID := s.recordCallback(ctx, tokens, facts, err)
	if err != nil {
		if errorCode := oidcErrorCode(err); errorCode != "" {
			oidcLogger(ctx, facts.attemptID).WithField("error_code", errorCode).WithField("audit_id", auditID).
				Warn("Single sign-on refused")
		} else if errorCode := oidcUnavailableCode(err); errorCode != "" {
			oidcLogger(ctx, facts.attemptID).WithField("error_code", errorCode).WithField("audit_id", auditID).
				Warn("Single sign-on unavailable")
		}
		return nil, err
	}
	oidcLogger(ctx, facts.attemptID).WithField("user_id", tokens.User.ID).WithField("audit_id", auditID).
		Info("Signed in through single sign-on")
	return tokens, nil
}

// oidcUnavailableCode is the reason code of a 503, "" for any other error.
func oidcUnavailableCode(err error) string {
	var unavailable *ssoUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.code
	}
	return ""
}

// oidcCallbackFacts is what a callback learned before it ended, for the
// audit row (#454): the claimed attempt, the verified identity, and the
// claim values the mapping used.
type oidcCallbackFacts struct {
	// attemptID is the claimed attempt, 0 when the claim did not succeed.
	attemptID uint
	// verified is set once the ID token verified and its nonce matched;
	// issuer and subject are its iss and sub.
	verified        bool
	issuer, subject string
	// groups and roles are nil until they were read and well formed; groups
	// is the resolved list once a Graph or userinfo resolution ran.
	groups, roles []string
}

// recordCallback writes the audit row of one callback and returns its id, 0
// without an audit service or when the write failed. subject_ref is set only
// on a refusal decided after the ID token verified.
func (s *oidcService) recordCallback(ctx context.Context, tokens *AuthTokens, facts oidcCallbackFacts, err error) uint {
	if s.audit == nil {
		return 0
	}
	entry := outcomeEntry(AuthAuditMethodOIDC, tokens, err, AuthAuditReasonInternalError)
	entry.AttemptID = facts.attemptID
	entry.ClaimGroups = facts.groups
	entry.ClaimRoles = facts.roles
	if err != nil && facts.verified && s.auth != nil {
		entry.SubjectRef = AuthAuditSubjectRef(s.auth.sessionTokenSecret(), facts.issuer, facts.subject)
	}
	return s.audit.Record(ctx, entry)
}

// callback finishes the sign-in and also returns what it learned: the id of
// the attempt it claimed (0 when the claim did not succeed), for the log line
// of Callback, and the verified identity and claim values, for the audit row.
func (s *oidcService) callback(ctx context.Context, code, state, binding string) (*AuthTokens, oidcCallbackFacts, error) {
	var facts oidcCallbackFacts
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return nil, facts, err
	}

	// The claim is its own committed statement: a plain read by the state's
	// HMAC, then one conditional UPDATE by id that stamps used_at and blanks
	// the nonce and verifier (an expired row only has them blanked). A state
	// is spent whatever happens next, so it can never be replayed; the
	// returned attempt still holds the verifier and nonce in memory.
	attempt, err := s.attempts.Claim(hashOpaqueToken(state, s.auth.sessionTokenSecret()), s.now().UTC())
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, facts, oidcRefuse(AuthAuditReasonStateInvalid)
		}
		return nil, facts, fmt.Errorf("claim the single sign-on attempt: %w", err)
	}
	facts.attemptID = attempt.ID
	// The sign-in cookie set by Start holds the raw state, and the attempt
	// was found by the state's HMAC: a cookie equal to the posted state is
	// bound to this attempt. Checked after the claim, so a refused callback
	// still spends its attempt and its audit row names it.
	if binding == "" {
		return nil, facts, oidcRefuse(AuthAuditReasonBindingMissing)
	}
	if subtle.ConstantTimeCompare([]byte(binding), []byte(state)) != 1 {
		return nil, facts, oidcRefuse(AuthAuditReasonBindingMismatch)
	}
	if attempt.Issuer != cfg.IssuerURL {
		return nil, facts, oidcRefuse(AuthAuditReasonIssuerChanged)
	}
	if !ssoConfigured(cfg) {
		oidcLogger(ctx, attempt.ID).WithField("error_code", "not_configured").Warn("Single sign-on is enabled but not configured")
		return nil, facts, ssoUnavailable(AuthAuditReasonNotConfigured)
	}
	ctx, discovered, err := s.discover(ctx, cfg, attempt.ID)
	if err != nil {
		return nil, facts, err
	}
	provider := discovered.provider

	token, err := s.exchangeCode(ctx, provider, cfg, code, attempt.CodeVerifier)
	if err != nil {
		oidcLogger(ctx, attempt.ID).WithField("error_code", "token_exchange_failed").Warn("Single sign-on code exchange failed")
		return nil, facts, ssoUnavailable(AuthAuditReasonTokenExchangeFailed)
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		return nil, facts, oidcRefuse(AuthAuditReasonIDTokenMissing)
	}
	idToken, err := discovered.verifier(cfg.ClientID).Verify(ctx, rawIDToken)
	if err != nil {
		return nil, facts, oidcRefuse(AuthAuditReasonIDTokenInvalid)
	}
	if idToken.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(attempt.Nonce)) != 1 {
		return nil, facts, oidcRefuse(AuthAuditReasonNonceMismatch)
	}
	facts.verified, facts.issuer, facts.subject = true, idToken.Issuer, idToken.Subject

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, facts, oidcRefuse(AuthAuditReasonClaimsInvalid)
	}
	// Distributed claims: an issuer that moved groups out of the token (a
	// group overage). Without a groups mapping the overage does not matter;
	// on Entra ID the groups are read from Microsoft Graph below, after every
	// other check; any other issuer fails closed. _claim_sources is never
	// read. A groups mapping and a token without a groups claim (and no
	// overage) read the groups from the userinfo endpoint instead.
	resolveGroups := false
	if len(claims.ClaimNames) > 0 {
		var names map[string]json.RawMessage
		if err := json.Unmarshal(claims.ClaimNames, &names); err != nil {
			return nil, facts, oidcRefuse(AuthAuditReasonClaimsInvalid)
		}
		if _, overage := names[SSOClaimGroups]; overage && rolemap.UsesGroups(cfg.RoleMapping) {
			if !oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) {
				return nil, facts, oidcRefuse(AuthAuditReasonGroupOverage)
			}
			resolveGroups = true
		}
	}
	groups, ok := oidcStringArray(claims.Groups)
	if !ok {
		return nil, facts, oidcRefuse(AuthAuditReasonGroupsMalformed)
	}
	roles, ok := oidcStringArray(claims.Roles)
	if !ok {
		return nil, facts, oidcRefuse(AuthAuditReasonRolesMalformed)
	}
	facts.roles = oidcClaimValues(roles)
	if !resolveGroups && !(rolemap.UsesGroups(cfg.RoleMapping) && len(claims.Groups) == 0) {
		facts.groups = oidcClaimValues(groups)
	}
	subject := idToken.Subject
	if !validOIDCSubject(subject) {
		return nil, facts, oidcRefuse(AuthAuditReasonSubjectInvalid)
	}
	if len(idToken.Issuer) > oidcIssuerMaxBytes {
		return nil, facts, oidcRefuse(AuthAuditReasonIssuerInvalid)
	}
	if resolveGroups {
		resolved, err := s.resolveEntraGroups(ctx, token.AccessToken, cfg.GraphMaxPages)
		if err != nil {
			return nil, facts, oidcRefuse(AuthAuditReasonGroupsUnavailable)
		}
		groups = resolved
		facts.groups = oidcClaimValues(groups)
	} else if rolemap.UsesGroups(cfg.RoleMapping) && len(claims.Groups) == 0 {
		resolved, err := s.resolveUserInfoGroups(ctx, provider, token.AccessToken, subject)
		if err != nil {
			if errors.Is(err, errUserInfoSubject) {
				return nil, facts, oidcRefuse(AuthAuditReasonUserInfoSubjectMismatch)
			}
			return nil, facts, oidcRefuse(AuthAuditReasonGroupsUnavailable)
		}
		groups = resolved
		facts.groups = oidcClaimValues(groups)
	}

	roleID, matched := rolemap.Match(cfg.RoleMapping, groups, roles)
	if !matched {
		return nil, facts, oidcRefuse(AuthAuditReasonNoMappingMatch)
	}

	// The oid links to a SCIM-created account only under an Entra ID issuer
	// and only when the token's tenant is the configured issuer's tenant,
	// compared byte for byte; a non-string claim counts as absent.
	scimSubject := ""
	if oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) {
		tid := oidcString(claims.TID)
		oid := oidcString(claims.OID)
		if tid != "" && tid == oidcEntraTenant(cfg.IssuerURL) && validOIDCSubject(oid) {
			scimSubject = oid
		}
	}

	passwordHash, err := oidcUnusablePasswordHash()
	if err != nil {
		return nil, facts, err
	}
	firstName, lastName := oidcProfileNames(oidcString(claims.GivenName), oidcString(claims.FamilyName), oidcString(claims.Name))
	tokens, err := s.users.ApplyOIDCSignIn(ctx, oidcSignIn{
		issuer:       idToken.Issuer,
		subject:      subject,
		roleID:       roleID,
		email:        oidcString(claims.Email),
		firstName:    firstName,
		lastName:     lastName,
		passwordHash: passwordHash,
		identities:   s.identities,
		issue:        s.issue,
		beforeInsert: s.beforeJITInsert,

		scimSubject:    scimSubject,
		beforeSCIMLink: s.beforeSCIMLink,
	})
	return tokens, facts, err
}

// Failures of the Microsoft Graph group resolution. They never carry a URL,
// a token, a status text or a response body.
var (
	errGraphNoAccessToken = errors.New("graph: no access token")
	errGraphRequest       = errors.New("graph: request failed")
	errGraphStatus        = errors.New("graph: unexpected status")
	errGraphBody          = errors.New("graph: malformed response")
	errGraphTooLarge      = errors.New("graph: response too large")
	errGraphNextLink      = errors.New("graph: next link refused")
	errGraphTooManyPages  = errors.New("graph: too many pages")
)

// graphMemberObjectsPage is one page of getMemberObjects.
type graphMemberObjectsPage struct {
	Value    json.RawMessage `json:"value"`
	NextLink string          `json:"@odata.nextLink"`
}

// resolveEntraGroups reads the security-group object ids of the signed-in
// person from Microsoft Graph (POST /v1.0/me/getMemberObjects with
// securityEnabledOnly) with the access token of the code exchange. One
// deadline (graphTimeout) covers every page; at most maxPages requests are
// sent, each to an https URL on the host of graphBaseURL, through the issuer
// client (no redirects, no proxy). The token is only put into the request
// header; it is never logged, stored or put into an error.
func (s *oidcService) resolveEntraGroups(ctx context.Context, accessToken string, maxPages int) ([]string, error) {
	if accessToken == "" {
		return nil, errGraphNoAccessToken
	}
	if maxPages < 1 {
		maxPages = oidcGraphDefaultMaxPages
	}
	base, err := url.Parse(s.graphBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errGraphNextLink
	}
	ctx, cancel := context.WithTimeout(ctx, s.graphTimeout)
	defer cancel()
	client := newOIDCHTTPClient(s.rootCAs)

	ids := []string{}
	next := strings.TrimRight(s.graphBaseURL, "/") + oidcGraphMemberObjects
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, errGraphTooManyPages
		}
		if !graphSameOrigin(next, base) {
			return nil, errGraphNextLink
		}
		values, nextLink, err := graphMemberObjectsRequest(ctx, client, next, accessToken)
		if err != nil {
			return nil, err
		}
		ids = append(ids, values...)
		if nextLink == "" {
			return ids, nil
		}
		next = nextLink
	}
}

// graphSameOrigin accepts an absolute https URL whose host, port included,
// is base's.
func graphSameOrigin(raw string, base *url.URL) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return false
	}
	return strings.EqualFold(graphHostPort(parsed), graphHostPort(base))
}

// graphHostPort is the URL's host with its port, the https default made
// explicit, so https://h and https://h:443 compare equal.
func graphHostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func graphMemberObjectsRequest(ctx context.Context, client *http.Client, target, accessToken string) ([]string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(oidcGraphMemberObjectsIn))
	if err != nil {
		return nil, "", errGraphRequest
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.Do(request)
	if err != nil {
		return nil, "", errGraphRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", errGraphStatus
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oidcGraphPageMaxBytes+1))
	if err != nil {
		return nil, "", errGraphRequest
	}
	if len(body) > oidcGraphPageMaxBytes {
		return nil, "", errGraphTooLarge
	}
	var page graphMemberObjectsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", errGraphBody
	}
	values, ok := graphStringArray(page.Value)
	if !ok {
		return nil, "", errGraphBody
	}
	return values, page.NextLink, nil
}

// graphStringArray decodes a value that must be a JSON array whose every
// element is a string; absent, null and any other element are refused.
func graphStringArray(raw json.RawMessage) ([]string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, false
	}
	values := make([]string, 0, len(elements))
	for _, element := range elements {
		var value string
		if !strings.HasPrefix(strings.TrimSpace(string(element)), `"`) || json.Unmarshal(element, &value) != nil {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

// Failures of the userinfo group resolution. Like the Graph errors they never
// carry a URL, a token, a status text or a response body.
var (
	errUserInfoNoAccessToken = errors.New("userinfo: no access token")
	errUserInfoEndpoint      = errors.New("userinfo: endpoint refused")
	errUserInfoRequest       = errors.New("userinfo: request failed")
	errUserInfoStatus        = errors.New("userinfo: unexpected status")
	errUserInfoContentType   = errors.New("userinfo: not a JSON response")
	errUserInfoTooLarge      = errors.New("userinfo: response too large")
	errUserInfoBody          = errors.New("userinfo: malformed response")
	errUserInfoSubject       = errors.New("userinfo: subject mismatch")
)

// resolveUserInfoGroups reads the groups member of the issuer's userinfo
// response (#451) with the access token of the code exchange: one GET to the
// discovered userinfo_endpoint (an absolute https URL without credentials),
// through the issuer client (no redirects, no proxy), under one deadline
// (userinfoTimeout), with a body of at most oidcUserInfoMaxBytes of
// application/json (signed application/jwt userinfo is not supported). The
// response's sub must equal the ID token's subject byte for byte; only groups
// is read, an absent groups member is an empty list. provider.UserInfo is not
// used: it reads the body without a bound and puts it into its errors. The
// token is only put into the request header; it is never logged, stored or
// put into an error.
func (s *oidcService) resolveUserInfoGroups(ctx context.Context, provider *oidc.Provider, accessToken, subject string) ([]string, error) {
	if accessToken == "" {
		return nil, errUserInfoNoAccessToken
	}
	endpoint := provider.UserInfoEndpoint()
	parsed, err := url.Parse(endpoint)
	if endpoint == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errUserInfoEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, s.userinfoTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errUserInfoRequest
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := newOIDCHTTPClient(s.rootCAs).Do(request)
	if err != nil {
		return nil, errUserInfoRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errUserInfoStatus
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errUserInfoContentType
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oidcUserInfoMaxBytes+1))
	if err != nil {
		return nil, errUserInfoRequest
	}
	if len(body) > oidcUserInfoMaxBytes {
		return nil, errUserInfoTooLarge
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		return nil, errUserInfoBody
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, errUserInfoBody
	}
	rawSub, found := members["sub"]
	var sub string
	if !found || !strings.HasPrefix(strings.TrimSpace(string(rawSub)), `"`) || json.Unmarshal(rawSub, &sub) != nil || sub != subject {
		return nil, errUserInfoSubject
	}
	rawGroups, found := members["groups"]
	if !found {
		return []string{}, nil
	}
	groups, ok := graphStringArray(rawGroups)
	if !ok {
		return nil, errUserInfoBody
	}
	return groups, nil
}

// issue issues the session on the transaction handle exactly as a wired login
// does: the refresh token through the transaction-bound repository, the
// access token signed from the row the transaction wrote.
func (s *oidcService) issue(handle *gorm.DB, user *models.User) (*AuthTokens, error) {
	session, err := newRefreshSession(models.RefreshTokenSignInOIDC)
	if err != nil {
		return nil, err
	}
	refresh, err := s.auth.issueRefreshToken(s.refreshTokens.WithTx(handle), user.ID, session)
	if err != nil {
		return nil, err
	}
	access, err := s.auth.accessTokenFor(user)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token at single sign-on: %w", err)
	}
	return &AuthTokens{AccessToken: access, RefreshToken: refresh, User: user}, nil
}

// oidcStringArray decodes a claim that must be absent or a JSON array of
// strings. Anything else, null included, is refused.
func oidcStringArray(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

// oidcClaimValues is a read claim array for the audit row: never nil, so an
// absent claim is stored as [] and not as NULL.
func oidcClaimValues(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// oidcString is a string claim, or "" when it is absent or not a string.
func oidcString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// oidcProfileNames are the first and last name a first sign-in provisions:
// given_name and family_name as they are when either is a non-empty string;
// otherwise name split at whitespace, its first field the first name and the
// remaining fields, joined by single spaces, the last name; otherwise both
// empty. Each is cut to oidcNameMaxBytes on a rune boundary.
func oidcProfileNames(given, family, name string) (first, last string) {
	if given != "" || family != "" {
		first, last = given, family
	} else if fields := strings.Fields(name); len(fields) > 0 {
		first, last = fields[0], strings.Join(fields[1:], " ")
	}
	return truncateUTF8(first, oidcNameMaxBytes), truncateUTF8(last, oidcNameMaxBytes)
}

// validOIDCSubject accepts a sub of 1-255 printable ASCII bytes without
// leading or trailing whitespace, so a subject that a PAD SPACE collation
// would equate with another never reaches the database.
func validOIDCSubject(sub string) bool {
	if sub == "" || len(sub) > oidcSubjectMaxBytes || strings.TrimSpace(sub) != sub {
		return false
	}
	for i := 0; i < len(sub); i++ {
		if sub[i] < 0x20 || sub[i] > 0x7e {
			return false
		}
	}
	return true
}

// truncateUTF8 cuts s to at most max bytes on a rune boundary.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
