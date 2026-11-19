package main

// Issue #444 through the router production uses: setupDependencies wires the
// identity repository into the auth service, so POST /auth/password-reset
// issues no token for an account with a linked OpenID Connect identity while
// answering exactly as it does for a password account.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
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
