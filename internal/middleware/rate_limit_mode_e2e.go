//go:build gophercrm_e2e

package middleware

import "os"

// Only the disposable functional-test binary supports this explicit opt-out.
// Normal builds do not read this environment variable at all.
func e2eRateLimitDisabled() bool {
	return os.Getenv("GOPHERCRM_E2E_DISABLE_RATE_LIMIT") == "true"
}
