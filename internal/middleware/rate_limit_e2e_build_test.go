//go:build gophercrm_e2e

package middleware

import (
	"net/http"
	"testing"
)

func TestE2EBuildCanBypassAllLiveRateLimitTiers(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "true")
	for _, tier := range liveRateLimitTiers {
		t.Run(tier.name, func(t *testing.T) {
			exerciseRateLimitBurst(t, tier.create, tier.burst, http.StatusNoContent)
		})
	}
}

func TestE2EBuildRequiresExplicitRateLimitOptOut(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	for _, value := range []string{"", "false", "TRUE", "1"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", value)
			exerciseRateLimitBurst(t, RateLimitModerate, 30, http.StatusTooManyRequests)
		})
	}
}
