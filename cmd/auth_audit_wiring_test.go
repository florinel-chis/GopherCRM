package main

// Issue #454 through the router production uses: setupRouter wires the sign-in
// audit writer into POST /auth/login, POST /auth/refresh, the OIDC callback,
// the ApiKey branch of the authentication middleware and (#550)
// POST /auth/change-password, /auth/password-reset (written by the reset
// queue after the answer),
// /auth/password-reset/confirm and /auth/logout, and (#551) the API key
// handler behind POST /api-keys, DELETE /api-keys/:id and
// PUT /api-keys/:id, so every outcome
// below leaves exactly the row it should, with the request id of the request
// and the client's /24. Over a SQLite file opened by the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	// awaitRow is newRow for a row the password reset queue writes after the
	// answer: it waits until the row exists.
	awaitRow := func(t *testing.T) models.AuthAuditLog {
		t.Helper()
		require.Eventually(t, func() bool {
			var n int64
			return models.DB.Model(&models.AuthAuditLog{}).Where("id > ?", lastID).Count(&n).Error == nil && n > 0
		}, 5*time.Second, 10*time.Millisecond, "the queued reset request wrote its audit row")
		return newRow(t)
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
		assert.EqualValues(t, 1, row.Attempts)
		if outcome == "refused" && (method == "password" || method == "refresh" || method == "api_key") {
			assert.NotNil(t, row.AggregateKey, "a refused %s outcome is counted in its bucket (#553)", method)
		} else {
			assert.Nil(t, row.AggregateKey, "a %s %s row is one row of its own", method, outcome)
		}
	}

	recorder := send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"Wrong-Passw0rd!"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "password", "refused", "invalid_credentials", user.ID)

	// #553: the same refusal again adds to the row of its method, reason,
	// account, network and UTC minute; across a minute boundary it starts a
	// second row.
	recorder = send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"Wrong-Passw0rd!"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	var refusedLogins []models.AuthAuditLog
	require.NoError(t, models.DB.Where("method = ? AND reason = ? AND user_id = ?", "password", "invalid_credentials", user.ID).
		Order("id").Find(&refusedLogins).Error)
	require.NotEmpty(t, refusedLogins)
	assert.LessOrEqual(t, len(refusedLogins), 2)
	var refusedAttempts uint
	for _, row := range refusedLogins {
		refusedAttempts += row.Attempts
		assert.NotNil(t, row.AggregateKey)
	}
	assert.EqualValues(t, 2, refusedAttempts, "two refused logins counted")
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID).Error)

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
	var rotated struct {
		Data struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &rotated))
	require.NotEmpty(t, rotated.Data.RefreshToken)

	// #547: the exchanged token presented again is the usual 401 and a
	// token_reuse row with its owner; the successor is then refused too.
	recorder = send(http.MethodPost, "/auth/refresh", `{"refresh_token":"`+login.Data.RefreshToken+`"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "Invalid or expired refresh token")
	assertRow(t, newRow(t), "refresh", "refused", "token_reuse", user.ID)
	recorder = send(http.MethodPost, "/auth/refresh", `{"refresh_token":"`+rotated.Data.RefreshToken+`"}`, "req-454", "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "refresh", "refused", "token_invalid", 0)

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

	// A request id outside ^[A-Za-z0-9._:-]{1,64}$ is not stored. Each
	// refusal names an account of its own, so each starts a row of its own
	// rather than adding to an earlier refusal's (#553).
	for i, bad := range []string{strings.Repeat("x", 100), "has space", "mail@example.test"} {
		other := &models.User{Email: "wired-rid-" + strconv.Itoa(i) + "-0454@crm.example.test", Password: string(hash),
			FirstName: "Wired", LastName: "Request", Role: models.RoleSales, IsActive: true}
		require.NoError(t, models.DB.Create(other).Error)
		recorder = send(http.MethodPost, "/auth/login", `{"email":"`+other.Email+`","password":"Wrong-Passw0rd!"}`, bad, "")
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		row := newRow(t)
		assert.Equal(t, "", row.RequestID, "request id %q is not stored", bad)
		assert.Equal(t, "invalid_credentials", row.Reason)
		require.NotNil(t, row.UserID)
		assert.Equal(t, other.ID, *row.UserID)
	}

	// #550: a password change, a reset request for a known and an unknown
	// email, a confirmation with an unknown token and a logout each leave
	// their own row.
	type session struct {
		Data struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	recorder = send(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, "req-454", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "password", "success", "", user.ID)
	var signedIn session
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &signedIn))
	require.NotEmpty(t, signedIn.Data.Token)

	// #551: creating a key, revoking it and the refused re-enable each leave
	// one row naming the caller and the key.
	assertKeyRow := func(t *testing.T, method, outcome, reason string, keyID uint) {
		t.Helper()
		row := newRow(t)
		assertRow(t, row, method, outcome, reason, user.ID)
		if assert.NotNil(t, row.APIKeyID, "api_key_id is set") {
			assert.Equal(t, keyID, *row.APIKeyID)
		}
	}
	recorder = send(http.MethodPost, "/api-keys", `{"name":"Wired key 0551"}`, "req-454", "Bearer "+signedIn.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			APIKey struct {
				ID uint `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	keyID := created.Data.APIKey.ID
	require.NotZero(t, keyID)
	assertKeyRow(t, "api_key_create", "success", "", keyID)
	keyPath := "/api-keys/" + strconv.FormatUint(uint64(keyID), 10)
	recorder = send(http.MethodDelete, keyPath, "", "req-454", "Bearer "+signedIn.Data.Token)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertKeyRow(t, "api_key_revoke", "success", "", keyID)
	recorder = send(http.MethodPut, keyPath, `{"is_active":true}`, "req-454", "Bearer "+signedIn.Data.Token)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	assertKeyRow(t, "api_key_enable", "refused", "key_revoked", keyID)

	const changed = "Wired-Changed-Passw0rd!-0550"
	recorder = send(http.MethodPost, "/auth/change-password",
		`{"current_password":"`+password+`","new_password":"`+changed+`","refresh_token":"`+signedIn.Data.RefreshToken+`"}`,
		"req-454", "Bearer "+signedIn.Data.Token)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "password_change", "success", "", user.ID)
	var renewed session
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &renewed))
	require.NotEmpty(t, renewed.Data.Token)

	recorder = send(http.MethodPost, "/auth/password-reset", `{"email":"`+email+`"}`, "req-454", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	// The queue issues the reset after the answer; the token is stored before
	// the audit row is written.
	assertRow(t, awaitRow(t), "reset_request", "success", "", user.ID)
	knownBody := recorder.Body.String()
	var stored int64
	require.NoError(t, models.DB.Model(&models.PasswordResetToken{}).Where("user_id = ? AND used_at IS NULL", user.ID).Count(&stored).Error)
	assert.EqualValues(t, 1, stored, "the log-only mailer's server stores the link")

	recorder = send(http.MethodPost, "/auth/password-reset", `{"email":"nobody-0550@crm.example.test"}`, "req-454", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, knownBody, recorder.Body.String(), "the same answer for an unknown email")
	assertRow(t, awaitRow(t), "reset_request", "refused", "account_unknown", 0)

	recorder = send(http.MethodPost, "/auth/password-reset/confirm",
		`{"token":"unknown-reset-token-0550","new_password":"Wired-Reset-Passw0rd!-0550"}`, "req-454", "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "reset_confirm", "refused", "token_invalid", 0)

	recorder = send(http.MethodPost, "/auth/logout", "", "req-454", "Bearer "+renewed.Data.Token)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assertRow(t, newRow(t), "logout", "success", "", user.ID)
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

// TestAuthAuditAPIKeyFailuresBounded is #466 through the production router:
// the moderate tier answers before Auth, so a burst of refused API keys from
// one address is cut off with 429s that look up no key and write no row, and
// the refusals that reach the key check add to one row per network and UTC
// minute. A valid key writes nothing. Its own router, so the limiter is fresh.
func TestAuthAuditAPIKeyFailuresBounded(t *testing.T) {
	boot := bootAutomation(t, false)
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	send := func(remoteAddr, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, boot.cfg.API.Prefix+"/leads", nil)
		request.RemoteAddr = remoteAddr
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	apiKeyRows := func() []models.AuthAuditLog {
		t.Helper()
		var rows []models.AuthAuditLog
		require.NoError(t, models.DB.Where("method = ?", "api_key").Order("id").Find(&rows).Error)
		return rows
	}
	require.Empty(t, apiKeyRows())

	firstMinute := time.Now().UTC().Truncate(time.Minute)
	unauthorized, throttled := 0, 0
	for i := 0; i < 250; i++ {
		remote := "192.0.2.1:1234"
		if i >= 200 {
			remote = "192.0.2.77:1234"
		}
		recorder := send(remote, "ApiKey gcrm_bogus0466")
		switch recorder.Code {
		case http.StatusUnauthorized:
			unauthorized++
			assert.Contains(t, recorder.Body.String(), "Invalid credentials")
		case http.StatusTooManyRequests:
			throttled++
		default:
			t.Fatalf("request %d: unexpected status %d: %s", i, recorder.Code, recorder.Body.String())
		}
	}
	lastMinute := time.Now().UTC().Truncate(time.Minute)
	minutes := int(lastMinute.Sub(firstMinute)/time.Minute) + 1

	assert.Positive(t, throttled, "the moderate tier answers before the key check")
	assert.Positive(t, unauthorized)
	rows := apiKeyRows()
	require.NotEmpty(t, rows)
	assert.LessOrEqual(t, len(rows), minutes, "at most one row per UTC minute the run spans")
	var attempts uint
	for _, row := range rows {
		assert.Equal(t, "refused", row.Outcome)
		assert.Equal(t, "key_invalid", row.Reason)
		assert.Equal(t, "192.0.2.0/24", row.ClientNetwork)
		attempts += row.Attempts
	}
	assert.EqualValues(t, unauthorized, attempts, "every 401 is counted, no 429 is")

	// A valid key from another address writes nothing.
	admin := &models.User{Email: "admin-0466@crm.example.test", Password: "x", FirstName: "Bounded", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	const raw = "gcrm_valid0466abcdef0123456789"
	require.NoError(t, models.DB.Create(&models.APIKey{Name: "valid 0466", KeyHash: utils.HashAPIKeyHMAC(raw, boot.cfg.API.APIKeySecret),
		Prefix: "0466abcd", UserID: admin.ID, IsActive: true}).Error)
	var before, after int64
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Count(&before).Error)
	recorder := send("203.0.113.9:1234", "ApiKey "+raw)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Count(&after).Error)
	assert.Equal(t, before, after, "a valid API key writes no audit row")
}
