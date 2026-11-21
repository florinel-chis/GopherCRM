package main

// Issue #532 through the router production uses: POST /api-keys gives a new
// key a one-year expiry, needs a sign-in within the last 15 minutes (the
// auth middleware publishes the token's auth_time; a token from
// POST /auth/refresh carries none), and refuses a customer account; a key
// past its expiry is refused at authentication.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestAPIKeyLifetimeProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)
	const password = "Lifetime-Passw0rd!-532"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	staff := &models.User{Email: "staff@apikey-lifetime-wiring.test", Password: string(hash), FirstName: "Sal",
		LastName: "Staff", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(staff).Error)
	customer := &models.User{Email: "customer@apikey-lifetime-wiring.test", Password: string(hash), FirstName: "Cora",
		LastName: "Customer", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, models.DB.Create(customer).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	send := func(t *testing.T, method, path, body, authorization string) (int, utils.APIResponse, string) {
		t.Helper()
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
		return recorder.Code, envelope, recorder.Body.String()
	}
	login := func(t *testing.T, email string) (access, refresh string) {
		t.Helper()
		status, envelope, body := send(t, http.MethodPost, "/auth/login",
			`{"email":"`+email+`","password":"`+password+`"}`, "")
		require.Equal(t, http.StatusOK, status, body)
		data := envelope.Data.(map[string]interface{})
		access, _ = data["token"].(string)
		refresh, _ = data["refresh_token"].(string)
		require.NotEmpty(t, access)
		return access, refresh
	}
	keys := func(t *testing.T, userID uint) int64 {
		t.Helper()
		var n int64
		require.NoError(t, models.DB.Model(&models.APIKey{}).Where("user_id = ?", userID).Count(&n).Error)
		return n
	}
	createKey := func(t *testing.T, token string) (int, utils.APIResponse, string) {
		t.Helper()
		return send(t, http.MethodPost, "/api-keys", `{"name":"wiring key"}`, "Bearer "+token)
	}
	assertReauthenticate := func(t *testing.T, status int, envelope utils.APIResponse, body string) {
		t.Helper()
		require.Equal(t, http.StatusForbidden, status, body)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "REAUTHENTICATION_REQUIRED", envelope.Error.Code)
		assert.Equal(t, "Sign in again to create an API key: creating a key needs a sign-in within the last 15 minutes",
			envelope.Error.Message)
	}
	// signed builds an access token as GenerateJWT does, with the given
	// auth_time, signed with the boot's JWT secret.
	signed := func(t *testing.T, signedInAt time.Time) string {
		t.Helper()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"user_id":       staff.ID,
			"email":         staff.Email,
			"role":          staff.Role,
			"exp":           time.Now().Add(time.Hour).Unix(),
			"token_version": staff.TokenVersion,
			"auth_time":     signedInAt.Unix(),
		}).SignedString([]byte(boot.cfg.JWT.Secret))
		require.NoError(t, err)
		return token
	}

	access, refresh := login(t, staff.Email)

	t.Run("a fresh sign-in creates a key that expires in one year", func(t *testing.T) {
		before := keys(t, staff.ID)
		status, envelope, body := createKey(t, access)
		require.Equal(t, http.StatusCreated, status, body)
		data := envelope.Data.(map[string]interface{})
		record := data["api_key"].(map[string]interface{})
		expires, err := time.Parse(time.RFC3339, record["expires_at"].(string))
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now().AddDate(1, 0, 0), expires, time.Minute)
		assert.Equal(t, before+1, keys(t, staff.ID))
	})

	t.Run("a refreshed token needs a new sign-in", func(t *testing.T) {
		require.NotEmpty(t, refresh)
		status, envelope, body := send(t, http.MethodPost, "/auth/refresh", `{"refresh_token":"`+refresh+`"}`, "")
		require.Equal(t, http.StatusOK, status, body)
		rotated, _ := envelope.Data.(map[string]interface{})["token"].(string)
		require.NotEmpty(t, rotated)
		before := keys(t, staff.ID)
		status, envelope, body = createKey(t, rotated)
		assertReauthenticate(t, status, envelope, body)
		assert.Equal(t, before, keys(t, staff.ID), "nothing is written")
	})

	t.Run("the sign-in must be at most 15 minutes old", func(t *testing.T) {
		before := keys(t, staff.ID)
		status, envelope, body := createKey(t, signed(t, time.Now().Add(-16*time.Minute)))
		assertReauthenticate(t, status, envelope, body)
		assert.Equal(t, before, keys(t, staff.ID))

		status, _, body = createKey(t, signed(t, time.Now().Add(-14*time.Minute)))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Equal(t, before+1, keys(t, staff.ID))
	})

	t.Run("a customer account cannot create a key", func(t *testing.T) {
		customerAccess, _ := login(t, customer.Email)
		status, envelope, body := createKey(t, customerAccess)
		require.Equal(t, http.StatusForbidden, status, body)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "FORBIDDEN", envelope.Error.Code)
		assert.Equal(t, "Customer accounts cannot hold API keys", envelope.Error.Message)
		assert.Zero(t, keys(t, customer.ID))
	})

	t.Run("a key past its expiry is refused", func(t *testing.T) {
		fresh := signed(t, time.Now())
		status, envelope, body := createKey(t, fresh)
		require.Equal(t, http.StatusCreated, status, body)
		data := envelope.Data.(map[string]interface{})
		key, _ := data["key"].(string)
		id := uint(data["api_key"].(map[string]interface{})["id"].(float64))
		status, _, body = send(t, http.MethodGet, "/api-keys", "", "ApiKey "+key)
		require.Equal(t, http.StatusOK, status, body, "the key works while it is live")

		require.NoError(t, models.DB.Model(&models.APIKey{}).Where("id = ?", id).
			Update("expires_at", time.Now().Add(-time.Minute)).Error)
		status, _, body = send(t, http.MethodGet, "/api-keys", "", "ApiKey "+key)
		assert.Equal(t, http.StatusUnauthorized, status, body)
	})
}
