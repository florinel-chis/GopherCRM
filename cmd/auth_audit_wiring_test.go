package main

// Issue #454 through the router production uses: setupRouter wires the sign-in
// audit writer into POST /auth/login, POST /auth/refresh, the OIDC callback
// and the ApiKey branch of the authentication middleware, so every outcome
// below leaves exactly the row it should, with the request id of the request
// and the client's /24. Over a SQLite file opened by the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestAuthAuditWired(t *testing.T) {
	// The strict /auth tier would otherwise refuse the sixth request.
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	boot := bootAutomation(t, false)
	// Single sign-on on, with an issuer that is never contacted: an unknown
	// state is refused before discovery.
	boot.cfg.SSO = config.SSOConfig{Enabled: true, EnabledSet: true, IssuerURL: "https://127.0.0.1:1", ClientID: "crm"}

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	const email, password = "wired-0454@crm.example.test", "Wired-Passw0rd!-0454"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Wired", LastName: "Audit", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(user).Error)

	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	send := func(method, path, body, requestID, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("X-Request-ID", requestID)
		request.Header.Set("User-Agent", ua)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	var lastID uint
	newRow := func(t *testing.T) models.AuthAuditLog {
		t.Helper()
		var rows []models.AuthAuditLog
		require.NoError(t, models.DB.Where("id > ?", lastID).Order("id").Find(&rows).Error)
		require.Len(t, rows, 1, "exactly one audit row")
		lastID = rows[0].ID
		return rows[0]
	}
	assertRow := func(t *testing.T, row models.AuthAuditLog, method, outcome, reason string, userID uint) {
		t.Helper()
		assert.Equal(t, method, row.Method)
		assert.Equal(t, outcome, row.Outcome)
		assert.Equal(t, reason, row.Reason)
		if userID == 0 {
			assert.Nil(t, row.UserID)
		} else if assert.NotNil(t, row.UserID) {
			assert.Equal(t, userID, *row.UserID)
		}
		assert.Equal(t, "req-454", row.RequestID)
		assert.Equal(t, "192.0.2.0/24", row.ClientNetwork, "httptest's RemoteAddr 192.0.2.1")
		assert.Equal(t, "Chrome on Windows", row.UserAgentFamily)
	}

	recorder := send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"Wrong-Passw0rd!"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "password", "refused", "invalid_credentials", user.ID)

	recorder = send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, "req-454", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "password", "success", "", user.ID)
	var login struct {
		Data struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	require.NotEmpty(t, login.Data.RefreshToken)

	recorder = send(http.MethodPost, "/auth/refresh", `{"refresh_token":"`+login.Data.RefreshToken+`"}`, "req-454", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "refresh", "success", "", user.ID)

	recorder = send(http.MethodGet, "/leads", "", "req-454", "ApiKey bogus")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "api_key", "refused", "key_invalid", 0)

	recorder = send(http.MethodGet, "/leads", "", "req-454", "Bearer bogus")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	var after int64
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("id > ?", lastID).Count(&after).Error)
	assert.Zero(t, after, "a Bearer failure writes no row")

	recorder = send(http.MethodPost, "/auth/oidc/callback", `{"code":"c","state":"unknown-state-0454"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "oidc", "refused", "state_invalid", 0)

	// A request id outside ^[A-Za-z0-9._:-]{1,64}$ is not stored.
	for _, bad := range []string{strings.Repeat("x", 100), "has space", "mail@example.test"} {
		recorder = send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"Wrong-Passw0rd!"}`, bad, "")
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		row := newRow(t)
		assert.Equal(t, "", row.RequestID, "request id %q is not stored", bad)
		assert.Equal(t, "invalid_credentials", row.Reason)
	}
}
