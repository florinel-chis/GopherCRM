package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// ssoConfigStore is a ConfigurationService holding the nine SSO rows in
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

// ssoLinkCounter answers CountByIssuer with links and records each issuer.
type ssoLinkCounter struct {
	links   int64
	issuers []string
}

func (c *ssoLinkCounter) CountByIssuer(issuer string) (int64, error) {
	c.issuers = append(c.issuers, issuer)
	return c.links, nil
}

func newSSOSettingsTestRouter(store *ssoConfigStore, caller func(*gin.Context)) *gin.Engine {
	return newSSOSettingsTestRouterWithLinks(store, &ssoLinkCounter{}, caller)
}

func newSSOSettingsTestRouterWithLinks(store *ssoConfigStore, links service.SSOIssuerLinks, caller func(*gin.Context)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewSSOSettingsHandler(service.NewSSOSettingsService(store, ssoRoleRepo{}, links, config.SSOConfig{}))
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
		h := NewSSOSettingsHandler(service.NewSSOSettingsService(store, ssoRoleRepo{}, nil, config.SSOConfig{}))
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

func TestSSOSettingsHandlerIssuerChange(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	const changed = `{"enabled":true,"issuer_url":"https://other-idp.example.com","client_id":"crm","scopes":["openid","email"],"role_mapping":[{"claim":"groups","value":"g-1","role_id":2}]}`

	t.Run("409 without the flag, nothing stored; 200 with it", func(t *testing.T) {
		store := newSSOConfigStore()
		store.values[models.ConfigKeySSOIssuerURL] = "https://idp.example.com"
		before := store.snapshot()
		links := &ssoLinkCounter{links: 2}
		router := newSSOSettingsTestRouterWithLinks(store, links, asSystemRole("admin"))

		w := ssoRequest(router, http.MethodPut, changed)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		var envelope struct {
			Error struct {
				Code    string                 `json:"code"`
				Details map[string]interface{} `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		assert.Equal(t, "ISSUER_CHANGE_UNCONFIRMED", envelope.Error.Code)
		assert.Equal(t, float64(2), envelope.Error.Details["affected_links"], "a JSON number")
		assert.Equal(t, []string{"https://idp.example.com"}, links.issuers, "counted under the current issuer")
		assert.Zero(t, store.writes)
		assert.Equal(t, before, store.snapshot())

		confirmed := changed[:len(changed)-1] + `,"confirm_issuer_change":true}`
		w = ssoRequest(router, http.MethodPut, confirmed)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "https://other-idp.example.com", store.values[models.ConfigKeySSOIssuerURL])
		assert.Len(t, links.issuers, 1, "a confirmed save counts nothing")
	})

	t.Run("400 for a 513-byte issuer", func(t *testing.T) {
		store := newSSOConfigStore()
		issuer := "https://idp.example.com/" + strings.Repeat("a", 513-len("https://idp.example.com/"))
		body := `{"issuer_url":"` + issuer + `","scopes":["openid"]}`
		w := ssoRequest(newSSOSettingsTestRouter(store, asSystemRole("admin")), http.MethodPut, body)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		var envelope struct {
			Error struct {
				Code    string            `json:"code"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		assert.Contains(t, envelope.Error.Details["issuer_url"], "at most 512 bytes")
		assert.Zero(t, store.writes)
	})
}

func TestSSOSettingsHandlerAllowPassword(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	getData := func(t *testing.T, router *gin.Engine) map[string]interface{} {
		t.Helper()
		w := ssoRequest(router, http.MethodGet, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var envelope struct {
			Data map[string]interface{} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		return envelope.Data
	}

	t.Run("GET reports allow_password and its source", func(t *testing.T) {
		store := newSSOConfigStore()
		router := newSSOSettingsTestRouter(store, asSystemRole("admin"))
		data := getData(t, router)
		assert.Equal(t, false, data["allow_password"])
		assert.Equal(t, "default", data["sources"].(map[string]interface{})["allow_password"])

		store.values[models.ConfigKeySSOAllowPassword] = "true"
		data = getData(t, router)
		assert.Equal(t, true, data["allow_password"])
		assert.Equal(t, "settings", data["sources"].(map[string]interface{})["allow_password"])
	})

	t.Run("PUT true and false store the value; null and absent clear it", func(t *testing.T) {
		store := newSSOConfigStore()
		router := newSSOSettingsTestRouter(store, asSystemRole("admin"))

		w := ssoRequest(router, http.MethodPut, `{"allow_password":true}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "true", store.values[models.ConfigKeySSOAllowPassword])
		var envelope struct {
			Data map[string]interface{} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		assert.Equal(t, true, envelope.Data["allow_password"])

		w = ssoRequest(router, http.MethodPut, `{"allow_password":false}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "false", store.values[models.ConfigKeySSOAllowPassword])

		w = ssoRequest(router, http.MethodPut, `{"allow_password":null}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "", store.values[models.ConfigKeySSOAllowPassword])

		store.values[models.ConfigKeySSOAllowPassword] = "true"
		w = ssoRequest(router, http.MethodPut, `{}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "", store.values[models.ConfigKeySSOAllowPassword], "absent clears it too")
		assert.Equal(t, false, getData(t, router)["allow_password"])
	})

	t.Run("PUT with a non-boolean allow_password is refused", func(t *testing.T) {
		store := newSSOConfigStore()
		w := ssoRequest(newSSOSettingsTestRouter(store, asSystemRole("admin")), http.MethodPut, `{"allow_password":"yes"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Zero(t, store.writes)
	})
}

// ssoHandlerTestPair is a generated self-signed certificate and its PKCS#8
// key, in PEM.
type ssoHandlerTestPair struct {
	cert            *x509.Certificate
	certPEM, keyPEM string
}

func newSSOHandlerTestPair(t *testing.T, bits int) ssoHandlerTestPair {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "gophercrm-handler-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return ssoHandlerTestPair{
		cert:    cert,
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		keyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
	}
}

// ssoBodyWithPair is ssoValidBody plus the given certificate fields; a nil
// value leaves the field out.
func ssoBodyWithPair(t *testing.T, certPEM, keyPEM *string) string {
	t.Helper()
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(ssoValidBody), &body))
	if certPEM != nil {
		body["client_certificate"] = *certPEM
	}
	if keyPEM != nil {
		body["client_private_key"] = *keyPEM
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return string(raw)
}

// assertNoPEM checks that a body carries no PEM text and no line of the
// given values.
func assertNoPEM(t *testing.T, body string, values ...string) {
	t.Helper()
	assert.NotContains(t, body, "BEGIN")
	assert.NotContains(t, body, "-----END")
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if len(line) >= 16 && !strings.HasPrefix(line, "-----") {
				assert.NotContains(t, body, line[:16])
			}
		}
	}
}

func TestSSOSettingsHandlerClientCertificate(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	pair := newSSOHandlerTestPair(t, 2048)
	sum := sha1.Sum(pair.cert.Raw)
	thumbprint := strings.ToUpper(hex.EncodeToString(sum[:]))
	str := func(v string) *string { return &v }

	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
		t.Helper()
		var envelope struct {
			Data map[string]interface{} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
		return envelope.Data
	}

	t.Run("PUT a pair, GET reports it without the PEM; absent keeps, empty clears", func(t *testing.T) {
		store := newSSOConfigStore()
		router := newSSOSettingsTestRouter(store, asSystemRole("admin"))

		w := ssoRequest(router, http.MethodGet, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		data := decode(t, w)
		assert.Equal(t, false, data["client_certificate_set"])
		assert.Equal(t, "", data["client_certificate_thumbprint"])
		assert.Equal(t, "", data["client_certificate_expires_at"])
		assert.Equal(t, "default", data["sources"].(map[string]interface{})["client_certificate"])

		w = ssoRequest(router, http.MethodPut, ssoBodyWithPair(t, &pair.certPEM, &pair.keyPEM))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assertNoPEM(t, w.Body.String(), pair.certPEM, pair.keyPEM)
		for _, body := range []string{w.Body.String(), ssoRequest(router, http.MethodGet, "").Body.String()} {
			var envelope struct {
				Data map[string]interface{} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &envelope))
			data := envelope.Data
			assert.Equal(t, true, data["client_certificate_set"])
			assert.Equal(t, thumbprint, data["client_certificate_thumbprint"])
			assert.Equal(t, pair.cert.NotAfter.UTC().Format(time.RFC3339), data["client_certificate_expires_at"])
			assert.Equal(t, "settings", data["sources"].(map[string]interface{})["client_certificate"])
			assert.NotContains(t, data, "client_certificate")
			assert.NotContains(t, data, "client_private_key")
			assertNoPEM(t, body, pair.certPEM, pair.keyPEM)
		}

		w = ssoRequest(router, http.MethodPut, ssoValidBody)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, true, decode(t, w)["client_certificate_set"], "absent keeps the pair")

		w = ssoRequest(router, http.MethodPut, ssoBodyWithPair(t, str(""), str("")))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		data = decode(t, w)
		assert.Equal(t, false, data["client_certificate_set"], "empty clears the pair")
		assert.Equal(t, "", data["client_certificate_thumbprint"])
		assert.Equal(t, "", store.values[models.ConfigKeySSOClientCertificate])
		assert.Equal(t, "", store.values[models.ConfigKeySSOClientPrivateKey])
	})

	small := newSSOHandlerTestPair(t, 1024)
	other := newSSOHandlerTestPair(t, 2048)
	for name, tc := range map[string]struct {
		cert, key *string
		field     string
	}{
		"certificate not PEM":   {str("not a certificate"), &pair.keyPEM, "client_certificate"},
		"key not PEM":           {&pair.certPEM, str("not a key"), "client_private_key"},
		"key under 2048 bits":   {&small.certPEM, &small.keyPEM, "client_private_key"},
		"key does not match":    {&pair.certPEM, &other.keyPEM, "client_private_key"},
		"only the certificate":  {&pair.certPEM, nil, "client_private_key"},
		"only the key":          {nil, &pair.keyPEM, "client_certificate"},
		"certificate too large": {str(pair.certPEM + strings.Repeat(" ", 16385)), &pair.keyPEM, "client_certificate"},
		"key too large":         {&pair.certPEM, str(pair.keyPEM + strings.Repeat(" ", 16385)), "client_private_key"},
	} {
		t.Run("400 "+name, func(t *testing.T) {
			store := newSSOConfigStore()
			store.values[models.ConfigKeySSOClientCertificate] = pair.certPEM
			store.values[models.ConfigKeySSOClientPrivateKey] = pair.keyPEM
			before := store.snapshot()
			w := ssoRequest(newSSOSettingsTestRouter(store, asSystemRole("admin")), http.MethodPut, ssoBodyWithPair(t, tc.cert, tc.key))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Equal(t, utils.ErrCodeValidation, ssoErrorCode(t, w))
			var envelope struct {
				Error struct {
					Details map[string]string `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			assert.Contains(t, envelope.Error.Details, tc.field)
			var submitted []string
			for _, value := range []*string{tc.cert, tc.key} {
				if value != nil {
					submitted = append(submitted, *value)
				}
			}
			assertNoPEM(t, w.Body.String(), submitted...)
			assert.Zero(t, store.writes)
			assert.Equal(t, before, store.snapshot(), "no row changed")
		})
	}
}
