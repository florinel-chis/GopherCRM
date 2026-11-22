package example

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/server"
	"github.com/florinel-chis/gophercrm/internal/idkit/sso"
)

// Routes and the binding cookie of the app.
const (
	StartPath    = "/auth/start"
	CallbackPath = "/auth/callback"
	// BindingCookie holds the raw state of a started sign-in, the binding
	// sso.Service.Callback compares with the posted state.
	BindingCookie = "idkit_example_binding"
	bindingPath   = "/auth"
	// maxCallbackBytes bounds the callback body.
	maxCallbackBytes = 16 << 10
)

// Options configure New.
type Options struct {
	// IssuerURL is the issuer the install trusts. Empty switches the
	// sign-in off; the sign-in routes then answer 404.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// RedirectURL is the redirect URI registered with the issuer.
	RedirectURL string
	// RootCAs, when set, replaces the system roots of the issuer client.
	RootCAs *x509.CertPool
	// RoleMapping maps the groups or roles claim to a role; its distinct
	// groups values are the SCIM groups.
	RoleMapping []rolemap.Rule
	// DefaultRoleID is the role of a SCIM user no group rule matches.
	DefaultRoleID uint
	// SCIMToken is the bearer token every SCIM request must carry. It is
	// required.
	SCIMToken string
	// BaseURL is the public origin SCIM resource locations lie under.
	BaseURL string
	// EntraIssuerHost is passed to sso.Options (entra.IssuerHost when
	// empty).
	EntraIssuerHost string
	// Log receives the log lines (none when nil).
	Log io.Writer
}

// App is the example host: the sign-in routes and the SCIM server over the
// in-memory stores. It is an http.Handler.
type App struct {
	store        *Store
	signIn       *sso.Service
	scim         *server.Server
	scimToken    [sha256.Size]byte
	secureCookie bool
}

// New builds the app, its stores, the sign-in and the SCIM server. The state
// hash is HMAC-SHA256 under a random key of this App.
func New(opts Options) (*App, error) {
	if opts.SCIMToken == "" {
		return nil, errors.New("example: SCIMToken is required")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("example: state key: %w", err)
	}
	keyed := func(prefix, value string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(prefix))
		mac.Write([]byte(value))
		return hex.EncodeToString(mac.Sum(nil))
	}

	store := newStore(idkit.Config{
		Enabled:      opts.IssuerURL != "",
		IssuerURL:    opts.IssuerURL,
		ClientID:     opts.ClientID,
		ClientSecret: opts.ClientSecret,
		Scopes:       []string{"openid", "email", "profile"},
		RoleMapping:  append([]rolemap.Rule(nil), opts.RoleMapping...),
	}, opts.DefaultRoleID)
	logger := NewLogger(opts.Log)

	signIn := sso.New(sso.Options{
		Config:          store.ConfigSource(),
		Attempts:        store.AttemptStore(),
		Identities:      store.IdentityStore(),
		Accounts:        store.AccountStore(),
		Sessions:        store.SessionIssuer(),
		Audit:           store.AuditSink(),
		Tx:              store.TxRunner(),
		Logger:          logger,
		StateHash:       func(state string) string { return keyed("state:", state) },
		SubjectRef:      func(issuer, subject string) string { return keyed("subject:", issuer+"\x00"+subject)[:16] },
		RedirectURL:     opts.RedirectURL,
		RootCAs:         opts.RootCAs,
		EntraIssuerHost: opts.EntraIssuerHost,
	})
	scimServer, err := server.New(server.Options{
		BaseURL: opts.BaseURL,
		Users:   store.Users(),
		Groups:  store.Groups(),
		Logger:  logger,
	})
	if err != nil {
		return nil, fmt.Errorf("example: %w", err)
	}
	return &App{
		store:        store,
		signIn:       signIn,
		scim:         scimServer,
		scimToken:    sha256.Sum256([]byte("Bearer " + opts.SCIMToken)),
		secureCookie: strings.HasPrefix(opts.RedirectURL, "https://"),
	}, nil
}

// Store is the app's in-memory state, for tests.
func (a *App) Store() *Store { return a.store }

// Account returns the account id names.
func (a *App) Account(id string) (Account, bool) { return a.store.account(id) }

// ServeHTTP routes the sign-in and the SCIM server; anything else is 404.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == StartPath:
		if !allowPost(w, r) {
			return
		}
		a.start(w, r)
	case r.URL.Path == CallbackPath:
		if !allowPost(w, r) {
			return
		}
		a.callback(w, r)
	case r.URL.Path == scim.BasePath || strings.HasPrefix(r.URL.Path, scim.BasePath+"/"):
		if !a.scimAuthorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			scim.WriteError(w, http.StatusUnauthorized, "Authentication required")
			return
		}
		a.scim.ServeHTTP(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func allowPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	return false
}

// scimAuthorized compares the Authorization header with the configured
// bearer token in constant time, over their hashes so the length does not
// show either.
func (a *App) scimAuthorized(r *http.Request) bool {
	sent := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	return subtle.ConstantTimeCompare(sent[:], a.scimToken[:]) == 1
}

// start answers the authorization URL and sets the binding cookie.
func (a *App) start(w http.ResponseWriter, r *http.Request) {
	authorization, err := a.signIn.Start(r.Context())
	if err != nil {
		writeSignInError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     BindingCookie,
		Value:    authorization.State,
		Path:     bindingPath,
		MaxAge:   int(sso.DefaultAttemptTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": authorization.URL})
}

// callbackBody is what the browser posts back after the issuer redirected
// it.
type callbackBody struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// callback finishes the sign-in with the posted code and state and the
// binding cookie, and clears the cookie: the attempt is spent either way.
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	var body callbackBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCallbackBytes))
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	binding := ""
	if cookie, err := r.Cookie(BindingCookie); err == nil {
		binding = cookie.Value
	}
	session, err := a.signIn.Callback(r.Context(), body.Code, body.State, binding)
	http.SetCookie(w, &http.Cookie{
		Name: BindingCookie, Value: "", Path: bindingPath, MaxAge: -1,
		HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	if err != nil {
		writeSignInError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"account_id": session.AccountID, "access_token": session.AccessToken})
}

// writeSignInError answers a refusal 401 and an unavailable sign-in 503 with
// its code, a disabled sign-in 404 and anything else 500.
func writeSignInError(w http.ResponseWriter, err error) {
	if code := oidc.RefusalCode(err); code != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": code})
		return
	}
	if code := oidc.UnavailableCode(err); code != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": code})
		return
	}
	if errors.Is(err, oidc.ErrDisabled) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		status, encoded = http.StatusInternalServerError, []byte(`{"error":"internal_error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
