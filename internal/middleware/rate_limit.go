package middleware

import (
	"container/heap"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// rateLimitVisitorCapacity is the maximum number of client identities one
	// RateLimit instance tracks. When the instance is full and no record can be
	// reclaimed, an unseen identity is still admitted: the record with the
	// earliest reclaim time (the least recently active client) is evicted to
	// make room, so the count never exceeds the capacity.
	rateLimitVisitorCapacity = 10000
	// rateLimitIdleTimeout is the minimum inactivity before a visitor record
	// may be reclaimed. Idle reclaim also waits until the bucket has refilled
	// to burst, so it never hands out a fresh burst early. Eviction at capacity
	// does not wait: an evicted record's bucket may not be full, and that
	// client starts again with a fresh burst on its next request.
	//
	// A record's reclaim time is the later of last-seen + rateLimitIdleTimeout
	// and the time its bucket refills to burst. Every live tier refills to
	// burst well within this timeout (strict 30 s, moderate 15 s, generous
	// 10 s), so the idle term dominates and the earliest reclaim time is the
	// least recently active client. Eviction relies on that order.
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

// admit consumes one token for key, creating its visitor record if needed.
// When the instance is full, an unseen key evicts the record with the earliest
// reclaim time (one record per admission) and is admitted in its place. The
// eviction writes no log line: one line per request during a burst of new
// clients would flood the log.
func (rl *rateLimiter) admit(key string) rateLimitDecision {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	rl.reclaimLocked(now, rateLimitReclaimPerRequest)

	v, exists := rl.visitors[key]
	if !exists {
		if len(rl.visitors) >= rl.capacity && len(rl.queue) > 0 {
			evicted := heap.Pop(&rl.queue).(*visitor)
			delete(rl.visitors, evicted.key)
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

// rateLimitResponder writes the 429 answer of a throttled request.
type rateLimitResponder func(c *gin.Context)

// respondAPIRateLimited is the 429 of the API tiers: the APIResponse envelope
// with TOO_MANY_REQUESTS and details.retry_after.
func respondAPIRateLimited(c *gin.Context) {
	utils.RespondError(c, http.StatusTooManyRequests,
		utils.ErrCodeTooManyRequests,
		"Too many requests. Please try again later.",
		gin.H{
			"retry_after": "60s",
		})
}

// respondSCIMRateLimited is the 429 of the SCIM tier: the RFC 7644 error body.
func respondSCIMRateLimited(c *gin.Context) {
	scim.WriteError(c, http.StatusTooManyRequests, "Too many requests. Please try again later.")
}

// middleware is the limiter with the API tiers' 429.
func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return rl.middlewareWith(respondAPIRateLimited)
}

// middlewareWith is the limiter answering a throttled request with respond.
func (rl *rateLimiter) middlewareWith(respond rateLimitResponder) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip rate limiting for CORS preflight requests — these are
		// browser-generated and should not consume rate limit tokens.
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		ip := c.ClientIP()
		if rl.admit(rateLimitClientKey(ip)) == rateLimitAllowed {
			c.Next()
			return
		}
		utils.GetLogger(c).WithField("client_ip", ip).Warn("Rate limit exceeded")

		respond(c)
		c.Abort()
	}
}

// rateLimitClientKey is the identity one bucket counts: an IPv4 client
// (including an IPv4-mapped IPv6 address) by its address, an IPv6 client by
// its /64 network. A value that does not parse as an address is used as is.
func rateLimitClientKey(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// RateLimit returns a middleware that implements rate limiting per client: an
// IPv4 address or an IPv6 /64 network. Each call creates its own independent
// rate limiter so that different tiers (strict, moderate, generous) do not
// share state. Each instance tracks at most 10,000 clients; while full, an
// unseen client is admitted and the least recently active record is evicted.
// Idle records are reclaimed by later requests, so no goroutine or shutdown
// is involved.
func RateLimit(rps float64, burst int) gin.HandlerFunc {
	if e2eRateLimitDisabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return newRateLimiter(rps, burst, rateLimitVisitorCapacity, time.Now).middleware()
}

// CheckRateLimitSwitch refuses the switches that lift rate limiting when the
// server runs in production mode: DISABLE_RATE_LIMIT=true, and in the
// functional-test build GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true. Outside
// production it returns nil and the switches behave as before.
func CheckRateLimitSwitch(serverMode string) error {
	if strings.ToLower(strings.TrimSpace(serverMode)) != "production" {
		return nil
	}
	if os.Getenv("DISABLE_RATE_LIMIT") == "true" {
		return errors.New("DISABLE_RATE_LIMIT=true is refused in production mode (SERVER_MODE=production): unset it")
	}
	if e2eRateLimitDisabled() {
		return errors.New("GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true is refused in production mode (SERVER_MODE=production): unset it")
	}
	return nil
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

// RateLimitStrictSCIM is the strict tier of /scim/v2 (#468): 10 requests per
// minute with burst of 5 per client address, its own instance, bypassed by
// DISABLE_RATE_LIMIT=true and by the e2e build switch like RateLimitStrict.
// It runs before the SCIM authentication, so a throttled request writes no
// audit row, and it answers 429 with the SCIM error body.
func RateLimitStrictSCIM() gin.HandlerFunc {
	if os.Getenv("DISABLE_RATE_LIMIT") == "true" || e2eRateLimitDisabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return newRateLimiter(10.0/60.0, 5, rateLimitVisitorCapacity, time.Now).middlewareWith(respondSCIMRateLimited)
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
