package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// scimTokenTestRepo is an in-memory SCIMTokenRepository.
type scimTokenTestRepo struct {
	mu   sync.Mutex
	rows []models.SCIMToken
}

func (r *scimTokenTestRepo) Create(token *models.SCIMToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token.ID = uint(len(r.rows) + 1)
	r.rows = append(r.rows, *token)
	return nil
}

func (r *scimTokenTestRepo) List() ([]models.SCIMToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]models.SCIMToken, 0, len(r.rows))
	for i := len(r.rows) - 1; i >= 0; i-- {
		out = append(out, r.rows[i])
	}
	return out, nil
}

func (r *scimTokenTestRepo) FindByHash(hash string) (*models.SCIMToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.TokenHash == hash {
			out := row
			return &out, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *scimTokenTestRepo) Revoke(id uint, at time.Time) (*models.SCIMToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rows {
		if r.rows[i].ID == id {
			if r.rows[i].RevokedAt == nil {
				r.rows[i].RevokedAt = &at
			}
			out := r.rows[i]
			return &out, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *scimTokenTestRepo) TouchLastUsed(uint, time.Time) error { return nil }

const scimHandlerTestSecret = "scim-handler-test-secret-0468-xxxxxxxxxxxxxx"

func newSCIMTokenTestRouter(repo *scimTokenTestRepo, caller func(*gin.Context)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	group := router.Group("", func(c *gin.Context) {
		c.Set("user_id", uint(1))
		caller(c)
		c.Next()
	})
	SetupSCIMTokenRoutes(group, NewSCIMTokenHandler(service.NewSCIMTokenService(repo, scimHandlerTestSecret)))
	return router
}

func scimTokenRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

type scimTokenJSON struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func TestSCIMTokenHandler(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	t.Run("admin", func(t *testing.T) {
		repo := &scimTokenTestRepo{}
		router := newSCIMTokenTestRouter(repo, asSystemRole("admin"))

		w := scimTokenRequest(router, http.MethodPost, "/settings/scim/tokens", `{"name":"Entra"}`)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var created struct {
			Data struct {
				Token     string        `json:"token"`
				SCIMToken scimTokenJSON `json:"scim_token"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
		assert.True(t, strings.HasPrefix(created.Data.Token, "scim_"))
		assert.Len(t, created.Data.Token, len("scim_")+64)
		assert.Equal(t, "Entra", created.Data.SCIMToken.Name)
		assert.Nil(t, created.Data.SCIMToken.RevokedAt)
		assert.NotContains(t, w.Body.String(), "hmac$")
		assert.Equal(t, utils.HashAPIKeyHMAC(created.Data.Token, scimHandlerTestSecret), repo.rows[0].TokenHash)

		w = scimTokenRequest(router, http.MethodPost, "/settings/scim/tokens", `{"name":"Okta"}`)
		require.Equal(t, http.StatusCreated, w.Code)

		w = scimTokenRequest(router, http.MethodGet, "/settings/scim/tokens", "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), created.Data.Token)
		assert.NotContains(t, w.Body.String(), "hmac$")
		assert.NotContains(t, w.Body.String(), "token_hash")
		assert.NotContains(t, w.Body.String(), `"token"`)
		var listed struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
		require.Len(t, listed.Data, 2)
		for _, item := range listed.Data {
			keys := make([]string, 0, len(item))
			for key := range item {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, []string{"id", "name", "created_at", "last_used_at", "revoked_at"}, keys)
		}
		assert.JSONEq(t, `2`, string(listed.Data[0]["id"]), "newest first")

		id := created.Data.SCIMToken.ID
		w = scimTokenRequest(router, http.MethodDelete, "/settings/scim/tokens/1", "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var revoked struct {
			Data scimTokenJSON `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &revoked))
		assert.Equal(t, id, revoked.Data.ID)
		require.NotNil(t, revoked.Data.RevokedAt)
		w = scimTokenRequest(router, http.MethodDelete, "/settings/scim/tokens/1", "")
		require.Equal(t, http.StatusOK, w.Code)
		var again struct {
			Data scimTokenJSON `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &again))
		require.NotNil(t, again.Data.RevokedAt)
		assert.True(t, revoked.Data.RevokedAt.Equal(*again.Data.RevokedAt), "a second DELETE keeps revoked_at")

		w = scimTokenRequest(router, http.MethodDelete, "/settings/scim/tokens/99", "")
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, "NOT_FOUND", ssoErrorCode(t, w))

		for _, bad := range []string{"0", "-1", "abc", "1.5", "99999999999"} {
			w = scimTokenRequest(router, http.MethodDelete, "/settings/scim/tokens/"+bad, "")
			assert.Equal(t, http.StatusBadRequest, w.Code, bad)
			assert.Equal(t, "VALIDATION_ERROR", ssoErrorCode(t, w))
			assert.Contains(t, w.Body.String(), `"id"`)
		}

		before := len(repo.rows)
		for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{}`, `{"name":"` + strings.Repeat("n", 101) + `"}`} {
			w = scimTokenRequest(router, http.MethodPost, "/settings/scim/tokens", body)
			assert.Equal(t, http.StatusBadRequest, w.Code, body)
			assert.Equal(t, "VALIDATION_ERROR", ssoErrorCode(t, w))
			var envelope struct {
				Error struct {
					Details map[string]string `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			assert.Contains(t, envelope.Error.Details, "name", body)
		}
		for _, body := range []string{`not json`, `{"name":5}`} {
			w = scimTokenRequest(router, http.MethodPost, "/settings/scim/tokens", body)
			assert.Equal(t, http.StatusBadRequest, w.Code, body)
			assert.Equal(t, "VALIDATION_ERROR", ssoErrorCode(t, w))
		}
		assert.Equal(t, before, len(repo.rows), "no refused request stores a token")
	})

	refused := map[string]func(*gin.Context){
		"sales":    asSystemRole("sales"),
		"support":  asSystemRole("support"),
		"customer": asSystemRole("customer"),
		"custom role without settings.scim_manage": func(c *gin.Context) {
			c.Set("user_role", "custom")
			c.Set(permissions.ContextKey, permissions.NewSet(
				permissions.Grant{Key: permissions.SettingsView, Scope: models.PermissionScopeAll},
				permissions.Grant{Key: permissions.SettingsUpdate, Scope: models.PermissionScopeAll},
				permissions.Grant{Key: permissions.SettingsSSOManage, Scope: models.PermissionScopeAll},
			))
		},
	}
	for name, caller := range refused {
		t.Run("forbidden/"+name, func(t *testing.T) {
			repo := &scimTokenTestRepo{}
			_, _, err := service.NewSCIMTokenService(repo, scimHandlerTestSecret).Create(t.Context(), "Existing")
			require.NoError(t, err)
			router := newSCIMTokenTestRouter(repo, caller)
			for _, req := range []struct{ method, path, body string }{
				{http.MethodGet, "/settings/scim/tokens", ""},
				{http.MethodPost, "/settings/scim/tokens", `{"name":"Entra"}`},
				{http.MethodDelete, "/settings/scim/tokens/1", ""},
			} {
				w := scimTokenRequest(router, req.method, req.path, req.body)
				assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", req.method, req.path)
				assert.Equal(t, "FORBIDDEN", ssoErrorCode(t, w))
			}
			assert.Len(t, repo.rows, 1, "nothing was created")
			assert.Nil(t, repo.rows[0].RevokedAt, "nothing was revoked")
		})
	}

	// The guard reads the injected set only: a published set holding the one
	// key opens the three routes.
	t.Run("a published permission set holding only the SCIM key", func(t *testing.T) {
		repo := &scimTokenTestRepo{}
		router := newSCIMTokenTestRouter(repo, func(c *gin.Context) {
			c.Set("user_role", "custom")
			c.Set(permissions.ContextKey, permissions.NewSet(
				permissions.Grant{Key: permissions.SettingsSCIMManage, Scope: models.PermissionScopeAll}))
		})
		assert.Equal(t, http.StatusCreated, scimTokenRequest(router, http.MethodPost, "/settings/scim/tokens", `{"name":"Entra"}`).Code)
		assert.Equal(t, http.StatusOK, scimTokenRequest(router, http.MethodGet, "/settings/scim/tokens", "").Code)
		assert.Equal(t, http.StatusOK, scimTokenRequest(router, http.MethodDelete, "/settings/scim/tokens/1", "").Code)
	})
}
