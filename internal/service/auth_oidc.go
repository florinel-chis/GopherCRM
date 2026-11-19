package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
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
// URL. Callback spends that row with one conditional UPDATE committed on its
// own, exchanges the code, verifies the ID token (signature, iss, aud, exp and
// the nonce), maps its groups or roles claim to a role (the first matching
// entry, in stored order; an Entra ID group overage is read from Microsoft
// Graph, #450) and then runs one governed transaction
// (userService.ApplyOIDCSignIn):
//
//	admin set → users → roles → user_identities → refresh_tokens
//
// An identity is linked only by its exact (iss, sub) pair. A first sign-in
// provisions a user (JIT) unless any user, erased ones included, already holds
// its email address in any letter case; it is never linked to an existing
// account. Every sign-in re-applies the mapped role through the governed role
// write, so the last-admin rule and the role audit (auth_method oidc) apply.
// Success returns the same token pair as a password login.

// Errors of the OIDC sign-in, classified with errors.Is.
var (
	// ErrSSODisabled: single sign-on is switched off (404).
	ErrSSODisabled = errors.New("single sign-on is disabled")
	// ErrSSOUnavailable: single sign-on is on but the issuer or client is not
	// configured, the issuer cannot be discovered, or the token endpoint
	// failed (503).
	ErrSSOUnavailable = errors.New("single sign-on is unavailable")
	// ErrSSOSignInFailed covers every refusal of a callback (401): an
	// unknown, used or expired state, a token that does not verify, a claim
	// that does not map, a refused account. The reason is logged as an
	// error_code and never disclosed.
	ErrSSOSignInFailed = errors.New("single sign-on failed")
)

// OIDCService is the OpenID Connect sign-in.
type OIDCService interface {
	// Start begins a sign-in and returns the issuer's authorization URL.
	Start(ctx context.Context) (string, error)
	// Callback finishes a sign-in with the code and state the issuer
	// redirected back with and returns the session, as a password login does.
	Callback(ctx context.Context, code, state string) (*AuthTokens, error)
}

// Limits and lifetimes of the OIDC sign-in.
const (
	oidcAttemptTTL         = 10 * time.Minute
	oidcAttemptPurgeAfter  = time.Hour
	oidcAttemptPurgeLimit  = 100
	oidcHTTPTimeout        = 10 * time.Second
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
	// issuer whose role mapping uses groups.
	oidcGraphScope           = "https://graph.microsoft.com/GroupMember.Read.All"
	oidcGraphMemberObjects   = "/v1.0/me/getMemberObjects"
	oidcGraphPageMaxBytes    = 1 << 20
	oidcGraphMemberObjectsIn = `{"securityEnabledOnly":true}`
)

// oidcRefusalError is a refusal with the error_code it is logged under. It is
// ErrSSOSignInFailed to errors.Is, and also the cause it wraps, if any.
type oidcRefusalError struct {
	code  string
	cause error
}

func (e *oidcRefusalError) Error() string { return "single sign-on failed: " + e.code }

func (e *oidcRefusalError) Is(target error) bool { return target == ErrSSOSignInFailed }

func (e *oidcRefusalError) Unwrap() error { return e.cause }

func oidcRefuse(code string) error { return &oidcRefusalError{code: code} }

func oidcRefuseWith(code string, cause error) error {
	return &oidcRefusalError{code: code, cause: cause}
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
	// beforeJITInsert is a test seam of the first sign-in (see
	// oidcSignIn.beforeInsert); nil in production.
	beforeJITInsert func()
	// entraIssuerHost, graphBaseURL and graphTimeout drive the Microsoft
	// Graph resolution of an Entra ID group overage. NewOIDCService sets the
	// production values; tests in this package replace them directly.
	entraIssuerHost string
	graphBaseURL    string
	graphTimeout    time.Duration
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
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// errOIDCNotWired is a wiring fault (500): the auth service is not the one
// NewAuthServiceWithSessions builds.
var errOIDCNotWired = errors.New("oidc sign-in needs the session-issuing auth service")

func oidcLogger() *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithField("component", "oidc_sign_in")
}

// newOIDCHTTPClient is the only client that talks to an issuer: discovery,
// JWKS and the code exchange. It has a timeout, never follows a redirect and
// ignores HTTP(S)_PROXY. rootCAs nil means the system roots.
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

// discover fetches the issuer's discovery document through the issuer
// client. Any failure (unreachable, redirect, timeout, an issuer mismatch) is
// ErrSSOUnavailable.
func (s *oidcService) discover(ctx context.Context, issuer string) (context.Context, *oidc.Provider, error) {
	ctx = oidc.ClientContext(ctx, newOIDCHTTPClient(s.rootCAs))
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		oidcLogger().WithField("error_code", "discovery_failed").Warn("Single sign-on issuer discovery failed")
		return nil, nil, ErrSSOUnavailable
	}
	return ctx, provider, nil
}

func (s *oidcService) oauthConfig(provider *oidc.Provider, cfg EffectiveSSOConfig) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  s.redirectURI(),
		Scopes:       s.requestScopes(cfg),
	}
}

// requestScopes are the configured scopes, plus the Microsoft Graph
// GroupMember.Read.All scope (once) when the issuer is Entra ID and the role
// mapping uses groups, so a group overage can be resolved. The added scope is
// never stored.
func (s *oidcService) requestScopes(cfg EffectiveSSOConfig) []string {
	scopes := append([]string(nil), cfg.Scopes...)
	if !oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) || !mappingUsesGroups(cfg.RoleMapping) {
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

// mappingUsesGroups reports whether any role mapping entry matches on the
// groups claim.
func mappingUsesGroups(mapping []SSORoleMapping) bool {
	for _, entry := range mapping {
		if entry.Claim == SSOClaimGroups {
			return true
		}
	}
	return false
}

func (s *oidcService) Start(ctx context.Context) (string, error) {
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return "", err
	}
	if !ssoConfigured(cfg) {
		oidcLogger().WithField("error_code", "not_configured").Warn("Single sign-on is enabled but not configured")
		return "", ErrSSOUnavailable
	}
	ctx, provider, err := s.discover(ctx, cfg.IssuerURL)
	if err != nil {
		return "", err
	}

	state, err := generateOpaqueToken()
	if err != nil {
		return "", err
	}
	nonce, err := generateOpaqueToken()
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()

	now := s.now().UTC()
	if purged, err := s.attempts.PurgeExpired(now.Add(-oidcAttemptPurgeAfter), oidcAttemptPurgeLimit); err != nil {
		oidcLogger().WithField("error_code", "attempt_purge_failed").Warn("Failed to purge expired single sign-on attempts")
	} else if purged > 0 {
		oidcLogger().WithField("purged", purged).Debug("Purged expired single sign-on attempts")
	}
	attempt := &models.OIDCLoginAttempt{
		StateHash:    hashOpaqueToken(state, s.auth.sessionTokenSecret()),
		Nonce:        nonce,
		CodeVerifier: verifier,
		Issuer:       cfg.IssuerURL,
		ExpiresAt:    now.Add(oidcAttemptTTL),
	}
	if err := s.attempts.Create(attempt); err != nil {
		return "", fmt.Errorf("store the single sign-on attempt: %w", err)
	}
	oidcLogger().WithField("attempt_id", attempt.ID).Info("Single sign-on started")
	return s.oauthConfig(provider, cfg).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
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
}

func (s *oidcService) Callback(ctx context.Context, code, state string) (*AuthTokens, error) {
	tokens, err := s.callback(ctx, code, state)
	if err != nil {
		if errorCode := oidcErrorCode(err); errorCode != "" {
			oidcLogger().WithField("error_code", errorCode).Warn("Single sign-on refused")
		}
		return nil, err
	}
	oidcLogger().WithField("user_id", tokens.User.ID).Info("Signed in through single sign-on")
	return tokens, nil
}

func (s *oidcService) callback(ctx context.Context, code, state string) (*AuthTokens, error) {
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return nil, err
	}

	// The claim is its own committed statement: a state is spent whatever
	// happens next, so it can never be replayed.
	attempt, err := s.attempts.Claim(hashOpaqueToken(state, s.auth.sessionTokenSecret()), s.now().UTC())
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, oidcRefuse("state_invalid")
		}
		return nil, fmt.Errorf("claim the single sign-on attempt: %w", err)
	}
	if attempt.Issuer != cfg.IssuerURL {
		return nil, oidcRefuse("issuer_changed")
	}
	if !ssoConfigured(cfg) {
		oidcLogger().WithField("error_code", "not_configured").Warn("Single sign-on is enabled but not configured")
		return nil, ErrSSOUnavailable
	}
	ctx, provider, err := s.discover(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}

	token, err := s.oauthConfig(provider, cfg).Exchange(ctx, code, oauth2.VerifierOption(attempt.CodeVerifier))
	if err != nil {
		oidcLogger().WithField("error_code", "token_exchange_failed").Warn("Single sign-on code exchange failed")
		return nil, ErrSSOUnavailable
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		return nil, oidcRefuse("id_token_missing")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return nil, oidcRefuse("id_token_invalid")
	}
	if idToken.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(attempt.Nonce)) != 1 {
		return nil, oidcRefuse("nonce_mismatch")
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, oidcRefuse("claims_invalid")
	}
	// Distributed claims: an issuer that moved groups out of the token (a
	// group overage). Without a groups mapping the overage does not matter;
	// on Entra ID the groups are read from Microsoft Graph below, after every
	// other check; any other issuer fails closed. _claim_sources is never
	// read.
	resolveGroups := false
	if len(claims.ClaimNames) > 0 {
		var names map[string]json.RawMessage
		if err := json.Unmarshal(claims.ClaimNames, &names); err != nil {
			return nil, oidcRefuse("claims_invalid")
		}
		if _, overage := names[SSOClaimGroups]; overage && mappingUsesGroups(cfg.RoleMapping) {
			if !oidcIsEntraIssuer(cfg.IssuerURL, s.entraIssuerHost) {
				return nil, oidcRefuse("group_overage")
			}
			resolveGroups = true
		}
	}
	groups, ok := oidcStringArray(claims.Groups)
	if !ok {
		return nil, oidcRefuse("groups_malformed")
	}
	roles, ok := oidcStringArray(claims.Roles)
	if !ok {
		return nil, oidcRefuse("roles_malformed")
	}
	subject := idToken.Subject
	if !validOIDCSubject(subject) {
		return nil, oidcRefuse("subject_invalid")
	}
	if len(idToken.Issuer) > oidcIssuerMaxBytes {
		return nil, oidcRefuse("issuer_invalid")
	}
	if resolveGroups {
		resolved, err := s.resolveEntraGroups(ctx, token.AccessToken, cfg.GraphMaxPages)
		if err != nil {
			return nil, oidcRefuse("groups_unavailable")
		}
		groups = resolved
	}

	roleID, matched := oidcMatchRole(cfg.RoleMapping, groups, roles)
	if !matched {
		return nil, oidcRefuse("no_mapping_match")
	}

	passwordHash, err := oidcUnusablePasswordHash()
	if err != nil {
		return nil, err
	}
	return s.users.ApplyOIDCSignIn(ctx, oidcSignIn{
		issuer:       idToken.Issuer,
		subject:      subject,
		roleID:       roleID,
		email:        oidcString(claims.Email),
		firstName:    truncateUTF8(oidcString(claims.GivenName), oidcNameMaxBytes),
		lastName:     truncateUTF8(oidcString(claims.FamilyName), oidcNameMaxBytes),
		passwordHash: passwordHash,
		identities:   s.identities,
		issue:        s.issue,
		beforeInsert: s.beforeJITInsert,
	})
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

// issue issues the session on the transaction handle exactly as a wired login
// does: the refresh token through the transaction-bound repository, the
// access token signed from the row the transaction wrote.
func (s *oidcService) issue(handle *gorm.DB, user *models.User) (*AuthTokens, error) {
	refresh, err := s.auth.issueRefreshToken(s.refreshTokens.WithTx(handle), user.ID)
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

// oidcString is a string claim, or "" when it is absent or not a string.
func oidcString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
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

// oidcMatchRole is the first mapping entry, in stored order, whose claim
// array holds its value exactly (case-sensitive).
func oidcMatchRole(mapping []SSORoleMapping, groups, roles []string) (uint, bool) {
	for _, entry := range mapping {
		values := groups
		if entry.Claim == SSOClaimRoles {
			values = roles
		} else if entry.Claim != SSOClaimGroups {
			continue
		}
		for _, value := range values {
			if value == entry.Value {
				return entry.RoleID, true
			}
		}
	}
	return 0, false
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
