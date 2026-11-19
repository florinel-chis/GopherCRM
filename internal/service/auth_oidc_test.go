package service

// Issue #435: OpenID Connect sign-in. TestOIDCSignInSQLite and
// TestOIDCSignInMySQL drive ONE table of subtests (oidcSignInCases) against
// the real service: the settings are stored through SSOSettingsService.Update,
// the issuer is an in-process httptest TLS server trusted through
// WithOIDCRootCAs, and every subtest asserts the same outcome on every engine.
// The MySQL root runs on a schema built by the real migrations
// (TEST_MYSQL_DSN; also pointed at MariaDB 10.11), the SQLite root on a file
// opened by the production connector. The test issuer checks the PKCE
// verifier against the challenge of the authorization request and answers 400
// when the redirect_uri of the exchange differs from the authorization
// request's, so every successful sign-in also proves both.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	oidcTestClientID     = "crm-client"
	oidcTestClientSecret = "crm-client-secret-0435"
	oidcTestAppBaseURL   = "https://crm.example.test"
	oidcTestJWTSecret    = "oidc-sign-in-test-jwt-secret-0123456789abcdef"
	oidcTestAPIKeySecret = "oidc-sign-in-test-api-key-secret-0123456789"
	oidcTestKeyID        = "k1"

	oidcGroupAdmins = "crm-admins"
	oidcGroupSales  = "crm-sales"
	oidcGroupUsers  = "crm-users"
	oidcAppRole     = "CRM.Support"

	// Entra ID group object ids (#450).
	oidcGraphGroupSales     = "6f1c2a3b-0d4e-4f5a-8b6c-7d8e9f0a1b2c"
	oidcGraphGroupUnrelated = "11111111-2222-4333-8444-555555555555"
	oidcGraphPath           = "/graph/v1.0/me/getMemberObjects"
)

// ------------------------------------------------------------ test issuer ---

// oidcTokenSpec is what the ID token of one sign-in carries.
type oidcTokenSpec struct {
	sub    string
	email  string
	groups interface{}
	roles  interface{}
	// extra claims, set last (they may override the standard ones).
	extra map[string]interface{}
	// nonce overrides the nonce of the authorization request when non-empty.
	nonce string
	// audience overrides the client id when non-empty.
	audience string
	// otherKey signs with a key the JWKS does not publish (same kid).
	otherKey bool
}

type oidcGrant struct {
	challenge, redirect, nonce string
	spec                       oidcTokenSpec
}

type oidcTestIssuer struct {
	t        *testing.T
	server   *httptest.Server
	key      *rsa.PrivateKey
	otherKey *rsa.PrivateKey
	// other is a second trusted TLS server standing for another host; it
	// counts the requests it receives.
	other *httptest.Server

	mu                sync.Mutex
	grants            map[string]oidcGrant
	tokenStatus       int
	discoveryRedirect bool
	issuedIDTokens    []string
	codeSeq           int
	// accessTokens maps each issued access token to its code.
	accessTokens map[string]string
	lastAccess   string
	graph        oidcFakeGraph
	otherHits    int
}

// Modes of the fake Microsoft Graph.
const (
	graphModePages       = "pages"
	graphModeStatus500   = "status500"
	graphModeSlow        = "slow"
	graphModeForeignNext = "foreign_next"
	graphModeRedirect    = "redirect"
	graphModeEndless     = "endless"
)

// oidcFakeGraph is the fake getMemberObjects: its mode, the ids it returns
// per page and what it received.
type oidcFakeGraph struct {
	mode     string
	pages    [][]string
	requests []oidcGraphRequest
}

type oidcGraphRequest struct {
	method, path, query, body, authorization, contentType string
}

func newOIDCTestIssuer(t *testing.T) *oidcTestIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := &oidcTestIssuer{t: t, key: key, otherKey: other, grants: map[string]oidcGrant{}, accessTokens: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/jwks", issuer.jwks)
	mux.HandleFunc("/token", issuer.token)
	mux.HandleFunc(oidcGraphPath, issuer.graphMemberObjects)
	issuer.server = httptest.NewTLSServer(mux)
	t.Cleanup(issuer.server.Close)
	issuer.other = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		issuer.mu.Lock()
		issuer.otherHits++
		issuer.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":["` + oidcGraphGroupSales + `"]}`))
	}))
	t.Cleanup(issuer.other.Close)
	return issuer
}

// graphMemberObjects is the fake Microsoft Graph POST
// /v1.0/me/getMemberObjects. It answers 401 unless the bearer is an access
// token this issuer returned from a code exchange.
func (i *oidcTestIssuer) graphMemberObjects(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	authorization := r.Header.Get("Authorization")
	i.mu.Lock()
	i.graph.requests = append(i.graph.requests, oidcGraphRequest{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body),
		authorization: authorization, contentType: r.Header.Get("Content-Type"),
	})
	_, known := i.accessTokens[strings.TrimPrefix(authorization, "Bearer ")]
	mode, pages := i.graph.mode, i.graph.pages
	i.mu.Unlock()
	if !strings.HasPrefix(authorization, "Bearer ") || !known {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		_, _ = fmt.Sscanf(raw, "%d", &page)
	}
	answer := map[string]interface{}{"value": []string{}}
	switch mode {
	case graphModeStatus500:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"generalException"}}`))
		return
	case graphModeSlow:
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"value":["` + oidcGraphGroupSales + `"]}`))
		return
	case graphModeRedirect:
		http.Redirect(w, r, i.other.URL+"/v1.0/me/getMemberObjects", http.StatusFound)
		return
	case graphModeForeignNext:
		answer["value"] = []string{oidcGraphGroupUnrelated}
		answer["@odata.nextLink"] = i.other.URL + "/v1.0/me/getMemberObjects?page=1"
	case graphModeEndless:
		answer["value"] = []string{oidcGraphGroupUnrelated}
		answer["@odata.nextLink"] = fmt.Sprintf("%s%s?page=%d", i.server.URL, oidcGraphPath, page+1)
	default:
		if page < len(pages) {
			answer["value"] = pages[page]
		}
		if page+1 < len(pages) {
			answer["@odata.nextLink"] = fmt.Sprintf("%s%s?page=%d", i.server.URL, oidcGraphPath, page+1)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

func (i *oidcTestIssuer) setGraph(mode string, pages ...[]string) {
	i.mu.Lock()
	i.graph.mode, i.graph.pages = mode, pages
	i.mu.Unlock()
}

func (i *oidcTestIssuer) resetGraph() {
	i.mu.Lock()
	i.graph = oidcFakeGraph{mode: graphModePages}
	i.otherHits = 0
	i.mu.Unlock()
}

func (i *oidcTestIssuer) graphRequests() []oidcGraphRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]oidcGraphRequest(nil), i.graph.requests...)
}

func (i *oidcTestIssuer) otherRequests() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.otherHits
}

// lastAccessToken is the access token of the latest code exchange.
func (i *oidcTestIssuer) lastAccessToken() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastAccess
}

func (i *oidcTestIssuer) URL() string { return i.server.URL }

func (i *oidcTestIssuer) rootCAs() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(i.server.Certificate())
	pool.AddCert(i.other.Certificate())
	return pool
}

func (i *oidcTestIssuer) discovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	redirect := i.discoveryRedirect
	i.mu.Unlock()
	if redirect {
		http.Redirect(w, r, i.server.URL+"/moved/.well-known/openid-configuration", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"issuer":                                i.server.URL,
		"authorization_endpoint":                i.server.URL + "/authorize",
		"token_endpoint":                        i.server.URL + "/token",
		"jwks_uri":                              i.server.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (i *oidcTestIssuer) jwks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &i.key.PublicKey, KeyID: oidcTestKeyID, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func oidcTokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (i *oidcTestIssuer) token(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	status := i.tokenStatus
	i.mu.Unlock()
	if status != 0 {
		oidcTokenError(w, status, "server_error")
		return
	}
	if err := r.ParseForm(); err != nil {
		oidcTokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	clientID, clientSecret, ok := r.BasicAuth()
	if ok {
		clientID, _ = url.QueryUnescape(clientID)
		clientSecret, _ = url.QueryUnescape(clientSecret)
	} else {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if clientID != oidcTestClientID || clientSecret != oidcTestClientSecret {
		oidcTokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oidcTokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	i.mu.Lock()
	grant, found := i.grants[r.PostForm.Get("code")]
	i.mu.Unlock()
	if !found {
		oidcTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// The redirect URI of the exchange must be the authorization request's.
	if r.PostForm.Get("redirect_uri") != grant.redirect {
		oidcTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// PKCE: S256 of the verifier must be the challenge of the request.
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if r.PostForm.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != grant.challenge {
		oidcTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	idToken := i.sign(grant)
	accessToken := "idp-access-token-" + r.PostForm.Get("code")
	i.mu.Lock()
	i.accessTokens[accessToken] = r.PostForm.Get("code")
	i.lastAccess = accessToken
	i.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func (i *oidcTestIssuer) sign(grant oidcGrant) string {
	spec := grant.spec
	now := time.Now()
	claims := map[string]interface{}{
		"iss":   i.server.URL,
		"sub":   spec.sub,
		"aud":   oidcTestClientID,
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": grant.nonce,
	}
	if spec.audience != "" {
		claims["aud"] = spec.audience
	}
	if spec.nonce != "" {
		claims["nonce"] = spec.nonce
	}
	if spec.email != "" {
		claims["email"] = spec.email
	}
	if spec.groups != nil {
		claims["groups"] = spec.groups
	}
	if spec.roles != nil {
		claims["roles"] = spec.roles
	}
	for k, v := range spec.extra {
		claims[k] = v
	}
	key := i.key
	if spec.otherKey {
		key = i.otherKey
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", oidcTestKeyID))
	require.NoError(i.t, err)
	payload, err := json.Marshal(claims)
	require.NoError(i.t, err)
	signed, err := signer.Sign(payload)
	require.NoError(i.t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(i.t, err)
	i.mu.Lock()
	i.issuedIDTokens = append(i.issuedIDTokens, compact)
	i.mu.Unlock()
	return compact
}

// authorize plays the browser and the issuer's login page: it checks the
// authorization URL and returns a code and the state to send back.
func (i *oidcTestIssuer) authorize(t *testing.T, authURL string, spec oidcTokenSpec) (code, state string) {
	t.Helper()
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	q := parsed.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	i.mu.Lock()
	defer i.mu.Unlock()
	i.codeSeq++
	code = fmt.Sprintf("code-%d-%s", i.codeSeq, strings.Repeat("x", 8))
	i.grants[code] = oidcGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), nonce: q.Get("nonce"), spec: spec}
	return code, q.Get("state")
}

func (i *oidcTestIssuer) setTokenStatus(status int) {
	i.mu.Lock()
	i.tokenStatus = status
	i.mu.Unlock()
}

func (i *oidcTestIssuer) setDiscoveryRedirect(on bool) {
	i.mu.Lock()
	i.discoveryRedirect = on
	i.mu.Unlock()
}

func (i *oidcTestIssuer) lastIDToken() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.issuedIDTokens) == 0 {
		return ""
	}
	return i.issuedIDTokens[len(i.issuedIDTokens)-1]
}

// ----------------------------------------------------------------- fixture ---

type oidcFixture struct {
	db *gorm.DB
	// competitor commits on a connection of its own (a second pool on the
	// SQLite file, the shared pool on MySQL).
	competitor *gorm.DB
	issuer     *oidcTestIssuer
	sso        SSOSettingsService
	// defaultSSO is the settings service the fixture was built with;
	// newSSO builds another one over the same stored settings.
	defaultSSO SSOSettingsService
	newSSO     func(base config.SSOConfig) SSOSettingsService
	auth       *authService
	svc        *oidcService

	mu    sync.Mutex
	clock time.Time

	roleIDs map[models.UserRole]uint
}

func (f *oidcFixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *oidcFixture) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func oidcTestJWTConfig() config.JWTConfig {
	return config.JWTConfig{Secret: oidcTestJWTSecret, ExpiryHours: 1, RefreshTokenDays: 30}
}

func newOIDCFixture(t *testing.T, db, competitor *gorm.DB) *oidcFixture {
	t.Helper()
	configs := NewConfigurationService(repository.NewConfigurationRepository(db),
		utils.NewSecretBox(oidcTestAPIKeySecret, "configuration-secret"))
	require.NoError(t, configs.InitializeDefaults())
	roleRepo := repository.NewRoleRepository(db)
	f := &oidcFixture{
		db:         db,
		competitor: competitor,
		issuer:     newOIDCTestIssuer(t),
		sso:        NewSSOSettingsService(configs, roleRepo, config.SSOConfig{}),
		clock:      time.Now().UTC().Truncate(time.Millisecond),
		roleIDs:    map[models.UserRole]uint{},
	}
	txManager := utils.NewTransactionManager(db)
	userRepo := repository.NewUserRepository(db)
	refreshRepo := repository.NewRefreshTokenRepository(db)
	f.auth = NewAuthServiceWithSessions(userRepo, repository.NewAPIKeyRepository(db), refreshRepo,
		repository.NewPasswordResetTokenRepository(db), txManager, nil, oidcTestJWTConfig(),
		oidcTestAppBaseURL, oidcTestAPIKeySecret).(*authService)
	f.defaultSSO = f.sso
	f.newSSO = func(base config.SSOConfig) SSOSettingsService { return NewSSOSettingsService(configs, roleRepo, base) }
	f.svc = NewOIDCService(f.sso, f.auth, userRepo, roleRepo, repository.NewRoleAuditRepository(db), refreshRepo,
		repository.NewUserIdentityRepository(db), repository.NewOIDCLoginAttemptRepository(db), txManager,
		oidcTestAppBaseURL+"/", WithOIDCClock(f.now), WithOIDCRootCAs(f.issuer.rootCAs())).(*oidcService)

	var roles []models.Role
	require.NoError(t, db.Where("system_key IS NOT NULL").Find(&roles).Error)
	for _, role := range roles {
		f.roleIDs[models.UserRole(*role.SystemKey)] = role.ID
	}
	require.Len(t, f.roleIDs, 4, "the four system roles are seeded")
	return f
}

func (f *oidcFixture) mapping() []SSORoleMapping {
	return []SSORoleMapping{
		{Claim: SSOClaimGroups, Value: oidcGroupAdmins, RoleID: f.roleIDs[models.RoleAdmin]},
		{Claim: SSOClaimGroups, Value: oidcGroupSales, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]},
	}
}

func (f *oidcFixture) configure(t *testing.T, mapping []SSORoleMapping) {
	t.Helper()
	enabled, secret := true, oidcTestClientSecret
	require.NoError(t, f.sso.Update(context.Background(), SSOSettingsUpdate{
		Enabled:      &enabled,
		IssuerURL:    f.issuer.URL(),
		ClientID:     oidcTestClientID,
		ClientSecret: &secret,
		RoleMapping:  mapping,
	}))
}

// reset empties the tables a subtest writes and restores the settings, the
// clock and the issuer.
func (f *oidcFixture) reset(t *testing.T) {
	t.Helper()
	for _, table := range []string{"refresh_tokens", "user_identities", "role_audit_log", "oidc_login_attempts", "users"} {
		require.NoError(t, f.db.Exec("DELETE FROM "+table).Error, table)
	}
	f.mu.Lock()
	f.clock = time.Now().UTC().Truncate(time.Millisecond)
	f.mu.Unlock()
	f.issuer.setTokenStatus(0)
	f.issuer.setDiscoveryRedirect(false)
	f.svc.beforeJITInsert = nil
	f.svc.entraIssuerHost = oidcEntraIssuerHost
	f.svc.graphBaseURL = oidcGraphBaseURL
	f.svc.graphTimeout = oidcGraphTimeout
	f.issuer.resetGraph()
	f.useSSO(f.defaultSSO)
	f.configure(t, f.mapping())
}

// useSSO swaps the settings service of the fixture and of the sign-in.
func (f *oidcFixture) useSSO(sso SSOSettingsService) {
	f.sso = sso
	f.svc.sso = sso
}

// entra makes the test issuer count as Entra ID and points Microsoft Graph at
// the fake under /graph on the issuer, with a 300 ms budget.
func (f *oidcFixture) entra(t *testing.T) {
	t.Helper()
	parsed, err := url.Parse(f.issuer.URL())
	require.NoError(t, err)
	f.svc.entraIssuerHost = parsed.Hostname()
	f.svc.graphBaseURL = f.issuer.URL() + "/graph"
	f.svc.graphTimeout = 300 * time.Millisecond
}

// graphMapping maps a group object id to sales and the app role to support.
func (f *oidcFixture) graphMapping() []SSORoleMapping {
	return []SSORoleMapping{
		{Claim: SSOClaimGroups, Value: oidcGraphGroupSales, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]},
	}
}

// oidcOverage is a token spec whose groups overflowed into _claim_names.
func oidcOverage(sub, email string) oidcTokenSpec {
	return oidcTokenSpec{sub: sub, email: email, extra: map[string]interface{}{
		"_claim_names":   map[string]string{"groups": "src1"},
		"_claim_sources": map[string]interface{}{"src1": map[string]string{"endpoint": "https://graph.example.test/getMemberObjects"}},
	}}
}

func (f *oidcFixture) start(t *testing.T) string {
	t.Helper()
	authURL, err := f.svc.Start(context.Background())
	require.NoError(t, err)
	return authURL
}

// signIn runs Start, the issuer's authorization and Callback.
func (f *oidcFixture) signIn(t *testing.T, spec oidcTokenSpec) (*AuthTokens, error) {
	t.Helper()
	code, state := f.issuer.authorize(t, f.start(t), spec)
	return f.svc.Callback(context.Background(), code, state)
}

func (f *oidcFixture) seedUser(t *testing.T, email string, role models.UserRole, active bool, password string) *models.User {
	t.Helper()
	hash := "x"
	if password != "" {
		raw, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		require.NoError(t, err)
		hash = string(raw)
	}
	user := &models.User{Email: email, Password: hash, FirstName: "Seed", LastName: "User", Role: role, IsActive: true}
	require.NoError(t, f.db.Create(user).Error)
	if !active {
		require.NoError(t, f.db.Model(&models.User{}).Where("id = ?", user.ID).Update("is_active", false).Error)
	}
	return user
}

func (f *oidcFixture) link(t *testing.T, userID uint, subject string) {
	t.Helper()
	require.NoError(t, f.db.Create(&models.UserIdentity{UserID: userID, Issuer: f.issuer.URL(), Subject: subject}).Error)
}

func (f *oidcFixture) count(t *testing.T, table, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	query := "SELECT COUNT(*) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	require.NoError(t, f.db.Raw(query, args...).Scan(&n).Error)
	return n
}

func (f *oidcFixture) users(t *testing.T) int64 { return f.count(t, "users", "") }

func (f *oidcFixture) identities(t *testing.T) int64 { return f.count(t, "user_identities", "") }

func (f *oidcFixture) oidcAudits(t *testing.T, userID uint) int64 {
	return f.count(t, "role_audit_log", "target_user_id = ? AND auth_method = ?", userID, AuthMethodOIDC)
}

func (f *oidcFixture) refreshTokens(t *testing.T, userID uint) int64 {
	return f.count(t, "refresh_tokens", "user_id = ?", userID)
}

func (f *oidcFixture) user(t *testing.T, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.Unscoped().First(&user, id).Error)
	return user
}

// assertRefused checks the generic refusal and that nothing was provisioned.
func (f *oidcFixture) assertRefused(t *testing.T, code string, tokens *AuthTokens, err error, usersBefore, identitiesBefore int64) {
	t.Helper()
	assert.Nil(t, tokens)
	assert.ErrorIs(t, err, ErrSSOSignInFailed)
	assert.Equal(t, code, oidcErrorCode(err), "refused for the property under test")
	assert.Equal(t, usersBefore, f.users(t), "no user is created")
	assert.Equal(t, identitiesBefore, f.identities(t), "no identity is created")
}

// valid is a token spec that signs in: a subject, an unused email and a
// group the mapping knows.
func oidcValid(sub, email string) oidcTokenSpec {
	return oidcTokenSpec{sub: sub, email: email, groups: []string{"unrelated", oidcGroupSales}}
}

// ------------------------------------------------------------------- cases ---

var oidcSignInCases = []struct {
	name string
	run  func(t *testing.T, f *oidcFixture)
}{
	{"start_authorization_url", func(t *testing.T, f *oidcFixture) {
		authURL := f.start(t)
		parsed, err := url.Parse(authURL)
		require.NoError(t, err)
		endpoint, err := url.Parse(f.issuer.URL() + "/authorize")
		require.NoError(t, err)
		assert.Equal(t, endpoint.Scheme, parsed.Scheme)
		assert.Equal(t, endpoint.Host, parsed.Host)
		assert.Equal(t, endpoint.Path, parsed.Path)
		q := parsed.Query()
		assert.Equal(t, "code", q.Get("response_type"))
		assert.Equal(t, oidcTestClientID, q.Get("client_id"))
		assert.Equal(t, "S256", q.Get("code_challenge_method"))
		assert.NotEmpty(t, q.Get("code_challenge"))
		assert.NotEmpty(t, q.Get("state"))
		assert.NotEmpty(t, q.Get("nonce"))
		assert.Equal(t, oidcTestAppBaseURL+"/auth/callback", q.Get("redirect_uri"))
		cfg, err := f.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		assert.Equal(t, strings.Join(cfg.Scopes, " "), q.Get("scope"))
		assert.Equal(t, "openid profile email", q.Get("scope"))

		var attempts []models.OIDCLoginAttempt
		require.NoError(t, f.db.Find(&attempts).Error)
		require.Len(t, attempts, 1, "exactly one attempt row")
		state := q.Get("state")
		assert.Equal(t, hashOpaqueToken(state, f.auth.sessionTokenSecret()), attempts[0].StateHash)
		assert.NotEqual(t, state, attempts[0].StateHash, "the raw state is never stored")
		assert.True(t, attempts[0].ExpiresAt.Equal(f.now().Add(10*time.Minute)),
			"expires at the test clock + 10m: %s vs %s", attempts[0].ExpiresAt, f.now().Add(10*time.Minute))
		assert.Nil(t, attempts[0].UsedAt)
		assert.Equal(t, f.issuer.URL(), attempts[0].Issuer)
		assert.Equal(t, q.Get("nonce"), attempts[0].Nonce)
	}},
	{"first_sign_in_provisions", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-first", "first@idp.example.test")
		spec.extra = map[string]interface{}{"given_name": "Ada", "family_name": "Lovelace"}
		tokens, err := f.signIn(t, spec)
		require.NoError(t, err)
		require.NotEmpty(t, tokens.AccessToken)
		require.NotEmpty(t, tokens.RefreshToken)
		require.NotNil(t, tokens.User)

		assert.Equal(t, int64(1), f.users(t), "one user")
		assert.Equal(t, int64(1), f.identities(t), "one identity")
		created := f.user(t, tokens.User.ID)
		assert.Equal(t, "first@idp.example.test", created.Email)
		assert.Equal(t, "Ada", created.FirstName)
		assert.Equal(t, "Lovelace", created.LastName)
		assert.Equal(t, models.RoleSales, created.Role)
		assert.Nil(t, created.RoleID)
		assert.True(t, created.IsActive)
		assert.Nil(t, created.LastLoginAt, "last_login_at is not touched")
		assert.Error(t, bcrypt.CompareHashAndPassword([]byte(created.Password), []byte("")), "no usable password")
		var identity models.UserIdentity
		require.NoError(t, f.db.First(&identity).Error)
		assert.Equal(t, created.ID, identity.UserID)
		assert.Equal(t, f.issuer.URL(), identity.Issuer)
		assert.Equal(t, "subject-first", identity.Subject)
		assert.Equal(t, int64(1), f.oidcAudits(t, created.ID), "one audit row with auth_method oidc")
		var audit models.RoleAuditEntry
		require.NoError(t, f.db.Where("target_user_id = ?", created.ID).First(&audit).Error)
		assert.Equal(t, created.ID, *audit.ActorUserID)
		assert.Equal(t, models.RoleAuditUserRoleChanged, audit.Action)
		assert.Nil(t, audit.BeforeState)

		validated, err := f.auth.ValidateToken(tokens.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, created.ID, validated.ID)
		assert.Equal(t, models.RoleSales, validated.Role)
		rotated, err := f.auth.RefreshAccessToken(tokens.RefreshToken)
		require.NoError(t, err)
		assert.NotEmpty(t, rotated.AccessToken)
		assert.NotEmpty(t, rotated.RefreshToken)
		assert.NotEqual(t, tokens.RefreshToken, rotated.RefreshToken)
		assert.Equal(t, created.ID, rotated.User.ID)
	}},
	{"second_sign_in_reapplies_mapping", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-second", "second@idp.example.test")
		spec.groups = []string{oidcGroupUsers}
		mapping := append([]SSORoleMapping{{Claim: SSOClaimGroups, Value: oidcGroupUsers, RoleID: f.roleIDs[models.RoleSales]}}, f.mapping()...)
		f.configure(t, mapping)
		first, err := f.signIn(t, spec)
		require.NoError(t, err)
		id := first.User.ID
		assert.Equal(t, int64(1), f.oidcAudits(t, id))

		mapping[0].RoleID = f.roleIDs[models.RoleSupport]
		f.configure(t, mapping)
		second, err := f.signIn(t, spec)
		require.NoError(t, err)
		assert.Equal(t, id, second.User.ID, "the same user")
		assert.Equal(t, int64(1), f.users(t))
		assert.Equal(t, models.RoleSupport, f.user(t, id).Role, "the new role is applied")
		assert.Equal(t, int64(2), f.oidcAudits(t, id), "one more audit row with oidc")
		validated, err := f.auth.ValidateToken(second.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, models.RoleSupport, validated.Role)

		third, err := f.signIn(t, spec)
		require.NoError(t, err)
		assert.Equal(t, id, third.User.ID)
		assert.Equal(t, int64(2), f.oidcAudits(t, id), "an unchanged mapping adds no audit row")
	}},
	{"email_of_local_user_not_linked", func(t *testing.T, f *oidcFixture) {
		local := f.seedUser(t, "local@crm.example.test", models.RoleSupport, true, "")
		before := f.user(t, local.ID)
		tokens, err := f.signIn(t, oidcValid("subject-local", "local@crm.example.test"))
		f.assertRefused(t, "email_taken", tokens, err, 1, 0)
		after := f.user(t, local.ID)
		assert.Equal(t, before.Role, after.Role)
		assert.Equal(t, before.Email, after.Email)
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""))
		assert.Equal(t, int64(0), f.refreshTokens(t, local.ID))
	}},
	{"email_case_variant_not_linked", func(t *testing.T, f *oidcFixture) {
		local := f.seedUser(t, "casey@crm.example.test", models.RoleSupport, true, "")
		tokens, err := f.signIn(t, oidcValid("subject-case", "Casey@CRM.Example.Test"))
		f.assertRefused(t, "email_taken", tokens, err, 1, 0)
		assert.Equal(t, "casey@crm.example.test", f.user(t, local.ID).Email)
		assert.Equal(t, int64(0), f.refreshTokens(t, local.ID))
	}},
	{"reused_state", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-reuse", "reuse@idp.example.test"))
		_, err := f.svc.Callback(context.Background(), code, state)
		require.NoError(t, err)
		users, identities := f.users(t), f.identities(t)
		tokens, err := f.svc.Callback(context.Background(), code, state)
		f.assertRefused(t, "state_invalid", tokens, err, users, identities)
	}},
	{"expired_state", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-expired", "expired@idp.example.test"))
		f.advance(10*time.Minute + time.Second)
		tokens, err := f.svc.Callback(context.Background(), code, state)
		f.assertRefused(t, "state_invalid", tokens, err, 0, 0)
	}},
	{"issuer_changed_after_start", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-moved", "moved@idp.example.test"))
		other := newOIDCTestIssuer(t)
		enabled, secret := true, oidcTestClientSecret
		require.NoError(t, f.sso.Update(context.Background(), SSOSettingsUpdate{
			Enabled: &enabled, IssuerURL: other.URL(), ClientID: oidcTestClientID, ClientSecret: &secret, RoleMapping: f.mapping(),
		}))
		tokens, err := f.svc.Callback(context.Background(), code, state)
		f.assertRefused(t, "issuer_changed", tokens, err, 0, 0)
	}},
	{"wrong_nonce", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-nonce", "nonce@idp.example.test")
		spec.nonce = "not-the-nonce-of-the-request"
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "nonce_mismatch", tokens, err, 0, 0)
	}},
	{"wrong_audience", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-aud", "aud@idp.example.test")
		spec.audience = "another-client"
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "id_token_invalid", tokens, err, 0, 0)
	}},
	{"bad_signature", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-sig", "sig@idp.example.test")
		spec.otherKey = true
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "id_token_invalid", tokens, err, 0, 0)
	}},
	{"malformed_groups_claim", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-groups", "groups@idp.example.test")
		spec.groups = oidcGroupSales
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "groups_malformed", tokens, err, 0, 0)
	}},
	{"no_mapping_match", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-nomatch", "nomatch@idp.example.test")
		spec.groups = []string{"crm-SALES", "other"}
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "no_mapping_match", tokens, err, 0, 0)
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""))
	}},
	{"group_overage", func(t *testing.T, f *oidcFixture) {
		spec := oidcTokenSpec{sub: "subject-overage", email: "overage@idp.example.test", roles: []string{oidcAppRole},
			extra: map[string]interface{}{
				"_claim_names":   map[string]string{"groups": "src1"},
				"_claim_sources": map[string]interface{}{"src1": map[string]string{"endpoint": "https://graph.example.test/getMemberObjects"}},
			}}
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "group_overage", tokens, err, 0, 0)
		assert.Empty(t, f.issuer.graphRequests(), "no Graph request")
	}},
	{"last_admin_demotion_refused", func(t *testing.T, f *oidcFixture) {
		admin := f.seedUser(t, "sole-admin@crm.example.test", models.RoleAdmin, true, "")
		f.link(t, admin.ID, "subject-admin")
		tokens, err := f.signIn(t, oidcValid("subject-admin", "sole-admin@idp.example.test"))
		f.assertRefused(t, "last_admin", tokens, err, 1, 1)
		assert.Equal(t, models.RoleAdmin, f.user(t, admin.ID).Role, "the role stays admin")
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""), "no audit row")
		assert.Equal(t, int64(0), f.refreshTokens(t, admin.ID), "no refresh token")
	}},
	{"deactivated_user", func(t *testing.T, f *oidcFixture) {
		user := f.seedUser(t, "inactive@crm.example.test", models.RoleSales, false, "")
		f.link(t, user.ID, "subject-inactive")
		tokens, err := f.signIn(t, oidcValid("subject-inactive", "inactive-new@idp.example.test"))
		f.assertRefused(t, "account_inactive", tokens, err, 1, 1)
		assert.Equal(t, int64(0), f.refreshTokens(t, user.ID))
	}},
	{"erased_user", func(t *testing.T, f *oidcFixture) {
		user := f.seedUser(t, "erased@crm.example.test", models.RoleSales, true, "")
		f.link(t, user.ID, "subject-erased")
		require.NoError(t, repository.NewUserRepository(f.db).Delete(user.ID))
		assert.Equal(t, int64(1), f.identities(t), "erasure keeps the identity row")
		tokens, err := f.signIn(t, oidcValid("subject-erased", "erased-new@idp.example.test"))
		f.assertRefused(t, "account_erased", tokens, err, 1, 1)
	}},
	{"subject_trailing_space", func(t *testing.T, f *oidcFixture) {
		linked := f.seedUser(t, "padded@crm.example.test", models.RoleSales, true, "")
		f.link(t, linked.ID, "abc")
		tokens, err := f.signIn(t, oidcValid("abc ", "unused-padded@idp.example.test"))
		f.assertRefused(t, "subject_invalid", tokens, err, 1, 1)
		assert.Equal(t, int64(0), f.refreshTokens(t, linked.ID), "no refresh token for the linked user")
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""), "no audit row")
	}},
	{"concurrent_first_sign_in_order", func(t *testing.T, f *oidcFixture) {
		const email = "race@idp.example.test"
		var once sync.Once
		f.svc.beforeJITInsert = func() {
			// A competing first sign-in commits the same address after this
			// one's lookup and email check, before its insert.
			once.Do(func() {
				competitor := &models.User{Email: email, Password: "x", FirstName: "R", LastName: "C", Role: models.RoleSales, IsActive: true}
				require.NoError(t, f.competitor.Create(competitor).Error)
			})
		}
		tokens, err := f.signIn(t, oidcValid("subject-race", email))
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrSSOSignInFailed)
		assert.Equal(t, "email_taken", oidcErrorCode(err))
		if f.db.Dialector.Name() == "mysql" {
			// MySQL/MariaDB: the insert itself hits the unique index and the
			// whole transaction rolls back.
			assert.ErrorIs(t, err, apperrors.ErrDuplicateEmail, "the losing insert met the unique index")
		} else {
			// SQLite (WAL): the write of a transaction whose snapshot predates
			// the competing commit is refused as busy, and the replay's email
			// check refuses the sign-in.
			assert.NotErrorIs(t, err, apperrors.ErrDuplicateEmail)
		}
		assert.Equal(t, int64(1), f.users(t), "exactly one user")
		assert.Equal(t, int64(1), f.count(t, "users", "email = ?", email))
		assert.Equal(t, int64(0), f.identities(t), "the losing sign-in left no identity")
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""))
	}},
	{"token_endpoint_error", func(t *testing.T, f *oidcFixture) {
		f.issuer.setTokenStatus(http.StatusInternalServerError)
		tokens, err := f.signIn(t, oidcValid("subject-tokenerr", "tokenerr@idp.example.test"))
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrSSOUnavailable)
		assert.Equal(t, int64(0), f.users(t))
	}},
	{"local_admin_password_login", func(t *testing.T, f *oidcFixture) {
		const password = "Break-Glass-0435!"
		f.seedUser(t, "breakglass@crm.example.test", models.RoleAdmin, true, password)
		cfg, err := f.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		require.True(t, cfg.Enabled)
		tokens, err := f.auth.LoginWithTokens("breakglass@crm.example.test", password)
		require.NoError(t, err)
		assert.NotEmpty(t, tokens.AccessToken)
		assert.NotEmpty(t, tokens.RefreshToken)
	}},
	{"issuer_redirect", func(t *testing.T, f *oidcFixture) {
		f.issuer.setDiscoveryRedirect(true)
		authURL, err := f.svc.Start(context.Background())
		assert.Empty(t, authURL)
		assert.ErrorIs(t, err, ErrSSOUnavailable)
		assert.Equal(t, int64(0), f.count(t, "oidc_login_attempts", ""))
	}},
	{"log_privacy", func(t *testing.T, f *oidcFixture) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		utils.Logger.SetOutput(&output)
		t.Cleanup(func() { utils.Logger = previous })

		const sub, email = "subject-private-0435", "private-0435@idp.example.test"
		code, state := f.issuer.authorize(t, f.start(t), oidcValid(sub, email))
		_, err := f.svc.Callback(context.Background(), code, state)
		require.NoError(t, err)
		goodToken := f.issuer.lastIDToken()

		const badSub, badEmail = "subject-private-bad-0435", "private-bad-0435@idp.example.test"
		bad := oidcValid(badSub, badEmail)
		bad.nonce = "wrong-nonce-0435"
		badCode, badState := f.issuer.authorize(t, f.start(t), bad)
		_, err = f.svc.Callback(context.Background(), badCode, badState)
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		badToken := f.issuer.lastIDToken()

		logs := output.String()
		require.Contains(t, logs, "Single sign-on refused", "the failure is logged")
		require.Contains(t, logs, "nonce_mismatch", "with its error code")
		for _, secret := range []string{code, state, goodToken, sub, email, badCode, badState, badToken, badSub, badEmail} {
			require.NotEmpty(t, secret)
			assert.NotContains(t, logs, secret)
		}
	}},
	{"graph_overage_match", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModePages, []string{oidcGraphGroupUnrelated}, []string{oidcGraphGroupSales})
		tokens, err := f.signIn(t, oidcOverage("subject-graph-match", "graph-match@idp.example.test"))
		require.NoError(t, err)
		require.NotNil(t, tokens.User)
		assert.Equal(t, models.RoleSales, f.user(t, tokens.User.ID).Role, "mapped from the group Graph returned")
		requests := f.issuer.graphRequests()
		require.Len(t, requests, 2, "two Graph pages")
		accessToken := f.issuer.lastAccessToken()
		require.NotEmpty(t, accessToken)
		for _, request := range requests {
			assert.Equal(t, http.MethodPost, request.method)
			assert.Equal(t, oidcGraphPath, request.path)
			assert.JSONEq(t, `{"securityEnabledOnly":true}`, request.body)
			assert.Equal(t, "application/json", request.contentType)
			assert.Equal(t, "Bearer "+accessToken, request.authorization, "the access token of this exchange")
		}
		assert.Empty(t, requests[0].query, "the first page is getMemberObjects itself")
		assert.Equal(t, "page=1", requests[1].query, "the second page follows the nextLink")
	}},
	{"graph_overage_no_match", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModePages, []string{oidcGraphGroupUnrelated})

		// A token refused by an earlier check never reaches Graph.
		tokens, err := f.signIn(t, oidcOverage("subject-graph-pad ", "graph-pad@idp.example.test"))
		f.assertRefused(t, "subject_invalid", tokens, err, 0, 0)
		assert.Empty(t, f.issuer.graphRequests(), "no Graph request before the subject check passes")

		tokens, err = f.signIn(t, oidcOverage("subject-graph-nomatch", "graph-nomatch@idp.example.test"))
		f.assertRefused(t, "no_mapping_match", tokens, err, 0, 0)
		assert.Len(t, f.issuer.graphRequests(), 1)
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""))
	}},
	{"graph_error", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModeStatus500)
		tokens, err := f.signIn(t, oidcOverage("subject-graph-500", "graph-500@idp.example.test"))
		f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
		assert.Len(t, f.issuer.graphRequests(), 1)
	}},
	{"graph_timeout", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModeSlow)
		started := time.Now()
		tokens, err := f.signIn(t, oidcOverage("subject-graph-slow", "graph-slow@idp.example.test"))
		f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
		assert.Less(t, time.Since(started), 2*time.Second, "the Graph budget ends the wait")
	}},
	{"graph_paging_bound", func(t *testing.T, f *oidcFixture) {
		f.useSSO(f.newSSO(config.SSOConfig{GraphMaxPages: 2}))
		f.entra(t)
		f.configure(t, f.graphMapping())
		cfg, err := f.svc.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, cfg.GraphMaxPages, "the sign-in reads the bound of 2")
		f.issuer.setGraph(graphModeEndless)
		tokens, err := f.signIn(t, oidcOverage("subject-graph-pages", "graph-pages@idp.example.test"))
		f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
		assert.Len(t, f.issuer.graphRequests(), 2, "never more requests than the bound")
	}},
	{"graph_foreign_host", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModeForeignNext)
		tokens, err := f.signIn(t, oidcOverage("subject-graph-foreign", "graph-foreign@idp.example.test"))
		f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
		assert.Len(t, f.issuer.graphRequests(), 1)
		assert.Equal(t, 0, f.issuer.otherRequests(), "the other host is never contacted")
	}},
	{"graph_redirect", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModeRedirect)
		tokens, err := f.signIn(t, oidcOverage("subject-graph-redirect", "graph-redirect@idp.example.test"))
		f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
		assert.Len(t, f.issuer.graphRequests(), 1)
		assert.Equal(t, 0, f.issuer.otherRequests(), "the redirect is not followed")
	}},
	{"graph_no_overage", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModePages, []string{oidcGraphGroupUnrelated})
		spec := oidcTokenSpec{sub: "subject-graph-inline", email: "graph-inline@idp.example.test", groups: []string{oidcGraphGroupSales}}
		tokens, err := f.signIn(t, spec)
		require.NoError(t, err)
		assert.Equal(t, models.RoleSales, f.user(t, tokens.User.ID).Role)
		assert.Empty(t, f.issuer.graphRequests(), "no Graph request without an overage")
	}},
	{"graph_roles_mapping", func(t *testing.T, f *oidcFixture) {
		f.entra(t)
		f.configure(t, []SSORoleMapping{{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]}})
		f.issuer.setGraph(graphModeStatus500)
		spec := oidcOverage("subject-graph-roles", "graph-roles@idp.example.test")
		spec.roles = []string{oidcAppRole}
		tokens, err := f.signIn(t, spec)
		require.NoError(t, err)
		assert.Equal(t, models.RoleSupport, f.user(t, tokens.User.ID).Role)
		assert.Empty(t, f.issuer.graphRequests(), "the overage is ignored without a groups mapping")
	}},
	{"graph_scope_requested", func(t *testing.T, f *oidcFixture) {
		scope := func() string {
			parsed, err := url.Parse(f.start(t))
			require.NoError(t, err)
			return parsed.Query().Get("scope")
		}
		const configured = "openid profile email"

		f.entra(t)
		f.configure(t, f.graphMapping())
		got := scope()
		assert.Equal(t, configured+" https://graph.microsoft.com/GroupMember.Read.All", got)
		assert.Equal(t, 1, strings.Count(got, "GroupMember.Read.All"), "added exactly once")
		cfg, err := f.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes, "the added scope is not stored")

		f.configure(t, []SSORoleMapping{{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]}})
		assert.Equal(t, configured, scope(), "an Entra issuer without a groups mapping")

		f.svc.entraIssuerHost = oidcEntraIssuerHost
		f.configure(t, f.graphMapping())
		assert.Equal(t, configured, scope(), "a groups mapping on another issuer")
	}},
	{"graph_token_privacy", func(t *testing.T, f *oidcFixture) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		utils.Logger.SetOutput(&output)
		t.Cleanup(func() { utils.Logger = previous })

		f.entra(t)
		f.configure(t, f.graphMapping())
		f.issuer.setGraph(graphModePages, []string{oidcGraphGroupSales})
		_, err := f.signIn(t, oidcOverage("subject-graph-private", "graph-private@idp.example.test"))
		require.NoError(t, err)
		goodAccess := f.issuer.lastAccessToken()

		f.issuer.setGraph(graphModeStatus500)
		tokens, err := f.signIn(t, oidcOverage("subject-graph-private-bad", "graph-private-bad@idp.example.test"))
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		require.Nil(t, tokens)
		badAccess := f.issuer.lastAccessToken()
		require.NotEqual(t, goodAccess, badAccess, "one access token per exchange")
		require.Len(t, f.issuer.graphRequests(), 2)

		logs := output.String()
		require.Contains(t, logs, "groups_unavailable", "the refusal is logged with its error code")
		tables, err := f.db.Migrator().GetTables()
		require.NoError(t, err)
		require.NotEmpty(t, tables)
		for _, secret := range []string{goodAccess, badAccess} {
			require.NotEmpty(t, secret)
			assert.NotContains(t, logs, secret, "no access token in the logs")
			for _, table := range tables {
				var rows []map[string]interface{}
				require.NoError(t, f.db.Table(table).Find(&rows).Error, table)
				for _, row := range rows {
					for column, value := range row {
						if raw, ok := value.([]byte); ok {
							value = string(raw)
						}
						assert.NotContains(t, fmt.Sprint(value), secret, "no access token in %s.%s", table, column)
					}
				}
			}
		}
	}},
}

func runOIDCSignIn(t *testing.T, f *oidcFixture) {
	for _, tc := range oidcSignInCases {
		t.Run(tc.name, func(t *testing.T) {
			f.reset(t)
			tc.run(t, f)
		})
	}
}

// openOIDCTestSQLite opens a SQLite file through the production connector
// (one pooled connection, WAL, busy_timeout) with the full schema, and a
// second pool on the same file for the competing writer.
func openOIDCTestSQLite(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	cfg := &config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(t.TempDir(), "oidc.db")}
	db, err := database.Open(cfg)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, models.MigrateDatabaseWith(db))
	competitor, err := database.Open(cfg)
	require.NoError(t, err)
	competitorDB, err := competitor.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = competitorDB.Close() })
	return db, competitor
}

func TestOIDCSignInSQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runOIDCSignIn(t, newOIDCFixture(t, db, competitor))
}

func TestOIDCSignInMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runOIDCSignIn(t, newOIDCFixture(t, db, db))
}

func TestOIDCHTTPClient(t *testing.T) {
	client := newOIDCHTTPClient(nil)
	assert.Greater(t, client.Timeout, time.Duration(0), "a non-zero timeout")
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "HTTP(S)_PROXY is ignored")

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	response, err := client.Get(redirecting.URL)
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.Equal(t, http.StatusFound, response.StatusCode, "the redirect is not followed")
}

func TestOIDCSubjectAndClaimRules(t *testing.T) {
	for _, sub := range []string{"abc", "00u1a2b3c4", strings.Repeat("a", 255)} {
		assert.True(t, validOIDCSubject(sub), sub)
	}
	for _, sub := range []string{"", "abc ", " abc", "a\tb", "abé", strings.Repeat("a", 256), "a\nb"} {
		assert.False(t, validOIDCSubject(sub), "%q", sub)
	}
	for raw, ok := range map[string]bool{``: true, `["a","b"]`: true, `[]`: true, `"a"`: false, `null`: false, `[1]`: false, `{}`: false} {
		_, got := oidcStringArray(json.RawMessage(raw))
		assert.Equal(t, ok, got, raw)
	}
	assert.Equal(t, "ab", truncateUTF8("abé", 3), "cut on a rune boundary")
	assert.Equal(t, "abé", truncateUTF8("abé", 4))
	mapping := []SSORoleMapping{{Claim: SSOClaimRoles, Value: "r", RoleID: 1}, {Claim: SSOClaimGroups, Value: "g", RoleID: 2}}
	id, ok := oidcMatchRole(mapping, []string{"g"}, []string{"r"})
	assert.True(t, ok)
	assert.Equal(t, uint(1), id, "the first entry in stored order decides")
	_, ok = oidcMatchRole(mapping, []string{"G"}, nil)
	assert.False(t, ok, "values compare case-sensitively")
}

func TestOIDCGraphDefaults(t *testing.T) {
	svc := NewOIDCService(nil, nil, nil, nil, nil, nil, nil, nil, nil, "").(*oidcService)
	assert.Equal(t, "login.microsoftonline.com", svc.entraIssuerHost)
	assert.Equal(t, "https://graph.microsoft.com", svc.graphBaseURL)
	assert.Equal(t, 5*time.Second, svc.graphTimeout)

	for issuer, want := range map[string]bool{
		"https://login.microsoftonline.com/tid/v2.0":     true,
		"https://LOGIN.microsoftonline.com/tid/v2.0":     true,
		"https://login.microsoftonline.com:443/tid/v2.0": true,
		"https://login.microsoftonline.com.evil.test/x":  false,
		"https://evil.test/login.microsoftonline.com":    false,
		"https://login.microsoftonline.us/tid/v2.0":      false,
		"https://login.chinacloudapi.cn/tid/v2.0":        false,
		"not a url %": false,
	} {
		assert.Equal(t, want, oidcIsEntraIssuer(issuer, svc.entraIssuerHost), issuer)
	}
	assert.False(t, oidcIsEntraIssuer("https://login.microsoftonline.com/tid/v2.0", ""), "no Entra host configured")

	for raw, want := range map[int]int{0: 10, -1: 10, 51: 10, 1: 1, 50: 50, 7: 7} {
		assert.Equal(t, want, effectiveGraphMaxPages(raw), "OIDC_GRAPH_MAX_PAGES=%d", raw)
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), config.SSOConfig{GraphMaxPages: raw}).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Equal(t, want, cfg.GraphMaxPages, "effective GraphMaxPages for %d", raw)
	}

	groups := []SSORoleMapping{{Claim: SSOClaimGroups, Value: "g", RoleID: 1}}
	roles := []SSORoleMapping{{Claim: SSOClaimRoles, Value: "r", RoleID: 1}}
	assert.True(t, mappingUsesGroups(append(roles, groups...)))
	assert.False(t, mappingUsesGroups(roles))
	assert.False(t, mappingUsesGroups(nil))
	entra := EffectiveSSOConfig{IssuerURL: "https://login.microsoftonline.com/tid/v2.0", Scopes: []string{"openid", oidcGraphScope}, RoleMapping: groups}
	assert.Equal(t, []string{"openid", oidcGraphScope}, svc.requestScopes(entra), "not added when already configured")

	// The nextLink rule: https on the Graph host and port only.
	base, err := url.Parse("https://graph.microsoft.com")
	require.NoError(t, err)
	for link, want := range map[string]bool{
		"https://graph.microsoft.com/v1.0/me/getMemberObjects?$skiptoken=x": true,
		"https://GRAPH.microsoft.com:443/v1.0/me/getMemberObjects":          true,
		"http://graph.microsoft.com/v1.0/me/getMemberObjects":               false,
		"https://graph.microsoft.com:8443/v1.0/me/getMemberObjects":         false,
		"https://graph.microsoft.com.evil.test/v1.0/me/getMemberObjects":    false,
		"https://user@graph.microsoft.com/v1.0/me/getMemberObjects":         false,
		"/v1.0/me/getMemberObjects":                                         false,
		"https://graph.windows.net/v1.0/me/getMemberObjects":                false,
	} {
		assert.Equal(t, want, graphSameOrigin(link, base), link)
	}
}

// TestOIDCResolveEntraGroups covers the Graph failure modes the sign-in
// subtests cannot tell apart: an http nextLink on the Graph host is refused
// before any request, a value that is not an array of strings and a page
// over 1 MiB fail, and an empty access token sends nothing.
func TestOIDCResolveEntraGroups(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
		answer   string
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		body := answer
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	svc := &oidcService{rootCAs: pool, graphBaseURL: server.URL, graphTimeout: 2 * time.Second}
	plainHost := "http://" + strings.TrimPrefix(server.URL, "https://")

	cases := []struct {
		name     string
		token    string
		answer   string
		want     []string
		requests int
	}{
		{"ids", "t", `{"value":["a","b"]}`, []string{"a", "b"}, 1},
		{"empty_list", "t", `{"value":[]}`, []string{}, 1},
		{"no_access_token", "", `{"value":["a"]}`, nil, 0},
		{"http_next_link_same_host", "t", `{"value":["a"],"@odata.nextLink":"` + plainHost + `/v1.0/me/getMemberObjects?page=1"}`, nil, 1},
		{"value_numbers", "t", `{"value":[1]}`, nil, 1},
		{"value_null_element", "t", `{"value":[null]}`, nil, 1},
		{"value_null", "t", `{"value":null}`, nil, 1},
		{"value_missing", "t", `{}`, nil, 1},
		{"value_object", "t", `{"value":{"a":"b"}}`, nil, 1},
		{"not_json", "t", `<html>`, nil, 1},
		{"over_one_mib", "t", `{"value":["` + strings.Repeat("a", 1<<20) + `"]}`, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			requests, answer = 0, tc.answer
			mu.Unlock()
			got, err := svc.resolveEntraGroups(context.Background(), tc.token, 10)
			mu.Lock()
			assert.Equal(t, tc.requests, requests, "requests sent")
			mu.Unlock()
			if tc.want == nil {
				assert.Error(t, err)
				assert.Nil(t, got)
				if err != nil {
					assert.NotContains(t, err.Error(), server.URL, "the error names no URL")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
