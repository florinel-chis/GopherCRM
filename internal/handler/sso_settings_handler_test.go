package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ssoConfigStore is a ConfigurationService holding the six SSO rows in
// plaintext; only the reads and SetSSO the SSO service uses are implemented.
type ssoConfigStore struct {
	service.ConfigurationService
	values map[string]string
	writes int
}

func newSSOConfigStore() *ssoConfigStore {
	values := map[string]string{}
	for _, entry := range models.DefaultConfigurations() {
		if models.IsSSOConfigurationKey(entry.Key) {
			values[entry.Key] = ""
		}
	}
	return &ssoConfigStore{values: values}
}

func (s *ssoConfigStore) GetString(key string) (string, error) { return s.values[key], nil }
func (s *ssoConfigStore) GetSecret(key string) (string, error) { return s.values[key], nil }
func (s *ssoConfigStore) SetSSO(key string, value interface{}) error {
	s.writes++
	s.values[key] = value.(string)
	return nil
}

func (s *ssoConfigStore) snapshot() map[string]string {
	out := map[string]string{}
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

type ssoRoleRepo struct {
	repository.RoleRepository
}

func (ssoRoleRepo) GetByID(id uint) (*models.Role, error) {
	if id >= 1 && id <= 4 {
		return &models.Role{ID: id}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

const ssoHandlerTestSecret = "handler-test-client-secret-0434"

// ssoValidBody is an otherwise-valid PUT body.
const ssoValidBody = `{"enabled":true,"issuer_url":"https://idp.example.com","client_id":"crm","client_secret":"` + ssoHandlerTestSecret + `","scopes":["openid","email"],"role_mapping":[{"claim":"groups","value":"g-1","role_id":2}]}`

func newSSOSettingsTestRouter(store *ssoConfigStore, caller func(*gin.Context)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewSSOSettingsHandler(service.NewSSOSettingsService(store, ssoRoleRepo{}, config.SSOConfig{}))
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	group := router.Group("", func(c *gin.Context) {
		c.Set("user_id", uint(1))
		caller(c)
		c.Next()
	})
	SetupSSOSettingsRoutes(group, h)
	return router
}

func asSystemRole(role string) func(*gin.Context) {
	return func(c *gin.Context) { c.Set("user_role", role) }
}

func ssoRequest(router *gin.Engine, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/settings/sso", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func ssoErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), w.Body.String())
	require.NotNil(t, response.Error, w.Body.String())
	return response.Error.Code
}

func TestSSOSettingsHandlerForbidden(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	callers := map[string]func(*gin.Context){
		"sales":    asSystemRole("sales"),
		"support":  asSystemRole("support"),
		"customer": asSystemRole("customer"),
		"custom role with settings.view and settings.update": func(c *gin.Context) {
			c.Set("user_role", "custom")
			c.Set(permissions.ContextKey, permissions.NewSet(
				permissions.Grant{Key: permissions.SettingsView, Scope: models.PermissionScopeAll},
				permissions.Grant{Key: permissions.SettingsUpdate, Scope: models.PermissionScopeAll},
			))
		},
	}
	for name, caller := range callers {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			t.Run(name+" "+method, func(t *testing.T) {
				store := newSSOConfigStore()
				w := ssoRequest(newSSOSettingsTestRouter(store, caller), method, ssoValidBody)
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.Equal(t, utils.ErrCodeForbidden, ssoErrorCode(t, w))
				assert.Zero(t, store.writes)
			})
		}
	}

	// The handler repeats the guard: mounted without the route guard, it
	// still refuses.
	t.Run("handler repeats the guard", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		store := newSSOConfigStore()
		h := NewSSOSettingsHandler(service.NewSSOSettingsService(store, ssoRoleRepo{}, config.SSOConfig{}))
		router := gin.New()
		router.Use(func(c *gin.Context) { c.Set("user_role", "sales"); c.Next() })
		router.GET("/settings/sso", h.Get)
		router.PUT("/settings/sso", h.Update)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			w := ssoRequest(router, method, ssoValidBody)
			assert.Equal(t, http.StatusForbidden, w.Code, method)
		}
		assert.Zero(t, store.writes)
	})
}

func TestSSOSettingsHandlerAdmin(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	t.Run("PUT then GET, the secret never returned", func(t *testing.T) {
		store := newSSOConfigStore()
		router := newSSOSettingsTestRouter(store, asSystemRole("admin"))

		w := ssoRequest(router, http.MethodPut, ssoValidBody)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), ssoHandlerTestSecret)

		w = ssoRequest(router, http.MethodGet, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), ssoHandlerTestSecret)
		var envelope struct {
			Data map[string]interface{} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		assert.Equal(t, true, envelope.Data["client_secret_set"])
		assert.Equal(t, true, envelope.Data["enabled"])
		assert.Equal(t, "https://idp.example.com", envelope.Data["issuer_url"])
		assert.Equal(t, []interface{}{"openid", "email"}, envelope.Data["scopes"])
		assert.Equal(t, []interface{}{map[string]interface{}{"claim": "groups", "value": "g-1", "role_id": float64(2)}}, envelope.Data["role_mapping"])
		assert.NotContains(t, envelope.Data, "client_secret")
		sources := envelope.Data["sources"].(map[string]interface{})
		assert.Equal(t, "settings", sources["client_secret"])
	})

	for name, tc := range map[string]struct {
		body, code string
	}{
		"http issuer": {
			`{"issuer_url":"http://idp.example.com","scopes":["openid"],"role_mapping":[{"claim":"groups","value":"g-1","role_id":2}]}`,
			utils.ErrCodeValidation,
		},
		"claim email": {
			`{"issuer_url":"https://idp.example.com","scopes":["openid"],"role_mapping":[{"claim":"email","value":"g-1","role_id":2}]}`,
			utils.ErrCodeValidation,
		},
		"unknown role id": {
			`{"issuer_url":"https://idp.example.com","scopes":["openid"],"role_mapping":[{"claim":"groups","value":"g-1","role_id":999999}]}`,
			"INVALID_REFERENCE",
		},
		"malformed JSON": {`{"issuer_url":`, utils.ErrCodeValidation},
		"wrong type":     {`{"enabled":"yes"}`, utils.ErrCodeValidation},
		"negative role":  {`{"role_mapping":[{"claim":"groups","value":"g","role_id":-1}]}`, utils.ErrCodeValidation},
	} {
		t.Run("400 "+name, func(t *testing.T) {
			store := newSSOConfigStore()
			store.values[models.ConfigKeySSOClientID] = "kept"
			before := store.snapshot()
			w := ssoRequest(newSSOSettingsTestRouter(store, asSystemRole("admin")), http.MethodPut, tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Equal(t, tc.code, ssoErrorCode(t, w))
			assert.Zero(t, store.writes)
			assert.Equal(t, before, store.snapshot(), "no row changed")
		})
	}
}
