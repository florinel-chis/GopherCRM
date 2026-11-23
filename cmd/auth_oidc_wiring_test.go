package main

// Issue #435 through the router production uses: setupDependencies builds the
// OIDC sign-in from the single sign-on settings and mounts POST
// /auth/oidc/start and /auth/oidc/callback on the public /auth group. With
// single sign-on on and an unreachable issuer the start answers 503
// SSO_UNAVAILABLE; with it off, 404 NOT_FOUND in the JSON envelope, which only
// the handler writes (gin's own 404 is plain text). Over a SQLite file opened
// by the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestOIDCRoutesWired(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sso    config.SSOConfig
		status int
		code   string
	}{
		{
			name:   "enabled_issuer_unreachable",
			sso:    config.SSOConfig{Enabled: true, EnabledSet: true, IssuerURL: "https://127.0.0.1:1", ClientID: "crm"},
			status: http.StatusServiceUnavailable,
			code:   "SSO_UNAVAILABLE",
		},
		{
			name:   "disabled",
			sso:    config.SSOConfig{},
			status: http.StatusNotFound,
			code:   utils.ErrCodeNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boot := bootAutomation(t, false)
			boot.cfg.SSO = tc.sso

			backgroundCtx, stopBackground := context.WithCancel(context.Background())
			t.Cleanup(stopBackground)
			router, stop := setupRouter(backgroundCtx, boot.cfg)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
				defer cancel()
				_ = stop(ctx)
			})

			post := func(path, body string) (*httptest.ResponseRecorder, utils.APIResponse) {
				t.Helper()
				request := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				var envelope utils.APIResponse
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), "a JSON envelope, not gin's 404: %s", recorder.Body.String())
				return recorder, envelope
			}

			// The public registration probe reports the effective single
			// sign-on switch from the same settings service (#436).
			probe := httptest.NewRecorder()
			router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, boot.cfg.API.Prefix+"/auth/registration", nil))
			require.Equal(t, http.StatusOK, probe.Code, probe.Body.String())
			var status struct {
				Data struct {
					SSOEnabled *bool `json:"sso_enabled"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(probe.Body.Bytes(), &status))
			require.NotNil(t, status.Data.SSOEnabled, "sso_enabled is reported: %s", probe.Body.String())
			assert.Equal(t, tc.sso.Enabled, *status.Data.SSOEnabled)

			recorder, envelope := post("/auth/oidc/start", "")
			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			require.NotNil(t, envelope.Error)
			assert.Equal(t, tc.code, envelope.Error.Code)
			assert.Empty(t, recorder.Header().Values("Set-Cookie"))

			// The callback is mounted too: an unknown state on an enabled
			// configuration is refused before the issuer is contacted, and a
			// disabled one answers 404 as the start does.
			recorder, envelope = post("/auth/oidc/callback", `{"code":"c","state":"unknown-state"}`)
			require.NotNil(t, envelope.Error)
			if tc.sso.Enabled {
				assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
				assert.Equal(t, utils.ErrCodeUnauthorized, envelope.Error.Code)
			} else {
				assert.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
				assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
			}
		})
	}
}

// TestOIDCRoutesWired_SignOut (#634) drives the shipped router through a
// sign-in at an in-process test issuer: the callback returns the ID token,
// POST /auth/logout with that token as id_token_hint answers the issuer's
// end-session URL, and GET /auth/oidc/frontchannel-logout, served without a
// credential, ends the sessions that signed in under the named sid and leaves
// another account's alone. It fails when main.go stops wiring the end-session
// lookup into the logout or stops mounting the front-channel route.
func TestOIDCRoutesWired_SignOut(t *testing.T) {
	// The sign-ins below exceed the strict /auth tier's burst.
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	issuer := oidctest.New(t)
	previous := oidcTestOptions
	oidcTestOptions = []service.OIDCServiceOption{service.WithOIDCRootCAs(issuer.RootCAs())}
	t.Cleanup(func() { oidcTestOptions = previous })

	boot := bootAutomation(t, false)
	var sales models.Role
	require.NoError(t, models.DB.Where("system_key = ?", models.RoleSales).First(&sales).Error)
	mapping, err := json.Marshal([]map[string]interface{}{{"claim": "groups", "value": "crm-sales", "role_id": sales.ID}})
	require.NoError(t, err)
	boot.cfg.SSO = config.SSOConfig{Enabled: true, EnabledSet: true, IssuerURL: issuer.Issuer(),
		ClientID: issuer.ClientID(), ClientSecret: issuer.ClientSecret(), Scopes: "openid email profile",
		RoleMapping: string(mapping)}

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})
	prefix := boot.cfg.API.Prefix

	serve := func(method, path, bearer, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, prefix+path, bytes.NewBufferString(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	type session struct{ token, refresh, idToken string }
	signIn := func(subject, sid string) session {
		t.Helper()
		started := serve(http.MethodPost, "/auth/oidc/start", "", "")
		require.Equal(t, http.StatusOK, started.Code, started.Body.String())
		var start struct {
			Data struct {
				URL string `json:"authorization_url"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(started.Body.Bytes(), &start))
		cookies := (&http.Response{Header: started.Header()}).Cookies()
		require.Len(t, cookies, 1)
		code, state := issuer.Authorize(t, start.Data.URL, oidctest.Claims{Subject: subject,
			Email: subject + "@sign-out.test", Groups: []string{"crm-sales"}, SID: sid})
		body, err := json.Marshal(map[string]string{"code": code, "state": state})
		require.NoError(t, err)
		callback := serve(http.MethodPost, "/auth/oidc/callback", "", string(body), cookies[0])
		require.Equal(t, http.StatusOK, callback.Code, callback.Body.String())
		var answer struct {
			Data struct {
				Token        string `json:"token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(callback.Body.Bytes(), &answer))
		require.NotEmpty(t, answer.Data.IDToken, "the callback returns the ID token")
		assert.Equal(t, issuer.LastIDToken(), answer.Data.IDToken)
		return session{answer.Data.Token, answer.Data.RefreshToken, answer.Data.IDToken}
	}
	refresh := func(token string) int {
		t.Helper()
		body, err := json.Marshal(map[string]string{"refresh_token": token})
		require.NoError(t, err)
		return serve(http.MethodPost, "/auth/refresh", "", string(body)).Code
	}

	t.Run("logout_answers_the_end_session_url", func(t *testing.T) {
		s := signIn("subject-logout-0634", "sid-logout-0634")
		body, err := json.Marshal(map[string]string{"id_token_hint": s.idToken})
		require.NoError(t, err)
		recorder := serve(http.MethodPost, "/auth/logout", s.token, string(body))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var answer struct {
			Data struct {
				Message       string `json:"message"`
				EndSessionURL string `json:"end_session_url"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
		assert.Equal(t, "Logged out successfully", answer.Data.Message)
		parsed, err := url.Parse(answer.Data.EndSessionURL)
		require.NoError(t, err, recorder.Body.String())
		assert.Equal(t, issuer.URL()+oidctest.EndSessionPath, parsed.Scheme+"://"+parsed.Host+parsed.Path)
		assert.Equal(t, url.Values{
			"id_token_hint":            {s.idToken},
			"post_logout_redirect_uri": {strings.TrimRight(boot.cfg.App.BaseURL, "/") + "/login"},
			"client_id":                {issuer.ClientID()},
		}, parsed.Query())
		assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/users/me", s.token, "").Code, "the session ended")
		assert.Equal(t, http.StatusUnauthorized, refresh(s.refresh))

		// Without a hint the answer is the message alone, as before.
		other := signIn("subject-logout-0634", "sid-logout-0634-b")
		plain := serve(http.MethodPost, "/auth/logout", other.token, "")
		require.Equal(t, http.StatusOK, plain.Code)
		assert.NotContains(t, plain.Body.String(), "end_session_url")
	})

	t.Run("frontchannel_logout_ends_the_sid's_sessions", func(t *testing.T) {
		a := signIn("subject-fc-a-0634", "sid-fc-a-0634")
		b := signIn("subject-fc-b-0634", "sid-fc-b-0634")
		var before int64
		require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ?", "logout").Count(&before).Error)
		recorder := serve(http.MethodGet, "/auth/oidc/frontchannel-logout?sid=sid-fc-a-0634", "", "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "no-cache, no-store", recorder.Header().Get("Cache-Control"))
		assert.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
		assert.Empty(t, recorder.Header().Values("X-Frame-Options"), "the provider can frame it")
		assert.Empty(t, recorder.Header().Values("Content-Security-Policy"))

		assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/users/me", a.token, "").Code, "A's access token ended")
		assert.Equal(t, http.StatusUnauthorized, refresh(a.refresh), "A's refresh token ended")
		assert.Equal(t, http.StatusOK, serve(http.MethodGet, "/users/me", b.token, "").Code, "B keeps its session")
		assert.Equal(t, http.StatusOK, refresh(b.refresh), "B keeps its refresh token")

		var audits int64
		require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ?", "logout").Count(&audits).Error)
		assert.EqualValues(t, before+1, audits, "one logout audit row for A")

		missing := serve(http.MethodGet, "/auth/oidc/frontchannel-logout", "", "")
		assert.Equal(t, http.StatusBadRequest, missing.Code, missing.Body.String())
	})
}
