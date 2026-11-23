// oidc-issuer is the disposable OpenID Connect issuer of the browser tests
// (`make e2e`, gocrm-ui/e2e/tests/sso.spec.ts). It serves discovery, a JWKS,
// an authorization endpoint with a small identity form, a token endpoint and
// an end-session endpoint, over the run's per-run test certificate on a
// loopback port, and it enforces what a real issuer enforces on the client:
// the registered client id and secret, the exact registered redirect_uri,
// PKCE with S256 and a nonce that is echoed in the ID token, and at sign-out
// an id_token_hint it signed for the client and the exact registered
// post_logout_redirect_uri. Every ID token carries a random sid. It is a test
// tool: one client, in-memory grants, a signing key generated at start.
// Codes, secrets, tokens and sids are never logged.
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const (
	keyID         = "e2e-issuer-key"
	codeLifetime  = 2 * time.Minute
	tokenLifetime = 5 * time.Minute
)

type grant struct {
	challenge   string
	redirectURI string
	nonce       string
	clientID    string
	claims      identity
	// sid is the random provider session id of this sign-in.
	sid     string
	expires time.Time
	spent   bool
}

// identity is what the sign-in page lets the test choose.
type identity struct {
	subject, email, givenName, familyName string
	roles, groups                         []string
}

type issuer struct {
	url                   string
	clientID              string
	clientSecret          string
	redirectURI           string
	postLogoutRedirectURI string
	key                   *rsa.PrivateKey
	now                   func() time.Time

	mu     sync.Mutex
	grants map[string]*grant
	// lastLogout is the outcome of the latest end-session request:
	// "accepted" or "refused: <reason>", "" before the first.
	lastLogout string
}

func newIssuer(issuerURL, clientID, clientSecret, redirectURI, postLogoutRedirectURI string) (*issuer, error) {
	if clientID == "" || clientSecret == "" || redirectURI == "" || postLogoutRedirectURI == "" {
		return nil, errors.New("client id, client secret, redirect URI and post-logout redirect URI are required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &issuer{
		url: issuerURL, clientID: clientID, clientSecret: clientSecret, redirectURI: redirectURI,
		postLogoutRedirectURI: postLogoutRedirectURI, key: key, now: time.Now, grants: map[string]*grant{},
	}, nil
}

func (i *issuer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", i.discovery)
	mux.HandleFunc("GET /jwks", i.jwks)
	mux.HandleFunc("GET /authorize", i.authorizePage)
	mux.HandleFunc("POST /authorize", i.authorizeSubmit)
	mux.HandleFunc("POST /token", i.token)
	mux.HandleFunc("GET /logout", i.endSession)
	mux.HandleFunc("GET /test/last-logout", i.lastLogoutOutcome)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (i *issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"issuer":                                i.url,
		"authorization_endpoint":                i.url + "/authorize",
		"token_endpoint":                        i.url + "/token",
		"end_session_endpoint":                  i.url + "/logout",
		"jwks_uri":                              i.url + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

func (i *issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &i.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

// authorizeParams are the parameters of an authorization request, read from
// the query (GET) or from the hidden fields of the sign-in form (POST).
type authorizeParams struct {
	ResponseType, ClientID, RedirectURI, Scope, State, Nonce, CodeChallenge, CodeChallengeMethod string
}

func readAuthorizeParams(values url.Values) authorizeParams {
	return authorizeParams{
		ResponseType:        values.Get("response_type"),
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		Scope:               values.Get("scope"),
		State:               values.Get("state"),
		Nonce:               values.Get("nonce"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
	}
}

// check returns why the request is refused, or "" for a valid request.
func (i *issuer) check(p authorizeParams) string {
	switch {
	case p.ResponseType != "code":
		return "response_type must be code"
	case p.ClientID != i.clientID:
		return "unknown client_id"
	case p.RedirectURI != i.redirectURI:
		return "redirect_uri is not the registered one"
	case p.CodeChallenge == "":
		return "code_challenge is required"
	case p.CodeChallengeMethod != "S256":
		return "code_challenge_method must be S256"
	case p.State == "":
		return "state is required"
	case p.Nonce == "":
		return "nonce is required"
	}
	return ""
}

// refuse never redirects: a refused request must not reach a redirect_uri
// that was not verified.
func refuse(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, "Authorization request refused: %s\n", reason)
}

var signInPage = template.Must(template.New("sign-in").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Test issuer sign-in</title></head>
<body>
<h1>Test issuer sign-in</h1>
<p>Disposable OpenID Connect issuer of the browser tests. Choose the identity to sign in as.</p>
<form method="post" action="/authorize">
<input type="hidden" name="response_type" value="{{.ResponseType}}">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
<p><label for="subject">Subject</label> <input id="subject" name="subject" required></p>
<p><label for="email">Email</label> <input id="email" name="email"></p>
<p><label for="given_name">Given name</label> <input id="given_name" name="given_name"></p>
<p><label for="family_name">Family name</label> <input id="family_name" name="family_name"></p>
<p><label for="roles">Roles</label> <input id="roles" name="roles"> (comma-separated; empty leaves the claim out)</p>
<p><label for="groups">Groups</label> <input id="groups" name="groups"> (comma-separated; empty leaves the claim out)</p>
<p><button type="submit">Sign in</button></p>
</form>
</body>
</html>
`))

func (i *issuer) authorizePage(w http.ResponseWriter, r *http.Request) {
	params := readAuthorizeParams(r.URL.Query())
	if reason := i.check(params); reason != "" {
		refuse(w, reason)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = signInPage.Execute(w, params)
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func randomCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (i *issuer) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		refuse(w, "malformed form")
		return
	}
	params := readAuthorizeParams(r.PostForm)
	if reason := i.check(params); reason != "" {
		refuse(w, reason)
		return
	}
	who := identity{
		subject:    strings.TrimSpace(r.PostForm.Get("subject")),
		email:      strings.TrimSpace(r.PostForm.Get("email")),
		givenName:  strings.TrimSpace(r.PostForm.Get("given_name")),
		familyName: strings.TrimSpace(r.PostForm.Get("family_name")),
		roles:      splitList(r.PostForm.Get("roles")),
		groups:     splitList(r.PostForm.Get("groups")),
	}
	if who.subject == "" {
		refuse(w, "subject is required")
		return
	}
	code, err := randomCode()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sid, err := randomCode()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	i.mu.Lock()
	i.grants[code] = &grant{
		challenge: params.CodeChallenge, redirectURI: params.RedirectURI, nonce: params.Nonce,
		clientID: params.ClientID, claims: who, sid: sid, expires: i.now().Add(codeLifetime),
	}
	i.mu.Unlock()
	target, err := url.Parse(params.RedirectURI)
	if err != nil {
		refuse(w, "redirect_uri is malformed")
		return
	}
	query := target.Query()
	query.Set("code", code)
	query.Set("state", params.State)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func equalSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (i *issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	clientID, clientSecret, basic := r.BasicAuth()
	if basic {
		// RFC 6749 2.3.1: both halves are form-encoded inside the header.
		var errID, errSecret error
		clientID, errID = url.QueryUnescape(clientID)
		clientSecret, errSecret = url.QueryUnescape(clientSecret)
		if errID != nil || errSecret != nil {
			tokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
	} else {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if clientID != i.clientID || !equalSecret(clientSecret, i.clientSecret) {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	// Every check runs under the lock, and the code is spent only when all of
	// them pass: a refused exchange leaves it usable.
	i.mu.Lock()
	defer i.mu.Unlock()
	g, found := i.grants[r.PostForm.Get("code")]
	if !found || g.spent || !i.now().Before(g.expires) || g.clientID != clientID {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// The exchange must name the redirect_uri of the authorization request.
	if r.PostForm.Get("redirect_uri") != g.redirectURI {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// PKCE (RFC 7636, S256): base64url(SHA-256(verifier)) is the challenge.
	verifier := r.PostForm.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if verifier == "" || !equalSecret(base64.RawURLEncoding.EncodeToString(sum[:]), g.challenge) {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	idToken, err := i.sign(g)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error")
		return
	}
	g.spent = true
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": "e2e-issuer-access-token",
		"token_type":   "Bearer",
		"expires_in":   int(tokenLifetime.Seconds()),
		"id_token":     idToken,
	})
}

func (i *issuer) sign(g *grant) (string, error) {
	now := i.now()
	claims := map[string]interface{}{
		"iss":   i.url,
		"sub":   g.claims.subject,
		"aud":   g.clientID,
		"iat":   now.Unix(),
		"exp":   now.Add(tokenLifetime).Unix(),
		"nonce": g.nonce,
		"sid":   g.sid,
	}
	for name, value := range map[string]string{
		"email": g.claims.email, "given_name": g.claims.givenName, "family_name": g.claims.familyName,
	} {
		if value != "" {
			claims[name] = value
		}
	}
	if len(g.claims.roles) > 0 {
		claims["roles"] = g.claims.roles
	}
	if len(g.claims.groups) > 0 {
		claims["groups"] = g.claims.groups
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
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
	return signed.CompactSerialize()
}

// hintValid reports whether raw is an ID token this issuer signed for the
// registered client: an RS256 signature by the issuer's key, iss = this
// issuer and aud naming the client. Its expiry is not checked, as a hint is
// usually presented after the ID token expired.
func (i *issuer) hintValid(raw string) bool {
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return false
	}
	payload, err := signed.Verify(&i.key.PublicKey)
	if err != nil {
		return false
	}
	var claims struct {
		Iss string          `json:"iss"`
		Aud json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Iss != i.url {
		return false
	}
	var single string
	if json.Unmarshal(claims.Aud, &single) == nil {
		return single == i.clientID
	}
	var many []string
	if json.Unmarshal(claims.Aud, &many) != nil {
		return false
	}
	for _, aud := range many {
		if aud == i.clientID {
			return true
		}
	}
	return false
}

// endSession is the RP-initiated sign-out: a valid id_token_hint, a client_id
// (when sent) equal to the registered one and the exact registered
// post_logout_redirect_uri redirect there with 302; anything else is 400 and
// never redirects. The outcome is logged without any token or URL value.
func (i *issuer) endSession(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	reason := ""
	switch {
	case !i.hintValid(query.Get("id_token_hint")):
		reason = "id_token_hint is not an ID token of this issuer for the registered client"
	case query.Has("client_id") && query.Get("client_id") != i.clientID:
		reason = "unknown client_id"
	case query.Get("post_logout_redirect_uri") != i.postLogoutRedirectURI:
		reason = "post_logout_redirect_uri is not the registered one"
	}
	i.mu.Lock()
	if reason == "" {
		i.lastLogout = "accepted"
	} else {
		i.lastLogout = "refused: " + reason
	}
	i.mu.Unlock()
	if reason != "" {
		log.Printf("oidc-issuer: end-session request refused: %s", reason)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "End-session request refused: %s\n", reason)
		return
	}
	log.Printf("oidc-issuer: end-session request accepted")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, i.postLogoutRedirectURI, http.StatusFound)
}

// lastLogoutOutcome is the test hook of the browser tests: the outcome of the
// latest end-session request.
func (i *issuer) lastLogoutOutcome(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	outcome := i.lastLogout
	i.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome})
}

func run() error {
	listen := flag.String("listen", "", "loopback address to serve on, 127.0.0.1:<port>")
	certFile := flag.String("cert", "", "PEM certificate of the run's test CA")
	keyFile := flag.String("key", "", "PEM private key of that certificate")
	clientID := flag.String("client-id", "", "the one registered client id")
	redirectURI := flag.String("redirect-uri", "", "the one registered redirect URI, matched exactly")
	postLogoutRedirectURI := flag.String("post-logout-redirect-uri", "", "the one registered post-logout redirect URI, matched exactly")
	flag.Parse()

	host, port, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("--listen must be 127.0.0.1:<port>")
	}
	if *certFile == "" || *keyFile == "" {
		return errors.New("--cert and --key are required")
	}
	// The secret never travels on the command line, where ps would show it.
	secret := os.Getenv("E2E_OIDC_CLIENT_SECRET")
	if secret == "" {
		return errors.New("E2E_OIDC_CLIENT_SECRET must be set")
	}
	iss, err := newIssuer("https://"+*listen, *clientID, secret, *redirectURI, *postLogoutRedirectURI)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           iss.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(os.Stderr, "oidc-issuer: ", 0),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServeTLS(*certFile, *keyFile) }()
	log.Printf("oidc-issuer: serving https://%s", *listen)
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "oidc-issuer:", err)
		os.Exit(1)
	}
}
