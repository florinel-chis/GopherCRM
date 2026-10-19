package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var liveRateLimitTiers = []struct {
	name   string
	create func() gin.HandlerFunc
	burst  int
}{
	{"strict", RateLimitStrict, 5},
	{"moderate", RateLimitModerate, 30},
	{"generous", RateLimitGenerous, 40},
}

func exerciseRateLimitBurst(t *testing.T, create func() gin.HandlerFunc, burst, finalStatus int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/limited", create(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for i := 0; i <= burst; i++ {
		request := httptest.NewRequest(http.MethodGet, "/limited", nil)
		request.RemoteAddr = "192.0.2.10:12345"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		expected := http.StatusNoContent
		if i == burst {
			expected = finalStatus
		}
		require.Equal(t, expected, response.Code, "request %d", i+1)
	}
}

func TestLiveRateLimitTiersEnforceTheirBurst(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
	for _, tier := range liveRateLimitTiers {
		t.Run(tier.name, func(t *testing.T) {
			exerciseRateLimitBurst(t, tier.create, tier.burst, http.StatusTooManyRequests)
		})
	}
}

func TestLegacyRateLimitSwitchStillBypassesOnlyStrict(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")
	for _, tier := range liveRateLimitTiers {
		t.Run(tier.name, func(t *testing.T) {
			expected := http.StatusTooManyRequests
			if tier.name == "strict" {
				expected = http.StatusNoContent
			}
			exerciseRateLimitBurst(t, tier.create, tier.burst, expected)
		})
	}
}
