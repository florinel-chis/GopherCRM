package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// scimTokenMemoryRepo is an in-memory SCIMTokenRepository; touchErr makes
// TouchLastUsed fail.
type scimTokenMemoryRepo struct {
	mu       sync.Mutex
	rows     []models.SCIMToken
	touchErr error
}

func (r *scimTokenMemoryRepo) Create(token *models.SCIMToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token.ID = uint(len(r.rows) + 1)
	r.rows = append(r.rows, *token)
	return nil
}

func (r *scimTokenMemoryRepo) List() ([]models.SCIMToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.SCIMToken(nil), r.rows...), nil
}

func (r *scimTokenMemoryRepo) FindByHash(hash string) (*models.SCIMToken, error) {
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

func (r *scimTokenMemoryRepo) Revoke(id uint, at time.Time) (*models.SCIMToken, error) {
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

func (r *scimTokenMemoryRepo) TouchLastUsed(id uint, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.touchErr != nil {
		return r.touchErr
	}
	for i := range r.rows {
		if r.rows[i].ID == id {
			r.rows[i].LastUsedAt = &at
		}
	}
	return nil
}

// scimAuditRecorder records the entries of Record.
type scimAuditRecorder struct {
	service.AuthAuditService
	mu      sync.Mutex
	entries []service.AuthAuditEntry
	ctxs    []context.Context
}

func (r *scimAuditRecorder) Record(ctx context.Context, entry service.AuthAuditEntry) uint {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	r.ctxs = append(r.ctxs, ctx)
	return uint(len(r.entries))
}

func (r *scimAuditRecorder) taken() []service.AuthAuditEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.entries
	r.entries, r.ctxs = nil, nil
	return out
}

const scimAuthBody = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication failed"}`

func TestSCIMAuth(t *testing.T) {
	originalLogger := utils.Logger
	t.Cleanup(func() { utils.Logger = originalLogger })
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	utils.Logger = logger
	gin.SetMode(gin.TestMode)

	const secret = "scim-middleware-test-secret-0468-xxxxxxxxxxxx"
	repo := &scimTokenMemoryRepo{}
	tokens := service.NewSCIMTokenService(repo, secret)
	valid, _, err := tokens.Create(context.Background(), "Entra")
	require.NoError(t, err)
	revoked, revokedRow, err := tokens.Create(context.Background(), "Old")
	require.NoError(t, err)
	_, err = tokens.Revoke(context.Background(), revokedRow.ID)
	require.NoError(t, err)

	audit := &scimAuditRecorder{}
	newRouter := func(limiter gin.HandlerFunc) (*gin.Engine, *int) {
		router := gin.New()
		router.Use(RequestID(), Logger())
		reached := 0
		group := router.Group("/scim/v2")
		if limiter != nil {
			group.Use(limiter)
		}
		group.Use(SCIMAuth(tokens, audit))
		group.GET("/ServiceProviderConfig", func(c *gin.Context) {
			reached++
			id, _ := c.Get(SCIMTokenIDContextKey)
			c.JSON(http.StatusOK, gin.H{"token_id": id})
		})
		return router, &reached
	}
	send := func(router *gin.Engine, authorization, remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
		request.RemoteAddr = remote
		request.Header.Set("X-Request-ID", "req-468")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	router, reached := newRouter(nil)
	for _, tc := range []struct {
		name, header, reason string
	}{
		{"no header", "", service.AuthAuditReasonTokenMissing},
		{"another scheme", "ApiKey " + valid, service.AuthAuditReasonTokenMissing},
		{"empty bearer", "Bearer ", service.AuthAuditReasonTokenMissing},
		{"unknown token", "Bearer scim_" + strings.Repeat("ab", 32), service.AuthAuditReasonTokenInvalid},
		{"upper-cased token", "Bearer " + strings.ToUpper(valid), service.AuthAuditReasonTokenInvalid},
		{"trailing space", "Bearer " + valid + " ", service.AuthAuditReasonTokenInvalid},
		{"revoked token", "Bearer " + revoked, service.AuthAuditReasonTokenRevoked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := send(router, tc.header, "192.0.2.10:1234")
			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), recorder.Header().Get("Content-Type"))
			assert.JSONEq(t, scimAuthBody, recorder.Body.String())
			assert.Equal(t, scimAuthBody, recorder.Body.String(), "the same bytes for every reason")
			entries := audit.taken()
			require.Len(t, entries, 1, "one audit row per refusal")
			assert.Equal(t, service.AuthAuditEntry{Method: "scim", Outcome: "refused", Reason: tc.reason}, entries[0])
		})
	}
	assert.Zero(t, *reached, "a refusal reaches no handler")

	t.Run("audit context", func(t *testing.T) {
		send(router, "", "192.0.2.10:1234")
		audit.mu.Lock()
		ctx := audit.ctxs[0]
		audit.mu.Unlock()
		audit.taken()
		assert.Equal(t, "req-468", utils.RequestIDFromContext(ctx))
		assert.Equal(t, "192.0.2.10", utils.ClientInfoFromContext(ctx).IP)
	})

	t.Run("valid token", func(t *testing.T) {
		recorder := send(router, "Bearer "+valid, "192.0.2.10:1234")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.JSONEq(t, `{"token_id":1}`, recorder.Body.String())
		assert.Empty(t, audit.taken(), "a success writes no audit row")
		rows, _ := repo.List()
		assert.NotNil(t, rows[0].LastUsedAt)
	})

	t.Run("a failed last-use write does not change the answer", func(t *testing.T) {
		repo.mu.Lock()
		repo.touchErr = errors.New("write failed for hmac$" + strings.Repeat("0", 64))
		repo.mu.Unlock()
		t.Cleanup(func() { repo.mu.Lock(); repo.touchErr = nil; repo.mu.Unlock() })
		recorder := send(router, "Bearer "+valid, "192.0.2.10:1234")
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, logs.String(), "SCIM token last use could not be recorded")
	})

	t.Run("the strict SCIM tier answers before the authentication", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "")
		limited, limitedReached := newRouter(RateLimitStrictSCIM())
		for i := 0; i < 5; i++ {
			recorder := send(limited, "Bearer "+valid, "198.51.100.5:1234")
			require.Equal(t, http.StatusOK, recorder.Code, "request %d", i+1)
		}
		audit.taken()
		recorder := send(limited, "Bearer scim_"+strings.Repeat("cd", 32), "198.51.100.5:1234")
		assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"))
		assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"429","detail":"Too many requests. Please try again later."}`,
			recorder.Body.String())
		assert.Empty(t, audit.taken(), "nothing reaches the authentication")
		assert.Equal(t, 5, *limitedReached)
	})

	// No log line carries a presented token or the hash of a stored one.
	captured := logs.String()
	require.NotEmpty(t, captured)
	for _, secretValue := range []string{valid, revoked, strings.TrimPrefix(valid, "scim_"),
		utils.HashAPIKeyHMAC(valid, secret), strings.Repeat("ab", 32), strings.Repeat("cd", 32), strings.Repeat("0", 64)} {
		assert.NotContains(t, captured, secretValue)
	}
}
