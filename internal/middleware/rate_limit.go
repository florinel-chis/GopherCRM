package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimitType represents the type of rate limit to apply
type RateLimitType int

const (
	RateLimitPublic RateLimitType = iota
	RateLimitAuthenticated
	RateLimitAdmin
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

// RateLimiter is a config-driven, role-aware rate limiter
type RateLimiter struct {
	cfg      *config.RateLimitConfig
	buckets  map[string]*tokenBucket
	mu       sync.RWMutex
}

type tokenBucket struct {
	tokens    int
	limit     int
	lastReset time.Time
	window    time.Duration
}

// NewRateLimiter creates a new config-driven RateLimiter
func NewRateLimiter(cfg *config.RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		cfg:     cfg,
		buckets: make(map[string]*tokenBucket),
	}
}

// getKey generates a rate limit key based on the request context and limit type
func (rl *RateLimiter) getKey(c *gin.Context, limitType RateLimitType) string {
	// Check for user_id in context (authenticated user)
	if userID, exists := c.Get("user_id"); exists && userID != nil && userID != "" {
		return fmt.Sprintf("user:%v", userID)
	}

	// Check for API key
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "ApiKey ") {
		key := strings.TrimPrefix(authHeader, "ApiKey ")
		if len(key) > 8 {
			key = key[:8]
		}
		return fmt.Sprintf("apikey:%s", key)
	}

	// Fall back to IP
	return rl.getClientIP(c)
}

func (rl *RateLimiter) getClientIP(c *gin.Context) string {
	// Check X-Forwarded-For
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// Check X-Real-IP
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return xri
	}
	// Fall back to RemoteAddr
	ip := c.Request.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

func (rl *RateLimiter) getLimitForType(limitType RateLimitType) int {
	switch limitType {
	case RateLimitAdmin:
		return rl.cfg.AdminEndpoints
	case RateLimitAuthenticated:
		return rl.cfg.AuthenticatedAPI
	default:
		return rl.cfg.PublicEndpoints
	}
}

func (rl *RateLimiter) allow(key string, limit int) (remaining int, allowed bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	window := time.Duration(rl.cfg.WindowDuration) * time.Minute
	bucket, exists := rl.buckets[key]
	if !exists || time.Since(bucket.lastReset) > window {
		rl.buckets[key] = &tokenBucket{
			tokens:    limit - 1,
			limit:     limit,
			lastReset: time.Now(),
			window:    window,
		}
		return limit - 1, true
	}

	if bucket.tokens <= 0 {
		return 0, false
	}

	bucket.tokens--
	return bucket.tokens, true
}

func (rl *RateLimiter) applyRateLimit(c *gin.Context, limitType RateLimitType) {
	if !rl.cfg.Enabled {
		c.Next()
		return
	}

	limit := rl.getLimitForType(limitType)
	key := rl.getKey(c, limitType)
	remaining, allowed := rl.allow(key, limit)

	window := time.Duration(rl.cfg.WindowDuration) * time.Minute

	// Set rate limit headers
	c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(window).Unix(), 10))
	c.Header("X-RateLimit-Window", fmt.Sprintf("%dm", rl.cfg.WindowDuration))

	if !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"error": gin.H{
				"code":        "RATE_LIMIT_EXCEEDED",
				"message":     "Rate limit exceeded",
				"retry_after": fmt.Sprintf("%ds", int(window.Seconds())),
			},
		})
		c.Abort()
		return
	}

	c.Next()
}

// PublicRateLimit applies rate limiting for public endpoints
func PublicRateLimit(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		rl.applyRateLimit(c, RateLimitPublic)
	}
}

// SmartRateLimit applies rate limiting based on the user's role
func SmartRateLimit(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		limitType := RateLimitPublic

		// Determine rate limit type based on user role
		if role, exists := c.Get("user_role"); exists {
			roleStr, _ := role.(string)
			if roleStr == string(models.RoleAdmin) {
				limitType = RateLimitAdmin
			} else if roleStr != "" {
				limitType = RateLimitAuthenticated
			}
		}

		rl.applyRateLimit(c, limitType)
	}
}
