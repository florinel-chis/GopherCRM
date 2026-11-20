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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
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

	oidcUserInfoPath = "/userinfo"

	// Entra ID tenants and object ids (#505). The oid, the sub and every
	// email of the SCIM link subtests differ from each other.
	oidcTestTenant      = "6f0c3a1e-0505-4d2b-9a11-000000000505"
	oidcTestOtherTenant = "6f0c3a1e-0505-4d2b-9a11-000000000506"
	oidcTestSCIMOID     = "0b5e7d2c-0505-4f1a-8e3d-00000000a1d0"
	oidcTestOtherOID    = "0b5e7d2c-0505-4f1a-8e3d-00000000a1d1"
	oidcTestPairwiseSub = "pairwise-subject-0505-Xk2Lq9"
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
	// accessSubjects maps each issued access token to the sub of its grant.
	accessSubjects map[string]string
	userinfo       oidcFakeUserInfo
	// path is the path of the issuer under the server ("" or
	// "/<tenant>/v2.0", #505): discovery is served under it and iss carries
	// it.
	path string
}

// Modes of the fake userinfo endpoint (#451).
const (
	userinfoModeJSON      = "json"
	userinfoModeStatus500 = "status500"
	userinfoModeSlow      = "slow"
	userinfoModeRedirect  = "redirect"
	userinfoModeOversized = "oversized"
	userinfoModeJWT       = "jwt"
	userinfoModeRaw       = "raw"
)

// oidcFakeUserInfo is the fake userinfo endpoint: how discovery advertises it,
// what it answers and what it received. The default (JSON, sub echoed, no
// groups) behaves the way an issuer did before #451 read userinfo.
type oidcFakeUserInfo struct {
	mode string
	// omitEndpoint leaves userinfo_endpoint out of discovery; plainHTTP
	// advertises an http:// URL instead.
	omitEndpoint, plainHTTP bool
	// sub is the sub answered; nil echoes the sub of the bearer's grant.
	sub *string
	// omitSub leaves sub out of the answer.
	omitSub bool
	// groups is the raw groups member; nil leaves it out.
	groups json.RawMessage
	// raw is the whole body of userinfoModeRaw.
	raw      string
	requests []oidcUserInfoRequest
}

type oidcUserInfoRequest struct {
	method, authorization, accept string
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
	issuer := &oidcTestIssuer{t: t, key: key, otherKey: other, grants: map[string]oidcGrant{}, accessTokens: map[string]string{},
		accessSubjects: map[string]string{}, userinfo: oidcFakeUserInfo{mode: userinfoModeJSON}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/"+oidcTestTenant+"/v2.0/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/jwks", issuer.jwks)
	mux.HandleFunc("/token", issuer.token)
	mux.HandleFunc(oidcGraphPath, issuer.graphMemberObjects)
	mux.HandleFunc(oidcUserInfoPath, issuer.userInfo)
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

// userInfo is the fake userinfo endpoint. It answers 401 unless the bearer is
// an access token this issuer returned from a code exchange.
func (i *oidcTestIssuer) userInfo(w http.ResponseWriter, r *http.Request) {
	authorization := r.Header.Get("Authorization")
	i.mu.Lock()
	i.userinfo.requests = append(i.userinfo.requests, oidcUserInfoRequest{
		method: r.Method, authorization: authorization, accept: r.Header.Get("Accept"),
	})
	echoed, known := i.accessSubjects[strings.TrimPrefix(authorization, "Bearer ")]
	info := i.userinfo
	i.mu.Unlock()
	if !strings.HasPrefix(authorization, "Bearer ") || !known {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	answer := map[string]interface{}{"email": "userinfo-only@idp.example.test", "roles": []string{oidcAppRole}}
	if !info.omitSub {
		answer["sub"] = echoed
		if info.sub != nil {
			answer["sub"] = *info.sub
		}
	}
	if info.groups != nil {
		answer["groups"] = info.groups
	}
	switch info.mode {
	case userinfoModeStatus500:
		w.WriteHeader(http.StatusInternalServerError)
		return
	case userinfoModeSlow:
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	case userinfoModeRedirect:
		http.Redirect(w, r, i.other.URL+oidcUserInfoPath, http.StatusFound)
		return
	case userinfoModeOversized:
		answer["padding"] = strings.Repeat("x", 1<<20)
	case userinfoModeJWT:
		w.Header().Set("Content-Type", "application/jwt")
		_, _ = w.Write([]byte("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."))
		return
	case userinfoModeRaw:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(info.raw))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(answer)
}

// setUserInfo replaces the userinfo behaviour, keeping the recorded requests.
func (i *oidcTestIssuer) setUserInfo(info oidcFakeUserInfo) {
	i.mu.Lock()
	if info.mode == "" {
		info.mode = userinfoModeJSON
	}
	info.requests = i.userinfo.requests
	i.userinfo = info
	i.mu.Unlock()
}

func (i *oidcTestIssuer) resetUserInfo() {
	i.mu.Lock()
	i.userinfo = oidcFakeUserInfo{mode: userinfoModeJSON}
	i.mu.Unlock()
}

func (i *oidcTestIssuer) userInfoRequests() []oidcUserInfoRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]oidcUserInfoRequest(nil), i.userinfo.requests...)
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

// Issuer is the issuer identifier: the server URL plus the tenant path when
// one is set.
func (i *oidcTestIssuer) Issuer() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.server.URL + i.path
}

// setPath sets the issuer path ("" for the bare server URL).
func (i *oidcTestIssuer) setPath(path string) {
	i.mu.Lock()
	i.path = path
	i.mu.Unlock()
}

func (i *oidcTestIssuer) rootCAs() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(i.server.Certificate())
	pool.AddCert(i.other.Certificate())
	return pool
}

func (i *oidcTestIssuer) discovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	redirect := i.discoveryRedirect
	omitUserInfo, plainUserInfo := i.userinfo.omitEndpoint, i.userinfo.plainHTTP
	issuerPath := i.path
	i.mu.Unlock()
	if redirect {
		http.Redirect(w, r, i.server.URL+"/moved/.well-known/openid-configuration", http.StatusFound)
		return
	}
	document := map[string]interface{}{
		"issuer":                                i.server.URL + issuerPath,
		"authorization_endpoint":                i.server.URL + "/authorize",
		"token_endpoint":                        i.server.URL + "/token",
		"jwks_uri":                              i.server.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	}
	switch {
	case omitUserInfo:
	case plainUserInfo:
		document["userinfo_endpoint"] = "http://" + strings.TrimPrefix(i.server.URL, "https://") + oidcUserInfoPath
	default:
		document["userinfo_endpoint"] = i.server.URL + oidcUserInfoPath
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
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
	i.accessSubjects[accessToken] = grant.spec.sub
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
		"iss":   i.Issuer(),
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
	// audit is the sign-in audit writer Callback records through (#454).
	audit AuthAuditService

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
	f.audit = NewAuthAuditService(repository.NewAuthAuditRepository(db))
	f.svc = NewOIDCService(f.sso, f.auth, userRepo, roleRepo, repository.NewRoleAuditRepository(db), refreshRepo,
		repository.NewUserIdentityRepository(db), repository.NewOIDCLoginAttemptRepository(db), txManager,
		oidcTestAppBaseURL+"/", WithOIDCClock(f.now), WithOIDCRootCAs(f.issuer.rootCAs()), WithOIDCAudit(f.audit)).(*oidcService)

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
		IssuerURL:    f.issuer.Issuer(),
		ClientID:     oidcTestClientID,
		ClientSecret: &secret,
		RoleMapping:  mapping,
	}))
}

// reset empties the tables a subtest writes and restores the settings, the
// clock and the issuer.
func (f *oidcFixture) reset(t *testing.T) {
	t.Helper()
	for _, table := range []string{"refresh_tokens", "user_identities", "role_audit_log", "oidc_login_attempts", "auth_audit_log", "users"} {
		require.NoError(t, f.db.Exec("DELETE FROM "+table).Error, table)
	}
	require.Zero(t, f.count(t, "auth_audit_log", ""), "every subtest starts with an empty audit log")
	f.mu.Lock()
	f.clock = time.Now().UTC().Truncate(time.Millisecond)
	f.mu.Unlock()
	f.issuer.setTokenStatus(0)
	f.issuer.setDiscoveryRedirect(false)
	f.issuer.setPath("")
	f.svc.beforeJITInsert = nil
	f.svc.beforeSCIMLink = nil
	f.svc.attempts = repository.NewOIDCLoginAttemptRepository(f.db)
	f.svc.audit = f.audit
	f.svc.entraIssuerHost = oidcEntraIssuerHost
	f.svc.graphBaseURL = oidcGraphBaseURL
	f.svc.graphTimeout = oidcGraphTimeout
	f.svc.userinfoTimeout = oidcUserInfoTimeout
	f.issuer.resetGraph()
	f.issuer.resetUserInfo()
	f.useSSO(f.defaultSSO)
	f.configure(t, f.mapping())
	cfg, err := f.sso.SSOConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, f.issuer.URL(), cfg.IssuerURL, "the configured issuer carries no tenant path")
	require.Nil(t, f.svc.beforeSCIMLink, "the SCIM link seam is cleared")
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

// entraTenant is entra plus a tenant-specific issuer
// <server>/<oidcTestTenant>/v2.0: discovery is served there, the ID tokens
// carry it as iss and the settings name it (#505).
func (f *oidcFixture) entraTenant(t *testing.T) {
	t.Helper()
	f.entra(t)
	f.issuer.setPath("/" + oidcTestTenant + "/v2.0")
	f.configure(t, f.mapping())
	cfg, err := f.sso.SSOConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, oidcTestTenant, oidcEntraTenant(cfg.IssuerURL))
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

// oidcStartURL is the authorization URL of a Start, "" when it failed.
func oidcStartURL(authz *OIDCAuthorization, err error) (string, error) {
	if authz == nil {
		return "", err
	}
	return authz.URL, err
}

func (f *oidcFixture) start(t *testing.T) string {
	t.Helper()
	authURL, err := oidcStartURL(f.svc.Start(context.Background()))
	require.NoError(t, err)
	return authURL
}

// signIn runs Start, the issuer's authorization and Callback.
func (f *oidcFixture) signIn(t *testing.T, spec oidcTokenSpec) (*AuthTokens, error) {
	t.Helper()
	code, state := f.issuer.authorize(t, f.start(t), spec)
	return f.svc.Callback(context.Background(), code, state, state)
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
	f.linkSource(t, userID, subject, models.UserIdentitySourceOIDC)
}

// linkSource links (issuer, subject) to userID with the given source.
func (f *oidcFixture) linkSource(t *testing.T, userID uint, subject, source string) {
	t.Helper()
	require.NoError(t, f.db.Create(&models.UserIdentity{UserID: userID, Issuer: f.issuer.Issuer(), Subject: subject, Source: source}).Error)
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

	// The audit row of the refusal (#454): the newest row, with the attempt
	// the callback claimed (none for an unknown state).
	row := f.lastAudit(t)
	assert.Equal(t, AuthAuditMethodOIDC, row.Method)
	assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
	assert.Equal(t, code, row.Reason, "the audit row carries the refusal code")
	if code == "state_invalid" {
		assert.Nil(t, row.AttemptID, "no attempt was claimed")
	} else if assert.NotNil(t, row.AttemptID, "the claimed attempt") {
		var newest models.OIDCLoginAttempt
		require.NoError(t, f.db.Order("id DESC").First(&newest).Error)
		assert.Equal(t, newest.ID, *row.AttemptID, "the attempt of this subtest")
	}
}

// captureLogs sends the logger's output, at debug level, to a buffer for the
// rest of the subtest.
func (f *oidcFixture) captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	return &output
}

// assertBindingRefused checks a callback refused for its sign-in cookie
// (#528): the generic refusal with the code, the audit row naming the claimed
// attempt, the attempt spent, nothing written, and no log line carrying the
// state.
func (f *oidcFixture) assertBindingRefused(t *testing.T, code, state string, output *bytes.Buffer, tokens *AuthTokens, err error) {
	t.Helper()
	f.assertRefused(t, code, tokens, err, 0, 0)
	var attempt models.OIDCLoginAttempt
	require.NoError(t, f.db.Where("state_hash = ?", hashOpaqueToken(state, f.auth.sessionTokenSecret())).First(&attempt).Error)
	row := f.lastAudit(t)
	require.NotNil(t, row.AttemptID)
	assert.Equal(t, attempt.ID, *row.AttemptID, "the audit row names the claimed attempt")
	assert.NotNil(t, attempt.UsedAt, "the attempt is spent")
	assert.Equal(t, "", attempt.Nonce, "the nonce is cleared")
	assert.Equal(t, "", attempt.CodeVerifier, "the verifier is cleared")
	assert.Equal(t, int64(0), f.count(t, "refresh_tokens", ""), "no refresh token")
	assert.Equal(t, int64(0), f.users(t), "no user")
	assert.Equal(t, int64(0), f.identities(t), "no identity")
	logs := output.String()
	assert.Contains(t, logs, `"error_code":"`+code+`"`, "the refusal is logged by its code")
	assert.NotContains(t, logs, state, "no log line carries the state or the cookie")
}

// lastAudit is the newest auth_audit_log row.
func (f *oidcFixture) lastAudit(t *testing.T) models.AuthAuditLog {
	t.Helper()
	var row models.AuthAuditLog
	require.NoError(t, f.db.Order("id DESC").First(&row).Error, "an audit row was written")
	return row
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
		_, err := f.svc.Callback(context.Background(), code, state, state)
		require.NoError(t, err)
		users, identities := f.users(t), f.identities(t)
		tokens, err := f.svc.Callback(context.Background(), code, state, state)
		f.assertRefused(t, "state_invalid", tokens, err, users, identities)
	}},
	{"expired_state", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-expired", "expired@idp.example.test"))
		f.advance(10*time.Minute + time.Second)
		tokens, err := f.svc.Callback(context.Background(), code, state, state)
		f.assertRefused(t, "state_invalid", tokens, err, 0, 0)
	}},
	{"binding_missing", func(t *testing.T, f *oidcFixture) {
		output := f.captureLogs(t)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-binding-missing", "binding-missing@idp.example.test"))
		tokens, err := f.svc.Callback(context.Background(), code, state, "")
		f.assertBindingRefused(t, "binding_missing", state, output, tokens, err)
	}},
	{"binding_mismatch", func(t *testing.T, f *oidcFixture) {
		output := f.captureLogs(t)
		// The cookie of another sign-in: a Start in another browser.
		other, err := f.svc.Start(context.Background())
		require.NoError(t, err)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-binding-mismatch", "binding-mismatch@idp.example.test"))
		require.NotEqual(t, state, other.State)
		tokens, err := f.svc.Callback(context.Background(), code, state, other.State)
		f.assertBindingRefused(t, "binding_mismatch", state, output, tokens, err)
		assert.NotContains(t, output.String(), other.State, "no log line carries the other cookie")
	}},
	{"start_cookie_secure", func(t *testing.T, f *oidcFixture) {
		for base, want := range map[string]bool{
			"https://crm.example.test":  true,
			"HTTPS://crm.example.test/": true,
			"http://localhost:5173":     false,
		} {
			svc := NewOIDCService(f.sso, f.auth, repository.NewUserRepository(f.db), repository.NewRoleRepository(f.db),
				repository.NewRoleAuditRepository(f.db), repository.NewRefreshTokenRepository(f.db),
				repository.NewUserIdentityRepository(f.db), repository.NewOIDCLoginAttemptRepository(f.db),
				utils.NewTransactionManager(f.db), base, WithOIDCClock(f.now), WithOIDCRootCAs(f.issuer.rootCAs()))
			authz, err := svc.Start(context.Background())
			require.NoError(t, err, base)
			assert.Equal(t, want, authz.SecureCookie, base)
			parsed, err := url.Parse(authz.URL)
			require.NoError(t, err)
			assert.Equal(t, parsed.Query().Get("state"), authz.State, "the cookie value is the state of the URL")
		}
	}},
	{"issuer_changed_after_start", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-moved", "moved@idp.example.test"))
		other := newOIDCTestIssuer(t)
		enabled, secret := true, oidcTestClientSecret
		require.NoError(t, f.sso.Update(context.Background(), SSOSettingsUpdate{
			Enabled: &enabled, IssuerURL: other.URL(), ClientID: oidcTestClientID, ClientSecret: &secret, RoleMapping: f.mapping(),
		}))
		tokens, err := f.svc.Callback(context.Background(), code, state, state)
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
		assert.Empty(t, f.issuer.userInfoRequests(), "no userinfo request")
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
	{"scim_oid_link", func(t *testing.T, f *oidcFixture) {
		ctx := context.Background()
		f.entraTenant(t)
		scim := f.scimService()
		record, err := scim.Create(ctx, scimInput("scim.person@crm.example.test", oidcTestSCIMOID, oidcAppRole))
		require.NoError(t, err)
		id := record.User.ID
		rows := f.identityRows(t, id)
		require.Len(t, rows, 1)
		assert.Equal(t, f.issuer.Issuer(), rows[0].Issuer)
		assert.Equal(t, oidcTestSCIMOID, rows[0].Subject)
		assert.Equal(t, models.UserIdentitySourceSCIM, rows[0].Source)

		tokens, err := f.signIn(t, oidcSCIMLinkSpec("token-only@idp.example.test"))
		require.NoError(t, err)
		require.NotNil(t, tokens.User)
		assert.Equal(t, id, tokens.User.ID, "the SCIM-created account signs in")
		assert.NotEmpty(t, tokens.AccessToken)
		assert.NotEmpty(t, tokens.RefreshToken)
		rows = f.identityRows(t, id)
		require.Len(t, rows, 2, "the (iss, sub) link is added")
		assert.Equal(t, [3]string{f.issuer.Issuer(), oidcTestSCIMOID, models.UserIdentitySourceSCIM},
			[3]string{rows[0].Issuer, rows[0].Subject, rows[0].Source})
		assert.Equal(t, [3]string{f.issuer.Issuer(), oidcTestPairwiseSub, models.UserIdentitySourceOIDC},
			[3]string{rows[1].Issuer, rows[1].Subject, rows[1].Source})
		assert.Equal(t, int64(2), f.identities(t))
		assert.Equal(t, int64(1), f.users(t), "no account is provisioned")
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", "target_user_id <> ?", id))

		// AC-6: the SCIM externalId is still the oid.
		got, err := scim.Get(ctx, strconv.FormatUint(uint64(id), 10))
		require.NoError(t, err)
		assert.Equal(t, oidcTestSCIMOID, got.ExternalID)

		// The next sign-in matches (iss, sub) directly, without oid or tid.
		again, err := f.signIn(t, oidcValid(oidcTestPairwiseSub, "token-only@idp.example.test"))
		require.NoError(t, err)
		assert.Equal(t, id, again.User.ID)
		assert.Equal(t, int64(2), f.identities(t))
	}},
	{"scim_oid_tid_mismatch", func(t *testing.T, f *oidcFixture) {
		oidcSCIMFallThrough(t, f, models.UserIdentitySourceSCIM, func(spec *oidcTokenSpec) {
			spec.extra["tid"] = oidcTestOtherTenant
		})
	}},
	{"scim_oid_tid_case_differs", func(t *testing.T, f *oidcFixture) {
		require.NotEqual(t, oidcTestTenant, strings.ToUpper(oidcTestTenant))
		oidcSCIMFallThrough(t, f, models.UserIdentitySourceSCIM, func(spec *oidcTokenSpec) {
			spec.extra["tid"] = strings.ToUpper(oidcTestTenant)
		})
	}},
	{"scim_oid_missing", func(t *testing.T, f *oidcFixture) {
		oidcSCIMFallThrough(t, f, models.UserIdentitySourceSCIM, func(spec *oidcTokenSpec) {
			delete(spec.extra, "oid")
		})
	}},
	{"scim_oid_non_entra_issuer", func(t *testing.T, f *oidcFixture) {
		oidcSCIMFallThrough(t, f, models.UserIdentitySourceSCIM, func(*oidcTokenSpec) {
			f.svc.entraIssuerHost = oidcEntraIssuerHost
		})
	}},
	{"scim_oid_matches_oidc_row", func(t *testing.T, f *oidcFixture) {
		oidcSCIMFallThrough(t, f, models.UserIdentitySourceOIDC, nil)
	}},
	{"scim_oid_email_only", func(t *testing.T, f *oidcFixture) {
		f.entraTenant(t)
		owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, true, "")
		f.linkSource(t, owner.ID, oidcTestSCIMOID, models.UserIdentitySourceSCIM)
		spec := oidcSCIMLinkSpec("scim.person@crm.example.test")
		spec.extra["oid"] = oidcTestOtherOID
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "email_taken", tokens, err, 1, 1)
		assert.Len(t, f.identityRows(t, owner.ID), 1)
		assert.Equal(t, int64(0), f.refreshTokens(t, owner.ID))
	}},
	{"scim_oid_deactivated", func(t *testing.T, f *oidcFixture) {
		f.entraTenant(t)
		owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, false, "")
		f.linkSource(t, owner.ID, oidcTestSCIMOID, models.UserIdentitySourceSCIM)
		tokens, err := f.signIn(t, oidcSCIMLinkSpec("new.person@idp.example.test"))
		f.assertRefused(t, "account_inactive", tokens, err, 1, 1)
		assert.Len(t, f.identityRows(t, owner.ID), 1)
		assert.Equal(t, int64(0), f.count(t, "user_identities", "subject = ?", oidcTestPairwiseSub))
		assert.Equal(t, int64(0), f.count(t, "refresh_tokens", ""))
	}},
	{"scim_oid_erased", func(t *testing.T, f *oidcFixture) {
		f.entraTenant(t)
		owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, true, "")
		f.linkSource(t, owner.ID, oidcTestSCIMOID, models.UserIdentitySourceSCIM)
		require.NoError(t, repository.NewUserRepository(f.db).Delete(owner.ID))
		tokens, err := f.signIn(t, oidcSCIMLinkSpec("new.person@idp.example.test"))
		f.assertRefused(t, "account_erased", tokens, err, 1, 1)
		assert.Len(t, f.identityRows(t, owner.ID), 1)
		assert.Equal(t, int64(0), f.count(t, "user_identities", "subject = ?", oidcTestPairwiseSub))
		assert.Equal(t, int64(0), f.count(t, "refresh_tokens", ""))
	}},
	{"scim_oid_concurrent_link_after_commit", func(t *testing.T, f *oidcFixture) {
		f.entraTenant(t)
		owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, true, "")
		f.linkSource(t, owner.ID, oidcTestSCIMOID, models.UserIdentitySourceSCIM)
		require.NoError(t, f.competitor.Create(&models.UserIdentity{UserID: owner.ID, Issuer: f.issuer.Issuer(),
			Subject: oidcTestPairwiseSub, Source: models.UserIdentitySourceOIDC}).Error)
		tokens, err := f.signIn(t, oidcSCIMLinkSpec("new.person@idp.example.test"))
		require.NoError(t, err)
		assert.Equal(t, owner.ID, tokens.User.ID, "the committed link matches directly")
		f.assertOneSCIMLink(t, owner.ID)
	}},
	{"scim_oid_concurrent_link_before_insert", func(t *testing.T, f *oidcFixture) {
		f.entraTenant(t)
		owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, true, "")
		f.linkSource(t, owner.ID, oidcTestSCIMOID, models.UserIdentitySourceSCIM)
		var once sync.Once
		f.svc.beforeSCIMLink = func() {
			// A competing first sign-in of the same person commits the
			// (iss, sub) link after this one's reads, before its insert.
			once.Do(func() {
				require.NoError(t, f.competitor.Create(&models.UserIdentity{UserID: owner.ID, Issuer: f.issuer.Issuer(),
					Subject: oidcTestPairwiseSub, Source: models.UserIdentitySourceOIDC}).Error)
			})
		}
		tokens, err := f.signIn(t, oidcSCIMLinkSpec("new.person@idp.example.test"))
		if f.db.Dialector.Name() == "mysql" {
			// MySQL/MariaDB: the link insert meets the unique index and the
			// whole transaction rolls back.
			assert.Nil(t, tokens)
			assert.ErrorIs(t, err, ErrSSOSignInFailed)
			assert.Equal(t, "identity_taken", oidcErrorCode(err))
			assert.ErrorIs(t, err, repository.ErrUserIdentityTaken)
			assert.Equal(t, int64(0), f.refreshTokens(t, owner.ID))
		} else {
			// SQLite (WAL): the first write after the competing commit is
			// refused as busy and replayed; the replay matches (iss, sub).
			require.NoError(t, err)
			assert.NotErrorIs(t, err, repository.ErrUserIdentityTaken)
			assert.Equal(t, owner.ID, tokens.User.ID)
			assert.Equal(t, int64(1), f.refreshTokens(t, owner.ID))
		}
		f.assertOneSCIMLink(t, owner.ID)
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
		authURL, err := oidcStartURL(f.svc.Start(context.Background()))
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
		_, err := f.svc.Callback(context.Background(), code, state, state)
		require.NoError(t, err)
		goodToken := f.issuer.lastIDToken()

		const badSub, badEmail = "subject-private-bad-0435", "private-bad-0435@idp.example.test"
		bad := oidcValid(badSub, badEmail)
		bad.nonce = "wrong-nonce-0435"
		badCode, badState := f.issuer.authorize(t, f.start(t), bad)
		_, err = f.svc.Callback(context.Background(), badCode, badState, badState)
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
		assert.Empty(t, f.issuer.userInfoRequests(), "an Entra overage goes to Graph only")
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
	{"userinfo_groups_mapped", func(t *testing.T, f *oidcFixture) {
		sub := "subject-userinfo-groups"
		f.issuer.setUserInfo(oidcFakeUserInfo{sub: &sub, groups: json.RawMessage(`["unrelated","` + oidcGroupSales + `"]`)})
		tokens, err := f.signIn(t, oidcTokenSpec{sub: sub, email: "userinfo-groups@idp.example.test"})
		require.NoError(t, err)
		require.NotNil(t, tokens.User)
		assert.Equal(t, models.RoleSales, f.user(t, tokens.User.ID).Role, "mapped from the userinfo groups")
		assert.Equal(t, "userinfo-groups@idp.example.test", f.user(t, tokens.User.ID).Email, "the email comes from the ID token")
		requests := f.issuer.userInfoRequests()
		require.Len(t, requests, 1, "exactly one userinfo request")
		accessToken := f.issuer.lastAccessToken()
		require.NotEmpty(t, accessToken)
		assert.Equal(t, http.MethodGet, requests[0].method)
		assert.Equal(t, "Bearer "+accessToken, requests[0].authorization, "the access token of this exchange")
		assert.Equal(t, "application/json", requests[0].accept)
	}},
	{"userinfo_groups_absent", func(t *testing.T, f *oidcFixture) {
		sub := "subject-userinfo-absent"
		f.issuer.setUserInfo(oidcFakeUserInfo{sub: &sub})
		tokens, err := f.signIn(t, oidcTokenSpec{sub: sub, email: "userinfo-absent@idp.example.test", roles: []string{oidcAppRole}})
		require.NoError(t, err)
		require.NotNil(t, tokens.User)
		assert.Equal(t, models.RoleSupport, f.user(t, tokens.User.ID).Role, "the roles entry decides")
		assert.Len(t, f.issuer.userInfoRequests(), 1)
	}},
	{"userinfo_not_called", func(t *testing.T, f *oidcFixture) {
		// A userinfo answer that would refuse, so a stray request shows.
		f.issuer.setUserInfo(oidcFakeUserInfo{mode: userinfoModeStatus500})
		tokens, err := f.signIn(t, oidcValid("subject-ui-token-groups", "ui-token-groups@idp.example.test"))
		require.NoError(t, err)
		require.NotNil(t, tokens)

		tokens, err = f.signIn(t, oidcTokenSpec{sub: "subject-ui-empty-groups", email: "ui-empty-groups@idp.example.test",
			groups: []string{}, roles: []string{oidcAppRole}})
		require.NoError(t, err, "a present [] groups claim counts as present")
		assert.Equal(t, models.RoleSupport, f.user(t, tokens.User.ID).Role)

		f.configure(t, []SSORoleMapping{{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]}})
		tokens, err = f.signIn(t, oidcTokenSpec{sub: "subject-ui-roles-only", email: "ui-roles-only@idp.example.test", roles: []string{oidcAppRole}})
		require.NoError(t, err, "a mapping without a groups entry")
		assert.Equal(t, models.RoleSupport, f.user(t, tokens.User.ID).Role)

		assert.Empty(t, f.issuer.userInfoRequests(), "no userinfo request")
	}},
	{"userinfo_subject_mismatch", func(t *testing.T, f *oidcFixture) {
		other := "subject-other"
		f.issuer.setUserInfo(oidcFakeUserInfo{sub: &other, groups: json.RawMessage(`["` + oidcGroupSales + `"]`)})
		tokens, err := f.signIn(t, oidcTokenSpec{sub: "subject-ui-mismatch", email: "ui-mismatch@idp.example.test"})
		f.assertRefused(t, "userinfo_subject_mismatch", tokens, err, 0, 0)

		f.issuer.setUserInfo(oidcFakeUserInfo{omitSub: true, groups: json.RawMessage(`["` + oidcGroupSales + `"]`)})
		tokens, err = f.signIn(t, oidcTokenSpec{sub: "subject-ui-nosub", email: "ui-nosub@idp.example.test"})
		f.assertRefused(t, "userinfo_subject_mismatch", tokens, err, 0, 0)
		assert.Len(t, f.issuer.userInfoRequests(), 2)
		assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""), "no audit row")
	}},
	{"userinfo_unavailable", func(t *testing.T, f *oidcFixture) {
		sales := json.RawMessage(`["` + oidcGroupSales + `"]`)
		for _, tc := range []struct {
			name     string
			info     oidcFakeUserInfo
			requests int
		}{
			{"no_endpoint", oidcFakeUserInfo{omitEndpoint: true, groups: sales}, 0},
			{"http_endpoint", oidcFakeUserInfo{plainHTTP: true, groups: sales}, 0},
			{"status_500", oidcFakeUserInfo{mode: userinfoModeStatus500, groups: sales}, 1},
			{"slow", oidcFakeUserInfo{mode: userinfoModeSlow, groups: sales}, 1},
			{"redirect", oidcFakeUserInfo{mode: userinfoModeRedirect, groups: sales}, 1},
			{"oversized", oidcFakeUserInfo{mode: userinfoModeOversized, groups: sales}, 1},
			{"jwt", oidcFakeUserInfo{mode: userinfoModeJWT, groups: sales}, 1},
			{"groups_string", oidcFakeUserInfo{groups: json.RawMessage(`"` + oidcGroupSales + `"`)}, 1},
			{"groups_null", oidcFakeUserInfo{groups: json.RawMessage(`null`)}, 1},
			{"groups_numbers", oidcFakeUserInfo{groups: json.RawMessage(`[1]`)}, 1},
			{"body_array", oidcFakeUserInfo{mode: userinfoModeRaw, raw: `[]`}, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f.reset(t)
				f.svc.userinfoTimeout = 300 * time.Millisecond
				f.issuer.setUserInfo(tc.info)
				sub := "subject-ui-" + strings.ReplaceAll(tc.name, "_", "-")
				started := time.Now()
				tokens, err := f.signIn(t, oidcTokenSpec{sub: sub, email: sub + "@idp.example.test"})
				f.assertRefused(t, "groups_unavailable", tokens, err, 0, 0)
				assert.Len(t, f.issuer.userInfoRequests(), tc.requests)
				assert.Zero(t, f.issuer.otherRequests(), "a redirect is not followed")
				if tc.name == "slow" {
					assert.Less(t, time.Since(started), 2*time.Second, "the timeout ends the wait")
				}
			})
		}
		// The empty access token: the oauth2 exchange itself refuses a token
		// response without access_token, so the guard is driven directly.
		f.reset(t)
		ctx, provider, err := f.svc.discover(context.Background(), f.issuer.URL(), 0)
		require.NoError(t, err)
		_, err = f.svc.resolveUserInfoGroups(ctx, provider, "", "subject-ui-no-access")
		assert.ErrorIs(t, err, errUserInfoNoAccessToken)
		assert.Empty(t, f.issuer.userInfoRequests(), "no request without an access token")
	}},
	{"userinfo_token_privacy", func(t *testing.T, f *oidcFixture) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		utils.Logger.SetOutput(&output)
		t.Cleanup(func() { utils.Logger = previous })

		f.issuer.setUserInfo(oidcFakeUserInfo{groups: json.RawMessage(`["` + oidcGroupSales + `"]`)})
		_, err := f.signIn(t, oidcTokenSpec{sub: "subject-ui-private", email: "ui-private@idp.example.test"})
		require.NoError(t, err)
		goodAccess := f.issuer.lastAccessToken()

		f.issuer.setUserInfo(oidcFakeUserInfo{mode: userinfoModeStatus500})
		tokens, err := f.signIn(t, oidcTokenSpec{sub: "subject-ui-private-bad", email: "ui-private-bad@idp.example.test"})
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		require.Nil(t, tokens)
		badAccess := f.issuer.lastAccessToken()
		require.NotEqual(t, goodAccess, badAccess, "one access token per exchange")
		require.Len(t, f.issuer.userInfoRequests(), 2)

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
	{"log_correlation", func(t *testing.T, f *oidcFixture) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		utils.Logger.SetOutput(&output)
		t.Cleanup(func() { utils.Logger = previous })

		// segment runs fn and returns the oidc_sign_in lines it logged.
		segment := func(fn func()) []map[string]interface{} {
			t.Helper()
			from := output.Len()
			fn()
			var lines []map[string]interface{}
			for _, raw := range strings.Split(strings.TrimSpace(output.String()[from:]), "\n") {
				var line map[string]interface{}
				if raw == "" || json.Unmarshal([]byte(raw), &line) != nil || line["component"] != "oidc_sign_in" {
					continue
				}
				lines = append(lines, line)
			}
			return lines
		}
		assertLine := func(t *testing.T, line map[string]interface{}, msg, requestID string, attemptID uint) {
			t.Helper()
			assert.Equal(t, msg, line["msg"])
			assert.Equal(t, requestID, line["request_id"], "request_id of %q", msg)
			require.Contains(t, line, "attempt_id", "attempt_id of %q", msg)
			assert.Equal(t, float64(attemptID), line["attempt_id"], "attempt_id of %q", msg)
		}
		attemptOf := func(t *testing.T, state string) models.OIDCLoginAttempt {
			t.Helper()
			var attempt models.OIDCLoginAttempt
			require.NoError(t, f.db.Where("state_hash = ?", hashOpaqueToken(state, f.auth.sessionTokenSecret())).First(&attempt).Error)
			return attempt
		}

		okCtx := utils.WithRequestID(context.Background(), "req-453-ok")
		const sub, email = "subject-correlated-0453", "correlated-0453@idp.example.test"
		var authURL string
		started := segment(func() {
			var err error
			authURL, err = oidcStartURL(f.svc.Start(okCtx))
			require.NoError(t, err)
		})
		code, state := f.issuer.authorize(t, authURL, oidcValid(sub, email))
		okAttempt := attemptOf(t, state)
		require.Len(t, started, 1)
		assertLine(t, started[0], "Single sign-on started", "req-453-ok", okAttempt.ID)
		succeeded := segment(func() {
			_, err := f.svc.Callback(okCtx, code, state, state)
			require.NoError(t, err)
		})
		require.Len(t, succeeded, 1)
		assertLine(t, succeeded[0], "Signed in through single sign-on", "req-453-ok", okAttempt.ID)
		assert.Contains(t, succeeded[0], "user_id")
		goodToken := f.issuer.lastIDToken()

		badCtx := utils.WithRequestID(context.Background(), "req-453-bad")
		bad := oidcValid("subject-correlated-bad-0453", "correlated-bad-0453@idp.example.test")
		bad.nonce = "wrong-nonce-0453"
		var badURL string
		badStarted := segment(func() {
			var err error
			badURL, err = oidcStartURL(f.svc.Start(badCtx))
			require.NoError(t, err)
		})
		badCode, badState := f.issuer.authorize(t, badURL, bad)
		badAttempt := attemptOf(t, badState)
		require.NotEqual(t, okAttempt.ID, badAttempt.ID)
		require.Len(t, badStarted, 1)
		assertLine(t, badStarted[0], "Single sign-on started", "req-453-bad", badAttempt.ID)
		refused := segment(func() {
			_, err := f.svc.Callback(badCtx, badCode, badState, badState)
			require.ErrorIs(t, err, ErrSSOSignInFailed)
		})
		require.Len(t, refused, 1)
		assertLine(t, refused[0], "Single sign-on refused", "req-453-bad", badAttempt.ID)
		assert.Equal(t, "nonce_mismatch", refused[0]["error_code"])
		badToken := f.issuer.lastIDToken()

		unknownCtx := utils.WithRequestID(context.Background(), "req-453-unknown")
		unknown := segment(func() {
			_, err := f.svc.Callback(unknownCtx, "code-unknown-0453", "state-unknown-0453", "state-unknown-0453")
			require.ErrorIs(t, err, ErrSSOSignInFailed)
		})
		require.Len(t, unknown, 1)
		assertLine(t, unknown[0], "Single sign-on refused", "req-453-unknown", 0)
		assert.Equal(t, "state_invalid", unknown[0]["error_code"])

		logs := output.String()
		for _, secret := range []string{code, state, okAttempt.Nonce, okAttempt.CodeVerifier, goodToken, sub, email,
			badCode, badState, badAttempt.Nonce, badAttempt.CodeVerifier, badToken, bad.sub, bad.email, "state-unknown-0453", "code-unknown-0453"} {
			require.NotEmpty(t, secret)
			assert.NotContains(t, logs, secret)
		}
	}},
	{"attempt_cleared_when_used", func(t *testing.T, f *oidcFixture) {
		check := func(t *testing.T, spec oidcTokenSpec, wantErr bool) {
			t.Helper()
			code, state := f.issuer.authorize(t, f.start(t), spec)
			hash := hashOpaqueToken(state, f.auth.sessionTokenSecret())
			var before models.OIDCLoginAttempt
			require.NoError(t, f.db.Where("state_hash = ?", hash).First(&before).Error)
			assert.NotEmpty(t, before.Nonce, "the nonce is stored until the callback")
			assert.NotEmpty(t, before.CodeVerifier, "the verifier is stored until the callback")
			_, err := f.svc.Callback(context.Background(), code, state, state)
			if wantErr {
				assert.Equal(t, "nonce_mismatch", oidcErrorCode(err))
			} else {
				require.NoError(t, err)
			}
			var after models.OIDCLoginAttempt
			require.NoError(t, f.db.Where("state_hash = ?", hash).First(&after).Error)
			assert.Equal(t, before.ID, after.ID)
			assert.NotNil(t, after.UsedAt, "the row is spent")
			assert.Equal(t, "", after.Nonce, "the nonce is cleared")
			assert.Equal(t, "", after.CodeVerifier, "the verifier is cleared")
		}
		check(t, oidcValid("subject-cleared", "cleared@idp.example.test"), false)
		bad := oidcValid("subject-cleared-bad", "cleared-bad@idp.example.test")
		bad.nonce = "not-the-nonce-0453"
		check(t, bad, true)
	}},
	{"attempt_cleared_when_expired", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-expired-clear", "expired-clear@idp.example.test"))
		hash := hashOpaqueToken(state, f.auth.sessionTokenSecret())
		f.advance(10*time.Minute + time.Second)
		tokens, err := f.svc.Callback(context.Background(), code, state, state)
		f.assertRefused(t, "state_invalid", tokens, err, 0, 0)
		var after models.OIDCLoginAttempt
		require.NoError(t, f.db.Where("state_hash = ?", hash).First(&after).Error)
		assert.Nil(t, after.UsedAt, "an expired row is not spent")
		assert.Equal(t, "", after.Nonce, "the nonce is cleared")
		assert.Equal(t, "", after.CodeVerifier, "the verifier is cleared")
	}},
	{"attempt_claim_lost_after_read", func(t *testing.T, f *oidcFixture) {
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-lost", "lost@idp.example.test"))
		hash := hashOpaqueToken(state, f.auth.sessionTokenSecret())
		competitor := repository.NewOIDCLoginAttemptRepository(f.competitor)
		var competing *models.OIDCLoginAttempt
		var competingErr error
		var once sync.Once
		f.svc.attempts = repository.NewOIDCLoginAttemptRepository(f.db, repository.WithOIDCClaimAfterRead(func() {
			// A second callback with the same state claims the row between
			// this claim's read and its update.
			once.Do(func() { competing, competingErr = competitor.Claim(hash, f.now().UTC()) })
		}))
		tokens, err := f.svc.Callback(context.Background(), code, state, state)
		f.assertRefused(t, "state_invalid", tokens, err, 0, 0)
		require.NoError(t, competingErr, "the competing claim succeeded")
		require.NotNil(t, competing)
		assert.NotNil(t, competing.UsedAt)
		assert.NotEmpty(t, competing.CodeVerifier, "the winner still holds the verifier in memory")
		var after models.OIDCLoginAttempt
		require.NoError(t, f.db.Where("state_hash = ?", hash).First(&after).Error)
		assert.Equal(t, competing.ID, after.ID)
		assert.NotNil(t, after.UsedAt)
		assert.Equal(t, "", after.Nonce)
		assert.Equal(t, "", after.CodeVerifier)
	}},
	{"attempts_pruned_after_a_day", func(t *testing.T, f *oidcFixture) {
		now := f.now()
		cutoff := now.Add(-OIDCLoginAttemptRetention)
		insert := func(t *testing.T, created time.Time, tag string) uint {
			t.Helper()
			attempt := &models.OIDCLoginAttempt{
				CreatedAt:    created,
				StateHash:    hashOpaqueToken("prune-0453-"+tag, f.auth.sessionTokenSecret()),
				Nonce:        "n-" + tag,
				CodeVerifier: "v-" + tag,
				Issuer:       f.issuer.URL(),
				ExpiresAt:    created.Add(10 * time.Minute),
			}
			require.NoError(t, f.db.Create(attempt).Error)
			return attempt.ID
		}
		oldID := insert(t, now.Add(-24*time.Hour-time.Minute), "old")
		recentID := insert(t, now.Add(-23*time.Hour), "recent")
		n := f.count(t, "oidc_login_attempts", "created_at < ?", cutoff)
		require.GreaterOrEqual(t, n, int64(1))
		pruned, err := PruneOIDCLoginAttempts(context.Background(), repository.NewOIDCLoginAttemptRepository(f.db), now)
		require.NoError(t, err)
		assert.Equal(t, n, pruned)
		assert.Zero(t, f.count(t, "oidc_login_attempts", "created_at < ?", cutoff), "no row older than a day remains")
		assert.Zero(t, f.count(t, "oidc_login_attempts", "id = ?", oldID), "the day-old row is gone")
		assert.Equal(t, int64(1), f.count(t, "oidc_login_attempts", "id = ?", recentID), "the 23-hour-old row stays")
	}},
	{"audit_success_row", func(t *testing.T, f *oidcFixture) {
		tokens, err := f.signIn(t, oidcValid("subject-audit-ok-0454", "audit-ok-0454@idp.example.test"))
		require.NoError(t, err)
		require.Equal(t, int64(1), f.count(t, "auth_audit_log", ""), "one row per callback")
		row := f.lastAudit(t)
		assert.Equal(t, AuthAuditMethodOIDC, row.Method)
		assert.Equal(t, AuthAuditOutcomeSuccess, row.Outcome)
		assert.Equal(t, "", row.Reason)
		require.NotNil(t, row.UserID)
		assert.Equal(t, tokens.User.ID, *row.UserID)
		require.NotNil(t, row.AttemptID)
		require.NotNil(t, row.ClaimGroups)
		assert.JSONEq(t, `["unrelated","`+oidcGroupSales+`"]`, *row.ClaimGroups)
		require.NotNil(t, row.ClaimRoles)
		assert.JSONEq(t, `[]`, *row.ClaimRoles, "an absent roles claim was read as empty")
		assert.Nil(t, row.SubjectRef, "no subject reference on success")
	}},
	{"audit_refusal_row", func(t *testing.T, f *oidcFixture) {
		usersBefore, identitiesBefore := f.users(t), f.identities(t)
		const sub = "subject-audit-refused-0454"
		spec := oidcTokenSpec{sub: sub, email: "audit-refused-0454@idp.example.test",
			groups: []string{"nobody-0454", "also-nobody-0454"}, roles: []string{"no-role-0454"}}
		tokens, err := f.signIn(t, spec)
		f.assertRefused(t, "no_mapping_match", tokens, err, usersBefore, identitiesBefore)
		row := f.lastAudit(t)
		assert.Nil(t, row.UserID, "no account was named")
		require.NotNil(t, row.SubjectRef, "a verified identity was refused")
		assert.Regexp(t, `^[0-9a-f]{64}$`, *row.SubjectRef)
		assert.Equal(t, AuthAuditSubjectRef(oidcTestAPIKeySecret, f.issuer.URL(), sub), *row.SubjectRef)
		require.NotNil(t, row.ClaimGroups)
		assert.JSONEq(t, `["nobody-0454","also-nobody-0454"]`, *row.ClaimGroups)
		require.NotNil(t, row.ClaimRoles)
		assert.JSONEq(t, `["no-role-0454"]`, *row.ClaimRoles)
	}},
	{"audit_linked_user_refusal", func(t *testing.T, f *oidcFixture) {
		seeded := f.seedUser(t, "audit-inactive-0454@crm.example.test", models.RoleSales, false, "")
		const sub = "subject-audit-inactive-0454"
		f.link(t, seeded.ID, sub)
		usersBefore, identitiesBefore := f.users(t), f.identities(t)
		tokens, err := f.signIn(t, oidcValid(sub, "audit-inactive-0454@idp.example.test"))
		f.assertRefused(t, "account_inactive", tokens, err, usersBefore, identitiesBefore)
		row := f.lastAudit(t)
		require.NotNil(t, row.UserID, "the linked account is named")
		assert.Equal(t, seeded.ID, *row.UserID)
		require.NotNil(t, row.SubjectRef)
	}},
	{"audit_id_on_refusal_log", func(t *testing.T, f *oidcFixture) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		utils.Logger.SetOutput(&output)
		t.Cleanup(func() { utils.Logger = previous })

		bad := oidcValid("subject-audit-log-0454", "audit-log-0454@idp.example.test")
		bad.nonce = "wrong-nonce-0454"
		_, err := f.signIn(t, bad)
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		row := f.lastAudit(t)
		assert.Nil(t, row.SubjectRef, "refused before the nonce matched: no subject reference")
		var refused map[string]interface{}
		for _, raw := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			var line map[string]interface{}
			if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == "Single sign-on refused" {
				refused = line
			}
		}
		require.NotNil(t, refused, "the refusal is logged")
		assert.Equal(t, "nonce_mismatch", refused["error_code"])
		require.Contains(t, refused, "audit_id")
		assert.Equal(t, float64(row.ID), refused["audit_id"], "the log line names the audit row")
		assert.NotZero(t, row.ID)
	}},
	{"names_split_from_name", func(t *testing.T, f *oidcFixture) {
		spec := oidcValid("subject-name-split", "name-split@idp.example.test")
		spec.extra = map[string]interface{}{"name": "José María García"}
		tokens, err := f.signIn(t, spec)
		require.NoError(t, err)
		created := f.user(t, tokens.User.ID)
		assert.Equal(t, "José", created.FirstName)
		assert.Equal(t, "María García", created.LastName)
	}},
	{"names_absent", func(t *testing.T, f *oidcFixture) {
		tokens, err := f.signIn(t, oidcValid("subject-name-absent", "name-absent@idp.example.test"))
		require.NoError(t, err)
		created := f.user(t, tokens.User.ID)
		assert.Equal(t, "", created.FirstName)
		assert.Equal(t, "", created.LastName)
	}},
}

func runOIDCSignIn(t *testing.T, f *oidcFixture) {
	for _, tc := range oidcSignInCases {
		t.Run(tc.name, func(t *testing.T) {
			f.reset(t)
			tc.run(t, f)
		})
	}
	if f.db.Dialector.Name() == "mysql" {
		// #513: a handover X -> Y that commits while a sign-in of Y's linked
		// identity, whose claims map to sales, waits for the admin set. The
		// sign-in's admin set comes back empty; the locking read of Y must
		// still count Y as the admin before the role is re-applied. Needs a
		// second connection and InnoDB lock waits, so it runs only under the
		// MySQL root and leaves oidcSignInCases (and the SQLite root) as is.
		t.Run("last_admin_race/after_handover", func(t *testing.T) {
			f.reset(t)
			x := f.seedUser(t, "x@crm.example.test", models.RoleAdmin, true, "")
			y := f.seedUser(t, "y@crm.example.test", models.RoleSales, true, "")
			f.link(t, y.ID, "subject-y")
			code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-y", "y@idp.example.test"))

			handover := holdAdminHandover(t, f.db, f.roleIDs, x.ID, y.ID)
			var tokens *AuthTokens
			done := make(chan error, 1)
			go func() {
				var err error
				tokens, err = f.svc.Callback(context.Background(), code, state, state)
				done <- err
			}()
			err := awaitLockWaitThenCommit(t, f.db, handover, done)

			f.assertRefused(t, "last_admin", tokens, err, 2, 1)
			assert.Equal(t, int64(1), countActiveAdmins(t, f.db), "exactly one active admin remains")
			assert.Equal(t, models.RoleAdmin, f.user(t, y.ID).Role, "Y stays admin")
			assert.Equal(t, int64(0), f.count(t, "role_audit_log", ""), "no audit row")
			assert.Equal(t, int64(0), f.refreshTokens(t, y.ID), "no refresh token")
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

// --------------------------------------------------- SCIM link (#505) ---

// oidcSCIMLinkSpec is the ID token of the SCIM link subtests: a sub no row
// holds, the oid SCIM stored as externalId and the configured tenant.
func oidcSCIMLinkSpec(email string) oidcTokenSpec {
	spec := oidcValid(oidcTestPairwiseSub, email)
	spec.extra = map[string]interface{}{"oid": oidcTestSCIMOID, "tid": oidcTestTenant}
	return spec
}

// scimService is the SCIM user service over the fixture's settings, so SCIM
// create writes its identity under the configured issuer.
func (f *oidcFixture) scimService() SCIMUserService {
	return NewSCIMUserService(repository.NewSCIMUserRepository(f.db), f.sso,
		WithSCIMUserWrites(repository.NewUserRepository(f.db), repository.NewRoleRepository(f.db), repository.NewRoleAuditRepository(f.db),
			repository.NewUserIdentityRepository(f.db), repository.NewRefreshTokenRepository(f.db), utils.NewTransactionManager(f.db), f.audit))
}

// identityRows are the user_identities rows of userID, id ascending.
func (f *oidcFixture) identityRows(t *testing.T, userID uint) []models.UserIdentity {
	t.Helper()
	var rows []models.UserIdentity
	require.NoError(t, f.db.Where("user_id = ?", userID).Order("id").Find(&rows).Error)
	return rows
}

// assertOneSCIMLink checks the end state of a concurrent link: one (iss, sub)
// row, owned by the SCIM user, one user, no audit row for another user.
func (f *oidcFixture) assertOneSCIMLink(t *testing.T, ownerID uint) {
	t.Helper()
	var links []models.UserIdentity
	require.NoError(t, f.db.Where("issuer = ? AND subject = ?", f.issuer.Issuer(), oidcTestPairwiseSub).Find(&links).Error)
	if assert.Len(t, links, 1, "exactly one (iss, sub) row") {
		assert.Equal(t, ownerID, links[0].UserID)
	}
	assert.Equal(t, int64(1), f.users(t), "exactly one user")
	assert.Equal(t, int64(0), f.count(t, "role_audit_log", "target_user_id <> ?", ownerID))
}

// oidcSCIMFallThrough is the scim_oid_link setup (an Entra ID tenant issuer
// and an account whose (iss, oid) row has the given source) with one input
// changed: the sign-in provisions a new account, which gets only its own
// (iss, sub, oidc) row, and the seeded account is untouched.
func oidcSCIMFallThrough(t *testing.T, f *oidcFixture, source string, change func(spec *oidcTokenSpec)) {
	t.Helper()
	f.entraTenant(t)
	owner := f.seedUser(t, "scim.person@crm.example.test", models.RoleSales, true, "")
	f.linkSource(t, owner.ID, oidcTestSCIMOID, source)
	spec := oidcSCIMLinkSpec("new.person@idp.example.test")
	if change != nil {
		change(&spec)
	}
	tokens, err := f.signIn(t, spec)
	require.NoError(t, err)
	require.NotNil(t, tokens.User)
	assert.NotEqual(t, owner.ID, tokens.User.ID, "a new account is provisioned")
	assert.Equal(t, int64(2), f.users(t))
	rows := f.identityRows(t, tokens.User.ID)
	if assert.Len(t, rows, 1) {
		assert.Equal(t, [3]string{f.issuer.Issuer(), oidcTestPairwiseSub, models.UserIdentitySourceOIDC},
			[3]string{rows[0].Issuer, rows[0].Subject, rows[0].Source})
	}
	owned := f.identityRows(t, owner.ID)
	if assert.Len(t, owned, 1, "the seeded account keeps its one identity") {
		assert.Equal(t, oidcTestSCIMOID, owned[0].Subject)
		assert.Equal(t, source, owned[0].Source)
	}
	assert.Equal(t, int64(0), f.refreshTokens(t, owner.ID))
}

func TestOIDCEntraTenant(t *testing.T) {
	for _, tc := range []struct{ issuer, want string }{
		{"https://login.microsoftonline.com/" + oidcTestTenant + "/v2.0", oidcTestTenant},
		{"https://login.microsoftonline.com/" + oidcTestTenant, oidcTestTenant},
		{"https://login.microsoftonline.com/" + oidcTestTenant + "/", oidcTestTenant},
		{"https://login.microsoftonline.com/common/v2.0", "common"},
		{"https://login.microsoftonline.com", ""},
		{"https://login.microsoftonline.com/", ""},
		{"https://login.microsoftonline.com//v2.0", ""},
		{"https://login.microsoftonline.com/%zz/v2.0", ""},
		{"", ""},
	} {
		assert.Equal(t, tc.want, oidcEntraTenant(tc.issuer), tc.issuer)
	}
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

// TestOIDCClaimExpiredClearFails: when blanking the nonce and verifier of an
// expired attempt fails, the callback still answers the generic state_invalid
// refusal (401), never a 500 or a 503 that would reveal the row exists.
func TestOIDCClaimExpiredClearFails(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	errForced := errors.New("forced update failure")
	// The handle is this test's own, so the callback needs no restoring. It
	// fails updates of oidc_login_attempts only, so Start's insert and the
	// settings writes go through.
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_oidc_attempt_update", func(tx *gorm.DB) {
		if tx.Statement.Table == "oidc_login_attempts" {
			_ = tx.AddError(errForced)
		}
	}))

	code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-clear-fails", "clear-fails@idp.example.test"))
	f.advance(10*time.Minute + time.Second)
	tokens, err := f.svc.Callback(context.Background(), code, state, state)
	f.assertRefused(t, "state_invalid", tokens, err, 0, 0)
	assert.NotErrorIs(t, err, ErrSSOUnavailable)
	assert.NotErrorIs(t, err, errForced, "the database error does not reach the caller")

	var after models.OIDCLoginAttempt
	require.NoError(t, db.Where("state_hash = ?", hashOpaqueToken(state, f.auth.sessionTokenSecret())).First(&after).Error)
	assert.NotEmpty(t, after.Nonce, "the clearing update really failed")
	assert.Nil(t, after.UsedAt)
}

func TestOIDCProfileNames(t *testing.T) {
	long := strings.Repeat("é", 60) // 120 bytes
	cases := []struct {
		name                string
		given, family, full string
		first, last         string
	}{
		{"given_only", "Ada", "", "", "Ada", ""},
		{"family_only", "", "Lovelace", "", "", "Lovelace"},
		{"both", "Ada", "Lovelace", "", "Ada", "Lovelace"},
		{"name_one_word", "", "", "Cher", "Cher", ""},
		{"name_extra_whitespace", "", "", "  Ada \t Augusta\n  King  ", "Ada", "Augusta King"},
		{"multibyte_truncation", "", "", long + " " + long, strings.Repeat("é", 50), strings.Repeat("é", 50)},
		{"given_wins_over_name", "Ada", "", "Someone Else", "Ada", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last := oidcProfileNames(tc.given, tc.family, tc.full)
			assert.Equal(t, tc.first, first)
			assert.Equal(t, tc.last, last)
			assert.LessOrEqual(t, len(first), oidcNameMaxBytes)
			assert.LessOrEqual(t, len(last), oidcNameMaxBytes)
		})
	}
}
