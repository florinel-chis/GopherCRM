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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
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
