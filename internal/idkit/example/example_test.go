package example_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/example"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/scimtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	issuerPath     = "/realms/example"
	salesGroup     = "example-sales"
	supportGroup   = "example-support"
	salesRole      = uint(7)
	supportRole    = uint(8)
	defaultRole    = uint(3)
	scimToken      = "scim-token-example-0001"
	redirectURL    = "https://app.example.test/auth/callback"
	baseURL        = "https://app.example.test"
	usersPath      = scim.BasePath + "/Users"
	groupsPath     = scim.BasePath + "/Groups"
	distinctGroups = 2
)

// syncBuffer is a log writer the app may write to from any goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is the app against a generic (not Entra ID) oidctest issuer with
// a groups mapping, and a SCIM client with the right token.
type harness struct {
	issuer *oidctest.Issuer
	app    *example.App
	scim   *scimtest.Client
	log    *syncBuffer
	opts   example.Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	issuer := oidctest.New(t)
	issuer.SetPath(issuerPath)
	log := &syncBuffer{}
	opts := example.Options{
		IssuerURL:    issuer.Issuer(),
		ClientID:     issuer.ClientID(),
		ClientSecret: issuer.ClientSecret(),
		RedirectURL:  redirectURL,
		RootCAs:      issuer.RootCAs(),
		RoleMapping: []rolemap.Rule{
			{Claim: rolemap.ClaimGroups, Value: salesGroup, RoleID: salesRole},
			{Claim: rolemap.ClaimGroups, Value: supportGroup, RoleID: supportRole},
			// A second rule on the same group and a roles rule add no group.
			{Claim: rolemap.ClaimGroups, Value: salesGroup, RoleID: supportRole},
			{Claim: rolemap.ClaimRoles, Value: "Example.Admin", RoleID: 1},
		},
		DefaultRoleID: defaultRole,
		SCIMToken:     scimToken,
		BaseURL:       baseURL,
		Log:           log,
	}
	app, err := example.New(opts)
	require.NoError(t, err)
	return &harness{issuer: issuer, app: app, scim: scimtest.New(app, scimToken), log: log, opts: opts}
}

// createUser provisions a SCIM user and returns its id.
func (h *harness) createUser(t *testing.T, externalID, email string) string {
	t.Helper()
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"` + email +
		`","externalId":"` + externalID + `","name":{"givenName":"Sam","familyName":"Example"},"active":true}`
	resp := h.scim.Do(http.MethodPost, usersPath, body)
	require.Equal(t, http.StatusCreated, resp.Status, string(resp.Body))
	var user scim.User
	require.NoError(t, resp.Decode(&user))
	require.NotEmpty(t, user.ID)
	assert.Equal(t, externalID, user.ExternalID)
	return user.ID
}

// signInResult is one sign-in through the app, with every value the
// browser and the issuer exchanged.
type signInResult struct {
	status        int
	body          map[string]string
	authURL       string
	code, state   string
	cookieWasSent bool
}

// signIn starts a sign-in, lets the issuer authorize it with claims and
// posts the callback, with the binding cookie when withBinding is set.
func (h *harness) signIn(t *testing.T, claims oidctest.Claims, withBinding bool) signInResult {
	t.Helper()
	start := httptest.NewRecorder()
	h.app.ServeHTTP(start, httptest.NewRequest(http.MethodPost, example.StartPath, nil))
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	var started map[string]string
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &started))
	authURL := started["authorization_url"]
	require.NotEmpty(t, authURL)

	var binding *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == example.BindingCookie {
			binding = cookie
		}
	}
	require.NotNil(t, binding, "the start sets the binding cookie")
	assert.True(t, binding.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, binding.SameSite)
	assert.Equal(t, "/auth", binding.Path)

	code, state := h.issuer.Authorize(t, authURL, claims)
	assert.Equal(t, state, binding.Value, "the binding is the raw state")
	payload, err := json.Marshal(map[string]string{"code": code, "state": state})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, example.CallbackPath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if withBinding {
		request.AddCookie(&http.Cookie{Name: binding.Name, Value: binding.Value})
	}
	callback := httptest.NewRecorder()
	h.app.ServeHTTP(callback, request)
	var body map[string]string
	require.NoError(t, json.Unmarshal(callback.Body.Bytes(), &body), callback.Body.String())
	return signInResult{status: callback.Code, body: body, authURL: authURL, code: code, state: state, cookieWasSent: withBinding}
}

func claims(subject, email string, extra map[string]interface{}) oidctest.Claims {
	return oidctest.Claims{Subject: subject, Email: email, Groups: []string{salesGroup}, Extra: extra}
}

func TestExampleSignIn(t *testing.T) {
	t.Run("scim_account_signs_in", func(t *testing.T) {
		h := newHarness(t)
		id := h.createUser(t, "sub-generic-1", "sales.person@example.test")
		before, ok := h.app.Account(id)
		require.True(t, ok)
		require.Equal(t, defaultRole, before.RoleID, "a SCIM user in no group has the default role")

		result := h.signIn(t, claims("sub-generic-1", "sales.person@example.test", nil), true)
		require.Equal(t, http.StatusOK, result.status, result.body)
		assert.Equal(t, id, result.body["account_id"], "the subject equal to the externalId signs in as the SCIM user")
		assert.NotEmpty(t, result.body["access_token"])
		account, ok := h.app.Account(id)
		require.True(t, ok)
		assert.Equal(t, salesRole, account.RoleID)
		assert.True(t, account.Active)
		assert.Len(t, h.app.Store().Snapshot().Accounts, 1, "no second account")
	})

	t.Run("first_sign_in_provisions", func(t *testing.T) {
		h := newHarness(t)
		existing := h.createUser(t, "sub-generic-existing", "existing@example.test")

		result := h.signIn(t, claims("sub-generic-new", "new.person@example.test", nil), true)
		require.Equal(t, http.StatusOK, result.status, result.body)
		id := result.body["account_id"]
		require.NotEmpty(t, id)
		assert.NotEqual(t, existing, id)
		account, ok := h.app.Account(id)
		require.True(t, ok)
		assert.Equal(t, "new.person@example.test", account.Email)
		assert.Equal(t, salesRole, account.RoleID)
		assert.True(t, account.Active)

		again := h.signIn(t, claims("sub-generic-new", "new.person@example.test", nil), true)
		require.Equal(t, http.StatusOK, again.status, again.body)
		assert.Equal(t, id, again.body["account_id"], "the same pair signs in as the provisioned account")
	})

	t.Run("entra_claims_ignored", func(t *testing.T) {
		h := newHarness(t)
		scimID := h.createUser(t, "oid-generic-1", "provisioned@example.test")

		// tid is the issuer URL's first path segment, the tenant an Entra ID
		// issuer would compare it with.
		result := h.signIn(t, claims("sub-generic-2", "other.person@example.test",
			map[string]interface{}{"oid": "oid-generic-1", "tid": "realms"}), true)
		require.Equal(t, http.StatusOK, result.status, result.body)
		assert.NotEqual(t, scimID, result.body["account_id"], "oid and tid do not link under a generic issuer")
		scimAccount, ok := h.app.Account(scimID)
		require.True(t, ok)
		assert.Equal(t, defaultRole, scimAccount.RoleID, "the SCIM user was not signed in")
	})

	t.Run("binding_missing", func(t *testing.T) {
		h := newHarness(t)
		result := h.signIn(t, claims("sub-generic-3", "nobinding@example.test", nil), false)
		assert.Equal(t, http.StatusUnauthorized, result.status)
		assert.Equal(t, map[string]string{"error": "binding_missing"}, result.body)
		assert.Empty(t, h.app.Store().Snapshot().Accounts)
	})

	t.Run("deprovisioned_refused", func(t *testing.T) {
		h := newHarness(t)
		id := h.createUser(t, "sub-generic-4", "leaver@example.test")
		resp := h.scim.Do(http.MethodDelete, usersPath+"/"+id, "")
		require.Equal(t, http.StatusNoContent, resp.Status, string(resp.Body))
		account, ok := h.app.Account(id)
		require.True(t, ok)
		require.False(t, account.Active)

		result := h.signIn(t, claims("sub-generic-4", "leaver@example.test", nil), true)
		assert.Equal(t, http.StatusUnauthorized, result.status)
		assert.Equal(t, map[string]string{"error": "account_inactive"}, result.body)
		after, _ := h.app.Account(id)
		assert.Equal(t, defaultRole, after.RoleID, "a refused sign-in changes no role")
	})

	t.Run("groups_listed", func(t *testing.T) {
		h := newHarness(t)
		resp := h.scim.Do(http.MethodGet, groupsPath, "")
		require.Equal(t, http.StatusOK, resp.Status, string(resp.Body))
		var list scim.ListResponse[scim.Group]
		require.NoError(t, resp.Decode(&list))
		assert.Equal(t, distinctGroups, list.TotalResults)
		names := make([]string, 0, len(list.Resources))
		for _, group := range list.Resources {
			names = append(names, group.DisplayName)
		}
		assert.Equal(t, []string{salesGroup, supportGroup}, names)

		// A member's role follows the group mapping.
		id := h.createUser(t, "sub-generic-5", "member@example.test")
		patch := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"` + id + `"}]}]}`
		resp = h.scim.Do(http.MethodPatch, groupsPath+"/"+scim.GroupID(supportGroup), patch)
		require.Equal(t, http.StatusNoContent, resp.Status, string(resp.Body))
		account, _ := h.app.Account(id)
		assert.Equal(t, supportRole, account.RoleID)
	})
}

// TestExampleSignInDisabled: without an issuer the sign-in routes answer 404.
func TestExampleSignInDisabled(t *testing.T) {
	app, err := example.New(example.Options{SCIMToken: scimToken})
	require.NoError(t, err)
	for _, path := range []string{example.StartPath, example.CallbackPath} {
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"code":"c","state":"s"}`)))
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
	}
}

func TestExampleTransactionRollback(t *testing.T) {
	h := newHarness(t)
	h.createUser(t, "sub-generic-6", "kept@example.test")
	store := h.app.Store()
	before := store.Snapshot()
	require.Len(t, before.Accounts, 1)
	require.Len(t, before.Identities, 1)

	errFail := errors.New("fail after the writes")
	err := store.TxRunner().WithTransaction(context.Background(), func(ctx context.Context) error {
		id, err := store.AccountStore().Provision(ctx, idkit.Profile{Email: "rolled.back@example.test"}, salesRole)
		require.NoError(t, err)
		require.NoError(t, store.IdentityStore().Create(ctx, idkit.Identity{
			AccountID: id, Issuer: h.issuer.Issuer(), Subject: "sub-rolled-back", Source: idkit.IdentitySourceOIDC,
		}))
		require.Len(t, store.Snapshot().Accounts, 2, "the writes are visible inside the transaction")

		// The stores that run only outside a transaction refuse its ctx.
		_, err = store.AttemptStore().Create(ctx, idkit.Attempt{StateHash: "h"})
		require.ErrorIs(t, err, idkit.ErrInTransaction)
		_, err = store.ConfigSource().Config(ctx)
		require.ErrorIs(t, err, idkit.ErrInTransaction)
		require.Empty(t, store.AuditSink().Record(ctx, idkit.AuditEvent{}))
		return errFail
	})
	require.ErrorIs(t, err, errFail)
	assert.Equal(t, before, store.Snapshot(), "the failed transaction left the accounts and identities as they were")

	err = store.TxRunner().WithTransaction(context.Background(), func(ctx context.Context) error {
		_, err := store.AccountStore().Provision(ctx, idkit.Profile{Email: "committed@example.test"}, salesRole)
		return err
	})
	require.NoError(t, err)
	assert.Len(t, store.Snapshot().Accounts, 2, "a transaction that returns nil keeps its writes")
}

func TestExampleSCIMToken(t *testing.T) {
	h := newHarness(t)
	for name, token := range map[string]string{"missing": "", "wrong": "scim-token-example-0002"} {
		t.Run(name, func(t *testing.T) {
			resp := scimtest.New(h.app, token).Do(http.MethodGet, usersPath, "")
			assert.Equal(t, http.StatusUnauthorized, resp.Status)
			_, isSCIMError := resp.SCIMError()
			assert.True(t, isSCIMError)
			assert.Zero(t, h.app.Store().SCIMCalls(), "the SCIM server was not called")
		})
	}

	resp := h.scim.Do(http.MethodGet, usersPath, "")
	assert.Equal(t, http.StatusOK, resp.Status, string(resp.Body))
	assert.Equal(t, 1, h.app.Store().SCIMCalls(), "the right token reaches the SCIM server")
}

func TestExampleLogPrivacy(t *testing.T) {
	h := newHarness(t)
	const sub, email = "sub-generic-private", "private.person@example.test"
	h.createUser(t, sub, email)

	ok := h.signIn(t, claims(sub, email, nil), true)
	require.Equal(t, http.StatusOK, ok.status, ok.body)
	refused := h.signIn(t, claims(sub, email, nil), false)
	require.Equal(t, http.StatusUnauthorized, refused.status, refused.body)

	out := h.log.String()
	require.Contains(t, out, "Signed in through single sign-on")
	require.Contains(t, out, "Single sign-on refused")
	secrets := map[string]string{
		"sub":               sub,
		"email":             email,
		"client secret":     h.opts.ClientSecret,
		"SCIM token":        scimToken,
		"access token":      ok.body["access_token"],
		"code":              ok.code,
		"state":             ok.state,
		"authorization URL": ok.authURL,
		"refused code":      refused.code,
		"refused state":     refused.state,
		"refused URL":       refused.authURL,
	}
	for name, secret := range secrets {
		require.NotEmpty(t, secret, name)
		assert.NotContains(t, out, secret, "the log holds the %s", name)
	}
}
