//go:build !gophercrm_e2e

package middleware

import (
	"net/http"
	"testing"
)

func TestOrdinaryBuildIgnoresE2ERateLimitSwitch(t *testing.T) {
	t.Setenv("DISABLE_RATE_LIMIT", "")
	t.Setenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT", "true")
	for _, tier := range liveRateLimitTiers {
		t.Run(tier.name, func(t *testing.T) {
			exerciseRateLimitBurst(t, tier.create, tier.burst, http.StatusTooManyRequests)
		})
	}
}
