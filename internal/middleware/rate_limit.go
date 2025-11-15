package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// visitor tracks rate limiting for a specific IP
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rateLimiter manages rate limiters for different IPs
type rateLimiter struct {
	visitors map[string]*visitor
	mu       sync.RWMutex
	limit    rate.Limit  // requests per second
	burst    int         // maximum burst size
	cleanup  time.Duration // cleanup interval for old visitors
}

var globalRateLimiter *rateLimiter

// initRateLimiter initializes the global rate limiter
func initRateLimiter(rps float64, burst int) {
	globalRateLimiter = &rateLimiter{
		visitors: make(map[string]*visitor),
		limit:    rate.Limit(rps),
		burst:    burst,
		cleanup:  time.Minute * 5, // cleanup visitors not seen for 5 minutes
	}

	// Start cleanup goroutine
	go globalRateLimiter.cleanupVisitors()
}

// getVisitor retrieves or creates a visitor for the given IP
func (rl *rateLimiter) getVisitor(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.limit, rl.burst)
		rl.visitors[ip] = &visitor{
			limiter:  limiter,
			lastSeen: time.Now(),
		}
		return limiter
	}

	// Update last seen time
	v.lastSeen = time.Now()
	return v.limiter
}

// cleanupVisitors removes visitors that haven't been seen recently
func (rl *rateLimiter) cleanupVisitors() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		for ip, v := range rl.visitors {
			if time.Since(v.lastSeen) > rl.cleanup {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit returns a middleware that implements rate limiting per IP address
// rps: requests per second allowed
// burst: maximum burst size
func RateLimit(rps float64, burst int) gin.HandlerFunc {
	// Initialize global rate limiter on first call
	if globalRateLimiter == nil {
		initRateLimiter(rps, burst)
	}

	return func(c *gin.Context) {
		ip := c.ClientIP()
		limiter := globalRateLimiter.getVisitor(ip)

		if !limiter.Allow() {
			utils.GetLogger(c).WithField("client_ip", ip).Warn("Rate limit exceeded")

			utils.RespondError(c, http.StatusTooManyRequests,
				utils.ErrCodeTooManyRequests,
				"Too many requests. Please try again later.",
				gin.H{
					"retry_after": "60s",
				})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RateLimitStrict returns a stricter rate limit middleware for sensitive endpoints
// This uses a more restrictive limit suitable for authentication endpoints
func RateLimitStrict() gin.HandlerFunc {
	// 5 requests per minute with burst of 2
	return RateLimit(5.0/60.0, 2)
}

// RateLimitModerate returns a moderate rate limit middleware for general API endpoints
func RateLimitModerate() gin.HandlerFunc {
	// 60 requests per minute with burst of 10
	return RateLimit(1.0, 10)
}

// RateLimitGenerous returns a generous rate limit for read-heavy endpoints
func RateLimitGenerous() gin.HandlerFunc {
	// 120 requests per minute with burst of 20
	return RateLimit(2.0, 20)
}
