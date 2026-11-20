package main

// Issue #468 through the router production uses: setupRouter mounts
// /scim/v2 outside the API prefix, behind its own strict rate tier and the
// SCIM token authentication, which records every refusal in the sign-in audit
// log with method scim. A user's JWT or API key is not a SCIM token, and a
// SCIM token is not a credential of the API. Over a SQLite file opened by the
// real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const scimWiredBody = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication failed"}`

// scimWiredRouter builds the production router over a fresh SQLite file.
func scimWiredRouter(t *testing.T) (*gin.Engine, *automationBoot) {
	t.Helper()
	boot := bootAutomation(t, false)
	boot.cfg.Forms.PublicBaseURL = "https://crm.example.test"
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})
	return router, boot
}

func scimWiredSend(router *gin.Engine, method, path, body, remote, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.RemoteAddr = remote
	request.Header.Set("X-Request-ID", "req-468")
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

func TestSCIMRoutesWired(t *testing.T) {
	// The strict SCIM tier stays live: every request below comes from its own
	// client address, so none is throttled.
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, boot := scimWiredRouter(t)
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("192.0.2.%d:1234", addr)
	}

	const password = "Wired-Passw0rd!-0468"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0468@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)

	recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+admin.Email+`","password":"`+password+`"}`, nextRemote(), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	jwt := login.Data.Token
	require.NotEmpty(t, jwt)

	recorder = scimWiredSend(router, http.MethodPost, prefix+"/api-keys", `{"name":"scim wiring key"}`, nextRemote(), "Bearer "+jwt)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var key struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &key))
	apiKey := key.Data.Key
	require.NotEmpty(t, apiKey)

	createToken := func(name string) (string, uint) {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"`+name+`"}`, nextRemote(), "Bearer "+jwt)
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
		var created struct {
			Data struct {
				Token     string `json:"token"`
				SCIMToken struct {
					ID uint `json:"id"`
				} `json:"scim_token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
		return created.Data.Token, created.Data.SCIMToken.ID
	}
	valid, validID := createToken("Entra")
	revoked, revokedID := createToken("Retired")
	recorder = scimWiredSend(router, http.MethodDelete, fmt.Sprintf("%s/settings/scim/tokens/%d", prefix, revokedID), "", nextRemote(), "ApiKey "+apiKey)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var stored models.SCIMToken
	require.NoError(t, models.DB.First(&stored, validID).Error)
	assert.Equal(t, utils.HashAPIKeyHMAC(valid, boot.cfg.API.APIKeySecret), stored.TokenHash, "HMAC under API_KEY_SECRET")
	unknown := "scim_" + strings.Repeat("9f", 32)

	var lastID uint
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID).Error)

	for _, tc := range []struct{ name, authorization, reason string }{
		{"no header", "", "token_missing"},
		{"unknown scim token", "Bearer " + unknown, "token_invalid"},
		{"revoked token", "Bearer " + revoked, "token_revoked"},
		{"admin JWT", "Bearer " + jwt, "token_invalid"},
		{"admin API key as Bearer", "Bearer " + apiKey, "token_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := scimWiredSend(router, http.MethodGet, "/scim/v2/ServiceProviderConfig", "", nextRemote(), tc.authorization)
			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), recorder.Header().Get("Content-Type"))
			assert.Equal(t, scimWiredBody, recorder.Body.String())

			var rows []models.AuthAuditLog
			require.NoError(t, models.DB.Where("id > ?", lastID).Order("id").Find(&rows).Error)
			require.Len(t, rows, 1, "exactly one audit row")
			lastID = rows[0].ID
			assert.Equal(t, "scim", rows[0].Method)
			assert.Equal(t, "refused", rows[0].Outcome)
			assert.Equal(t, tc.reason, rows[0].Reason)
			assert.Nil(t, rows[0].UserID)
			assert.EqualValues(t, 1, rows[0].Attempts)
			assert.Nil(t, rows[0].AggregateKey)
			assert.Equal(t, "req-468", rows[0].RequestID)
		})
	}

	for _, path := range []string{"/scim/v2/ServiceProviderConfig", "/scim/v2/Schemas", "/scim/v2/ResourceTypes"} {
		recorder := scimWiredSend(router, http.MethodGet, path, "", nextRemote(), "Bearer "+valid)
		require.Equal(t, http.StatusOK, recorder.Code, "%s: %s", path, recorder.Body.String())
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), path)
		assert.Contains(t, recorder.Body.String(), `"location":"https://crm.example.test/scim/v2/`, path)
	}
	require.NoError(t, models.DB.First(&stored, validID).Error)
	assert.NotNil(t, stored.LastUsedAt, "a successful authentication sets last_used_at")
	var after int64
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("id > ?", lastID).Count(&after).Error)
	assert.Zero(t, after, "a successful SCIM authentication writes no audit row")

	// A SCIM token is not a credential of the API.
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users", "", nextRemote(), "Bearer "+valid)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())

	// The best-effort last-use write fails: the answer stays 200.
	require.NoError(t, models.DB.Exec("ALTER TABLE scim_tokens DROP COLUMN last_used_at").Error)
	recorder = scimWiredSend(router, http.MethodGet, "/scim/v2/ServiceProviderConfig", "", nextRemote(), "Bearer "+valid)
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Positive(t, boot.logs.count("SCIM token last use could not be recorded"))

	// No log line carries a presented token or the hash of a stored one.
	boot.logs.mu.Lock()
	captured := boot.logs.buf.String()
	boot.logs.mu.Unlock()
	require.NotEmpty(t, captured)
	for _, secret := range []string{valid, revoked, unknown, strings.TrimPrefix(valid, "scim_"), strings.TrimPrefix(unknown, "scim_"),
		utils.HashAPIKeyHMAC(valid, boot.cfg.API.APIKeySecret), utils.HashAPIKeyHMAC(revoked, boot.cfg.API.APIKeySecret),
		strings.TrimPrefix(utils.HashAPIKeyHMAC(valid, boot.cfg.API.APIKeySecret), "hmac$")} {
		assert.NotContains(t, captured, secret)
	}
}

// TestSCIMStrictTierWired: from one client address the sixth rapid request is
// answered 429 with the SCIM error body by the strict tier, before the
// authentication, so it adds no audit row.
func TestSCIMStrictTierWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, _ := scimWiredRouter(t)

	count := func() int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ?", "scim").Count(&n).Error)
		return n
	}
	for i := 0; i < 5; i++ {
		recorder := scimWiredSend(router, http.MethodGet, "/scim/v2/ServiceProviderConfig", "", "203.0.113.7:1234", "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code, "request %d", i+1)
	}
	require.EqualValues(t, 5, count())

	recorder := scimWiredSend(router, http.MethodGet, "/scim/v2/ServiceProviderConfig", "", "203.0.113.7:1234", "")
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
	assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"429","detail":"Too many requests. Please try again later."}`,
		recorder.Body.String())
	assert.EqualValues(t, 5, count(), "the 429 adds no audit row")
}
