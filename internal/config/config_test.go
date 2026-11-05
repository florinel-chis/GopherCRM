package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		"ANTHROPIC_API_KEY", "AEO_ANTHROPIC_MODEL", "AEO_ANTHROPIC_BASE_URL",
		"OPENAI_API_KEY", "AEO_OPENAI_MODEL", "AEO_OPENAI_BASE_URL",
		"GEMINI_API_KEY", "AEO_GEMINI_MODEL", "AEO_GEMINI_BASE_URL",
		"MOONSHOT_API_KEY", "AEO_KIMI_MODEL", "AEO_KIMI_BASE_URL",
		"PERPLEXITY_API_KEY", "AEO_PERPLEXITY_MODEL", "AEO_PERPLEXITY_BASE_URL",
		"AEO_CUSTOM_NAME", "AEO_CUSTOM_BASE_URL", "AEO_CUSTOM_MODEL", "AEO_CUSTOM_API_KEY",
		"AEO_SCHEDULE_ENABLED", "AEO_SCHEDULE_HOUR",
		"AEO_EGRESS_ALLOWED_ORIGINS", "AEO_EGRESS_LOCAL_ORIGINS",
		// Forms keys, for the same reason as the AEO ones.
		"PUBLIC_BASE_URL", "RECAPTCHA_SITE_KEY", "RECAPTCHA_SECRET_KEY", "RECAPTCHA_MIN_SCORE",
		// Database driver selection, so a developer shell pointing at SQLite
		// cannot flip the default-value assertions.
		"DB_DRIVER", "DB_PATH",
		// Automation worker settings, for the same reason as the AEO ones.
		"AUTOMATION_ENABLED", "AUTOMATION_POLL_INTERVAL_SECONDS",
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

func TestLoad_AutomationDefaultsAndClamp(t *testing.T) {
	withCleanEnv(t, map[string]string{"JWT_SECRET": validSecret()}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.True(t, cfg.Automation.Enabled, "the worker is on by default")
		assert.Equal(t, AutomationPollIntervalDefault, cfg.Automation.PollIntervalSeconds)
	})

	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"0", AutomationPollIntervalMin},
		{"-5", AutomationPollIntervalMin},
		{"7", 7},
		{"3600", AutomationPollIntervalMax},
		{"99999", AutomationPollIntervalMax},
		{"garbage", AutomationPollIntervalDefault},
	} {
		withCleanEnv(t, map[string]string{
			"JWT_SECRET":                       validSecret(),
			"AUTOMATION_ENABLED":               "false",
			"AUTOMATION_POLL_INTERVAL_SECONDS": tc.raw,
		}, func() {
			cfg, err := Load()
			assert.NoError(t, err)
			assert.False(t, cfg.Automation.Enabled)
			assert.Equalf(t, tc.want, cfg.Automation.PollIntervalSeconds, "AUTOMATION_POLL_INTERVAL_SECONDS=%s", tc.raw)
		})
	}
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

// jwtSecretRefusalPrefix is the start of the refusal message, which the runner's
// environment-failure classifier matches.
const jwtSecretRefusalPrefix = "JWT_SECRET cannot be the default"

// envExampleJWTSecret returns the value of the active JWT_SECRET= line of the
// repository's .env.example and the comment lines directly above that line, so
// the tests follow the file a fork really copies rather than a duplicated literal.
func envExampleJWTSecret(t *testing.T) (value, comment string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	require.NoError(t, err)

	lines := strings.Split(string(data), "\n")
	active := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "JWT_SECRET=") {
			require.Equal(t, -1, active, ".env.example has more than one active JWT_SECRET= line")
			active = i
		}
	}
	require.NotEqual(t, -1, active, ".env.example has no active JWT_SECRET= line")

	start := active
	for start > 0 && strings.HasPrefix(lines[start-1], "#") {
		start--
	}

	value = strings.TrimPrefix(lines[active], "JWT_SECRET=")
	require.NotEmpty(t, value, "the active JWT_SECRET= line of .env.example has no value")
	return value, strings.Join(lines[start:active], "\n")
}

// requireSecretRefused asserts that Load, under the current environment, returns
// no config and the placeholder refusal, and returns the error text.
func requireSecretRefused(t *testing.T) string {
	t.Helper()

	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Truef(t, strings.HasPrefix(err.Error(), jwtSecretRefusalPrefix),
		"error must start with %q, got %q", jwtSecretRefusalPrefix, err.Error())
	return err.Error()
}

func TestLoad_EnvExampleJWTSecretRejected(t *testing.T) {
	secret, _ := envExampleJWTSecret(t)
	withCleanEnv(t, map[string]string{"JWT_SECRET": secret}, func() {
		requireSecretRefused(t)
	})
}

func TestLoad_ChangeThisPrefixJWTSecretRejected(t *testing.T) {
	const edited = "CHANGE_THIS_edited_by_hand_but_still_the_placeholder"
	require.GreaterOrEqual(t, len(edited), 32, "the edited placeholder must clear the length check")

	withCleanEnv(t, map[string]string{"JWT_SECRET": edited}, func() {
		requireSecretRefused(t)
	})

	// The bare marker is also too short, and the placeholder message wins.
	withCleanEnv(t, map[string]string{"JWT_SECRET": "CHANGE_THIS"}, func() {
		msg := requireSecretRefused(t)
		assert.NotContains(t, msg, "at least 32 characters")
	})
}

func TestLoad_ExampleJWTSecretRejectedInEveryServerMode(t *testing.T) {
	secret, _ := envExampleJWTSecret(t)

	for _, mode := range []string{"", "development", "production"} {
		env := map[string]string{"JWT_SECRET": secret}
		if mode != "" {
			env["SERVER_MODE"] = mode
		}
		withCleanEnv(t, env, func() {
			if mode == "" {
				_, set := os.LookupEnv("SERVER_MODE")
				require.False(t, set, "SERVER_MODE must be unset for this case")
			}
			requireSecretRefused(t)
		})
	}
}

func TestLoad_ExampleJWTSecretErrorGivesRemedyAndOmitsValue(t *testing.T) {
	const suffix = "q7Zunique9SuffixK3"

	for _, secret := range []string{"CHANGE_THIS_" + suffix + "_padding_past_32_chars", "CHANGE_THIS"} {
		withCleanEnv(t, map[string]string{"JWT_SECRET": secret}, func() {
			msg := requireSecretRefused(t)
			assert.Contains(t, msg, "JWT_SECRET")
			assert.Contains(t, msg, "openssl rand -base64 32")
			assert.NotContains(t, msg, secret)
			assert.NotContains(t, msg, suffix)
			assert.NotContains(t, msg, "CHANGE_THIS")
		})
	}
}

func TestLoad_SecretContainingChangeThisAccepted(t *testing.T) {
	for _, secret := range []string{
		"xCHANGE_THIS_after_the_first_character_of_the_value",
		"a-random-looking-value-CHANGE_THIS-with-more-after-it",
		// The match is case-sensitive.
		"change_this_in_lower_case_is_not_the_placeholder",
	} {
		require.GreaterOrEqual(t, len(secret), 32, "the secret must clear the length check")

		withCleanEnv(t, map[string]string{"JWT_SECRET": secret}, func() {
			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, secret, cfg.JWT.Secret)
		})
	}
}

func TestLoad_LegacyDefaultJWTSecretKeepsErrorPrefix(t *testing.T) {
	withCleanEnv(t, map[string]string{"JWT_SECRET": "default-secret-change-this"}, func() {
		msg := requireSecretRefused(t)
		assert.NotContains(t, msg, "default-secret-change-this")
	})
}

func TestEnvExample_JWTSecretCommentStatesRefusal(t *testing.T) {
	_, comment := envExampleJWTSecret(t)

	assert.Contains(t, comment, "refused")
	assert.Contains(t, comment, "CHANGE_THIS")
	assert.NotContains(t, comment, "long enough")
	assert.NotContains(t, comment, "#210")
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

// TestDatabaseConfig_HasNoSSLModeField pins the removal of DB_SSL_MODE. The
// field was parsed from the environment but never reached a DSN, so a re-added
// field would be dead configuration again — and only a structural assertion
// catches that, since re-adding it changes no observable value.
func TestDatabaseConfig_HasNoSSLModeField(t *testing.T) {
	_, found := reflect.TypeOf(DatabaseConfig{}).FieldByName("SSLMode")
	assert.False(t, found,
		"DB_SSL_MODE was removed as dead configuration; wire TLS into DSN() if it is ever needed")
}

// TestDSN_CarriesNoTLSParameters is built from a literal config rather than
// Load() so an exported DB_* value in the developer's shell cannot smuggle
// "ssl"/"tls" into the DSN and fail this spuriously.
func TestDSN_CarriesNoTLSParameters(t *testing.T) {
	dbCfg := DatabaseConfig{
		User:     "testuser",
		Password: "testpass",
		Host:     "127.0.0.1",
		Port:     3306,
		Name:     "testdb",
	}
	dsn := strings.ToLower(dbCfg.DSN())
	assert.NotContains(t, dsn, "ssl")
	assert.NotContains(t, dsn, "tls")
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

func TestLoad_DatabaseDriverDefaultsToMySQL(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, DriverMySQL, cfg.Database.Driver,
			"MySQL stays the default driver when DB_DRIVER is unset")
		assert.Equal(t, "gophercrm.db", cfg.Database.Path)
	})
}

func TestLoad_DatabaseDriverSQLite(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
		"DB_DRIVER":  "sqlite",
		"DB_PATH":    "/var/lib/gophercrm/crm.db",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, DriverSQLite, cfg.Database.Driver)
		assert.Equal(t, "/var/lib/gophercrm/crm.db", cfg.Database.Path)
	})
}

func TestLoad_DatabaseDriverInvalidRejected(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
		"DB_DRIVER":  "postgres",
	}, func() {
		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `invalid DB_DRIVER "postgres"`)
		assert.Contains(t, err.Error(), "mysql")
		assert.Contains(t, err.Error(), "sqlite")
	})
}

func TestLoad_SQLitePathWithQueryRejected(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
		"DB_DRIVER":  "sqlite",
		"DB_PATH":    "crm.db?_pragma=journal_mode(DELETE)",
	}, func() {
		_, err := Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "DB_PATH")
		assert.Contains(t, err.Error(), "?")
	})
}

func TestLoad_MySQLPathWithQueryIgnored(t *testing.T) {
	// The DB_PATH check only guards the SQLite DSN; MySQL never reads it.
	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
		"DB_PATH":    "irrelevant?x=1",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, DriverMySQL, cfg.Database.Driver)
	})
}

func TestParseTrustedProxies(t *testing.T) {
	assert.Nil(t, parseTrustedProxies(""))
	assert.Nil(t, parseTrustedProxies("  "))
	assert.Equal(t, []string{"10.0.0.0/8"}, parseTrustedProxies("10.0.0.0/8"))
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12"}, parseTrustedProxies("10.0.0.0/8, 172.16.0.0/12"))
	assert.Equal(t, []string{"192.168.1.1"}, parseTrustedProxies(" 192.168.1.1 "))
}

func TestLoad_AEOBaseURLs(t *testing.T) {
	withCleanEnv(t, map[string]string{
		"JWT_SECRET":              validSecret(),
		"AEO_ANTHROPIC_BASE_URL":  "https://anthropic.example.test",
		"AEO_OPENAI_BASE_URL":     "https://openai.example.test/v1",
		"AEO_GEMINI_BASE_URL":     "https://gemini.example.test/v1beta/openai/",
		"AEO_KIMI_BASE_URL":       "https://kimi.example.test/v1",
		"AEO_PERPLEXITY_BASE_URL": "https://perplexity.example.test",
	}, func() {
		cfg, err := Load()
		assert.NoError(t, err)
		assert.Equal(t, "https://anthropic.example.test", cfg.AEO.AnthropicBaseURL)
		assert.Equal(t, "https://openai.example.test/v1", cfg.AEO.OpenAIBaseURL)
		assert.Equal(t, "https://gemini.example.test/v1beta/openai/", cfg.AEO.GeminiBaseURL)
		assert.Equal(t, "https://kimi.example.test/v1", cfg.AEO.KimiBaseURL)
		assert.Equal(t, "https://perplexity.example.test", cfg.AEO.PerplexityBaseURL)
	})
}
