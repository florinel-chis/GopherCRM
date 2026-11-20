package main

// Issue #526 through the router production uses: setupDependencies wires the
// API key and refresh token repositories into the governed user service
// (WithUserCredentialRevocation) and the API key repository into the SCIM
// writes (WithSCIMUserWrites). An administrator's deactivation, a password
// change and a SCIM delete each revoke the account's API keys in their
// transaction: the key's next request answers 401 and its row carries
// revoked_at. Reactivating the account brings neither the key nor the old
// refresh token back. Over a SQLite file opened by the real connector.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestCredentialRevocationProductionWiring(t *testing.T) {
	const issuer = "https://idp.credential-revocation.test"
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		var role models.Role
		require.NoError(t, models.DB.Where("system_key = ?", "sales").First(&role).Error)
		boot.cfg.SSO.IssuerURL = issuer
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"roles","value":"CRM.Sales","role_id":%d}]`, role.ID)
		// The managed account below signs in with a password to hold a
		// session and issue its key.
		boot.cfg.SSO.AllowPassword, boot.cfg.SSO.AllowPasswordSet = true, true
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("198.51.100.%d:1234", addr)
	}
	const password = "Wired-Passw0rd!-0526"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)

	type session struct{ token, refresh string }
	signIn := func(email string) session {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, nextRemote(), "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var body struct {
			Data struct {
				Token        string `json:"token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.NotEmpty(t, body.Data.Token)
		require.NotEmpty(t, body.Data.RefreshToken)
		return session{body.Data.Token, body.Data.RefreshToken}
	}
	newUser := func(email string, role models.UserRole) *models.User {
		t.Helper()
		user := &models.User{Email: email, Password: string(hash), FirstName: "Wired", LastName: "User", Role: role, IsActive: true}
		require.NoError(t, models.DB.Create(user).Error)
		return user
	}
	// issueKey creates a key signed in as the owner and checks it works.
	issueKey := func(s session) string {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodPost, prefix+"/api-keys", `{"name":"wired key"}`, nextRemote(), "Bearer "+s.token)
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
		var body struct {
			Data struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.NotEmpty(t, body.Data.Key)
		key := "ApiKey " + body.Data.Key
		recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), key)
		require.Equal(t, http.StatusOK, recorder.Code, "the key works before: %s", recorder.Body.String())
		return key
	}
	assertKeyRevoked := func(key string, userID uint, what string) {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), key)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, "%s: %s", what, recorder.Body.String())
		var keys []models.APIKey
		require.NoError(t, models.DB.Where("user_id = ?", userID).Find(&keys).Error)
		require.NotEmpty(t, keys, what)
		for _, k := range keys {
			assert.False(t, k.IsActive, "%s: key %d is switched off", what, k.ID)
			assert.NotNil(t, k.RevokedAt, "%s: key %d carries revoked_at", what, k.ID)
		}
	}

	admin := newUser("admin@credential-revocation.test", models.RoleAdmin)
	adminSession := signIn(admin.Email)

	t.Run("administrator_deactivation_then_activation", func(t *testing.T) {
		user := newUser("deactivated@credential-revocation.test", models.RoleSales)
		s := signIn(user.Email)
		key := issueKey(s)

		recorder := scimWiredSend(router, http.MethodPost, fmt.Sprintf("%s/users/%d/deactivate", prefix, user.ID), "", nextRemote(), "Bearer "+adminSession.token)
		require.Equal(t, http.StatusOK, recorder.Code, "an unwired service would answer 500: %s", recorder.Body.String())
		assertKeyRevoked(key, user.ID, "after the deactivation")

		recorder = scimWiredSend(router, http.MethodPost, fmt.Sprintf("%s/users/%d/activate", prefix, user.ID), "", nextRemote(), "Bearer "+adminSession.token)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assertKeyRevoked(key, user.ID, "after the activation")
		recorder = scimWiredSend(router, http.MethodPost, prefix+"/auth/refresh", `{"refresh_token":"`+s.refresh+`"}`, nextRemote(), "")
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the old refresh token stays refused: %s", recorder.Body.String())
		recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+s.token)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the old access token stays refused")
	})

	t.Run("password_change", func(t *testing.T) {
		user := newUser("changer@credential-revocation.test", models.RoleSales)
		s := signIn(user.Email)
		key := issueKey(s)

		recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/change-password",
			`{"current_password":"`+password+`","new_password":"Wired-Changed!-0526x"}`, nextRemote(), "Bearer "+s.token)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assertKeyRevoked(key, user.ID, "after the password change")
	})

	t.Run("scim_delete", func(t *testing.T) {
		recorder := scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+adminSession.token)
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
		var created struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))

		user := newUser("managed@credential-revocation.test", models.RoleSales)
		require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: user.ID, Issuer: issuer, Subject: "sub-0526",
			Source: models.UserIdentitySourceSCIM}).Error)
		key := issueKey(signIn(user.Email))

		recorder = scimWiredSend(router, http.MethodDelete, fmt.Sprintf("/scim/v2/Users/%d", user.ID), "", nextRemote(), "Bearer "+created.Data.Token)
		require.Equal(t, http.StatusNoContent, recorder.Code, "an unwired service would answer 500: %s", recorder.Body.String())
		assertKeyRevoked(key, user.ID, "after the SCIM delete")
	})
}
