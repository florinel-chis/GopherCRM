package config

import (
	"os"
	"strconv"
	"strings"
)

// SCIMConfig holds the SCIM token limit of /scim/v2 (#555): the requests a
// minute each SCIM provider token may make (SCIM_RATE_LIMIT_PER_MINUTE) and
// its burst (SCIM_RATE_LIMIT_BURST). Both pass through
// NormalizeSCIMRateLimits, so they are never below 1 or above the maximums.
type SCIMConfig struct {
	RateLimitPerMinute int
	RateLimitBurst     int
}

// Defaults and maximums of the SCIM token limit.
const (
	DefaultSCIMRateLimitPerMinute = 1500
	DefaultSCIMRateLimitBurst     = 150
	MaxSCIMRateLimitPerMinute     = 6000
	MaxSCIMRateLimitBurst         = 1000
)

// NormalizeSCIMRateLimits applies the defaults and maximums of the SCIM token
// limit: a value below 1 takes its default, a value above its maximum takes
// the maximum. config.Load and the middleware constructor both use it, so a
// Config built without Load never yields a zero limit.
func NormalizeSCIMRateLimits(perMinute, burst int) (int, int) {
	return normalizeBounded(perMinute, DefaultSCIMRateLimitPerMinute, MaxSCIMRateLimitPerMinute),
		normalizeBounded(burst, DefaultSCIMRateLimitBurst, MaxSCIMRateLimitBurst)
}

func normalizeBounded(value, defaultValue, maxValue int) int {
	if value < 1 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// loadSCIMConfig reads the SCIM token limit. An unset, empty or unparseable
// value takes the default; surrounding spaces are ignored. It never fails.
func loadSCIMConfig() SCIMConfig {
	perMinute, burst := NormalizeSCIMRateLimits(
		parseSCIMRateLimitValue(os.Getenv("SCIM_RATE_LIMIT_PER_MINUTE")),
		parseSCIMRateLimitValue(os.Getenv("SCIM_RATE_LIMIT_BURST")))
	return SCIMConfig{RateLimitPerMinute: perMinute, RateLimitBurst: burst}
}

// parseSCIMRateLimitValue returns 0 (the default after normalisation) for a
// value that is not an integer.
func parseSCIMRateLimitValue(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
