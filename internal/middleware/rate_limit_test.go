package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rateLimitClock is a deterministic clock for expiry and refill tests.
type rateLimitClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRateLimitClock() *rateLimitClock {
	return &rateLimitClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *rateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rateLimitClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func initRateLimitTestLogger() {
	gin.SetMode(gin.TestMode)
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
}

// newRateLimitTestRouter mounts limiter on /limited for every method and counts
// the requests that reach the downstream handler.
func newRateLimitTestRouter(t testing.TB, limiter gin.HandlerFunc, trustedProxies []string) (*gin.Engine, *atomic.Int64) {
	t.Helper()
	initRateLimitTestLogger()
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(trustedProxies))
	var reached atomic.Int64
	router.Any("/limited", limiter, func(c *gin.Context) {
		reached.Add(1)
		c.Status(http.StatusNoContent)
	})
	return router, &reached
}

func sendRateLimitRequest(router http.Handler, method, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/limited", nil)
	request.RemoteAddr = remoteAddr
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func requireStatuses(t *testing.T, router http.Handler, remoteAddr string, headers map[string]string, statuses ...int) {
	t.Helper()
	for i, status := range statuses {
		response := sendRateLimitRequest(router, http.MethodGet, remoteAddr, headers)
		require.Equal(t, status, response.Code, "request %d from %s %v", i+1, remoteAddr, headers)
	}
}

func repeatStatus(status, n int) []int {
	statuses := make([]int, n)
	for i := range statuses {
		statuses[i] = status
	}
	return statuses
}

func rateLimitTestIP(i int) string {
	return fmt.Sprintf("10.%d.%d.%d", (i>>16)&255, (i>>8)&255, i&255)
}

func rateLimitTestAddr(i int) string {
	return rateLimitTestIP(i) + ":40000"
}

// trackedVisitors returns the visitor count after checking map/heap consistency.
func trackedVisitors(t *testing.T, rl *rateLimiter) int {
	t.Helper()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	require.Equal(t, len(rl.visitors), len(rl.queue), "map and reclaim queue disagree")
	for i, v := range rl.queue {
		require.Equal(t, i, v.index)
		require.Same(t, v, rl.visitors[v.key])
	}
	return len(rl.visitors)
}

func isTracked(rl *rateLimiter, key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	_, ok := rl.visitors[key]
	return ok
}

func requireTooManyRequestsEnvelope(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	var body struct {
		Success bool `json:"success"`
		Error   *struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.False(t, body.Success)
	require.NotNil(t, body.Error)
	assert.Equal(t, utils.ErrCodeTooManyRequests, body.Error.Code)
	assert.Equal(t, "Too many requests. Please try again later.", body.Error.Message)
	assert.Equal(t, "60s", body.Error.Details["retry_after"])
}

func TestRateLimitCapacity(t *testing.T) {
	t.Run("production RateLimit refuses unseen identities at capacity", func(t *testing.T) {
		t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
		// One token per hour: nothing refills or expires during the test.
		router, _ := newRateLimitTestRouter(t, RateLimit(1.0/3600.0, 3), nil)

		for i := 0; i < rateLimitVisitorCapacity; i++ {
			require.Equal(t, http.StatusNoContent,
				sendRateLimitRequest(router, http.MethodGet, rateLimitTestAddr(i), nil).Code, "visitor %d", i)
		}
		// Repeated overflow: every further unseen identity is throttled, not given a fresh bucket.
		for k := 0; k < 5; k++ {
			requireTooManyRequestsEnvelope(t,
				sendRateLimitRequest(router, http.MethodGet, rateLimitTestAddr(rateLimitVisitorCapacity+k), nil))
		}
		// Existing clients are not evicted and spend only their own remaining tokens.
		requireStatuses(t, router, rateLimitTestAddr(0), nil,
			http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests)
		requireStatuses(t, router, rateLimitTestAddr(rateLimitVisitorCapacity-1), nil,
			http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests)
	})

	t.Run("capacity holds until idle records are safely reclaimed", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(10.0/60.0, 5, rateLimitVisitorCapacity, clock.Now)
		router, reached := newRateLimitTestRouter(t, rl.middleware(), nil)

		for i := 0; i < rateLimitVisitorCapacity; i++ {
			require.Equal(t, rateLimitAllowed, rl.admit(rateLimitTestIP(i)))
		}
		require.Equal(t, rateLimitVisitorCapacity, trackedVisitors(t, rl))

		for k := 0; k < 5; k++ {
			ip := rateLimitTestIP(rateLimitVisitorCapacity + k)
			requireTooManyRequestsEnvelope(t, sendRateLimitRequest(router, http.MethodGet, ip+":1", nil))
			assert.False(t, isTracked(rl, ip), "rejected identity must not be tracked")
		}
		require.Equal(t, rateLimitVisitorCapacity, trackedVisitors(t, rl))
		require.Zero(t, reached.Load(), "rejected requests must not reach the handler")

		// The existing client keeps its remaining four tokens, then is throttled.
		requireStatuses(t, router, rateLimitTestAddr(0), nil,
			append(repeatStatus(http.StatusNoContent, 4), http.StatusTooManyRequests)...)

		// Negative control: buckets have refilled, but nobody has been idle five minutes.
		clock.Advance(rateLimitIdleTimeout - time.Second)
		requireStatuses(t, router, rateLimitTestAddr(rateLimitVisitorCapacity+10), nil, http.StatusTooManyRequests)
		require.Equal(t, rateLimitVisitorCapacity, trackedVisitors(t, rl))

		// Positive control: once idle and full, a new identity is admitted after
		// a bounded amount of reclamation.
		clock.Advance(2 * time.Second)
		newcomer := rateLimitTestIP(rateLimitVisitorCapacity + 11)
		requireStatuses(t, router, newcomer+":1", nil, http.StatusNoContent)
		assert.True(t, isTracked(rl, newcomer))
		tracked := trackedVisitors(t, rl)
		assert.LessOrEqual(t, tracked, rateLimitVisitorCapacity)
		assert.GreaterOrEqual(t, tracked, rateLimitVisitorCapacity-rateLimitReclaimPerRequest+1,
			"one request must reclaim at most %d records", rateLimitReclaimPerRequest)
	})
}

func TestRateLimitExpiry(t *testing.T) {
	t.Run("strict rates refill on the clock and expire after five idle minutes", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(10.0/60.0, 5, rateLimitVisitorCapacity, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)
		client := "192.0.2.10"

		requireStatuses(t, router, client+":1", nil,
			append(repeatStatus(http.StatusNoContent, 5), http.StatusTooManyRequests)...)
		clock.Advance(5 * time.Second)
		requireStatuses(t, router, client+":1", nil, http.StatusTooManyRequests)
		clock.Advance(1500 * time.Millisecond)
		requireStatuses(t, router, client+":1", nil, http.StatusNoContent, http.StatusTooManyRequests)

		clock.Advance(rateLimitIdleTimeout - time.Second)
		requireStatuses(t, router, "192.0.2.11:1", nil, http.StatusNoContent)
		assert.True(t, isTracked(rl, client), "record must survive until the idle period ends")

		clock.Advance(2 * time.Second)
		requireStatuses(t, router, "192.0.2.11:1", nil, http.StatusNoContent)
		assert.False(t, isTracked(rl, client), "idle full record must be reclaimed")

		// The recreated record carries exactly the burst the old one had refilled to.
		requireStatuses(t, router, client+":1", nil,
			append(repeatStatus(http.StatusNoContent, 5), http.StatusTooManyRequests)...)
	})

	t.Run("low-rate custom limiters are not reclaimed before their bucket refills", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(1.0/3600.0, 2, 1, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)
		first, second := "198.51.100.1", "198.51.100.2"

		requireStatuses(t, router, first+":1", nil,
			http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests)

		clock.Advance(10 * time.Minute)
		requireStatuses(t, router, second+":1", nil, http.StatusTooManyRequests)
		requireStatuses(t, router, first+":1", nil, http.StatusTooManyRequests)

		clock.Advance(51 * time.Minute) // one hourly token has refilled
		requireStatuses(t, router, first+":1", nil, http.StatusNoContent, http.StatusTooManyRequests)

		clock.Advance(90 * time.Minute) // idle, but only 1.5 of 2 tokens
		requireStatuses(t, router, second+":1", nil, http.StatusTooManyRequests)
		assert.True(t, isTracked(rl, first))

		clock.Advance(35 * time.Minute) // bucket full
		requireStatuses(t, router, second+":1", nil, http.StatusNoContent)
		assert.False(t, isTracked(rl, first))
		assert.True(t, isTracked(rl, second))
		assert.Equal(t, 1, trackedVisitors(t, rl))
	})

	t.Run("a non-refilling bucket is never reclaimed", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(0, 1, 1, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)

		requireStatuses(t, router, "198.51.100.3:1", nil, http.StatusNoContent, http.StatusTooManyRequests)
		clock.Advance(24 * time.Hour)
		requireStatuses(t, router, "198.51.100.4:1", nil, http.StatusTooManyRequests)
		requireStatuses(t, router, "198.51.100.3:1", nil, http.StatusTooManyRequests)
	})
}

func TestRateLimitLifecycle(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
	initRateLimitTestLogger()

	t.Run("repeated production construction and disposal starts no goroutines", func(t *testing.T) {
		before := runtime.NumGoroutine()
		for i := 0; i < 200; i++ {
			router := gin.New()
			require.NoError(t, router.SetTrustedProxies(nil))
			ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
			router.GET("/strict", RateLimitStrict(), ok)
			router.GET("/moderate", RateLimitModerate(), ok)
			router.GET("/generous", RateLimitGenerous(), ok)
			router.GET("/custom", RateLimit(1, 1), ok)
			for _, path := range []string{"/strict", "/moderate", "/generous", "/custom"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr = "192.0.2.20:1"
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				require.Equal(t, http.StatusNoContent, response.Code)
			}
		}
		// The previous implementation left one ticker goroutine per construction (800 here).
		require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 },
			2*time.Second, 10*time.Millisecond, "goroutines grew from %d to %d", before, runtime.NumGoroutine())
	})

	t.Run("disposed limiter is collected without any shutdown call", func(t *testing.T) {
		var collected atomic.Bool
		func() {
			rl := newRateLimiter(2, 30, rateLimitVisitorCapacity, time.Now)
			runtime.SetFinalizer(rl, func(*rateLimiter) { collected.Store(true) })
			router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)
			requireStatuses(t, router, "192.0.2.21:1", nil, http.StatusNoContent)
		}()
		require.Eventually(t, func() bool {
			runtime.GC()
			return collected.Load()
		}, 5*time.Second, 10*time.Millisecond, "something still references the disposed limiter")
	})
}

func TestRateLimitConcurrency(t *testing.T) {
	t.Run("concurrent same-IP arrivals receive exactly the burst", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(1, 5, rateLimitVisitorCapacity, clock.Now)
		router, reached := newRateLimitTestRouter(t, rl.middleware(), nil)

		var allowed atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < 32; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					if sendRateLimitRequest(router, http.MethodGet, "198.51.100.7:1", nil).Code == http.StatusNoContent {
						allowed.Add(1)
					}
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int64(5), allowed.Load())
		assert.Equal(t, int64(5), reached.Load())
		assert.Equal(t, 1, trackedVisitors(t, rl))
	})

	t.Run("concurrent new-IP arrivals fill exactly the capacity", func(t *testing.T) {
		const capacity = 16
		clock := newRateLimitClock()
		rl := newRateLimiter(1, 5, capacity, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)

		var admitted atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < 64; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				if sendRateLimitRequest(router, http.MethodGet, rateLimitTestAddr(g), nil).Code == http.StatusNoContent {
					admitted.Add(1)
				}
			}(g)
		}
		wg.Wait()
		assert.Equal(t, int64(capacity), admitted.Load())
		assert.Equal(t, capacity, trackedVisitors(t, rl))
	})

	t.Run("churn with clock-driven expiry stays bounded and grants no excess tokens", func(t *testing.T) {
		const (
			capacity = 16
			rps      = 0.1
			burst    = 3
			steps    = 300
			step     = 2 * time.Second
		)
		clock := newRateLimitClock()
		rl := newRateLimiter(rps, burst, capacity, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)
		hot := "203.0.113.50:1"

		var hotAllowed, violations atomic.Int64
		require.Equal(t, http.StatusNoContent, sendRateLimitRequest(router, http.MethodGet, hot, nil).Code)
		hotAllowed.Add(1)

		var done atomic.Bool
		var checker sync.WaitGroup
		checker.Add(1)
		go func() {
			defer checker.Done()
			for !done.Load() {
				rl.mu.Lock()
				if len(rl.visitors) > capacity || len(rl.visitors) != len(rl.queue) {
					violations.Add(1)
				}
				rl.mu.Unlock()
				runtime.Gosched()
			}
		}()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < steps; i++ {
				clock.Advance(step)
				runtime.Gosched()
			}
		}()
		for g := 0; g < 8; g++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					if sendRateLimitRequest(router, http.MethodGet, hot, nil).Code == http.StatusNoContent {
						hotAllowed.Add(1)
					}
				}
			}()
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					sendRateLimitRequest(router, http.MethodGet, rateLimitTestAddr(g*1000+i), nil)
				}
			}(g)
		}
		wg.Wait()
		done.Store(true)
		checker.Wait()

		assert.Zero(t, violations.Load(), "visitor state exceeded capacity or diverged from the queue")
		maxAllowed := int64(burst + rps*(steps*step).Seconds())
		assert.LessOrEqual(t, hotAllowed.Load(), maxAllowed, "hot client exceeded burst plus refill")
		assert.LessOrEqual(t, trackedVisitors(t, rl), capacity)

		// After the churn every record is idle and full, so cleanup admits newcomers
		// until they alone fill the instance again.
		clock.Advance(time.Hour)
		for i := 0; i < capacity; i++ {
			requireStatuses(t, router, rateLimitTestAddr(100000+i), nil, http.StatusNoContent)
		}
		assert.Equal(t, capacity, trackedVisitors(t, rl))
		requireStatuses(t, router, rateLimitTestAddr(100000+capacity), nil, http.StatusTooManyRequests)
	})
}

func TestRateLimitHTTPBehavior(t *testing.T) {
	t.Run("production RateLimit throttles per IP with the standard 429 envelope", func(t *testing.T) {
		t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
		router, reached := newRateLimitTestRouter(t, RateLimit(1.0/3600.0, 2), nil)

		requireStatuses(t, router, "192.0.2.30:1", nil, http.StatusNoContent, http.StatusNoContent)
		requireTooManyRequestsEnvelope(t, sendRateLimitRequest(router, http.MethodPost, "192.0.2.30:2", nil))
		assert.Equal(t, int64(2), reached.Load(), "a throttled request must abort before the handler")

		// A different IP has its own bucket.
		requireStatuses(t, router, "192.0.2.31:1", nil, http.StatusNoContent)
		assert.Equal(t, int64(3), reached.Load())
	})

	t.Run("OPTIONS bypasses throttling without allocating or spending tokens", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(1.0/3600.0, 2, rateLimitVisitorCapacity, clock.Now)
		router, reached := newRateLimitTestRouter(t, rl.middleware(), nil)

		for i := 0; i < 20; i++ {
			require.Equal(t, http.StatusNoContent,
				sendRateLimitRequest(router, http.MethodOptions, "192.0.2.32:1", nil).Code)
		}
		assert.Equal(t, int64(20), reached.Load())
		assert.Zero(t, trackedVisitors(t, rl), "preflight must not allocate visitor state")

		requireStatuses(t, router, "192.0.2.32:1", nil,
			http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests)
		require.Equal(t, http.StatusNoContent,
			sendRateLimitRequest(router, http.MethodOptions, "192.0.2.32:1", nil).Code,
			"preflight is still answered once the client is throttled")
	})
}

func TestRateLimitTrustedProxy(t *testing.T) {
	t.Run("untrusted forwarded headers cannot split one client into many", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(1.0/3600.0, 2, rateLimitVisitorCapacity, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), nil)

		spoofs := []map[string]string{
			{"X-Forwarded-For": "10.0.0.1"},
			{"X-Real-IP": "10.0.0.2"},
			{"X-Forwarded-For": "2001:db8::1"},
		}
		requireStatuses(t, router, "192.0.2.40:1", spoofs[0], http.StatusNoContent)
		requireStatuses(t, router, "192.0.2.40:2", spoofs[1], http.StatusNoContent)
		requireStatuses(t, router, "192.0.2.40:3", spoofs[2], http.StatusTooManyRequests)
		assert.Equal(t, 1, trackedVisitors(t, rl))
		assert.True(t, isTracked(rl, "192.0.2.40"))

		// A direct IPv6 peer is keyed on its own address, not on its spoofed header.
		requireStatuses(t, router, "[2001:db8::99]:443", spoofs[0], http.StatusNoContent)
		assert.True(t, isTracked(rl, "2001:db8::99"))
	})

	t.Run("trusted proxies select the forwarded client, including IPv6", func(t *testing.T) {
		clock := newRateLimitClock()
		rl := newRateLimiter(1.0/3600.0, 2, rateLimitVisitorCapacity, clock.Now)
		router, _ := newRateLimitTestRouter(t, rl.middleware(), []string{"10.0.0.0/8", "fd00::/8"})

		exhausted := []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests}
		requireStatuses(t, router, "10.0.0.5:1", map[string]string{"X-Forwarded-For": "203.0.113.7"}, exhausted...)
		requireStatuses(t, router, "10.0.0.6:1", map[string]string{"X-Forwarded-For": "203.0.113.8"}, http.StatusNoContent)
		requireStatuses(t, router, "10.0.0.5:1", map[string]string{"X-Real-IP": "203.0.113.9"}, http.StatusNoContent)

		requireStatuses(t, router, "[fd00::1]:443", map[string]string{"X-Forwarded-For": "2001:db8::1"}, exhausted...)
		requireStatuses(t, router, "10.0.0.5:1", map[string]string{"X-Forwarded-For": "2001:db8::2"}, http.StatusNoContent)
		// The rightmost untrusted hop is the client; a forged leftmost entry is ignored.
		requireStatuses(t, router, "10.0.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.200, 2001:db8::1"},
			http.StatusTooManyRequests)

		for _, ip := range []string{"203.0.113.7", "203.0.113.8", "203.0.113.9", "2001:db8::1", "2001:db8::2"} {
			assert.True(t, isTracked(rl, ip), "expected client identity %s", ip)
		}
		assert.False(t, isTracked(rl, "10.0.0.5"), "the proxy itself must not be the identity")
		assert.False(t, isTracked(rl, "198.51.100.200"))

		// A peer outside the trusted ranges still cannot spoof through headers.
		requireStatuses(t, router, "192.0.2.41:1", map[string]string{"X-Forwarded-For": "203.0.113.70"}, http.StatusNoContent)
		requireStatuses(t, router, "192.0.2.41:1", map[string]string{"X-Forwarded-For": "203.0.113.71"}, http.StatusNoContent)
		requireStatuses(t, router, "192.0.2.41:1", map[string]string{"X-Forwarded-For": "203.0.113.72"}, http.StatusTooManyRequests)
		assert.False(t, isTracked(rl, "203.0.113.70"))
	})
}

func TestRateLimitTierIsolation(t *testing.T) {
	t.Run("production tiers and constructions keep independent state", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "")
		t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
		initRateLimitTestLogger()
		router := gin.New()
		require.NoError(t, router.SetTrustedProxies(nil))
		ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
		// Two strict instances mirror the auth group and the public form submit route.
		router.POST("/auth", RateLimitStrict(), ok)
		router.POST("/submit", RateLimitStrict(), ok)
		router.GET("/protected", RateLimitModerate(), ok)
		router.GET("/form", RateLimitGenerous(), ok)

		send := func(method, path string) int {
			request := httptest.NewRequest(method, path, nil)
			request.RemoteAddr = "192.0.2.50:1"
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			return response.Code
		}
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusNoContent, send(http.MethodPost, "/auth"))
		}
		require.Equal(t, http.StatusTooManyRequests, send(http.MethodPost, "/auth"))
		require.Equal(t, http.StatusNoContent, send(http.MethodPost, "/submit"))
		require.Equal(t, http.StatusNoContent, send(http.MethodGet, "/protected"))
		require.Equal(t, http.StatusNoContent, send(http.MethodGet, "/form"))
	})

	t.Run("a full instance does not reduce another instance's capacity", func(t *testing.T) {
		clock := newRateLimitClock()
		full := newRateLimiter(1, 1, 2, clock.Now)
		other := newRateLimiter(1, 1, 2, clock.Now)
		require.Equal(t, rateLimitAllowed, full.admit("192.0.2.60"))
		require.Equal(t, rateLimitAllowed, full.admit("192.0.2.61"))
		require.Equal(t, rateLimitOverCapacity, full.admit("192.0.2.62"))

		require.Equal(t, rateLimitAllowed, other.admit("192.0.2.62"))
		require.Equal(t, rateLimitAllowed, other.admit("192.0.2.60"))
		assert.Equal(t, 2, trackedVisitors(t, full))
		assert.Equal(t, 2, trackedVisitors(t, other))
	})
}

func BenchmarkRateLimit(b *testing.B) {
	b.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
	router, _ := newRateLimitTestRouter(b, RateLimit(1e6, 1e6), nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sendRateLimitRequest(router, http.MethodGet, rateLimitTestAddr(i%rateLimitVisitorCapacity), nil)
	}
}
