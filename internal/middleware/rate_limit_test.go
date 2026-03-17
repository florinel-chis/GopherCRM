package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setupTestRateLimiter() *RateLimiter {
	cfg := &config.RateLimitConfig{
		PublicEndpoints:  2, // Very low limits for testing
		AuthenticatedAPI: 5,
		AdminEndpoints:   10,
		WindowDuration:   1, // 1 minute
		BurstMultiplier:  1,
		Enabled:          true,
	}
	return NewRateLimiter(cfg)
}

func setupTestRouter(rateLimiter *RateLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Public endpoint
	router.GET("/public", PublicRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "public"})
	})

	// Authenticated endpoint
	router.GET("/auth", func(c *gin.Context) {
		c.Set("user_id", "user123")
		c.Set("user_role", string(models.RoleCustomer))
		c.Next()
	}, SmartRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "authenticated"})
	})

	// Admin endpoint
	router.GET("/admin", func(c *gin.Context) {
		c.Set("user_id", "admin123")
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	}, SmartRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "admin"})
	})

	return router
}

func TestPublicRateLimit(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// First request should succeed
	req1, _ := http.NewRequest("GET", "/public", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Equal(t, "2", w1.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "1", w1.Header().Get("X-RateLimit-Remaining"))

	// Second request should succeed
	req2, _ := http.NewRequest("GET", "/public", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "0", w2.Header().Get("X-RateLimit-Remaining"))

	// Third request should be rate limited
	req3, _ := http.NewRequest("GET", "/public", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	assert.Equal(t, http.StatusTooManyRequests, w3.Code)
}

func TestAuthenticatedRateLimit(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// Make 5 requests (authenticated limit)
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", "/auth", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		expectedRemaining := 5 - i - 1
		assert.Equal(t, strconv.Itoa(expectedRemaining), w.Header().Get("X-RateLimit-Remaining"))
	}

	// 6th request should be rate limited
	req, _ := http.NewRequest("GET", "/auth", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestAdminRateLimit(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// Admin should have higher limits (10 requests)
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest("GET", "/admin", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		expectedRemaining := 10 - i - 1
		assert.Equal(t, strconv.Itoa(expectedRemaining), w.Header().Get("X-RateLimit-Remaining"))
	}

	// 11th request should be rate limited
	req, _ := http.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestRateLimitHeaders(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	req, _ := http.NewRequest("GET", "/public", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	
	// Check required headers
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
	assert.Equal(t, "1m", w.Header().Get("X-RateLimit-Window"))
}

func TestRateLimitByIP(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// Two different IPs should have separate rate limits
	req1, _ := http.NewRequest("GET", "/public", nil)
	req1.RemoteAddr = "192.168.1.1:12345"
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	req2, _ := http.NewRequest("GET", "/public", nil)
	req2.RemoteAddr = "192.168.1.2:12345"
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// Both should show remaining count of 1 (since they're different IPs)
	assert.Equal(t, "1", w1.Header().Get("X-RateLimit-Remaining"))
	assert.Equal(t, "1", w2.Header().Get("X-RateLimit-Remaining"))
}

func TestRateLimitByUserID(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/test", func(c *gin.Context) {
		c.Set("user_id", "user123")
		c.Set("user_role", string(models.RoleCustomer))
		c.Next()
	}, SmartRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "test"})
	})

	// Make requests as the same user
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// 6th request should be rate limited
	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestRateLimitByAPIKey(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/test", SmartRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "test"})
	})

	// Make requests with API key
	for i := 0; i < 2; i++ { // Public limit is 2
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "ApiKey test-api-key-12345")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// 3rd request should be rate limited
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "ApiKey test-api-key-12345")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestRateLimitWithForwardedIP(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// Test X-Forwarded-For header
	req, _ := http.NewRequest("GET", "/public", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 192.168.1.1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "1", w.Header().Get("X-RateLimit-Remaining"))

	// Test X-Real-IP header
	req2, _ := http.NewRequest("GET", "/public", nil)
	req2.Header.Set("X-Real-IP", "203.0.113.2")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "1", w2.Header().Get("X-RateLimit-Remaining"))
}

func TestRateLimitDisabled(t *testing.T) {
	cfg := &config.RateLimitConfig{
		PublicEndpoints:  1,
		AuthenticatedAPI: 1,
		AdminEndpoints:   1,
		WindowDuration:   1,
		BurstMultiplier:  1,
		Enabled:          false, // Disabled
	}
	rateLimiter := NewRateLimiter(cfg)
	router := setupTestRouter(rateLimiter)

	// Make many requests - all should succeed when rate limiting is disabled
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest("GET", "/public", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}
}

func TestRateLimitErrorResponse(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	router := setupTestRouter(rateLimiter)

	// Exhaust rate limit with 2 requests
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("GET", "/public", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Next request should return proper error
	req, _ := http.NewRequest("GET", "/public", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	
	// Check that response contains error information in the body
	responseBody := w.Body.String()
	assert.Contains(t, responseBody, "Rate limit exceeded")
	assert.Contains(t, responseBody, "retry_after")
}

func TestGetKeyGeneration(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	gin.SetMode(gin.TestMode)
	
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request, _ = http.NewRequest("GET", "/test", nil)
	c.Request.RemoteAddr = "192.168.1.1:12345"

	// Test public endpoint key (should use IP)
	key := rateLimiter.getKey(c, RateLimitPublic)
	assert.Equal(t, "192.168.1.1", key)

	// Test authenticated endpoint key with user ID
	c.Set("user_id", "user123")
	key = rateLimiter.getKey(c, RateLimitAuthenticated)
	assert.Equal(t, "user:user123", key)

	// Test API key (clear user_id context)
	c.Request.Header.Set("Authorization", "ApiKey test-api-key-12345")
	c.Keys = make(map[string]interface{}) // Clear all context including user_id
	key = rateLimiter.getKey(c, RateLimitAuthenticated)
	assert.Equal(t, "apikey:test-api", key) // First 8 chars
}

func TestSmartRateLimitRoleDetection(t *testing.T) {
	rateLimiter := setupTestRateLimiter()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/test", func(c *gin.Context) {
		// Set different roles for testing
		role := c.Query("role")
		if role != "" {
			c.Set("user_id", "test-user")
			c.Set("user_role", role)
		}
		c.Next()
	}, SmartRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "success"})
	})

	// Test admin role (should get higher limits)
	req, _ := http.NewRequest("GET", "/test?role=admin", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit")) // Admin limit

	// Test regular user role
	req2, _ := http.NewRequest("GET", "/test?role=customer", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "5", w2.Header().Get("X-RateLimit-Limit")) // Authenticated limit

	// Test unauthenticated
	req3, _ := http.NewRequest("GET", "/test", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusOK, w3.Code)
	assert.Equal(t, "2", w3.Header().Get("X-RateLimit-Limit")) // Public limit
}

func BenchmarkRateLimit(b *testing.B) {
	rateLimiter := setupTestRateLimiter()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	
	router.GET("/bench", PublicRateLimit(rateLimiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "/bench", nil)
		req.RemoteAddr = fmt.Sprintf("192.168.1.%d:12345", i%255+1) // Different IPs
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}