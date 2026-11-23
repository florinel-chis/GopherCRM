package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

// Key ids of the issuer's signing keys: KeyID before a rotation,
// RotatedKeyID after it.
const (
	KeyID        = "k1"
	RotatedKeyID = "k2"
)

// Paths the issuer serves, relative to its server URL. Discovery is served
// at DiscoveryPath and, when an issuer path is set, under that path too.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	AuthorizePath = "/authorize"
	TokenPath     = "/token"
	JWKSPath      = "/jwks"
	UserInfoPath  = "/userinfo"
	// EndSessionPath is the end_session_endpoint discovery advertises by
	// default.
	EndSessionPath = "/logout"
)

// Default client credentials, replaced with WithClient.
const (
	DefaultClientID     = "test-client"
	DefaultClientSecret = "test-client-secret"
)

// ClientAssertionType is the client_assertion_type of a private_key_jwt
// token request (RFC 7523).
const ClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// Claims is what the ID token of one sign-in carries, beyond iss, exp and
// iat, which the issuer always sets.
type Claims struct {
	Subject string
	// Email, Groups and Roles are left out of the token when zero.
	Email  string
	Groups interface{}
	Roles  interface{}
	// SID is the provider session id of the sign-in, left out when empty.
	SID string
	// Extra claims are set last, so they may override the standard ones.
	Extra map[string]interface{}
	// Nonce overrides the nonce of the authorization request when non-empty.
	Nonce string
	// Audience overrides the client id when non-empty.
	Audience string
	// UnpublishedKey signs with a key the JWKS never publishes, under the
	// kid of the current key.
	UnpublishedKey bool
	// SigningKey, when set, signs the token instead of the issuer's key,
	// under KeyIDOverride (a key a JWKS hook publishes, for instance).
	SigningKey    *rsa.PrivateKey
	KeyIDOverride string
}

// TokenRequest is what one token request carried.
type TokenRequest struct {
	Form          url.Values
	Authorization string
}

// UserInfoMode selects what the userinfo endpoint answers.
type UserInfoMode string

// Modes of the userinfo endpoint.
const (
	// UserInfoJSON answers 200 with a JSON object.
	UserInfoJSON UserInfoMode = "json"
	// UserInfoStatus500 answers 500 with no body.
	UserInfoStatus500 UserInfoMode = "status500"
	// UserInfoSlow answers the JSON object after two seconds, or when the
	// request is cancelled.
	UserInfoSlow UserInfoMode = "slow"
	// UserInfoRedirect answers 302 to UserInfo.RedirectURL.
	UserInfoRedirect UserInfoMode = "redirect"
	// UserInfoOversized pads the JSON object past one MiB.
	UserInfoOversized UserInfoMode = "oversized"
	// UserInfoJWT answers an unsigned application/jwt body.
	UserInfoJWT UserInfoMode = "jwt"
	// UserInfoRaw answers UserInfo.Raw as the JSON body.
	UserInfoRaw UserInfoMode = "raw"
)

// UserInfo is the behaviour of the userinfo endpoint and how discovery
// advertises it. The zero value answers JSON with the sub of the bearer's
// grant and no groups.
type UserInfo struct {
	Mode UserInfoMode
	// OmitEndpoint leaves userinfo_endpoint out of discovery; PlainHTTP
	// advertises an http:// URL instead of the https:// one.
	OmitEndpoint, PlainHTTP bool
	// Sub is the sub answered; nil echoes the sub of the bearer's grant.
	Sub *string
	// OmitSub leaves sub out of the answer.
	OmitSub bool
	// Groups is the raw groups member; nil leaves it out.
	Groups json.RawMessage
	// Raw is the whole body of UserInfoRaw.
	Raw string
	// RedirectURL is where UserInfoRedirect points.
	RedirectURL string
}

// UserInfoRequest is what one userinfo request carried.
type UserInfoRequest struct {
	Method, Authorization, Accept string
}

// Option configures an Issuer at construction.
type Option func(*Issuer)

// WithClient sets the client id and secret the token endpoint accepts.
func WithClient(id, secret string) Option {
	return func(i *Issuer) { i.clientID, i.clientSecret = id, secret }
}

// WithUserInfoClaims sets members every JSON userinfo answer carries next to
// sub and groups. They survive SetUserInfo and ResetUserInfo.
func WithUserInfoClaims(claims map[string]interface{}) Option {
	return func(i *Issuer) {
		i.userinfoClaims = map[string]interface{}{}
		for key, value := range claims {
			i.userinfoClaims[key] = value
		}
	}
}

type grant struct {
	challenge, redirect, nonce string
	claims                     Claims
}

// Issuer is an in-process OpenID Connect issuer on a TLS server. Every method
// is safe for concurrent use.
type Issuer struct {
	t      testing.TB
	server *httptest.Server
	mux    *http.ServeMux

	clientID, clientSecret string
	userinfoClaims         map[string]interface{}

	key, unpublishedKey, rotatedKey *rsa.PrivateKey

	mu                sync.Mutex
	grants            map[string]grant
	codeSeq           int
	tokenStatus       int
	discoveryRedirect bool
	path              string
	rotated           bool
	jwksHook          func(url.Values) []jose.JSONWebKey
	assertionCert     *x509.Certificate
	userinfo          UserInfo
	userinfoRequests  []UserInfoRequest
	tokenRequests     []TokenRequest
	accessSubjects    map[string]string
	lastAccess        string
	issuedIDTokens    []string
	discoveryHits     int
	jwksHits          int
	jwksQueries       []string
	discoveryPaths    []string
	discoveryDelay    time.Duration
	endSession        string
	omitEndSession    bool
	endSessionQueries []url.Values
}

// New starts the issuer. The server is closed when the test ends.
func New(t testing.TB, opts ...Option) *Issuer {
	t.Helper()
	i := &Issuer{
		t: t, clientID: DefaultClientID, clientSecret: DefaultClientSecret,
		key: newKey(t), unpublishedKey: newKey(t), rotatedKey: newKey(t),
		grants: map[string]grant{}, accessSubjects: map[string]string{},
	}
	for _, opt := range opts {
		opt(i)
	}
	i.mux = http.NewServeMux()
	i.mux.HandleFunc(JWKSPath, i.jwks)
	i.mux.HandleFunc(TokenPath, i.token)
	i.mux.HandleFunc(UserInfoPath, i.userInfo)
	i.mux.HandleFunc(EndSessionPath, i.endSessionHandler)
	i.server = httptest.NewTLSServer(http.HandlerFunc(i.route))
	t.Cleanup(i.server.Close)
	return i
}

func newKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("oidctest: generate key: %v", err)
	}
	return key
}

// route serves discovery at the root and under the current issuer path, and
// everything else through the mux.
func (i *Issuer) route(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	path := i.path
	i.mu.Unlock()
	if r.URL.Path == DiscoveryPath || (path != "" && r.URL.Path == path+DiscoveryPath) {
		i.discovery(w, r)
		return
	}
	i.mux.ServeHTTP(w, r)
}

// Handle mounts an extra handler on the issuer's server, for endpoints a
// host adds next to the protocol ones.
func (i *Issuer) Handle(pattern string, handler http.Handler) {
	i.mux.Handle(pattern, handler)
}

// URL is the server URL, without the issuer path.
func (i *Issuer) URL() string { return i.server.URL }

// Issuer is the issuer identifier: the server URL plus the issuer path.
func (i *Issuer) Issuer() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.server.URL + i.path
}

// SetPath sets the issuer path ("" for the bare server URL). Discovery is
// then also served under it and iss carries it.
func (i *Issuer) SetPath(path string) {
	i.mu.Lock()
	i.path = path
	i.mu.Unlock()
}

// TokenEndpoint is the URL of the token endpoint, the audience of a client
// assertion.
func (i *Issuer) TokenEndpoint() string { return i.server.URL + TokenPath }

// ClientID and ClientSecret are the credentials the token endpoint accepts.
func (i *Issuer) ClientID() string { return i.clientID }

// ClientSecret is the client secret the token endpoint accepts.
func (i *Issuer) ClientSecret() string { return i.clientSecret }

// Certificate is the server's TLS certificate.
func (i *Issuer) Certificate() *x509.Certificate { return i.server.Certificate() }

// RootCAs is a pool that trusts the server's certificate.
func (i *Issuer) RootCAs() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(i.server.Certificate())
	return pool
}

// HTTPClient is an HTTP client that trusts the server.
func (i *Issuer) HTTPClient() *http.Client { return i.server.Client() }

// AccessSubject returns the sub of the grant an access token was issued for.
func (i *Issuer) AccessSubject(accessToken string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	sub, ok := i.accessSubjects[accessToken]
	return sub, ok
}

// SetJWKSHook sets a function that returns keys to publish next to the
// issuer's own, given the query of the JWKS request; nil removes it.
func (i *Issuer) SetJWKSHook(hook func(query url.Values) []jose.JSONWebKey) {
	i.mu.Lock()
	i.jwksHook = hook
	i.mu.Unlock()
}

// SetTokenStatus makes every token request answer status with
// error=server_error; 0 restores normal answers.
func (i *Issuer) SetTokenStatus(status int) {
	i.mu.Lock()
	i.tokenStatus = status
	i.mu.Unlock()
}

// SetDiscoveryRedirect makes discovery answer 302 to a path that serves
// nothing.
func (i *Issuer) SetDiscoveryRedirect(on bool) {
	i.mu.Lock()
	i.discoveryRedirect = on
	i.mu.Unlock()
}

// Rotate makes the issuer sign with a new key under RotatedKeyID and publish
// only that key; Rotate(false) undoes it.
func (i *Issuer) Rotate(on bool) {
	i.mu.Lock()
	i.rotated = on
	i.mu.Unlock()
}

// SetAssertionCertificate registers the client certificate a private_key_jwt
// assertion must verify against; nil refuses every assertion.
func (i *Issuer) SetAssertionCertificate(cert *x509.Certificate) {
	i.mu.Lock()
	i.assertionCert = cert
	i.mu.Unlock()
}

// SetEndSessionEndpoint replaces the end_session_endpoint discovery
// advertises; "" restores the default, the server URL plus EndSessionPath.
func (i *Issuer) SetEndSessionEndpoint(raw string) {
	i.mu.Lock()
	i.endSession = raw
	i.mu.Unlock()
}

// OmitEndSessionEndpoint leaves end_session_endpoint out of discovery while
// on is true.
func (i *Issuer) OmitEndSessionEndpoint(on bool) {
	i.mu.Lock()
	i.omitEndSession = on
	i.mu.Unlock()
}

// EndSessionQueries is the query of every request to EndSessionPath, in
// order.
func (i *Issuer) EndSessionQueries() []url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]url.Values, 0, len(i.endSessionQueries))
	for _, query := range i.endSessionQueries {
		out = append(out, cloneValues(query))
	}
	return out
}

// LastEndSessionQuery is the query of the latest request to EndSessionPath,
// nil before the first.
func (i *Issuer) LastEndSessionQuery() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.endSessionQueries) == 0 {
		return nil
	}
	return cloneValues(i.endSessionQueries[len(i.endSessionQueries)-1])
}

// SetDiscoveryDelay makes discovery answer only after d, or when the request
// is cancelled; 0 answers at once.
func (i *Issuer) SetDiscoveryDelay(d time.Duration) {
	i.mu.Lock()
	i.discoveryDelay = d
	i.mu.Unlock()
}

// IDToken signs an ID token with claims outside any sign-in, for a test that
// presents one back to the client (an id_token_hint, say). It carries no
// nonce unless claims.Nonce sets one, and is recorded as LastIDToken.
func (i *Issuer) IDToken(t testing.TB, claims Claims) string {
	t.Helper()
	token, err := i.sign(grant{claims: claims})
	if err != nil {
		t.Fatalf("oidctest: sign the ID token: %v", err)
	}
	return token
}

// TakeTokenRequests returns and forgets the recorded token requests.
func (i *Issuer) TakeTokenRequests() []TokenRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := i.tokenRequests
	i.tokenRequests = nil
	return out
}

// SetUserInfo replaces the userinfo behaviour, keeping the recorded
// requests. An empty Mode is UserInfoJSON.
func (i *Issuer) SetUserInfo(info UserInfo) {
	if info.Mode == "" {
		info.Mode = UserInfoJSON
	}
	i.mu.Lock()
	i.userinfo = info
	i.mu.Unlock()
}

// ResetUserInfo restores the default userinfo behaviour and forgets the
// recorded requests.
func (i *Issuer) ResetUserInfo() {
	i.mu.Lock()
	i.userinfo = UserInfo{Mode: UserInfoJSON}
	i.userinfoRequests = nil
	i.mu.Unlock()
}

// UserInfoRequests returns the recorded userinfo requests.
func (i *Issuer) UserInfoRequests() []UserInfoRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]UserInfoRequest(nil), i.userinfoRequests...)
}

// Fetches is how many discovery (a redirected one included) and JWKS
// requests the issuer received.
func (i *Issuer) Fetches() (discovery, jwks int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.discoveryHits, i.jwksHits
}

// LastDiscoveryPath is the path of the latest discovery request.
func (i *Issuer) LastDiscoveryPath() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.discoveryPaths) == 0 {
		return ""
	}
	return i.discoveryPaths[len(i.discoveryPaths)-1]
}

// JWKSQueries is the raw query of every JWKS request.
func (i *Issuer) JWKSQueries() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.jwksQueries...)
}

// ResetFetches zeroes the discovery and JWKS counters and undoes the
// rotation.
func (i *Issuer) ResetFetches() {
	i.mu.Lock()
	i.discoveryHits, i.jwksHits, i.jwksQueries, i.discoveryPaths, i.rotated = 0, 0, nil, nil, false
	i.mu.Unlock()
}

// LastIDToken is the latest ID token issued, "" before the first.
func (i *Issuer) LastIDToken() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.issuedIDTokens) == 0 {
		return ""
	}
	return i.issuedIDTokens[len(i.issuedIDTokens)-1]
}

// LastAccessToken is the access token of the latest code exchange.
func (i *Issuer) LastAccessToken() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastAccess
}

// Authorize plays the browser and the issuer's login page: it checks that
// the authorization URL asks for a code with an S256 challenge, records the
// challenge, the redirect URI and the nonce, and returns a code whose
// exchange issues an ID token with claims, and the state to send back.
func (i *Issuer) Authorize(t testing.TB, authURL string, claims Claims) (code, state string) {
	t.Helper()
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("oidctest: authorization URL: %v", err)
	}
	q := parsed.Query()
	if got := q.Get("response_type"); got != "code" {
		t.Fatalf("oidctest: response_type is %q, want \"code\"", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("oidctest: code_challenge_method is %q, want \"S256\"", got)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.codeSeq++
	code = fmt.Sprintf("code-%d-%s", i.codeSeq, strings.Repeat("x", 8))
	i.grants[code] = grant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), nonce: q.Get("nonce"), claims: claims}
	return code, q.Get("state")
}

func (i *Issuer) discovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	i.discoveryHits++
	i.discoveryPaths = append(i.discoveryPaths, r.URL.Path)
	redirect := i.discoveryRedirect
	omitUserInfo, plainUserInfo := i.userinfo.OmitEndpoint, i.userinfo.PlainHTTP
	issuerPath := i.path
	delay, endSession, omitEndSession := i.discoveryDelay, i.endSession, i.omitEndSession
	i.mu.Unlock()
	if delay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
	}
	if redirect {
		http.Redirect(w, r, i.server.URL+"/moved"+DiscoveryPath, http.StatusFound)
		return
	}
	document := map[string]interface{}{
		"issuer":                                i.server.URL + issuerPath,
		"authorization_endpoint":                i.server.URL + AuthorizePath,
		"token_endpoint":                        i.server.URL + TokenPath,
		"jwks_uri":                              i.server.URL + JWKSPath,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	}
	switch {
	case omitUserInfo:
	case plainUserInfo:
		document["userinfo_endpoint"] = "http://" + strings.TrimPrefix(i.server.URL, "https://") + UserInfoPath
	default:
		document["userinfo_endpoint"] = i.server.URL + UserInfoPath
	}
	switch {
	case omitEndSession:
	case endSession != "":
		document["end_session_endpoint"] = endSession
	default:
		document["end_session_endpoint"] = i.server.URL + EndSessionPath
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
}

func (i *Issuer) jwks(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	i.jwksHits++
	i.jwksQueries = append(i.jwksQueries, r.URL.RawQuery)
	key, kid := i.key, KeyID
	if i.rotated {
		key, kid = i.rotatedKey, RotatedKeyID
	}
	hook := i.jwksHook
	i.mu.Unlock()
	keys := []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"}}
	if hook != nil {
		keys = append(keys, hook(r.URL.Query())...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
}

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	status := i.tokenStatus
	i.mu.Unlock()
	if status != 0 {
		tokenError(w, status, "server_error")
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	i.mu.Lock()
	i.tokenRequests = append(i.tokenRequests, TokenRequest{Form: cloneValues(r.PostForm), Authorization: r.Header.Get("Authorization")})
	assertionCert := i.assertionCert
	i.mu.Unlock()
	if _, posted := r.PostForm["client_assertion"]; posted {
		// private_key_jwt: the assertion is the only client credential.
		if !i.clientAssertionValid(r, assertionCert) {
			tokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
	} else {
		clientID, clientSecret, ok := r.BasicAuth()
		if ok {
			clientID, _ = url.QueryUnescape(clientID)
			clientSecret, _ = url.QueryUnescape(clientSecret)
		} else {
			clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if clientID != i.clientID || clientSecret != i.clientSecret {
			tokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	i.mu.Lock()
	grant, found := i.grants[r.PostForm.Get("code")]
	i.mu.Unlock()
	if !found {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// The redirect URI of the exchange must be the authorization request's.
	if r.PostForm.Get("redirect_uri") != grant.redirect {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// PKCE: S256 of the verifier must be the challenge of the request.
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if r.PostForm.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != grant.challenge {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	idToken, err := i.sign(grant)
	if err != nil {
		i.t.Errorf("oidctest: sign the ID token: %v", err)
		tokenError(w, http.StatusInternalServerError, "server_error")
		return
	}
	accessToken := "idp-access-token-" + r.PostForm.Get("code")
	i.mu.Lock()
	i.accessSubjects[accessToken] = grant.claims.Subject
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

// clientAssertionValid checks a private_key_jwt token request: no
// Authorization header and no client_secret, client_id and the assertion type
// set, and an assertion that verifies as PS256 against the registered
// certificate, names it by x5t, and carries aud = the token endpoint and
// iss = sub = the client id.
func (i *Issuer) clientAssertionValid(r *http.Request, cert *x509.Certificate) bool {
	if cert == nil || r.Header.Get("Authorization") != "" {
		return false
	}
	if _, posted := r.PostForm["client_secret"]; posted {
		return false
	}
	if r.PostForm.Get("client_id") != i.clientID || r.PostForm.Get("client_assertion_type") != ClientAssertionType {
		return false
	}
	thumbprint := sha1.Sum(cert.Raw)
	token, err := jwt.Parse(r.PostForm.Get("client_assertion"), func(token *jwt.Token) (interface{}, error) {
		if token.Header["x5t"] != base64.RawURLEncoding.EncodeToString(thumbprint[:]) {
			return nil, errors.New("unknown certificate")
		}
		return cert.PublicKey, nil
	}, jwt.WithValidMethods([]string{"PS256"}), jwt.WithAudience(i.TokenEndpoint()),
		jwt.WithIssuer(i.clientID), jwt.WithSubject(i.clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	return err == nil && token.Valid
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func (i *Issuer) sign(grant grant) (string, error) {
	spec := grant.claims
	now := time.Now()
	claims := map[string]interface{}{
		"iss": i.Issuer(),
		"sub": spec.Subject,
		"aud": i.clientID,
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
	}
	if grant.nonce != "" {
		claims["nonce"] = grant.nonce
	}
	if spec.SID != "" {
		claims["sid"] = spec.SID
	}
	if spec.Audience != "" {
		claims["aud"] = spec.Audience
	}
	if spec.Nonce != "" {
		claims["nonce"] = spec.Nonce
	}
	if spec.Email != "" {
		claims["email"] = spec.Email
	}
	if spec.Groups != nil {
		claims["groups"] = spec.Groups
	}
	if spec.Roles != nil {
		claims["roles"] = spec.Roles
	}
	for k, v := range spec.Extra {
		claims[k] = v
	}
	i.mu.Lock()
	key, kid := i.key, KeyID
	if i.rotated {
		key, kid = i.rotatedKey, RotatedKeyID
	}
	i.mu.Unlock()
	switch {
	case spec.SigningKey != nil:
		key, kid = spec.SigningKey, spec.KeyIDOverride
	case spec.UnpublishedKey:
		key = i.unpublishedKey
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	compact, err := signed.CompactSerialize()
	if err != nil {
		return "", err
	}
	i.mu.Lock()
	i.issuedIDTokens = append(i.issuedIDTokens, compact)
	i.mu.Unlock()
	return compact, nil
}

// endSessionHandler records the query of a sign-out request and answers 200.
func (i *Issuer) endSessionHandler(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	i.endSessionQueries = append(i.endSessionQueries, cloneValues(r.URL.Query()))
	i.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("signed out\n"))
}

// userInfo answers 401 unless the bearer is an access token this issuer
// returned from a code exchange.
func (i *Issuer) userInfo(w http.ResponseWriter, r *http.Request) {
	authorization := r.Header.Get("Authorization")
	i.mu.Lock()
	i.userinfoRequests = append(i.userinfoRequests, UserInfoRequest{
		Method: r.Method, Authorization: authorization, Accept: r.Header.Get("Accept"),
	})
	echoed, known := i.accessSubjects[strings.TrimPrefix(authorization, "Bearer ")]
	info := i.userinfo
	i.mu.Unlock()
	if !strings.HasPrefix(authorization, "Bearer ") || !known {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	answer := map[string]interface{}{}
	for key, value := range i.userinfoClaims {
		answer[key] = value
	}
	if !info.OmitSub {
		answer["sub"] = echoed
		if info.Sub != nil {
			answer["sub"] = *info.Sub
		}
	}
	if info.Groups != nil {
		answer["groups"] = info.Groups
	}
	switch info.Mode {
	case UserInfoStatus500:
		w.WriteHeader(http.StatusInternalServerError)
		return
	case UserInfoSlow:
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	case UserInfoRedirect:
		http.Redirect(w, r, info.RedirectURL, http.StatusFound)
		return
	case UserInfoOversized:
		answer["padding"] = strings.Repeat("x", 1<<20)
	case UserInfoJWT:
		w.Header().Set("Content-Type", "application/jwt")
		_, _ = w.Write([]byte("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."))
		return
	case UserInfoRaw:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(info.Raw))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(answer)
}
