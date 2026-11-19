package main

// Issues #444 and #457 through the router production uses: setupDependencies
// wires the identity repository and the single sign-on password policy into
// the auth service. TestPasswordResetSSOAccountWired runs with
// security.sso.allow_password off (the shipped state): POST
// /auth/password-reset issues no token for an account with a linked OpenID
// Connect identity while answering exactly as it does for a password account.
// TestSSOPasswordPolicyWired switches the setting on through PUT /settings/sso
// and checks password login and reset follow it.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
)

func TestPasswordResetSSOAccountWired(t *testing.T) {
	boot := bootAutomation(t, false)

	provisioned := &models.User{Email: "provisioned@reset-sso.test", Password: "x", FirstName: "Sso", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(provisioned).Error)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: provisioned.ID, Issuer: "https://idp.example.com", Subject: "subject-444"}).Error)
	password := &models.User{Email: "password@reset-sso.test", Password: "x", FirstName: "Pass", LastName: "Word", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(password).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	request := func(email string) string {
		t.Helper()
		body, err := json.Marshal(map[string]string{"email": email})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+"/auth/password-reset", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var envelope struct {
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		require.NotEmpty(t, envelope.Data.Message)
		return envelope.Data.Message
	}

	provisionedMessage := request(provisioned.Email)
	passwordMessage := request(password.Email)
	assert.Equal(t, passwordMessage, provisionedMessage, "the public answer does not reveal the account kind")

	tokens := func(userID uint) int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.PasswordResetToken{}).Where("user_id = ?", userID).Count(&n).Error)
		return n
	}
	assert.Zero(t, tokens(provisioned.ID), "an account provisioned by single sign-on gets no reset token")
	assert.Equal(t, int64(1), tokens(password.ID), "a password account gets one reset token")
}

func TestSSOPasswordPolicyWired(t *testing.T) {
	boot := bootAutomation(t, false)
	const knownPassword = "Wired-Policy-457!"
	hash, err := bcrypt.GenerateFromPassword([]byte(knownPassword), bcrypt.MinCost)
	require.NoError(t, err)

	linked := &models.User{Email: "linked@sso-policy.test", Password: string(hash), FirstName: "Sso", LastName: "Linked", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(linked).Error)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: linked.ID, Issuer: "https://idp.example.com", Subject: "subject-457"}).Error)
	control := &models.User{Email: "local@sso-policy.test", Password: string(hash), FirstName: "Local", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(control).Error)
	admin := &models.User{Email: "admin@sso-policy.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	adminToken, err := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT).GenerateJWT(admin)
	require.NoError(t, err)

	// At most five /auth requests: the strict tier allows a burst of 5.
	do := func(method, path, body, bearer string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder.Code, recorder.Body.String()
	}
	login := func(email string) (int, string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"email": email, "password": knownPassword})
		require.NoError(t, err)
		return do(http.MethodPost, "/auth/login", string(body), "")
	}
	refreshTokens := func(userID uint) int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.RefreshToken{}).Where("user_id = ?", userID).Count(&n).Error)
		return n
	}

	// The shipped state: allow_password empty, OIDC_ALLOW_PASSWORD unset, off.
	status, raw := login(linked.Email)
	require.Equal(t, http.StatusUnauthorized, status, raw)
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &envelope))
	assert.Equal(t, "Invalid email or password", envelope.Error.Message)
	assert.Zero(t, refreshTokens(linked.ID), "a refused login issues no refresh token")

	status, raw = login(control.Email)
	require.Equal(t, http.StatusOK, status, raw)

	status, raw = do(http.MethodPut, "/settings/sso", `{"allow_password":true}`, adminToken)
	require.Equal(t, http.StatusOK, status, raw)
	var settings struct {
		Data struct {
			AllowPassword bool `json:"allow_password"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &settings))
	require.True(t, settings.Data.AllowPassword, raw)

	status, raw = login(linked.Email)
	require.Equal(t, http.StatusOK, status, "with allow_password on a linked account signs in with its password: %s", raw)
	assert.Equal(t, int64(1), refreshTokens(linked.ID))

	body, err := json.Marshal(map[string]string{"email": linked.Email})
	require.NoError(t, err)
	status, raw = do(http.MethodPost, "/auth/password-reset", string(body), "")
	require.Equal(t, http.StatusOK, status, raw)
	var resetTokens int64
	require.NoError(t, models.DB.Model(&models.PasswordResetToken{}).Where("user_id = ?", linked.ID).Count(&resetTokens).Error)
	assert.Equal(t, int64(1), resetTokens, "with allow_password on a linked account gets one reset token")
}
