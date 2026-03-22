package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// helper to clear all env vars that Load() reads and restore them after the test.
func withCleanEnv(t *testing.T, setVars map[string]string, fn func()) {
	t.Helper()

	// Keys that Load() inspects (superset – we only need to worry about the
	// ones that affect CookieSecure and the mandatory JWT_SECRET).
	keysToClean := []string{
		"JWT_SECRET", "JWT_COOKIE_SECURE", "SERVER_MODE",
	}

	// Save originals.
	saved := make(map[string]*string, len(keysToClean))
	for _, k := range keysToClean {
		if v, ok := os.LookupEnv(k); ok {
			vCopy := v
			saved[k] = &vCopy
		} else {
			saved[k] = nil
		}
	}

	// Restore after test.
	t.Cleanup(func() {
		for k, v := range saved {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	})

	// Unset all, then apply caller-provided values.
	for _, k := range keysToClean {
		os.Unsetenv(k)
	}
	for k, v := range setVars {
		os.Setenv(k, v)
	}

	fn()
}

// validSecret returns a JWT_SECRET that passes the length check.
func validSecret() string {
	return "this-is-a-very-secure-secret-that-is-at-least-32-chars"
}

func TestResolveCookieSecure_ProductionDefaultsTrue(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":  validSecret(),
		"SERVER_MODE": "production",
		// JWT_COOKIE_SECURE is NOT set
	}, func() {
		assert.True(t, resolveCookieSecure("production"),
			"production mode should default CookieSecure to true")
	})
}

func TestResolveCookieSecure_DevelopmentDefaultsFalse(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":  validSecret(),
		"SERVER_MODE": "development",
		// JWT_COOKIE_SECURE is NOT set
	}, func() {
		assert.False(t, resolveCookieSecure("development"),
			"development mode should default CookieSecure to false")
	})
}

func TestResolveCookieSecure_ExplicitOverrideTrueInDev(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":        validSecret(),
		"SERVER_MODE":       "development",
		"JWT_COOKIE_SECURE": "true",
	}, func() {
		assert.True(t, resolveCookieSecure("development"),
			"explicit JWT_COOKIE_SECURE=true should override dev default")
	})
}

func TestResolveCookieSecure_ExplicitOverrideFalseInProd(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":        validSecret(),
		"SERVER_MODE":       "production",
		"JWT_COOKIE_SECURE": "false",
	}, func() {
		assert.False(t, resolveCookieSecure("production"),
			"explicit JWT_COOKIE_SECURE=false should override prod default")
	})
}

func TestLoad_ProductionCookieSecureDefault(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":  validSecret(),
		"SERVER_MODE": "production",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.True(t, cfg.JWT.CookieSecure,
			"Load() in production should set CookieSecure=true by default")
	})
}

func TestLoad_DevelopmentCookieSecureDefault(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":  validSecret(),
		"SERVER_MODE": "development",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.False(t, cfg.JWT.CookieSecure,
			"Load() in development should set CookieSecure=false by default")
	})
}
