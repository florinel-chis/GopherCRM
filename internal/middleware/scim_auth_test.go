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
	newRouter := func(limits *SCIMRateLimits) (*gin.Engine, *int) {
		router := gin.New()
		router.Use(RequestID(), Logger())
		reached := 0
		group := router.Group("/scim/v2")
		group.Use(SCIMAuth(tokens, audit, limits))
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

	t.Run("the throttles log no token", func(t *testing.T) {
		limited, limitedReached := newRouter(newSCIMRateLimits(1, 1, newRateLimitClock().Now))
		require.Equal(t, http.StatusOK, send(limited, "Bearer "+valid, "198.51.100.5:1234").Code)
		assert.Equal(t, http.StatusTooManyRequests, send(limited, "Bearer "+valid, "198.51.100.5:1234").Code)
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusUnauthorized, send(limited, "Bearer scim_"+strings.Repeat("cd", 32), "198.51.100.5:1234").Code)
		}
		assert.Equal(t, http.StatusTooManyRequests, send(limited, "Bearer scim_"+strings.Repeat("cd", 32), "198.51.100.5:1234").Code)
		assert.Len(t, audit.taken(), 5, "the throttled refusal writes no audit row")
		assert.Equal(t, 1, *limitedReached)
		assert.Contains(t, logs.String(), `"scim_token_id":1`)
		assert.Contains(t, logs.String(), `"client_ip":"198.51.100.5"`)
	})

	// No log line carries a presented token or the hash of a stored one.
	captured := logs.String()
	require.NotEmpty(t, captured)
	assert.Contains(t, captured, "Rate limit exceeded", "the loop covers the throttle lines")
	for _, secretValue := range []string{valid, revoked, strings.TrimPrefix(valid, "scim_"),
		utils.HashAPIKeyHMAC(valid, secret), utils.HashAPIKeyHMAC(revoked, secret),
		"Bearer " + valid, "Bearer scim_" + strings.Repeat("cd", 32),
		strings.Repeat("ab", 32), strings.Repeat("cd", 32), strings.Repeat("0", 64)} {
		assert.NotContains(t, captured, secretValue)
	}
}

const scimRateLimitedBody = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"429","detail":"Too many requests. Please try again later."}`

// TestSCIMRateLimits pins the two SCIM limits (#555): accepted requests are
// counted per token, refusals per client address, whatever the method. Both
// limiters share one injected clock, frozen unless a subtest advances it.
func TestSCIMRateLimits(t *testing.T) {
	originalLogger := utils.Logger
	t.Cleanup(func() { utils.Logger = originalLogger })
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	utils.Logger = logger
	gin.SetMode(gin.TestMode)

	const secret = "scim-rate-limit-test-secret-0555-xxxxxxxxxxxx"
	repo := &scimTokenMemoryRepo{}
	tokens := service.NewSCIMTokenService(repo, secret)
	tokenA, _, err := tokens.Create(context.Background(), "A")
	require.NoError(t, err)
	tokenB, _, err := tokens.Create(context.Background(), "B")
	require.NoError(t, err)
	tokenC, _, err := tokens.Create(context.Background(), "C")
	require.NoError(t, err)
	unknown := "Bearer scim_" + strings.Repeat("ef", 32)

	type fixture struct {
		router  *gin.Engine
		audit   *scimAuditRecorder
		reached *int
		clock   *rateLimitClock
	}
	setup := func(t *testing.T, build func(now func() time.Time) *SCIMRateLimits) fixture {
		t.Helper()
		clock := newRateLimitClock()
		audit := &scimAuditRecorder{}
		router := gin.New()
		router.Use(RequestID(), Logger())
		reached := 0
		group := router.Group("/scim/v2")
		group.Use(SCIMAuth(tokens, audit, build(clock.Now)))
		handle := func(c *gin.Context) {
			reached++
			c.Status(http.StatusOK)
		}
		group.GET("/Users", handle)
		group.OPTIONS("/Users", handle)
		return fixture{router: router, audit: audit, reached: &reached, clock: clock}
	}
	defaults := func(now func() time.Time) *SCIMRateLimits { return newSCIMRateLimits(600, 60, now) }
	send := func(f fixture, method, token, remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/scim/v2/Users", nil)
		request.RemoteAddr = remote
		if token != "" {
			if !strings.HasPrefix(token, "Bearer ") {
				token = "Bearer " + token
			}
			request.Header.Set("Authorization", token)
		}
		recorder := httptest.NewRecorder()
		f.router.ServeHTTP(recorder, request)
		return recorder
	}
	assertThrottled := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json"), recorder.Header().Get("Content-Type"))
		assert.JSONEq(t, scimRateLimitedBody, recorder.Body.String())
	}

	t.Run("a provisioning cycle from one address", func(t *testing.T) {
		f := setup(t, defaults)
		for i := 0; i < 60; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "198.51.100.5:1234").Code, "request %d", i+1)
		}
		before := *f.reached
		assertThrottled(t, send(f, http.MethodGet, tokenA, "198.51.100.5:1234"))
		assert.Equal(t, before, *f.reached, "a throttled request reaches no handler")
		assert.Empty(t, f.audit.taken(), "a throttled request writes no audit row")
		f.clock.Advance(time.Second)
		for i := 0; i < 600; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "198.51.100.5:1234").Code, "follow-up request %d", i+1)
			f.clock.Advance(100 * time.Millisecond)
		}
		assert.Equal(t, 660, *f.reached)
	})

	t.Run("counted per token", func(t *testing.T) {
		f := setup(t, defaults)
		for i := 0; i < 60; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "198.51.100.5:1234").Code)
		}
		assertThrottled(t, send(f, http.MethodGet, tokenA, "198.51.100.5:1234"))
		assertThrottled(t, send(f, http.MethodGet, tokenA, "203.0.113.9:1234"))
		assert.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenB, "198.51.100.5:1234").Code)
	})

	t.Run("refusals per address", func(t *testing.T) {
		f := setup(t, defaults)
		for i := 0; i < 5; i++ {
			recorder := send(f, http.MethodGet, unknown, "192.0.2.20:1234")
			require.Equal(t, http.StatusUnauthorized, recorder.Code, "refusal %d", i+1)
			assert.JSONEq(t, scimAuthBody, recorder.Body.String())
		}
		assert.Len(t, f.audit.taken(), 5)
		assertThrottled(t, send(f, http.MethodGet, unknown, "192.0.2.20:1234"))
		assert.Empty(t, f.audit.taken(), "the throttled refusal writes no audit row")
		assert.Zero(t, *f.reached)
		assert.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "192.0.2.20:1234").Code)
		assert.Equal(t, http.StatusUnauthorized, send(f, http.MethodGet, unknown, "192.0.2.21:1234").Code)
		assert.Len(t, f.audit.taken(), 1)
	})

	t.Run("every method counts", func(t *testing.T) {
		f := setup(t, defaults)
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusUnauthorized, send(f, http.MethodOptions, "", "192.0.2.40:1234").Code, "OPTIONS refusal %d", i+1)
		}
		assert.Len(t, f.audit.taken(), 5)
		assertThrottled(t, send(f, http.MethodOptions, "", "192.0.2.40:1234"))
		assert.Empty(t, f.audit.taken(), "the throttled OPTIONS refusal writes no audit row")
		assert.Zero(t, *f.reached)

		g := setup(t, func(now func() time.Time) *SCIMRateLimits { return newSCIMRateLimits(60, 60, now) })
		for i := 0; i < 59; i++ {
			require.Equal(t, http.StatusOK, send(g, http.MethodGet, tokenC, "192.0.2.41:1234").Code, "GET %d", i+1)
		}
		require.Equal(t, http.StatusOK, send(g, http.MethodOptions, tokenC, "192.0.2.41:1234").Code)
		assertThrottled(t, send(g, http.MethodGet, tokenC, "192.0.2.41:1234"))
		assert.Equal(t, 60, *g.reached)
	})

	t.Run("accepted requests do not spend the refusal budget", func(t *testing.T) {
		f := setup(t, defaults)
		for i := 0; i < 50; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "192.0.2.30:1234").Code)
		}
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusUnauthorized, send(f, http.MethodGet, unknown, "192.0.2.30:1234").Code, "refusal %d", i+1)
		}
		assert.Len(t, f.audit.taken(), 5)
	})

	t.Run("the switches lift both limits", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "true")
		require.Nil(t, NewSCIMRateLimits(600, 60))
		f := setup(t, func(func() time.Time) *SCIMRateLimits { return NewSCIMRateLimits(600, 60) })
		for i := 0; i < 6; i++ {
			require.Equal(t, http.StatusUnauthorized, send(f, http.MethodGet, unknown, "192.0.2.50:1234").Code, "refusal %d", i+1)
		}
		assert.Len(t, f.audit.taken(), 6)
		for i := 0; i < 61; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "192.0.2.50:1234").Code, "request %d", i+1)
		}
	})

	t.Run("out-of-range values take the defaults and caps", func(t *testing.T) {
		f := setup(t, func(now func() time.Time) *SCIMRateLimits { return newSCIMRateLimits(0, 0, now) })
		for i := 0; i < 60; i++ {
			require.Equal(t, http.StatusOK, send(f, http.MethodGet, tokenA, "192.0.2.60:1234").Code, "request %d", i+1)
		}
		assertThrottled(t, send(f, http.MethodGet, tokenA, "192.0.2.60:1234"))

		g := setup(t, func(now func() time.Time) *SCIMRateLimits { return newSCIMRateLimits(-1, 999999, now) })
		for i := 0; i < 1000; i++ {
			require.Equal(t, http.StatusOK, send(g, http.MethodGet, tokenA, "192.0.2.61:1234").Code, "request %d", i+1)
		}
		assertThrottled(t, send(g, http.MethodGet, tokenA, "192.0.2.61:1234"))
	})

	captured := logs.String()
	assert.Contains(t, captured, "Rate limit exceeded")
	for _, token := range []string{tokenA, tokenB, tokenC} {
		for _, secretValue := range []string{token, strings.TrimPrefix(token, "scim_"), utils.HashAPIKeyHMAC(token, secret)} {
			assert.NotContains(t, captured, secretValue)
		}
	}
	assert.NotContains(t, captured, strings.Repeat("ef", 32))
}
