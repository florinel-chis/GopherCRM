package oidctest_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
)

const redirectURL = "https://app.example.test/callback"

// flow is one client of the issuer built on go-oidc and x/oauth2.
type flow struct {
	issuer   *oidctest.Issuer
	ctx      context.Context
	provider *oidc.Provider
	config   oauth2.Config
}

func newFlow(t *testing.T, issuer *oidctest.Issuer) *flow {
	t.Helper()
	ctx := oidc.ClientContext(context.Background(), issuer.HTTPClient())
	provider, err := oidc.NewProvider(ctx, issuer.Issuer())
	require.NoError(t, err)
	return &flow{issuer: issuer, ctx: ctx, provider: provider, config: oauth2.Config{
		ClientID: issuer.ClientID(), ClientSecret: issuer.ClientSecret(), Endpoint: provider.Endpoint(),
		RedirectURL: redirectURL, Scopes: []string{oidc.ScopeOpenID, "email"},
	}}
}

// signIn runs the code flow with PKCE S256 and a nonce and returns the
// verified ID token and the raw one.
func (f *flow) signIn(t *testing.T, claims oidctest.Claims) (*oidc.IDToken, string) {
	t.Helper()
	verifier, nonce := oauth2.GenerateVerifier(), "nonce-"+oauth2.GenerateVerifier()
	authURL := f.config.AuthCodeURL("state-1", oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	code, state := f.issuer.Authorize(t, authURL, claims)
	require.Equal(t, "state-1", state)
	token, err := f.config.Exchange(f.ctx, code, oauth2.VerifierOption(verifier))
	require.NoError(t, err)
	raw, ok := token.Extra("id_token").(string)
	require.True(t, ok, "the exchange returns an ID token")
	idToken, err := f.provider.Verifier(&oidc.Config{ClientID: f.config.ClientID}).Verify(f.ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, nonce, idToken.Nonce)
	return idToken, raw
}

func TestIssuerCodeFlow(t *testing.T) {
	issuer := oidctest.New(t, oidctest.WithClient("generic-client", "generic-secret"))
	f := newFlow(t, issuer)
	idToken, raw := f.signIn(t, oidctest.Claims{Subject: "subject-1", Email: "one@idp.example.test", Groups: []string{"g1", "g2"}})
	assert.Equal(t, "subject-1", idToken.Subject)
	assert.Equal(t, issuer.Issuer(), idToken.Issuer)
	assert.Equal(t, []string{"generic-client"}, idToken.Audience)
	var claims struct {
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}
	require.NoError(t, idToken.Claims(&claims))
	assert.Equal(t, "one@idp.example.test", claims.Email)
	assert.Equal(t, []string{"g1", "g2"}, claims.Groups)
	assert.Equal(t, oidctest.KeyID, keyID(t, raw))
	assert.Equal(t, raw, issuer.LastIDToken())

	discovery, jwks := issuer.Fetches()
	assert.Equal(t, 1, discovery)
	assert.Equal(t, 1, jwks)
	requests := issuer.TakeTokenRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, redirectURL, requests[0].Form.Get("redirect_uri"))
	assert.NotEmpty(t, requests[0].Form.Get("code_verifier"))

	sub, ok := issuer.AccessSubject(issuer.LastAccessToken())
	assert.True(t, ok)
	assert.Equal(t, "subject-1", sub)

	userinfo, err := f.provider.UserInfo(f.ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: issuer.LastAccessToken(), TokenType: "Bearer"}))
	require.NoError(t, err)
	assert.Equal(t, "subject-1", userinfo.Subject)
}

func TestIssuerPath(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.SetPath("/realm/v1")
	f := newFlow(t, issuer)
	idToken, _ := f.signIn(t, oidctest.Claims{Subject: "subject-path"})
	assert.Equal(t, issuer.URL()+"/realm/v1", idToken.Issuer)
	assert.Equal(t, "/realm/v1"+oidctest.DiscoveryPath, issuer.LastDiscoveryPath())
}

// exchange posts a token request authenticated in the form.
func exchange(t *testing.T, issuer *oidctest.Issuer, form url.Values) (int, string) {
	t.Helper()
	response, err := issuer.HTTPClient().PostForm(issuer.TokenEndpoint(), form)
	require.NoError(t, err)
	defer response.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return response.StatusCode, body.Error
}

func TestIssuerRefusals(t *testing.T) {
	issuer := oidctest.New(t)
	verifier := oauth2.GenerateVerifier()
	authURL := "https://unused.example.test/authorize?" + url.Values{
		"response_type": {"code"}, "code_challenge_method": {"S256"}, "redirect_uri": {redirectURL},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "nonce": {"n-1"}, "state": {"s-1"},
	}.Encode()
	valid := func(code string) url.Values {
		return url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURL},
			"code_verifier": {verifier}, "client_id": {issuer.ClientID()}, "client_secret": {issuer.ClientSecret()},
		}
	}
	cases := []struct {
		name   string
		change func(url.Values)
		status int
		code   string
	}{
		{"redirect_uri", func(form url.Values) { form.Set("redirect_uri", redirectURL+"/other") }, http.StatusBadRequest, "invalid_grant"},
		{"verifier", func(form url.Values) { form.Set("code_verifier", oauth2.GenerateVerifier()) }, http.StatusBadRequest, "invalid_grant"},
		{"client_secret", func(form url.Values) { form.Set("client_secret", "wrong") }, http.StatusUnauthorized, "invalid_client"},
		{"unknown_code", func(form url.Values) { form.Set("code", "code-unknown") }, http.StatusBadRequest, "invalid_grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := issuer.Authorize(t, authURL, oidctest.Claims{Subject: "subject-" + tc.name})
			form := valid(code)
			tc.change(form)
			status, errCode := exchange(t, issuer, form)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.code, errCode)
		})
	}
	t.Run("control", func(t *testing.T) {
		code, _ := issuer.Authorize(t, authURL, oidctest.Claims{Subject: "subject-control"})
		response, err := issuer.HTTPClient().PostForm(issuer.TokenEndpoint(), valid(code))
		require.NoError(t, err)
		defer response.Body.Close()
		assert.Equal(t, http.StatusOK, response.StatusCode, "the unchanged request is accepted")
	})
	t.Run("userinfo_unknown_bearer", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodGet, issuer.URL()+oidctest.UserInfoPath, nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer not-issued")
		response, err := issuer.HTTPClient().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
		require.Len(t, issuer.UserInfoRequests(), 1)
		assert.Equal(t, "Bearer not-issued", issuer.UserInfoRequests()[0].Authorization)
	})
}

func TestIssuerRotation(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.Rotate(true)
	assert.Equal(t, []string{oidctest.RotatedKeyID}, publishedKeyIDs(t, issuer, ""))
	f := newFlow(t, issuer)
	_, raw := f.signIn(t, oidctest.Claims{Subject: "subject-rotated"})
	assert.Equal(t, oidctest.RotatedKeyID, keyID(t, raw))

	issuer.ResetFetches()
	discovery, jwks := issuer.Fetches()
	assert.Zero(t, discovery)
	assert.Zero(t, jwks)
	assert.Equal(t, []string{oidctest.KeyID}, publishedKeyIDs(t, issuer, ""), "the reset undoes the rotation")
}

func TestIssuerHostHooks(t *testing.T) {
	issuer := oidctest.New(t)
	extra, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer.SetJWKSHook(func(query url.Values) []jose.JSONWebKey {
		if query.Get("extra") != "on" {
			return nil
		}
		return []jose.JSONWebKey{{Key: &extra.PublicKey, KeyID: "extra", Algorithm: string(jose.RS256), Use: "sig"}}
	})
	assert.Equal(t, []string{oidctest.KeyID}, publishedKeyIDs(t, issuer, ""))
	assert.Equal(t, []string{oidctest.KeyID, "extra"}, publishedKeyIDs(t, issuer, "extra=on"))
	assert.Equal(t, []string{"", "extra=on"}, issuer.JWKSQueries())

	issuer.Handle("/custom", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "custom")
	}))
	response, err := issuer.HTTPClient().Get(issuer.URL() + "/custom")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "custom", string(body))
}

func publishedKeyIDs(t *testing.T, issuer *oidctest.Issuer, query string) []string {
	t.Helper()
	target := issuer.URL() + oidctest.JWKSPath
	if query != "" {
		target += "?" + query
	}
	response, err := issuer.HTTPClient().Get(target)
	require.NoError(t, err)
	defer response.Body.Close()
	var set jose.JSONWebKeySet
	require.NoError(t, json.NewDecoder(response.Body).Decode(&set))
	var ids []string
	for _, key := range set.Keys {
		ids = append(ids, key.KeyID)
	}
	return ids
}

func keyID(t *testing.T, raw string) string {
	t.Helper()
	header, err := base64.RawURLEncoding.DecodeString(strings.SplitN(raw, ".", 2)[0])
	require.NoError(t, err)
	var decoded struct {
		KeyID string `json:"kid"`
	}
	require.NoError(t, json.Unmarshal(header, &decoded))
	return decoded.KeyID
}
