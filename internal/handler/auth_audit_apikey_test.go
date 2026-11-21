package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #551: POST /api-keys (201), a revocation through DELETE /api-keys/:id or
// PUT /api-keys/:id with is_active false (200), and a refused PUT with
// is_active true on a revoked key (409) each write one auth_audit_log row
// naming the caller and the key id. Every other answer writes none, and the
// answers are the same with or without the audit writer.

const (
	akaRequestID = "req-551"
	akaUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0"
	akaRemote    = "192.0.2.10:4321"
	akaNetwork   = "192.0.2.0/24"
	akaFamily    = "Firefox on Linux"
)

// Request headers the test caller middleware reads.
const (
	akaUserHeader  = "X-Test-User"
	akaAuthHeader  = "X-Test-Auth"
	akaStaleHeader = "X-Test-Stale-Sign-In"
)

// failingAuthAuditRepo refuses every write.
type failingAuthAuditRepo struct {
	repository.AuthAuditRepository
}

func (failingAuthAuditRepo) Create(*models.AuthAuditLog) error {
	return errors.New("audit store unavailable")
}

// revokeBeforeRenameRepo commits a revocation of the key just before the
// service's conditional rename, the race of a PUT that read the key live.
type revokeBeforeRenameRepo struct {
	repository.APIKeyRepository
	armed bool
}

func (r *revokeBeforeRenameRepo) Rename(id, userID uint, name string, liveOnly bool) (int64, error) {
	if r.armed {
		r.armed = false
		if _, err := r.APIKeyRepository.Revoke(id, userID, time.Now().UTC()); err != nil {
			return 0, err
		}
	}
	return r.APIKeyRepository.Rename(id, userID, name, liveOnly)
}

type akaEnv struct {
	db      *gorm.DB
	router  *gin.Engine
	keys    *revokeBeforeRenameRepo
	owner   uint
	other   uint
	otherID uint
}

// newAKAEnv builds an in-memory SQLite database, the real API key service
// and, unless audit is nil, the handler with that audit writer.
func newAKAEnv(t *testing.T, audit func(db *gorm.DB) service.AuthAuditService) *akaEnv {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	env := &akaEnv{db: db}
	for i, email := range []string{"owner@aka.test", "other@aka.test"} {
		user := &models.User{Email: email, Password: "x", FirstName: "Key", LastName: fmt.Sprint(i),
			Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		if i == 0 {
			env.owner = user.ID
		} else {
			env.other = user.ID
		}
	}

	env.keys = &revokeBeforeRenameRepo{APIKeyRepository: repository.NewAPIKeyRepository(db)}
	svc := service.NewAPIKeyService(env.keys, repository.NewUserRepository(db), utils.NewTransactionManager(db),
		"aka-secret-0123456789abcdef0123456789")
	_, otherKey, err := svc.Generate(env.other, "Other key", nil)
	require.NoError(t, err)
	env.otherID = otherKey.ID

	h := NewAPIKeyHandler(svc)
	if audit != nil {
		h = h.WithAudit(audit(db))
	}
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		userID := env.owner
		if c.GetHeader(akaUserHeader) == "other" {
			userID = env.other
		}
		c.Set("user_id", userID)
		c.Set("user_role", models.RoleSales)
		if c.GetHeader(akaAuthHeader) == "api_key" {
			c.Set(service.AuthMethodContextKey, service.AuthMethodAPIKey)
		} else {
			c.Set(service.AuthMethodContextKey, service.AuthMethodBearer)
		}
		signedIn := time.Now()
		if c.GetHeader(akaStaleHeader) != "" {
			signedIn = signedIn.Add(-time.Hour)
		}
		c.Set(service.SignedInAtContextKey, signedIn)
		c.Next()
	})
	router.POST("/api-keys", h.Create)
	router.GET("/api-keys", h.List)
	router.GET("/api-keys/:id", h.Get)
	router.PUT("/api-keys/:id", h.Update)
	router.DELETE("/api-keys/:id", h.Revoke)
	env.router = router
	return env
}

func realAuthAudit(db *gorm.DB) service.AuthAuditService {
	return service.NewAuthAuditService(repository.NewAuthAuditRepository(db))
}

func (e *akaEnv) do(t *testing.T, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", akaRequestID)
	req.Header.Set("User-Agent", akaUserAgent)
	req.RemoteAddr = akaRemote
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// create issues a key to the owner and returns its id, its plaintext and the
// stored prefix and hash.
func (e *akaEnv) create(t *testing.T, name string) (uint, string, []string) {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api-keys", `{"name":"`+name+`"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Key    string `json:"key"`
			APIKey struct {
				ID uint `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotZero(t, body.Data.APIKey.ID)
	require.NotEmpty(t, body.Data.Key)
	var stored models.APIKey
	require.NoError(t, e.db.First(&stored, body.Data.APIKey.ID).Error)
	require.NotEmpty(t, stored.Prefix)
	require.NotEmpty(t, stored.KeyHash)
	return body.Data.APIKey.ID, body.Data.Key, []string{stored.Prefix, stored.KeyHash}
}

func (e *akaEnv) count(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&models.AuthAuditLog{}).Count(&n).Error)
	return n
}

func (e *akaEnv) rows(t *testing.T, method string) []models.AuthAuditLog {
	t.Helper()
	var rows []models.AuthAuditLog
	require.NoError(t, e.db.Where("method = ?", method).Order("id").Find(&rows).Error)
	return rows
}

// assertKeyRow checks one row's method, outcome, reason, ids and request facts.
func assertKeyRow(t *testing.T, row models.AuthAuditLog, method, outcome, reason string, userID, keyID uint) {
	t.Helper()
	assert.Equal(t, method, row.Method)
	assert.Equal(t, outcome, row.Outcome)
	assert.Equal(t, reason, row.Reason)
	require.NotNil(t, row.UserID)
	assert.Equal(t, userID, *row.UserID)
	require.NotNil(t, row.APIKeyID)
	assert.Equal(t, keyID, *row.APIKeyID)
	assert.EqualValues(t, 1, row.Attempts)
	assert.Equal(t, akaRequestID, row.RequestID)
	assert.Equal(t, akaNetwork, row.ClientNetwork)
	assert.Equal(t, akaFamily, row.UserAgentFamily)
	assert.Nil(t, row.AttemptID)
	assert.Nil(t, row.SubjectRef)
	assert.Nil(t, row.AggregateKey)
}

// stringColumns returns every string and *string field of row.
func stringColumns(row models.AuthAuditLog) map[string]string {
	out := map[string]string{}
	value := reflect.ValueOf(row)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		switch {
		case field.Kind() == reflect.String:
			out[value.Type().Field(i).Name] = field.String()
		case field.Kind() == reflect.Ptr && field.Type().Elem().Kind() == reflect.String && !field.IsNil():
			out[value.Type().Field(i).Name] = field.Elem().String()
		}
	}
	return out
}

func TestAuthAuditAPIKeyChanges(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		env := newAKAEnv(t, realAuthAudit)
		const name = "Pipeline 551 key"
		id, plaintext, stored := env.create(t, name)

		rows := env.rows(t, service.AuthAuditMethodAPIKeyCreate)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 1, env.count(t), "exactly one row")
		assertKeyRow(t, rows[0], "api_key_create", "success", "", env.owner, id)
		forbidden := append([]string{plaintext, strings.TrimPrefix(plaintext, "gcrm_"), name}, stored...)
		for column, value := range stringColumns(rows[0]) {
			for _, secret := range forbidden {
				assert.NotContains(t, value, secret, "column %s holds no key material and no key name", column)
			}
		}
	})

	t.Run("revoke_delete", func(t *testing.T) {
		env := newAKAEnv(t, realAuthAudit)
		id, _, _ := env.create(t, "Delete me")
		before := env.count(t)
		w := env.do(t, http.MethodDelete, fmt.Sprintf("/api-keys/%d", id), "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, before+1, env.count(t))
		rows := env.rows(t, service.AuthAuditMethodAPIKeyRevoke)
		require.Len(t, rows, 1)
		assertKeyRow(t, rows[0], "api_key_revoke", "success", "", env.owner, id)

		// An idempotent repeat is a 200 and writes a row too.
		w = env.do(t, http.MethodDelete, fmt.Sprintf("/api-keys/%d", id), "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, before+2, env.count(t))
	})

	t.Run("revoke_put_false", func(t *testing.T) {
		env := newAKAEnv(t, realAuthAudit)
		id, _, _ := env.create(t, "Switch me off")
		before := env.count(t)
		w := env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":false}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, before+1, env.count(t))
		rows := env.rows(t, service.AuthAuditMethodAPIKeyRevoke)
		require.Len(t, rows, 1)
		assertKeyRow(t, rows[0], "api_key_revoke", "success", "", env.owner, id)

		// A name and is_active false together write exactly one row.
		second, _, _ := env.create(t, "Rename and revoke")
		before = env.count(t)
		w = env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", second), `{"name":"Renamed key","is_active":false}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, before+1, env.count(t), "exactly one row")
		rows = env.rows(t, service.AuthAuditMethodAPIKeyRevoke)
		require.Len(t, rows, 2)
		assertKeyRow(t, rows[1], "api_key_revoke", "success", "", env.owner, second)
	})

	t.Run("enable_revoked_refused", func(t *testing.T) {
		env := newAKAEnv(t, realAuthAudit)
		assertRefused := func(t *testing.T, w *httptest.ResponseRecorder, id uint, wantRows int) {
			t.Helper()
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "CONFLICT", body.Error.Code)
			assert.Equal(t, "A revoked API key cannot be switched on again", body.Error.Message)
			var stored models.APIKey
			require.NoError(t, env.db.First(&stored, id).Error)
			assert.False(t, stored.IsActive, "the key stays revoked")
			assert.NotNil(t, stored.RevokedAt)
			rows := env.rows(t, service.AuthAuditMethodAPIKeyEnable)
			require.Len(t, rows, wantRows)
			assertKeyRow(t, rows[wantRows-1], "api_key_enable", "refused", "key_revoked", env.owner, id)
		}

		id, _, _ := env.create(t, "Revoked key")
		require.Equal(t, http.StatusOK, env.do(t, http.MethodDelete, fmt.Sprintf("/api-keys/%d", id), "").Code)
		before := env.count(t)
		w := env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":true}`)
		assertRefused(t, w, id, 1)
		assert.Equal(t, before+1, env.count(t))

		// The key was live when the PUT read it and revoked before its
		// conditional rename: the same 409 and one refused row.
		raced, _, _ := env.create(t, "Raced key")
		before = env.count(t)
		env.keys.armed = true
		w = env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", raced), `{"name":"Raced rename","is_active":true}`)
		assert.False(t, env.keys.armed, "the revocation ran before the rename")
		assertRefused(t, w, raced, 2)
		assert.Equal(t, before+1, env.count(t))
	})

	t.Run("no_row", func(t *testing.T) {
		env := newAKAEnv(t, realAuthAudit)
		id, _, _ := env.create(t, "Live key")
		other := fmt.Sprintf("/api-keys/%d", env.otherID)
		own := fmt.Sprintf("/api-keys/%d", id)
		cases := []struct {
			name    string
			method  string
			path    string
			body    string
			status  int
			headers []string
		}{
			{"rename only", http.MethodPut, own, `{"name":"Renamed live key"}`, http.StatusOK, nil},
			{"is_active true on a live key", http.MethodPut, own, `{"is_active":true}`, http.StatusOK, nil},
			{"PUT bad id", http.MethodPut, "/api-keys/abc", `{"is_active":false}`, http.StatusBadRequest, nil},
			{"DELETE bad id", http.MethodDelete, "/api-keys/abc", "", http.StatusBadRequest, nil},
			{"DELETE another user's key", http.MethodDelete, other, "", http.StatusForbidden, nil},
			{"PUT another user's key", http.MethodPut, other, `{"is_active":false}`, http.StatusForbidden, nil},
			{"PUT is_active true from an API key", http.MethodPut, own, `{"is_active":true}`, http.StatusForbidden,
				[]string{akaAuthHeader, "api_key"}},
			{"DELETE unknown id", http.MethodDelete, "/api-keys/999999", "", http.StatusNotFound, nil},
			{"PUT unknown id", http.MethodPut, "/api-keys/999999", `{"is_active":false}`, http.StatusNotFound, nil},
			{"POST from an API key", http.MethodPost, "/api-keys", `{"name":"Not issued"}`, http.StatusForbidden,
				[]string{akaAuthHeader, "api_key"}},
			{"POST after a stale sign-in", http.MethodPost, "/api-keys", `{"name":"Not issued"}`, http.StatusForbidden,
				[]string{akaStaleHeader, "1"}},
			{"POST bad body", http.MethodPost, "/api-keys", `{"name":"x"}`, http.StatusBadRequest, nil},
			{"GET list", http.MethodGet, "/api-keys", "", http.StatusOK, nil},
			{"GET one", http.MethodGet, own, "", http.StatusOK, nil},
		}
		for _, tc := range cases {
			before := env.count(t)
			w := env.do(t, tc.method, tc.path, tc.body, tc.headers...)
			assert.Equal(t, tc.status, w.Code, "%s: %s", tc.name, w.Body.String())
			assert.Equal(t, before, env.count(t), "%s writes no row", tc.name)
		}
		var stored models.APIKey
		require.NoError(t, env.db.First(&stored, id).Error)
		assert.True(t, stored.IsActive, "the live key is still live")
	})

	t.Run("without_audit", func(t *testing.T) {
		env := newAKAEnv(t, nil)
		id, _, _ := env.create(t, "Unaudited key")
		assert.Equal(t, http.StatusOK, env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":false}`).Code)
		assert.Equal(t, http.StatusOK, env.do(t, http.MethodDelete, fmt.Sprintf("/api-keys/%d", id), "").Code)
		assert.Equal(t, http.StatusConflict, env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":true}`).Code)
		assert.Zero(t, env.count(t))
	})

	t.Run("audit_failure", func(t *testing.T) {
		env := newAKAEnv(t, func(*gorm.DB) service.AuthAuditService {
			return service.NewAuthAuditService(failingAuthAuditRepo{})
		})
		id, _, _ := env.create(t, "Failing audit key")
		assert.Equal(t, http.StatusOK, env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":false}`).Code)
		assert.Equal(t, http.StatusOK, env.do(t, http.MethodDelete, fmt.Sprintf("/api-keys/%d", id), "").Code)
		w := env.do(t, http.MethodPut, fmt.Sprintf("/api-keys/%d", id), `{"is_active":true}`)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "A revoked API key cannot be switched on again")
		assert.Zero(t, env.count(t))
	})
}
