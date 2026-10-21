package middleware

import (
	"container/heap"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// rateLimitVisitorCapacity is the maximum number of client identities one
	// RateLimit instance tracks. Unseen identities are refused with 429 while
	// the instance is full; existing clients are never evicted to make room.
	rateLimitVisitorCapacity = 10000
	// rateLimitIdleTimeout is the minimum inactivity before a visitor record
	// may be reclaimed. A record is also kept until its bucket has refilled to
	// burst, so reclaiming it can never hand out a fresh burst early.
	rateLimitIdleTimeout = 5 * time.Minute
	// rateLimitReclaimPerRequest bounds the cleanup work done by one request.
	// Cleanup is opportunistic: there is no background goroutine to stop.
	rateLimitReclaimPerRequest = 4
	// rateLimitNeverReclaim schedules buckets that cannot refill (rate <= 0).
	rateLimitNeverReclaim = 100 * 365 * 24 * time.Hour
)

// rateLimitDecision is the outcome of one admission attempt.
type rateLimitDecision int

const (
	rateLimitAllowed rateLimitDecision = iota
	rateLimitThrottled
	rateLimitOverCapacity
)

// visitor tracks rate limiting for one client identity.
type visitor struct {
	key       string
	limiter   *rate.Limiter
	reclaimAt time.Time // earliest time the record may be safely dropped
	index     int       // position in reclaimQueue
}

// reclaimQueue is a min-heap of visitors ordered by reclaimAt.
type reclaimQueue []*visitor

func (q reclaimQueue) Len() int           { return len(q) }
func (q reclaimQueue) Less(i, j int) bool { return q[i].reclaimAt.Before(q[j].reclaimAt) }

func (q reclaimQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *reclaimQueue) Push(x any) {
	v := x.(*visitor)
	v.index = len(*q)
	*q = append(*q, v)
}

func (q *reclaimQueue) Pop() any {
	old := *q
	n := len(old)
	v := old[n-1]
	old[n-1] = nil
	v.index = -1
	*q = old[:n-1]
	return v
}

// rateLimiter holds the bounded per-identity token buckets of one RateLimit
// instance. All state is owned by the returned handler and is released with it.
type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	queue    reclaimQueue
	limit    rate.Limit // requests per second
	burst    int        // maximum burst size
	capacity int        // maximum number of tracked visitors
	now      func() time.Time
}

func newRateLimiter(rps float64, burst, capacity int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		visitors: make(map[string]*visitor),
		limit:    rate.Limit(rps),
		burst:    burst,
		capacity: capacity,
		now:      now,
	}
}

// admit consumes one token for key, creating its visitor record if there is room.
func (rl *rateLimiter) admit(key string) rateLimitDecision {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	rl.reclaimLocked(now, rateLimitReclaimPerRequest)

	v, exists := rl.visitors[key]
	if !exists {
		if len(rl.visitors) >= rl.capacity {
			return rateLimitOverCapacity
		}
		v = &visitor{key: key, limiter: rate.NewLimiter(rl.limit, rl.burst)}
	}

	allowed := v.limiter.AllowN(now, 1)
	// Every request, allowed or not, restarts the idle period.
	v.reclaimAt = now.Add(rateLimitIdleTimeout)
	if full := rl.fullAt(v.limiter, now); full.After(v.reclaimAt) {
		v.reclaimAt = full
	}
	if exists {
		heap.Fix(&rl.queue, v.index)
	} else {
		rl.visitors[key] = v
		heap.Push(&rl.queue, v)
	}

	if !allowed {
		return rateLimitThrottled
	}
	return rateLimitAllowed
}

// reclaimLocked drops at most budget records that are idle and whose bucket has
// refilled to burst. Such a record is indistinguishable from a fresh one, so
// dropping it never grants extra tokens. The heap keeps this O(budget·log n).
func (rl *rateLimiter) reclaimLocked(now time.Time, budget int) {
	for i := 0; i < budget && len(rl.queue) > 0; i++ {
		v := rl.queue[0]
		if v.reclaimAt.After(now) {
			return
		}
		if !rl.bucketFull(v.limiter, now) {
			// Guard against rounding in fullAt: reschedule, never drop early.
			v.reclaimAt = rl.fullAt(v.limiter, now)
			heap.Fix(&rl.queue, 0)
			continue
		}
		heap.Pop(&rl.queue)
		delete(rl.visitors, v.key)
	}
}

func (rl *rateLimiter) bucketFull(lim *rate.Limiter, now time.Time) bool {
	if rl.limit == rate.Inf {
		return true
	}
	return lim.TokensAt(now) >= float64(rl.burst)
}

// fullAt returns when lim will have refilled to burst.
func (rl *rateLimiter) fullAt(lim *rate.Limiter, now time.Time) time.Time {
	if rl.bucketFull(lim, now) {
		return now
	}
	if rl.limit <= 0 {
		return now.Add(rateLimitNeverReclaim)
	}
	wait := (float64(rl.burst) - lim.TokensAt(now)) / float64(rl.limit)
	if wait >= rateLimitNeverReclaim.Seconds() {
		return now.Add(rateLimitNeverReclaim)
	}
	return now.Add(time.Duration(math.Ceil(wait*float64(time.Second))) + time.Millisecond)
}

func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip rate limiting for CORS preflight requests — these are
		// browser-generated and should not consume rate limit tokens.
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		ip := c.ClientIP()
		switch rl.admit(ip) {
		case rateLimitAllowed:
			c.Next()
			return
		case rateLimitOverCapacity:
			utils.GetLogger(c).WithField("client_ip", ip).Warn("Rate limit visitor capacity reached")
		default:
			utils.GetLogger(c).WithField("client_ip", ip).Warn("Rate limit exceeded")
		}

		utils.RespondError(c, http.StatusTooManyRequests,
			utils.ErrCodeTooManyRequests,
			"Too many requests. Please try again later.",
			gin.H{
				"retry_after": "60s",
			})
		c.Abort()
	}
}

// RateLimit returns a middleware that implements rate limiting per IP address.
// Each call creates its own independent rate limiter so that different tiers
// (strict, moderate, generous) do not share state. Each instance tracks at most
// 10,000 client IPs; while full, unseen IPs receive 429. Idle records are
// reclaimed by later requests, so no goroutine or shutdown is involved.
func RateLimit(rps float64, burst int) gin.HandlerFunc {
	if e2eRateLimitDisabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return newRateLimiter(rps, burst, rateLimitVisitorCapacity, time.Now).middleware()
}

// RateLimitStrict returns a stricter rate limit middleware for sensitive endpoints.
// 10 requests per minute with burst of 5 — prevents brute force while allowing
// normal browser login attempts (which include preflight OPTIONS + POST pairs).
// Set DISABLE_RATE_LIMIT=true to disable for testing.
func RateLimitStrict() gin.HandlerFunc {
	if os.Getenv("DISABLE_RATE_LIMIT") == "true" {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return RateLimit(10.0/60.0, 5)
}

// RateLimitModerate returns a moderate rate limit middleware for general API endpoints.
// 120 requests per minute with burst of 30 — suitable for SPAs that make
// multiple concurrent requests on page load.
func RateLimitModerate() gin.HandlerFunc {
	return RateLimit(2.0, 30)
}

// RateLimitGenerous returns a generous rate limit for read-heavy endpoints.
// 240 requests per minute with burst of 40.
func RateLimitGenerous() gin.HandlerFunc {
	return RateLimit(4.0, 40)
}
