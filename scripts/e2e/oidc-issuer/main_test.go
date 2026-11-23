package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

const (
	testClientID    = "gophercrm-e2e"
	testSecret      = "issuer-test-secret"
	testRedirectURI = "http://localhost:15173/auth/callback"
	testLogoutURI   = "http://localhost:15173/login"
)

type fixture struct {
	t      *testing.T
	server *httptest.Server
	iss    *issuer
	client *http.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	iss, err := newIssuer("", testClientID, testSecret, testRedirectURI, testLogoutURI)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(iss.handler())
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	iss.url = server.URL
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &fixture{t: t, server: server, iss: iss, client: client}
}

func randomHex() string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// request is one client's authorization request.
type request struct {
	verifier string
	values   url.Values
}

func newRequest() request {
	verifier := randomHex() + randomHex()
	return request{verifier: verifier, values: url.Values{
		"response_type":         {"code"},
		"client_id":             {testClientID},
		"redirect_uri":          {testRedirectURI},
		"scope":                 {"openid profile email"},
		"state":                 {randomHex()},
		"nonce":                 {randomHex()},
		"code_challenge":        {challengeOf(verifier)},
		"code_challenge_method": {"S256"},
	}}
}

func (r request) with(name, value string) request {
	values := url.Values{}
	for k, v := range r.values {
		values[k] = append([]string(nil), v...)
	}
	if value == "" {
		values.Del(name)
	} else {
		values.Set(name, value)
	}
	return request{verifier: r.verifier, values: values}
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(raw)
}

// authorizeGet opens the sign-in page.
func (f *fixture) authorizeGet(r request) (int, string) {
	response, err := f.client.Get(f.server.URL + "/authorize?" + r.values.Encode())
	require.NoError(f.t, err)
	return response.StatusCode, body(f.t, response)
}

// signIn posts the identity form and returns the code of the redirect.
func (f *fixture) signIn(r request, subject string) string {
	f.t.Helper()
	form := url.Values{}
	for k, v := range r.values {
		form[k] = v
	}
	form.Set("subject", subject)
	form.Set("email", subject+"@example.test")
	form.Set("given_name", "Test")
	form.Set("family_name", "Person")
	form.Set("roles", "crm-admin, crm-sales")
	form.Set("groups", "")
	response, err := f.client.PostForm(f.server.URL+"/authorize", form)
	require.NoError(f.t, err)
	_ = body(f.t, response)
	require.Equal(f.t, http.StatusSeeOther, response.StatusCode)
	location, err := url.Parse(response.Header.Get("Location"))
	require.NoError(f.t, err)
	require.Equal(f.t, testRedirectURI, location.Scheme+"://"+location.Host+location.Path)
	require.Equal(f.t, r.values.Get("state"), location.Query().Get("state"))
	code := location.Query().Get("code")
	require.NotEmpty(f.t, code)
	return code
}

type exchange struct {
	code, verifier, redirectURI string
	clientID, secret            string
	post                        bool
}

func (f *fixture) token(x exchange) (int, map[string]interface{}) {
	f.t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {x.code},
		"redirect_uri":  {x.redirectURI},
		"code_verifier": {x.verifier},
	}
	if x.post {
		form.Set("client_id", x.clientID)
		form.Set("client_secret", x.secret)
	}
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/token", strings.NewReader(form.Encode()))
	require.NoError(f.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !x.post {
		req.SetBasicAuth(url.QueryEscape(x.clientID), url.QueryEscape(x.secret))
	}
	response, err := f.client.Do(req)
	require.NoError(f.t, err)
	var decoded map[string]interface{}
	require.NoError(f.t, json.Unmarshal([]byte(body(f.t, response)), &decoded))
	return response.StatusCode, decoded
}

func good(code string, r request) exchange {
	return exchange{code: code, verifier: r.verifier, redirectURI: testRedirectURI, clientID: testClientID, secret: testSecret}
}

// verifyIDToken checks the signature against the published JWKS and returns
// the claims.
func (f *fixture) verifyIDToken(raw string) map[string]interface{} {
	f.t.Helper()
	response, err := f.client.Get(f.server.URL + "/jwks")
	require.NoError(f.t, err)
	var set jose.JSONWebKeySet
	require.NoError(f.t, json.Unmarshal([]byte(body(f.t, response)), &set))
	require.Len(f.t, set.Keys, 1)
	parsed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(f.t, err)
	payload, err := parsed.Verify(set.Keys[0])
	require.NoError(f.t, err)
	var claims map[string]interface{}
	require.NoError(f.t, json.Unmarshal(payload, &claims))
	return claims
}

func TestDiscovery(t *testing.T) {
	f := newFixture(t)
	response, err := f.client.Get(f.server.URL + "/.well-known/openid-configuration")
	require.NoError(t, err)
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body(t, response)), &doc))
	require.Equal(t, f.server.URL, doc["issuer"])
	require.Equal(t, f.server.URL+"/authorize", doc["authorization_endpoint"])
	require.Equal(t, f.server.URL+"/token", doc["token_endpoint"])
	require.Equal(t, f.server.URL+"/jwks", doc["jwks_uri"])
	require.Equal(t, []interface{}{"S256"}, doc["code_challenge_methods_supported"])
	require.Equal(t, []interface{}{"RS256"}, doc["id_token_signing_alg_values_supported"])
}

func TestSuccessfulSignIn(t *testing.T) {
	for _, post := range []bool{false, true} {
		name := "client_secret_basic"
		if post {
			name = "client_secret_post"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			r := newRequest()
			status, page := f.authorizeGet(r)
			require.Equal(t, http.StatusOK, status)
			require.Contains(t, page, "Test issuer sign-in")
			for _, label := range []string{"Subject", "Email", "Given name", "Family name", "Roles", "Groups"} {
				require.Contains(t, page, ">"+label+"</label>")
			}
			require.Contains(t, page, r.values.Get("nonce"))

			code := f.signIn(r, "subject-ok")
			x := good(code, r)
			x.post = post
			status, answer := f.token(x)
			require.Equal(t, http.StatusOK, status, answer)
			claims := f.verifyIDToken(answer["id_token"].(string))
			require.Equal(t, f.server.URL, claims["iss"])
			require.Equal(t, "subject-ok", claims["sub"])
			require.Equal(t, testClientID, claims["aud"])
			require.Equal(t, r.values.Get("nonce"), claims["nonce"])
			require.Equal(t, "subject-ok@example.test", claims["email"])
			require.Equal(t, "Test", claims["given_name"])
			require.Equal(t, "Person", claims["family_name"])
			require.Equal(t, []interface{}{"crm-admin", "crm-sales"}, claims["roles"])
			require.NotContains(t, claims, "groups")
			exp, iat := claims["exp"].(float64), claims["iat"].(float64)
			require.InDelta(t, tokenLifetime.Seconds(), exp-iat, 1)
		})
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	cases := []struct {
		name, param, value, reason string
	}{
		{"wrong_redirect_uri", "redirect_uri", "http://localhost:15173/elsewhere", "redirect_uri is not the registered one"},
		{"missing_nonce", "nonce", "", "nonce is required"},
		{"missing_state", "state", "", "state is required"},
		{"missing_code_challenge", "code_challenge", "", "code_challenge is required"},
		{"plain_code_challenge", "code_challenge_method", "plain", "code_challenge_method must be S256"},
		{"missing_code_challenge_method", "code_challenge_method", "", "code_challenge_method must be S256"},
		{"unknown_client_id", "client_id", "someone-else", "unknown client_id"},
		{"wrong_response_type", "response_type", "token", "response_type must be code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			r := newRequest().with(tc.param, tc.value)
			status, page := f.authorizeGet(r)
			require.Equal(t, http.StatusBadRequest, status)
			require.Contains(t, page, "Authorization request refused: "+tc.reason)

			// The form post re-runs the same checks and never redirects.
			form := url.Values{}
			for k, v := range r.values {
				form[k] = v
			}
			form.Set("subject", "refused")
			response, err := f.client.PostForm(f.server.URL+"/authorize", form)
			require.NoError(t, err)
			text := body(t, response)
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
			require.Empty(t, response.Header.Get("Location"))
			require.Contains(t, text, "Authorization request refused: "+tc.reason)
			f.iss.mu.Lock()
			require.Empty(t, f.iss.grants)
			f.iss.mu.Unlock()
		})
	}
}

func TestTokenRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(x *exchange)
		status int
		error  string
	}{
		{"wrong_client_secret", func(x *exchange) { x.secret = "not-the-secret" }, http.StatusUnauthorized, "invalid_client"},
		{"wrong_client_secret_post", func(x *exchange) { x.secret = "not-the-secret"; x.post = true }, http.StatusUnauthorized, "invalid_client"},
		{"unknown_client_id", func(x *exchange) { x.clientID = "someone-else" }, http.StatusUnauthorized, "invalid_client"},
		{"wrong_redirect_uri_right_verifier", func(x *exchange) { x.redirectURI = "http://localhost:15173/elsewhere" }, http.StatusBadRequest, "invalid_grant"},
		{"wrong_verifier_right_redirect_uri", func(x *exchange) { x.verifier = randomHex() + randomHex() }, http.StatusBadRequest, "invalid_grant"},
		{"missing_verifier", func(x *exchange) { x.verifier = "" }, http.StatusBadRequest, "invalid_grant"},
		{"unknown_code", func(x *exchange) { x.code = "not-a-code" }, http.StatusBadRequest, "invalid_grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			r := newRequest()
			code := f.signIn(r, "subject-"+tc.name)
			x := good(code, r)
			tc.mutate(&x)
			status, answer := f.token(x)
			require.Equal(t, tc.status, status, answer)
			require.Equal(t, tc.error, answer["error"])
			require.NotContains(t, answer, "id_token")

			// The refusal left the code unspent: the correct exchange
			// succeeds and carries the nonce of the authorization request.
			status, answer = f.token(good(code, r))
			require.Equal(t, http.StatusOK, status, answer)
			claims := f.verifyIDToken(answer["id_token"].(string))
			require.Equal(t, r.values.Get("nonce"), claims["nonce"])
		})
	}
}

func TestReusedCodeIsRefused(t *testing.T) {
	f := newFixture(t)
	r := newRequest()
	code := f.signIn(r, "subject-reuse")
	status, answer := f.token(good(code, r))
	require.Equal(t, http.StatusOK, status, answer)
	status, answer = f.token(good(code, r))
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "invalid_grant", answer["error"])
}

func TestExpiredCodeIsRefused(t *testing.T) {
	f := newFixture(t)
	clock := time.Now()
	f.iss.now = func() time.Time { return clock }
	r := newRequest()
	code := f.signIn(r, "subject-expired")
	f.iss.mu.Lock() // the handlers read the clock under the same lock
	clock = clock.Add(codeLifetime)
	f.iss.mu.Unlock()
	status, answer := f.token(good(code, r))
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "invalid_grant", answer["error"])
}

func TestSignInWithoutSubjectIsRefused(t *testing.T) {
	f := newFixture(t)
	r := newRequest()
	response, err := f.client.PostForm(f.server.URL+"/authorize", r.values)
	require.NoError(t, err)
	text := body(t, response)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Contains(t, text, "Authorization request refused: subject is required")
}

// signedInIDToken runs a sign-in and returns its ID token.
func (f *fixture) signedInIDToken(subject string) string {
	f.t.Helper()
	r := newRequest()
	status, answer := f.token(good(f.signIn(r, subject), r))
	require.Equal(f.t, http.StatusOK, status, answer)
	return answer["id_token"].(string)
}

// logout sends an end-session request and returns the status, the Location
// and the recorded outcome.
func (f *fixture) logout(query url.Values) (int, string, string) {
	f.t.Helper()
	response, err := f.client.Get(f.server.URL + "/logout?" + query.Encode())
	require.NoError(f.t, err)
	_ = body(f.t, response)
	hook, err := f.client.Get(f.server.URL + "/test/last-logout")
	require.NoError(f.t, err)
	var outcome struct {
		Outcome string `json:"outcome"`
	}
	require.NoError(f.t, json.Unmarshal([]byte(body(f.t, hook)), &outcome))
	return response.StatusCode, response.Header.Get("Location"), outcome.Outcome
}

// TestEndSession: discovery advertises the end-session endpoint, every ID
// token carries a random sid, and the endpoint redirects to the registered
// post-logout URI only for a hint it signed for the client; anything else is
// 400 without a redirect. No log line carries the hint or the URI.
func TestEndSession(t *testing.T) {
	var logs strings.Builder
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	f := newFixture(t)
	response, err := f.client.Get(f.server.URL + "/.well-known/openid-configuration")
	require.NoError(t, err)
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body(t, response)), &doc))
	require.Equal(t, f.server.URL+"/logout", doc["end_session_endpoint"])

	hint := f.signedInIDToken("subject-logout")
	claims := f.verifyIDToken(hint)
	sid, _ := claims["sid"].(string)
	require.NotEmpty(t, sid, "the ID token carries a sid")
	require.NotEqual(t, sid, f.verifyIDToken(f.signedInIDToken("subject-logout"))["sid"], "each sign-in gets its own sid")

	_, _, before := f.logout(url.Values{})
	require.Contains(t, before, "refused")

	valid := url.Values{"id_token_hint": {hint}, "post_logout_redirect_uri": {testLogoutURI}, "client_id": {testClientID}}
	status, location, outcome := f.logout(valid)
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, testLogoutURI, location)
	require.Equal(t, "accepted", outcome)

	withoutClient := url.Values{"id_token_hint": {hint}, "post_logout_redirect_uri": {testLogoutURI}}
	status, _, outcome = f.logout(withoutClient)
	require.Equal(t, http.StatusFound, status, "client_id is optional")
	require.Equal(t, "accepted", outcome)

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signedHint := func(claims map[string]interface{}, key *rsa.PrivateKey) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
		require.NoError(t, err)
		payload, err := json.Marshal(claims)
		require.NoError(t, err)
		signed, err := signer.Sign(payload)
		require.NoError(t, err)
		compact, err := signed.CompactSerialize()
		require.NoError(t, err)
		return compact
	}
	base := map[string]interface{}{"iss": f.server.URL, "sub": "s", "aud": testClientID, "exp": time.Now().Add(-time.Hour).Unix()}
	expired := signedHint(base, f.iss.key)
	status, _, _ = f.logout(url.Values{"id_token_hint": {expired}, "post_logout_redirect_uri": {testLogoutURI}})
	require.Equal(t, http.StatusFound, status, "an expired hint is accepted")

	otherAud := map[string]interface{}{"iss": f.server.URL, "sub": "s", "aud": "another-client"}
	otherIss := map[string]interface{}{"iss": "https://elsewhere.example.test", "sub": "s", "aud": testClientID}
	for name, query := range map[string]url.Values{
		"no_hint":           {"post_logout_redirect_uri": {testLogoutURI}},
		"malformed_hint":    {"id_token_hint": {"not-a-token"}, "post_logout_redirect_uri": {testLogoutURI}},
		"hint_other_key":    {"id_token_hint": {signedHint(base, otherKey)}, "post_logout_redirect_uri": {testLogoutURI}},
		"hint_other_aud":    {"id_token_hint": {signedHint(otherAud, f.iss.key)}, "post_logout_redirect_uri": {testLogoutURI}},
		"hint_other_iss":    {"id_token_hint": {signedHint(otherIss, f.iss.key)}, "post_logout_redirect_uri": {testLogoutURI}},
		"other_client_id":   {"id_token_hint": {hint}, "post_logout_redirect_uri": {testLogoutURI}, "client_id": {"other"}},
		"redirect_mismatch": {"id_token_hint": {hint}, "post_logout_redirect_uri": {testLogoutURI + "/"}},
		"redirect_missing":  {"id_token_hint": {hint}},
		"redirect_elsewhere": {"id_token_hint": {hint},
			"post_logout_redirect_uri": {"https://other-app.example.test/login"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, location, outcome := f.logout(query)
			require.Equal(t, http.StatusBadRequest, status)
			require.Empty(t, location, "a refusal never redirects")
			require.True(t, strings.HasPrefix(outcome, "refused: "), outcome)
		})
	}

	require.Contains(t, logs.String(), "end-session request accepted")
	require.Contains(t, logs.String(), "end-session request refused")
	for _, value := range []string{hint, expired, sid, testLogoutURI, "other-app.example.test"} {
		require.NotContains(t, logs.String(), value, "no log line carries a token, sid or URI")
	}
}
