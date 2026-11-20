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
	"github.com/florinel-chis/gophercrm/internal/utils"
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
	bogusKeyBody := recorder.Body.String()

	// #465: a revoked key is found by the lookup and refused as key_revoked
	// with its owner, while the client gets the same 401 as for a bogus key.
	const revokedRaw = "gcrm_revoked0465abcdef0123456789"
	revoked := &models.APIKey{Name: "revoked 0465", KeyHash: utils.HashAPIKeyHMAC(revokedRaw, boot.cfg.API.APIKeySecret),
		Prefix: "0465abcd", UserID: user.ID}
	require.NoError(t, models.DB.Create(revoked).Error)
	// The default:true tag overwrites a false IsActive on create.
	require.NoError(t, models.DB.Model(&models.APIKey{}).Where("id = ?", revoked.ID).Update("is_active", false).Error)
	recorder = send(http.MethodGet, "/leads", "", "req-454", "ApiKey "+revokedRaw)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assert.Equal(t, bogusKeyBody, recorder.Body.String(), "a revoked key gets the bogus key's answer byte for byte")
	assertRow(t, newRow(t), "api_key", "refused", "key_revoked", user.ID)

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

// TestSignInAuditRouteWired is #455 through the production router: setupRouter
// mounts GET /audit/sign-ins behind audit.sign_ins_view. An admin Bearer token
// and an admin's API key read the rows the test's own logins wrote, newest
// first; a sales user's API key is refused.
func TestSignInAuditRouteWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	boot := bootAutomation(t, false)
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	const password = "Wired-Passw0rd!-0455"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0455@crm.example.test", Password: string(hash), FirstName: "Audit", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	sales := &models.User{Email: "sales-0455@crm.example.test", Password: string(hash), FirstName: "Audit", LastName: "Sales", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	require.NoError(t, models.DB.Create(sales).Error)

	send := func(method, path, body, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	login := func(email, pw string, want int) string {
		t.Helper()
		recorder := send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"`+pw+`"}`, "")
		require.Equal(t, want, recorder.Code, recorder.Body.String())
		var body struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		return body.Data.Token
	}
	apiKey := func(token string) string {
		t.Helper()
		recorder := send(http.MethodPost, "/api-keys", `{"name":"audit wiring key"}`, "Bearer "+token)
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
		var body struct {
			Data struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.NotEmpty(t, body.Data.Key)
		return body.Data.Key
	}

	salesToken := login(sales.Email, password, http.StatusOK)
	require.NotEmpty(t, salesToken)
	login(admin.Email, "Wrong-Passw0rd!-0455", http.StatusUnauthorized)
	adminToken := login(admin.Email, password, http.StatusOK)
	require.NotEmpty(t, adminToken)

	type row struct {
		Method  string `json:"method"`
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
		UserID  *uint  `json:"user_id"`
	}
	read := func(authorization string) []row {
		t.Helper()
		recorder := send(http.MethodGet, "/audit/sign-ins?method=password", "", authorization)
		require.Equal(t, http.StatusOK, recorder.Code, "an unmounted route answers 404: %s", recorder.Body.String())
		var body struct {
			Data []row `json:"data"`
			Meta struct {
				Total int64 `json:"total"`
			} `json:"meta"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.Len(t, body.Data, 3, recorder.Body.String())
		assert.EqualValues(t, 3, body.Meta.Total)
		return body.Data
	}
	uid := func(r row) uint {
		if r.UserID == nil {
			return 0
		}
		return *r.UserID
	}

	rows := read("Bearer " + adminToken)
	assert.Equal(t, "success", rows[0].Outcome)
	assert.Equal(t, admin.ID, uid(rows[0]))
	assert.Equal(t, "refused", rows[1].Outcome)
	assert.Equal(t, "invalid_credentials", rows[1].Reason)
	assert.Equal(t, admin.ID, uid(rows[1]))
	assert.Equal(t, "success", rows[2].Outcome)
	assert.Equal(t, sales.ID, uid(rows[2]))

	assert.Equal(t, rows, read("ApiKey "+apiKey(adminToken)), "an admin's API key reads the same rows")

	recorder := send(http.MethodGet, "/audit/sign-ins", "", "ApiKey "+apiKey(salesToken))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "FORBIDDEN", envelope.Error.Code)
}
