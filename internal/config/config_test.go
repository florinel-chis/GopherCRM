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
		// AEO keys are cleaned too: a developer shell that exports a real
		// provider key would otherwise make the default-value assertions fail.
		"ANTHROPIC_API_KEY", "AEO_ANTHROPIC_MODEL",
		"OPENAI_API_KEY", "AEO_OPENAI_MODEL",
		"GEMINI_API_KEY", "AEO_GEMINI_MODEL",
		"MOONSHOT_API_KEY", "AEO_KIMI_MODEL",
		"PERPLEXITY_API_KEY", "AEO_PERPLEXITY_MODEL",
		"AEO_CUSTOM_NAME", "AEO_CUSTOM_BASE_URL", "AEO_CUSTOM_MODEL", "AEO_CUSTOM_API_KEY",
		"AEO_SCHEDULE_ENABLED", "AEO_SCHEDULE_HOUR",
		// Forms keys, for the same reason as the AEO ones.
		"PUBLIC_BASE_URL", "RECAPTCHA_SITE_KEY", "RECAPTCHA_SECRET_KEY", "RECAPTCHA_MIN_SCORE",
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

func TestLoad_MissingJWTSecret(t *testing.T) {
	withCleanEnv(t, map[string]string{
		// JWT_SECRET is NOT set
	}, func() {
		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "JWT_SECRET")
	})
}

func TestLoad_DefaultJWTSecretRejected(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": "default-secret-change-this",
	}, func() {
		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "default value")
	})
}

func TestLoad_ShortJWTSecretRejected(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": "too-short",
	}, func() {
		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least 32 characters")
	})
}

func TestLoad_ValidJWTSecretAccepted(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, validSecret(), cfg.JWT.Secret)
	})
}

func TestLoad_DefaultValues(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, 8080, cfg.Server.Port)
		assert.Equal(t, "development", cfg.Server.Mode)
		assert.Equal(t, "localhost", cfg.Database.Host)
		assert.Equal(t, 3306, cfg.Database.Port)
		assert.Equal(t, "gocrm", cfg.Database.Name)
		assert.Equal(t, "/api/v1", cfg.API.Prefix)
		assert.Equal(t, 24, cfg.JWT.ExpiryHours)
	})
}

func TestDSN(t *testing.T) {
	dbCfg := DatabaseConfig{
		User:     "testuser",
		Password: "testpass",
		Host:     "127.0.0.1",
		Port:     3307,
		Name:     "testdb",
	}
	dsn := dbCfg.DSN()
	assert.Contains(t, dsn, "testuser:testpass")
	assert.Contains(t, dsn, "127.0.0.1:3307")
	assert.Contains(t, dsn, "/testdb")
	assert.Contains(t, dsn, "parseTime=True")
}

func TestLoad_AEODefaults(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)

		// No provider is configured out of the box.
		assert.Empty(t, cfg.AEO.AnthropicAPIKey)
		assert.Empty(t, cfg.AEO.OpenAIAPIKey)
		assert.Empty(t, cfg.AEO.GeminiAPIKey)
		assert.Empty(t, cfg.AEO.MoonshotAPIKey)
		assert.Empty(t, cfg.AEO.PerplexityAPIKey)
		assert.Empty(t, cfg.AEO.CustomBaseURL)
		assert.Empty(t, cfg.AEO.CustomAPIKey)

		// Models fall back to the documented defaults.
		assert.Equal(t, "claude-opus-5", cfg.AEO.AnthropicModel)
		assert.Equal(t, "gpt-4o-mini", cfg.AEO.OpenAIModel)
		assert.Equal(t, "gemini-flash-latest", cfg.AEO.GeminiModel)
		assert.Equal(t, "moonshot-v1-8k", cfg.AEO.KimiModel)
		assert.Equal(t, "sonar", cfg.AEO.PerplexityModel)
		assert.Equal(t, "custom", cfg.AEO.CustomName)
		assert.Equal(t, "openai/gpt-oss-20b", cfg.AEO.CustomModel)

		assert.True(t, cfg.AEO.ScheduleEnabled)
		assert.Equal(t, 6, cfg.AEO.ScheduleHour)
	})
}

func TestLoad_AEOOverrides(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":           validSecret(),
		"ANTHROPIC_API_KEY":    "anthropic-key",
		"AEO_ANTHROPIC_MODEL":  "claude-test-model",
		"OPENAI_API_KEY":       "openai-key",
		"AEO_OPENAI_MODEL":     "gpt-test",
		"GEMINI_API_KEY":       "gemini-key",
		"AEO_GEMINI_MODEL":     "gemini-test",
		"MOONSHOT_API_KEY":     "moonshot-key",
		"AEO_KIMI_MODEL":       "kimi-test",
		"PERPLEXITY_API_KEY":   "perplexity-key",
		"AEO_PERPLEXITY_MODEL": "sonar-test",
		"AEO_CUSTOM_NAME":      "lmstudio",
		"AEO_CUSTOM_BASE_URL":  "http://10.0.1.21:1234/v1",
		"AEO_CUSTOM_MODEL":     "local/model",
		"AEO_CUSTOM_API_KEY":   "custom-key",
		"AEO_SCHEDULE_ENABLED": "false",
		"AEO_SCHEDULE_HOUR":    "3",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)

		assert.Equal(t, "anthropic-key", cfg.AEO.AnthropicAPIKey)
		assert.Equal(t, "claude-test-model", cfg.AEO.AnthropicModel)
		assert.Equal(t, "openai-key", cfg.AEO.OpenAIAPIKey)
		assert.Equal(t, "gpt-test", cfg.AEO.OpenAIModel)
		assert.Equal(t, "gemini-key", cfg.AEO.GeminiAPIKey)
		assert.Equal(t, "gemini-test", cfg.AEO.GeminiModel)
		assert.Equal(t, "moonshot-key", cfg.AEO.MoonshotAPIKey)
		assert.Equal(t, "kimi-test", cfg.AEO.KimiModel)
		assert.Equal(t, "perplexity-key", cfg.AEO.PerplexityAPIKey)
		assert.Equal(t, "sonar-test", cfg.AEO.PerplexityModel)
		assert.Equal(t, "lmstudio", cfg.AEO.CustomName)
		assert.Equal(t, "http://10.0.1.21:1234/v1", cfg.AEO.CustomBaseURL)
		assert.Equal(t, "local/model", cfg.AEO.CustomModel)
		assert.Equal(t, "custom-key", cfg.AEO.CustomAPIKey)
		assert.False(t, cfg.AEO.ScheduleEnabled)
		assert.Equal(t, 3, cfg.AEO.ScheduleHour)
	})
}

func TestLoad_AEOScheduleHourClamped(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":        validSecret(),
		"AEO_SCHEDULE_HOUR": "99",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, 23, cfg.AEO.ScheduleHour)
	})

	withCleanEnv(t, map[string]string{
		"JWT_SECRET":        validSecret(),
		"AEO_SCHEDULE_HOUR": "-4",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, 0, cfg.AEO.ScheduleHour)
	})

	// A non-numeric value falls back to the default before clamping.
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":        validSecret(),
		"AEO_SCHEDULE_HOUR": "midnight",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, 6, cfg.AEO.ScheduleHour)
	})
}

func TestGetEnvAsBool(t *testing.T) {
	const key = "AEO_TEST_BOOL"
	t.Cleanup(func() { os.Unsetenv(key) })

	cases := []struct {
		value      string
		set        bool
		defaultVal bool
		expected   bool
	}{
		{set: false, defaultVal: true, expected: true},
		{set: false, defaultVal: false, expected: false},
		{value: "", set: true, defaultVal: true, expected: true},
		{value: "true", set: true, defaultVal: false, expected: true},
		{value: "TRUE", set: true, defaultVal: false, expected: true},
		{value: " 1 ", set: true, defaultVal: false, expected: true},
		{value: "false", set: true, defaultVal: true, expected: false},
		{value: "0", set: true, defaultVal: true, expected: false},
		// Unparseable values keep the default rather than guessing.
		{value: "yes", set: true, defaultVal: true, expected: true},
		{value: "yes", set: true, defaultVal: false, expected: false},
	}

	for _, tc := range cases {
		os.Unsetenv(key)
		if tc.set {
			os.Setenv(key, tc.value)
		}
		assert.Equal(t, tc.expected, getEnvAsBool(key, tc.defaultVal),
			"value=%q set=%v default=%v", tc.value, tc.set, tc.defaultVal)
	}
}

func TestClampHour(t *testing.T) {
	assert.Equal(t, 0, clampHour(-1))
	assert.Equal(t, 0, clampHour(0))
	assert.Equal(t, 6, clampHour(6))
	assert.Equal(t, 23, clampHour(23))
	assert.Equal(t, 23, clampHour(24))
}

func TestLoad_FormsDefaults(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)

		assert.Equal(t, "http://localhost:8080", cfg.Forms.PublicBaseURL)
		assert.Empty(t, cfg.Forms.RecaptchaSiteKey)
		assert.Empty(t, cfg.Forms.RecaptchaSecret)
		assert.Equal(t, 0.5, cfg.Forms.RecaptchaMinScore)
		assert.False(t, cfg.Forms.RecaptchaActive(), "no keys configured means the check is off")
	})
}

func TestLoad_FormsOverrides(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":           validSecret(),
		"PUBLIC_BASE_URL":      "https://crm.example.com/",
		"RECAPTCHA_SITE_KEY":   "site-key",
		"RECAPTCHA_SECRET_KEY": "secret-key",
		"RECAPTCHA_MIN_SCORE":  "0.7",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)

		assert.Equal(t, "https://crm.example.com", cfg.Forms.PublicBaseURL,
			"a trailing slash must be trimmed so links concatenate cleanly")
		assert.Equal(t, "site-key", cfg.Forms.RecaptchaSiteKey)
		assert.Equal(t, "secret-key", cfg.Forms.RecaptchaSecret)
		assert.Equal(t, 0.7, cfg.Forms.RecaptchaMinScore)
		assert.True(t, cfg.Forms.RecaptchaActive())
	})
}

func TestFormsConfig_RecaptchaActiveNeedsBothKeys(t *testing.T) {
	assert.False(t, FormsConfig{}.RecaptchaActive())
	assert.False(t, FormsConfig{RecaptchaSiteKey: "site"}.RecaptchaActive())
	assert.False(t, FormsConfig{RecaptchaSecret: "secret"}.RecaptchaActive())
	assert.True(t, FormsConfig{RecaptchaSiteKey: "site", RecaptchaSecret: "secret"}.RecaptchaActive())
}

func TestLoad_FormsMinScoreClamped(t *testing.T) {
	cases := []struct {
		value    string
		expected float64
	}{
		{value: "-1", expected: 0},
		{value: "2", expected: 1},
		{value: "0", expected: 0},
		{value: "1", expected: 1},
		// Unparseable input falls back to the default rather than failing boot.
		{value: "strict", expected: 0.5},
	}

	for _, tc := range cases {
		withCleanEnv(t, map[string]string{
			"JWT_SECRET":          validSecret(),
			"RECAPTCHA_MIN_SCORE": tc.value,
		}, func() {
			cfg, err := Load()
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, cfg.Forms.RecaptchaMinScore, "RECAPTCHA_MIN_SCORE=%q", tc.value)
		})
	}
}

func TestGetEnvAsFloat(t *testing.T) {
	const key = "FORMS_TEST_FLOAT"
	t.Cleanup(func() { os.Unsetenv(key) })

	cases := []struct {
		value      string
		set        bool
		defaultVal float64
		expected   float64
	}{
		{set: false, defaultVal: 0.5, expected: 0.5},
		{value: "", set: true, defaultVal: 0.5, expected: 0.5},
		{value: "0.9", set: true, defaultVal: 0.5, expected: 0.9},
		{value: " 0.25 ", set: true, defaultVal: 0.5, expected: 0.25},
		{value: "3", set: true, defaultVal: 0.5, expected: 3},
		{value: "-2.5", set: true, defaultVal: 0.5, expected: -2.5},
		{value: "half", set: true, defaultVal: 0.5, expected: 0.5},
	}

	for _, tc := range cases {
		os.Unsetenv(key)
		if tc.set {
			os.Setenv(key, tc.value)
		}
		assert.Equal(t, tc.expected, getEnvAsFloat(key, tc.defaultVal),
			"value=%q set=%v", tc.value, tc.set)
	}
}

func TestClampUnitInterval(t *testing.T) {
	assert.Equal(t, 0.0, clampUnitInterval(-0.1))
	assert.Equal(t, 0.0, clampUnitInterval(0))
	assert.Equal(t, 0.5, clampUnitInterval(0.5))
	assert.Equal(t, 1.0, clampUnitInterval(1))
	assert.Equal(t, 1.0, clampUnitInterval(1.5))
}

func TestParseTrustedProxies(t *testing.T) {
	assert.Nil(t, parseTrustedProxies(""))
	assert.Nil(t, parseTrustedProxies("  "))
	assert.Equal(t, []string{"10.0.0.0/8"}, parseTrustedProxies("10.0.0.0/8"))
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12"}, parseTrustedProxies("10.0.0.0/8, 172.16.0.0/12"))
	assert.Equal(t, []string{"192.168.1.1"}, parseTrustedProxies(" 192.168.1.1 "))
}
