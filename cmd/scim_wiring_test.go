package main

// Issue #468 through the router production uses: setupRouter mounts
// /scim/v2 outside the API prefix, behind its own strict rate tier and the
// SCIM token authentication, which records every refusal in the sign-in audit
// log with method scim. A user's JWT or API key is not a SCIM token, and a
// SCIM token is not a credential of the API. Issue #469 adds GET /Users and
// /Users/:id, with externalId taken from the identities under the effective
// single sign-on issuer (ssoSettings). Issue #470 adds POST /Users and
// PUT /Users/:id, whose role comes from the roles mapping and whose audit
// rows carry method scim. Issue #471 adds PATCH /Users/:id, whose active
// false ends the sessions of the account. Issue #504 adds DELETE /Users/:id,
// the same deactivation, never an erasure. Issue #506 adds the Groups:
// GET /Groups and /Groups/:id, PATCH /Groups/:id and the 405 of POST and
// DELETE. Over a SQLite file opened by the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const scimWiredBody = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication failed"}`

// scimWiredRouter builds the production router over a fresh SQLite file.
func scimWiredRouter(t *testing.T) (*gin.Engine, *automationBoot) {
	t.Helper()
	return scimWiredRouterWith(t, nil)
}

// scimWiredRouterWith is scimWiredRouter with configure applied to the boot
// configuration before setupRouter reads it.
func scimWiredRouterWith(t *testing.T, configure func(*automationBoot)) (*gin.Engine, *automationBoot) {
	t.Helper()
	boot := bootAutomation(t, false)
	boot.cfg.Forms.PublicBaseURL = "https://crm.example.test"
	if configure != nil {
		configure(boot)
	}
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

type scimWiredUser struct {
	ID         string `json:"id"`
	ExternalID string `json:"externalId"`
	UserName   string `json:"userName"`
	Name       struct {
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Type    string `json:"type"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active bool `json:"active"`
	Meta   struct {
		Location string `json:"location"`
	} `json:"meta"`
}

type scimWiredList struct {
	Schemas      []string        `json:"schemas"`
	TotalResults int             `json:"totalResults"`
	ItemsPerPage int             `json:"itemsPerPage"`
	StartIndex   int             `json:"startIndex"`
	Resources    []scimWiredUser `json:"Resources"`
}

func TestSCIMUsersWired(t *testing.T) {
	// The strict SCIM tier stays live: every request below comes from its own
	// client address, so none is throttled.
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		boot.cfg.SSO.IssuerURL = "https://idp.example.test"
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("198.51.100.%d:1234", addr)
	}

	const password = "Wired-Passw0rd!-0469"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0469@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	staff := &models.User{Email: "staff-0469@crm.example.test", Password: string(hash), FirstName: "Stella", LastName: "Staff", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(staff).Error)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: staff.ID, Issuer: "https://idp.example.test", Subject: "sub-469"}).Error)
	customer := &models.User{Email: "customer-0469@crm.example.test", Password: string(hash), FirstName: "Cora", LastName: "Customer", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, models.DB.Create(customer).Error)
	erased := &models.User{Email: "erased-0469@crm.example.test", Password: string(hash), FirstName: "Ernie", LastName: "Erased", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, models.DB.Create(erased).Error)
	erasedEmail := erased.Email
	require.NoError(t, repository.NewUserRepository(models.DB).Delete(context.Background(), erased.ID), "erasure through the real path")

	recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+admin.Email+`","password":"`+password+`"}`, nextRemote(), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+login.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	token := "Bearer " + created.Data.Token
	require.NotEqual(t, "Bearer ", token)

	get := func(t *testing.T, path string, want int) []byte {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodGet, path, "", nextRemote(), token)
		require.Equal(t, want, recorder.Code, "%s: %s", path, recorder.Body.String())
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), path)
		return recorder.Body.Bytes()
	}
	getList := func(t *testing.T, path string) scimWiredList {
		t.Helper()
		var list scimWiredList
		body := get(t, path, http.StatusOK)
		require.NoError(t, json.Unmarshal(body, &list))
		assert.Equal(t, []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, list.Schemas)
		assert.Contains(t, string(body), `"Resources":[`, "Resources is an array")
		return list
	}
	staffID := fmt.Sprint(staff.ID)
	assertStaff := func(t *testing.T, u scimWiredUser) {
		t.Helper()
		assert.Equal(t, staffID, u.ID)
		assert.Equal(t, staff.Email, u.UserName)
		assert.Equal(t, "Stella", u.Name.GivenName)
		assert.Equal(t, "Staff", u.Name.FamilyName)
		require.Len(t, u.Emails, 1)
		assert.Equal(t, staff.Email, u.Emails[0].Value)
		assert.Equal(t, "work", u.Emails[0].Type)
		assert.True(t, u.Emails[0].Primary)
		assert.True(t, u.Active)
		assert.Equal(t, "sub-469", u.ExternalID, "externalId from the identity under the configured issuer")
		assert.Equal(t, "https://crm.example.test/scim/v2/Users/"+staffID, u.Meta.Location)
	}

	t.Run("Entra filter shape", func(t *testing.T) {
		list := getList(t, "/scim/v2/Users?filter=userName+eq+%22"+url.QueryEscape(staff.Email)+"%22")
		assert.Equal(t, 1, list.TotalResults)
		assert.Equal(t, 1, list.StartIndex)
		assert.Equal(t, 1, list.ItemsPerPage)
		require.Len(t, list.Resources, 1)
		assertStaff(t, list.Resources[0])
	})

	t.Run("Okta filter shape", func(t *testing.T) {
		list := getList(t, "/scim/v2/Users?filter=userName%20eq%20%22"+url.PathEscape(staff.Email)+"%22&startIndex=1&count=100")
		assert.Equal(t, 1, list.TotalResults)
		assert.Equal(t, 1, list.StartIndex)
		assert.Equal(t, 1, list.ItemsPerPage)
		require.Len(t, list.Resources, 1)
		assertStaff(t, list.Resources[0])
	})

	t.Run("unknown, customer, erased and local userNames", func(t *testing.T) {
		for _, email := range []string{"nobody-0469@crm.example.test", customer.Email, erasedEmail, admin.Email} {
			list := getList(t, "/scim/v2/Users?filter="+url.QueryEscape(`userName eq "`+email+`"`))
			assert.Equal(t, 0, list.TotalResults, email)
			assert.Empty(t, list.Resources, email)
		}
	})

	t.Run("unfiltered list", func(t *testing.T) {
		list := getList(t, "/scim/v2/Users")
		assert.Equal(t, 1, list.TotalResults, "only the staff user linked under the issuer")
		ids := []string{}
		for _, u := range list.Resources {
			ids = append(ids, u.ID)
		}
		assert.Equal(t, []string{staffID}, ids)
		assert.NotContains(t, ids, fmt.Sprint(admin.ID), "the local admin (no identity link) is not listed")
		assert.NotContains(t, ids, fmt.Sprint(customer.ID))
		assert.NotContains(t, ids, fmt.Sprint(erased.ID))
	})

	t.Run("get", func(t *testing.T) {
		var u scimWiredUser
		require.NoError(t, json.Unmarshal(get(t, "/scim/v2/Users/"+staffID, http.StatusOK), &u))
		assertStaff(t, u)
		for _, id := range []uint{erased.ID, customer.ID, admin.ID} {
			body := get(t, fmt.Sprintf("/scim/v2/Users/%d", id), http.StatusNotFound)
			assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`, string(body))
		}
	})

	t.Run("unsupported filter", func(t *testing.T) {
		body := get(t, "/scim/v2/Users?filter="+url.QueryEscape(`externalId eq "sub-469"`), http.StatusBadRequest)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(body, &doc))
		assert.Equal(t, "invalidFilter", doc["scimType"])
		assert.Equal(t, "400", doc["status"])
	})

	t.Run("no token", func(t *testing.T) {
		recorder := scimWiredSend(router, http.MethodGet, "/scim/v2/Users", "", nextRemote(), "")
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Equal(t, scimWiredBody, recorder.Body.String())
	})
}

// TestSCIMUserWritesWired: through setupRouter a SCIM create answers 201 and a
// replace 200, and both leave their audit rows: a role_audit_log row with
// auth_method scim and actor_user_id NULL, and a successful auth_audit_log
// row with method scim. It fails when cmd/main.go leaves WithSCIMUserWrites
// or the sign-in audit writer out.
func TestSCIMUserWritesWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		roleID := func(key string) uint {
			var role models.Role
			require.NoError(t, models.DB.Where("system_key = ?", key).First(&role).Error)
			return role.ID
		}
		boot.cfg.SSO.IssuerURL = "https://idp.example.test"
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"roles","value":"CRM.Sales","role_id":%d},{"claim":"roles","value":"CRM.Support","role_id":%d}]`,
			roleID("sales"), roleID("support"))
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("203.0.113.%d:1234", 100+addr)
	}

	const password = "Wired-Passw0rd!-0470"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0470@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+admin.Email+`","password":"`+password+`"}`, nextRemote(), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+login.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	require.NotEmpty(t, created.Data.Token)
	token := "Bearer " + created.Data.Token

	recorder = scimWiredSend(router, http.MethodPost, "/scim/v2/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"hire-0470@crm.example.test","externalId":"sub-0470","name":{"givenName":"New","familyName":"Hire"},
		"roles":[{"value":"CRM.Sales"}],"password":"Ignored-Passw0rd!"}`, nextRemote(), token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
	var user scimWiredUser
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &user))
	assert.Equal(t, "sub-0470", user.ExternalID)
	assert.True(t, user.Active)
	assert.Equal(t, "https://crm.example.test/scim/v2/Users/"+user.ID, recorder.Header().Get("Location"))
	assert.Equal(t, user.Meta.Location, recorder.Header().Get("Location"))

	recorder = scimWiredSend(router, http.MethodPut, "/scim/v2/Users/"+user.ID, `{"userName":"hire-0470@crm.example.test",
		"name":{"givenName":"New","familyName":"Hire"},"roles":[{"value":"CRM.Support"}]}`, nextRemote(), token)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var replaced scimWiredUser
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &replaced))
	assert.Equal(t, user.ID, replaced.ID)
	assert.Equal(t, "sub-0470", replaced.ExternalID)

	var stored models.User
	require.NoError(t, models.DB.Where("email = ?", "hire-0470@crm.example.test").First(&stored).Error)
	assert.Equal(t, models.RoleSupport, stored.Role)
	var roleAudits []models.RoleAuditEntry
	require.NoError(t, models.DB.Where("target_user_id = ?", stored.ID).Order("id").Find(&roleAudits).Error)
	require.Len(t, roleAudits, 2, "the create and the role change")
	for _, row := range roleAudits {
		assert.Equal(t, "scim", row.AuthMethod)
		assert.Nil(t, row.ActorUserID)
	}
	var authAudits []models.AuthAuditLog
	require.NoError(t, models.DB.Where("method = ? AND outcome = ? AND user_id = ?", "scim", "success", stored.ID).Find(&authAudits).Error)
	assert.Len(t, authAudits, 2, "one success row per write")
}

// TestSCIMUserPatchWired (#471): through setupRouter a SCIM PATCH in Entra
// ID's form (op "Replace", value "False") deactivates a signed-in account and
// ends its sessions at once: the earlier access token answers 401 on
// GET /users/me and the earlier refresh token 401 on /auth/refresh. Okta's
// path-less form reactivates the account, the old access token stays refused
// and a new sign-in works. It fails when cmd/main.go leaves the refresh token
// repository out of WithSCIMUserWrites (500 on the deactivating PATCH).
func TestSCIMUserPatchWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		var role models.Role
		require.NoError(t, models.DB.Where("system_key = ?", "sales").First(&role).Error)
		boot.cfg.SSO.IssuerURL = "https://idp.example.test"
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"roles","value":"CRM.Sales","role_id":%d}]`, role.ID)
		// The managed account below holds password sessions, so a linked
		// account may sign in with a password here.
		boot.cfg.SSO.AllowPassword, boot.cfg.SSO.AllowPasswordSet = true, true
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("203.0.113.%d:1234", 150+addr)
	}
	type session struct {
		Data struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	login := func(email, password string) *httptest.ResponseRecorder {
		return scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, nextRemote(), "")
	}
	signIn := func(email, password string) session {
		t.Helper()
		recorder := login(email, password)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var s session
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &s))
		require.NotEmpty(t, s.Data.Token)
		require.NotEmpty(t, s.Data.RefreshToken)
		return s
	}
	createUser := func(email, password string, role models.UserRole) *models.User {
		t.Helper()
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		require.NoError(t, err)
		user := &models.User{Email: email, Password: string(hash), FirstName: "Wired", LastName: "User", Role: role, IsActive: true}
		require.NoError(t, models.DB.Create(user).Error)
		return user
	}

	const password = "Wired-Passw0rd!-0471"
	admin := createUser("admin-0471@crm.example.test", password, models.RoleAdmin)
	adminSession := signIn(admin.Email, password)
	recorder := scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+adminSession.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	scimToken := "Bearer " + created.Data.Token

	// A local admin (a password, no identity link) is not changed through
	// SCIM: 404, and its row stays as it was.
	adminBefore := *admin
	require.NoError(t, models.DB.First(&adminBefore, admin.ID).Error)
	recorder = scimWiredSend(router, http.MethodPatch, fmt.Sprintf("/scim/v2/Users/%d", admin.ID), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"replace","path":"userName","value":"renamed-0471@crm.example.test"},{"op":"replace","path":"active","value":false}]}`, nextRemote(), scimToken)
	assert.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`, recorder.Body.String())
	var adminAfter models.User
	require.NoError(t, models.DB.First(&adminAfter, admin.ID).Error)
	assert.Equal(t, adminBefore.Email, adminAfter.Email, "the local admin keeps its address")
	assert.True(t, adminAfter.IsActive, "the local admin stays active")
	assert.Equal(t, adminBefore.TokenVersion, adminAfter.TokenVersion)
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+adminSession.Data.Token)
	assert.Equal(t, http.StatusOK, recorder.Code, "the local admin's session still works: %s", recorder.Body.String())

	// A sales account with a password, linked under the issuer so SCIM
	// manages it, signed in twice.
	sales := createUser("sales-0471@crm.example.test", password, models.RoleSales)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: sales.ID, Issuer: "https://idp.example.test", Subject: "sub-0471",
		Source: models.UserIdentitySourceSCIM}).Error)
	first, second := signIn(sales.Email, password), signIn(sales.Email, password)
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+first.Data.Token)
	require.Equal(t, http.StatusOK, recorder.Code, "the access token works before: %s", recorder.Body.String())

	path := fmt.Sprintf("/scim/v2/Users/%d", sales.ID)
	recorder = scimWiredSend(router, http.MethodPatch, path, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"Replace","path":"active","value":"False"}]}`, nextRemote(), scimToken)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
	var patched scimWiredUser
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &patched))
	assert.Equal(t, fmt.Sprint(sales.ID), patched.ID)
	assert.False(t, patched.Active)

	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+first.Data.Token)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the earlier access token is refused at once")
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/auth/refresh", `{"refresh_token":"`+first.Data.RefreshToken+`"}`, nextRemote(), "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the earlier refresh token is refused at once")

	recorder = scimWiredSend(router, http.MethodPatch, path, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"replace","value":{"active":true}}]}`, nextRemote(), scimToken)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &patched))
	assert.True(t, patched.Active)

	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+first.Data.Token)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the access token issued before the deactivation stays refused")
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/auth/refresh", `{"refresh_token":"`+second.Data.RefreshToken+`"}`, nextRemote(), "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the refresh token issued before the deactivation stays refused")
	fresh := signIn(sales.Email, password)
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+fresh.Data.Token)
	assert.Equal(t, http.StatusOK, recorder.Code, "a new sign-in works: %s", recorder.Body.String())

	var stored models.User
	require.NoError(t, models.DB.First(&stored, sales.ID).Error)
	assert.True(t, stored.IsActive)
	assert.Equal(t, uint(1), stored.TokenVersion, "the deactivation moved the generation once")
	var successes int64
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ? AND outcome = ? AND user_id = ?", "scim", "success", sales.ID).Count(&successes).Error)
	assert.Equal(t, int64(2), successes, "one success row per patch")
}

func TestSCIMUserDeleteWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		var role models.Role
		require.NoError(t, models.DB.Where("system_key = ?", "sales").First(&role).Error)
		boot.cfg.SSO.IssuerURL = "https://idp.example.test"
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"roles","value":"CRM.Sales","role_id":%d}]`, role.ID)
		// The managed account below holds password sessions, so a linked
		// account may sign in with a password here.
		boot.cfg.SSO.AllowPassword, boot.cfg.SSO.AllowPasswordSet = true, true
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("203.0.113.%d:1234", 200+addr)
	}
	type session struct {
		Data struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	const password = "Wired-Passw0rd!-0504"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	signIn := func(email string) session {
		t.Helper()
		recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, nextRemote(), "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var s session
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &s))
		require.NotEmpty(t, s.Data.Token)
		require.NotEmpty(t, s.Data.RefreshToken)
		return s
	}

	admin := &models.User{Email: "admin-0504@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	adminSession := signIn(admin.Email)
	recorder := scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+adminSession.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	scimToken := "Bearer " + created.Data.Token

	// A local admin (a password, no identity link) is not deleted through
	// SCIM: 404, and its row stays as it was.
	recorder = scimWiredSend(router, http.MethodDelete, fmt.Sprintf("/scim/v2/Users/%d", admin.ID), "", nextRemote(), scimToken)
	assert.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`, recorder.Body.String())
	var adminAfter models.User
	require.NoError(t, models.DB.First(&adminAfter, admin.ID).Error)
	assert.True(t, adminAfter.IsActive, "the local admin stays active")
	assert.Equal(t, admin.TokenVersion, adminAfter.TokenVersion, "token_version is unchanged")
	assert.Equal(t, admin.Email, adminAfter.Email)
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+adminSession.Data.Token)
	assert.Equal(t, http.StatusOK, recorder.Code, "the local admin's session still works: %s", recorder.Body.String())

	// A sales account with a password, linked under the issuer so SCIM
	// manages it, as in TestSCIMUserPatchWired, so it holds a session.
	sales := &models.User{Email: "sales-0504@crm.example.test", Password: string(hash), FirstName: "Wired", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(sales).Error)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: sales.ID, Issuer: "https://idp.example.test", Subject: "sub-0504",
		Source: models.UserIdentitySourceSCIM}).Error)
	user := struct{ ID, UserName string }{fmt.Sprint(sales.ID), sales.Email}
	hire := signIn(user.UserName)
	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+hire.Data.Token)
	require.Equal(t, http.StatusOK, recorder.Code, "the access token works before: %s", recorder.Body.String())

	stored := func() models.User {
		t.Helper()
		var u models.User
		require.NoError(t, models.DB.Unscoped().Where("id = ?", user.ID).First(&u).Error)
		return u
	}
	successes := func() int64 {
		t.Helper()
		var n int64
		require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ? AND outcome = ? AND user_id = ?", "scim", "success", user.ID).Count(&n).Error)
		return n
	}
	before := stored()
	require.True(t, before.IsActive)

	path := "/scim/v2/Users/" + user.ID
	for name, authorization := range map[string]string{"no header": "", "unknown token": "Bearer scim_unknown0504"} {
		recorder = scimWiredSend(router, http.MethodDelete, path, "", nextRemote(), authorization)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, name)
		assert.Equal(t, scimWiredBody, recorder.Body.String(), name)
		after := stored()
		assert.True(t, after.IsActive, "%s: the user stays active", name)
		assert.Equal(t, before.TokenVersion, after.TokenVersion, "%s: token_version is unchanged", name)
	}

	recorder = scimWiredSend(router, http.MethodDelete, path, "", nextRemote(), scimToken)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.Empty(t, recorder.Body.String())
	after := stored()
	assert.False(t, after.IsActive)
	assert.False(t, after.DeletedAt.Valid, "a delete never erases")
	assert.Equal(t, before.Email, after.Email)
	assert.Equal(t, before.TokenVersion+1, after.TokenVersion)

	recorder = scimWiredSend(router, http.MethodGet, prefix+"/users/me", "", nextRemote(), "Bearer "+hire.Data.Token)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the earlier access token is refused at once")
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/auth/refresh", `{"refresh_token":"`+hire.Data.RefreshToken+`"}`, nextRemote(), "")
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "the earlier refresh token is refused at once")

	audits := successes()
	recorder = scimWiredSend(router, http.MethodDelete, path, "", nextRemote(), scimToken)
	require.Equal(t, http.StatusNoContent, recorder.Code, "a repeat answers 204: %s", recorder.Body.String())
	assert.Equal(t, audits, successes(), "a repeat writes no audit row")
	assert.Equal(t, after.TokenVersion, stored().TokenVersion)

	recorder = scimWiredSend(router, http.MethodDelete, "/scim/v2/Users/999999", "", nextRemote(), scimToken)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`, recorder.Body.String())
}

// TestSCIMGroupsWired: issue #506 through the production router. GET
// /Groups and PATCH /Groups/:id sit behind the SCIM token authentication
// (each refusal writes one scim audit row); with a valid token the list is
// exactly the groups-claim values of the role mapping, a members add changes
// the role, and POST and DELETE answer a SCIM 405.
func TestSCIMGroupsWired(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	var adminRole, salesRole models.Role
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		require.NoError(t, models.DB.Where("system_key = ?", "admin").First(&adminRole).Error)
		require.NoError(t, models.DB.Where("system_key = ?", "sales").First(&salesRole).Error)
		boot.cfg.SSO.IssuerURL = "https://idp.example.test"
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"groups","value":"Admins","role_id":%d},{"claim":"groups","value":"Sales","role_id":%d},{"claim":"roles","value":"CRM.Sales","role_id":%d}]`,
			adminRole.ID, salesRole.ID, salesRole.ID)
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("198.51.100.%d:1234", 10+addr)
	}
	const password = "Wired-Passw0rd!-0506"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0506@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+admin.Email+`","password":"`+password+`"}`, nextRemote(), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+login.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	scimToken := "Bearer " + created.Data.Token

	member := &models.User{Email: "member-0506@crm.example.test", Password: "x", FirstName: "Group", LastName: "Member", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(member).Error)
	require.NoError(t, models.DB.Create(&models.UserIdentity{UserID: member.ID, Issuer: "https://idp.example.test", Subject: "sub-0506",
		Source: models.UserIdentitySourceSCIM}).Error)
	// base64url of "Admins" and of "Sales".
	const adminsID, salesID = "QWRtaW5z", "U2FsZXM"
	addBody := fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"members","value":[{"value":"%d"}]}]}`, member.ID)

	var lastID uint
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID).Error)
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/scim/v2/Groups", ""},
		{http.MethodPatch, "/scim/v2/Groups/" + adminsID, addBody},
	} {
		for _, tc := range []struct{ name, authorization, reason string }{
			{"no header", "", "token_missing"},
			{"unknown token", "Bearer scim_" + strings.Repeat("ab", 32), "token_invalid"},
		} {
			t.Run(route.method+" "+tc.name, func(t *testing.T) {
				recorder := scimWiredSend(router, route.method, route.path, route.body, nextRemote(), tc.authorization)
				assert.Equal(t, http.StatusUnauthorized, recorder.Code)
				assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
				assert.Equal(t, scimWiredBody, recorder.Body.String())
				var rows []models.AuthAuditLog
				require.NoError(t, models.DB.Where("id > ?", lastID).Order("id").Find(&rows).Error)
				require.Len(t, rows, 1, "exactly one audit row")
				lastID = rows[0].ID
				assert.Equal(t, "scim", rows[0].Method)
				assert.Equal(t, "refused", rows[0].Outcome)
				assert.Equal(t, tc.reason, rows[0].Reason)
			})
		}
	}
	var stored models.User
	require.NoError(t, models.DB.First(&stored, member.ID).Error)
	assert.Equal(t, models.RoleSales, stored.Role, "a refused patch changes nothing")

	recorder = scimWiredSend(router, http.MethodGet, "/scim/v2/Groups", "", nextRemote(), scimToken)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
	var list struct {
		TotalResults int `json:"totalResults"`
		Resources    []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"Resources"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &list))
	assert.Equal(t, 2, list.TotalResults)
	require.Len(t, list.Resources, 2)
	assert.Equal(t, []string{"Admins", "Sales"}, []string{list.Resources[0].DisplayName, list.Resources[1].DisplayName})
	assert.Equal(t, []string{adminsID, salesID}, []string{list.Resources[0].ID, list.Resources[1].ID})

	// The local admin (a password, no identity link) is refused as a member
	// like an unknown id, and nothing is written.
	var membersBefore int64
	require.NoError(t, models.DB.Model(&models.SCIMGroupMember{}).Count(&membersBefore).Error)
	recorder = scimWiredSend(router, http.MethodPatch, "/scim/v2/Groups/"+adminsID,
		fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"members","value":[{"value":"%d"}]}]}`, admin.ID),
		nextRemote(), scimToken)
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &refusal))
	assert.Equal(t, "invalidValue", refusal["scimType"])
	var membersAfter int64
	require.NoError(t, models.DB.Model(&models.SCIMGroupMember{}).Count(&membersAfter).Error)
	assert.Equal(t, membersBefore, membersAfter, "no membership row is written")

	recorder = scimWiredSend(router, http.MethodPatch, "/scim/v2/Groups/"+adminsID, addBody, nextRemote(), scimToken)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"members":[{"value":"%d"}]`, member.ID))
	require.NoError(t, models.DB.First(&stored, member.ID).Error)
	assert.Equal(t, models.RoleAdmin, stored.Role, "the add maps the groups claim")
	var roleAudits []models.RoleAuditEntry
	require.NoError(t, models.DB.Where("target_user_id = ?", member.ID).Find(&roleAudits).Error)
	require.Len(t, roleAudits, 1)
	assert.Equal(t, "scim", roleAudits[0].AuthMethod)
	assert.Nil(t, roleAudits[0].ActorUserID)
	var successes int64
	require.NoError(t, models.DB.Model(&models.AuthAuditLog{}).Where("method = ? AND outcome = ? AND user_id = ?", "scim", "success", member.ID).Count(&successes).Error)
	assert.Equal(t, int64(1), successes)

	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodPost, "/scim/v2/Groups", "GET"},
		{http.MethodDelete, "/scim/v2/Groups/" + adminsID, "GET, PATCH"},
	} {
		recorder = scimWiredSend(router, tc.method, tc.path, `{"displayName":"New"}`, nextRemote(), scimToken)
		assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code, tc.method)
		assert.Equal(t, tc.allow, recorder.Header().Get("Allow"), tc.method)
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), tc.method)
		assert.Contains(t, recorder.Body.String(), `"status":"405"`, tc.method)
	}
	recorder = scimWiredSend(router, http.MethodPut, "/scim/v2/Groups/"+adminsID, `{}`, nextRemote(), scimToken)
	assert.Equal(t, http.StatusNotFound, recorder.Code, "PUT /Groups/:id is not routed")
}
