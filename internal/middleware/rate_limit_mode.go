//go:build !gophercrm_e2e

package middleware

// Ordinary binaries cannot opt out of rate limiting through the E2E switch.
func e2eRateLimitDisabled() bool { return false }
