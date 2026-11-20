package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
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

func TestProductionRefusesRateLimitSwitch(t *testing.T) {
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "")

	t.Run("refused in production in any letter case", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "true")
		for _, mode := range []string{"production", "Production", " PRODUCTION "} {
			err := CheckRateLimitSwitch(mode)
			require.Error(t, err, "mode %q", mode)
			require.Contains(t, err.Error(), "DISABLE_RATE_LIMIT")
			require.Contains(t, err.Error(), "SERVER_MODE")
		}
	})

	t.Run("allowed outside production", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "true")
		for _, mode := range []string{"development", ""} {
			require.NoError(t, CheckRateLimitSwitch(mode), "mode %q", mode)
		}
	})

	t.Run("production without the exact switch value starts", func(t *testing.T) {
		for _, value := range []string{"", "false", "1"} {
			t.Setenv("DISABLE_RATE_LIMIT", value)
			require.NoError(t, CheckRateLimitSwitch("production"), "DISABLE_RATE_LIMIT=%q", value)
		}
	})

	t.Run("only the functional-test build considers the e2e switch", func(t *testing.T) {
		t.Setenv("DISABLE_RATE_LIMIT", "") // restored after the test
		require.NoError(t, os.Unsetenv("DISABLE_RATE_LIMIT"))
		t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "true")
		if e2eRateLimitDisabled() {
			// -tags=gophercrm_e2e: see TestE2EBuildRateLimitSwitchRefusedInProduction.
			require.Error(t, CheckRateLimitSwitch("production"))
			return
		}
		require.NoError(t, CheckRateLimitSwitch("production"))
	})
}
