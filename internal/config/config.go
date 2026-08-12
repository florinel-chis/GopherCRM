package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	Database DatabaseConfig
	Server   ServerConfig
	JWT      JWTConfig
	Logging  LoggingConfig
	API      APIConfig
	SMTP     SMTPConfig
	App      AppConfig
	AEO      AEOConfig
	Forms    FormsConfig
}

type DatabaseConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

type ServerConfig struct {
	Port           int
	Mode           string
	TrustedProxies []string // Comma-separated CIDRs from TRUSTED_PROXIES env var; empty means trust no proxies
	// Comma-separated origins from CORS_ALLOWED_ORIGINS, allowed in addition
	// to the built-in development defaults. Required whenever the UI is
	// served from any origin the defaults do not cover (e.g. a non-3000
	// UI_PORT in docker compose).
	CORSExtraOrigins []string
}

type JWTConfig struct {
	Secret             string
	ExpiryHours        int
	AccessTokenMinutes int
	RefreshTokenDays   int
	CookieSameSite     string
	CookieDomain       string
	CookieSecure       bool
}

type CSRFConfig struct {
	Enabled    bool
	HeaderName string
	CookieName string
	Secret     string
}

type LoggingConfig struct {
	Level  string
	Format string
}

type APIConfig struct {
	Prefix       string
	APIKeySecret string
}

// SMTPConfig configures outbound transactional mail. An empty Host selects
// the logging fallback mailer instead of a real SMTP transport.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// AppConfig holds settings about the user-facing application, used when the
// backend must build absolute links into the frontend (e.g. reset links).
type AppConfig struct {
	BaseURL string
}

// AEOConfig holds the answer-engine credentials and the daily-run schedule for
// the AEO module. Every key is optional: an engine whose key is empty is simply
// absent, and the custom engine is absent unless CustomBaseURL is set. Load()
// never fails because an AEO key is missing.
type AEOConfig struct {
	// ANTHROPIC_API_KEY is shared between the Anthropic answer engine and
	// prompt generation.
	AnthropicAPIKey string
	AnthropicModel  string

	OpenAIAPIKey string
	OpenAIModel  string

	GeminiAPIKey string
	GeminiModel  string

	MoonshotAPIKey string
	KimiModel      string

	PerplexityAPIKey string
	PerplexityModel  string

	// Custom is any OpenAI-compatible server (LM Studio, vLLM, Ollama, TGI).
	// CustomAPIKey is optional; local servers usually need none.
	CustomName    string
	CustomBaseURL string
	CustomModel   string
	CustomAPIKey  string

	ScheduleEnabled bool
	ScheduleHour    int // local time, 0..23

	// QueryTimeoutSeconds is the per-query deadline. The 60s default suits
	// hosted APIs; a single-GPU self-hosted server answering serially needs
	// more headroom, because queued requests carry the wait in their latency.
	QueryTimeoutSeconds int
}

// FormsConfig holds the settings of the forms module. Every key is optional:
// with no reCAPTCHA pair configured the check is simply unavailable and forms
// that ask for it fall back to the remaining spam layers. Load() never fails
// because a forms key is missing.
type FormsConfig struct {
	// PublicBaseURL is the externally reachable base URL of this backend. It
	// is what external visitors hit, so it differs from AppConfig.BaseURL
	// (the CRM frontend). Used to build confirmation links, the hosted form
	// page URL and the embed snippet. Stored without a trailing slash.
	PublicBaseURL string

	RecaptchaSiteKey string
	RecaptchaSecret  string

	// RecaptchaMinScore is the lowest v3 score still treated as human,
	// clamped to [0,1] so a mistyped value cannot disable or block everything.
	RecaptchaMinScore float64
}

// RecaptchaActive reports whether the reCAPTCHA check can run at all. Both
// halves of the key pair are needed: the site key for the browser, the secret
// for the server-side verification.
func (f FormsConfig) RecaptchaActive() bool {
	return f.RecaptchaSiteKey != "" && f.RecaptchaSecret != ""
}

type RateLimitConfig struct {
	PublicEndpoints  int
	AuthenticatedAPI int
	AdminEndpoints   int
	WindowDuration   int // in minutes
	BurstMultiplier  int
	Enabled          bool
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("error loading .env file: %w", err)
		}
	}

	// Validate JWT secret - CRITICAL SECURITY REQUIREMENT
	jwtSecret := getEnv("JWT_SECRET", "")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable must be set")
	}
	if jwtSecret == "default-secret-change-this" {
		return nil, fmt.Errorf("JWT_SECRET cannot be the default value 'default-secret-change-this' - please set a secure secret")
	}
	if len(jwtSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters long for security (current length: %d)", len(jwtSecret))
	}

	config := &Config{
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnvAsInt("DB_PORT", 3306),
			Name:     getEnv("DB_NAME", "gocrm"),
			User:     getEnv("DB_USER", "root"),
			Password: getEnv("DB_PASSWORD", ""),
			SSLMode:  getEnv("DB_SSL_MODE", "disable"),
		},
		Server: ServerConfig{
			Port:             getEnvAsInt("SERVER_PORT", 8080),
			Mode:             getEnv("SERVER_MODE", "development"),
			TrustedProxies:   parseTrustedProxies(getEnv("TRUSTED_PROXIES", "")),
			CORSExtraOrigins: parseCommaList(getEnv("CORS_ALLOWED_ORIGINS", "")),
		},
		JWT: JWTConfig{
			Secret:             jwtSecret,
			ExpiryHours:        getEnvAsInt("JWT_EXPIRY_HOURS", 24),
			AccessTokenMinutes: getEnvAsInt("JWT_ACCESS_TOKEN_MINUTES", 15),
			// REFRESH_TOKEN_EXPIRY_DAYS is the canonical variable; the older
			// JWT_REFRESH_TOKEN_DAYS is honoured as a fallback for existing
			// environments. Default is 30 days.
			RefreshTokenDays: getEnvAsInt("REFRESH_TOKEN_EXPIRY_DAYS", getEnvAsInt("JWT_REFRESH_TOKEN_DAYS", 30)),
			CookieSameSite:   getEnv("JWT_COOKIE_SAMESITE", "Lax"),
			CookieDomain:     getEnv("JWT_COOKIE_DOMAIN", ""),
			CookieSecure:     resolveCookieSecure(getEnv("SERVER_MODE", "development")),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
		API: APIConfig{
			Prefix:       getEnv("API_PREFIX", "/api/v1"),
			APIKeySecret: getEnv("API_KEY_SECRET", jwtSecret),
		},
		SMTP: SMTPConfig{
			Host:     getEnv("SMTP_HOST", ""),
			Port:     getEnvAsInt("SMTP_PORT", 587),
			User:     getEnv("SMTP_USER", ""),
			Password: getEnv("SMTP_PASSWORD", ""),
			From:     getEnv("SMTP_FROM", "no-reply@localhost"),
		},
		App: AppConfig{
			BaseURL: strings.TrimRight(getEnv("APP_BASE_URL", "http://localhost:5173"), "/"),
		},
		AEO: AEOConfig{
			AnthropicAPIKey:     getEnv("ANTHROPIC_API_KEY", ""),
			AnthropicModel:      getEnv("AEO_ANTHROPIC_MODEL", "claude-opus-5"),
			OpenAIAPIKey:        getEnv("OPENAI_API_KEY", ""),
			OpenAIModel:         getEnv("AEO_OPENAI_MODEL", "gpt-4o-mini"),
			GeminiAPIKey:        getEnv("GEMINI_API_KEY", ""),
			GeminiModel:         getEnv("AEO_GEMINI_MODEL", "gemini-flash-latest"),
			MoonshotAPIKey:      getEnv("MOONSHOT_API_KEY", ""),
			KimiModel:           getEnv("AEO_KIMI_MODEL", "moonshot-v1-8k"),
			PerplexityAPIKey:    getEnv("PERPLEXITY_API_KEY", ""),
			PerplexityModel:     getEnv("AEO_PERPLEXITY_MODEL", "sonar"),
			CustomName:          getEnv("AEO_CUSTOM_NAME", "custom"),
			CustomBaseURL:       getEnv("AEO_CUSTOM_BASE_URL", ""),
			CustomModel:         getEnv("AEO_CUSTOM_MODEL", "openai/gpt-oss-20b"),
			CustomAPIKey:        getEnv("AEO_CUSTOM_API_KEY", ""),
			ScheduleEnabled:     getEnvAsBool("AEO_SCHEDULE_ENABLED", true),
			ScheduleHour:        clampHour(getEnvAsInt("AEO_SCHEDULE_HOUR", 6)),
			QueryTimeoutSeconds: getEnvAsInt("AEO_QUERY_TIMEOUT_SECONDS", 60),
		},
		Forms: FormsConfig{
			PublicBaseURL:     strings.TrimRight(getEnv("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
			RecaptchaSiteKey:  getEnv("RECAPTCHA_SITE_KEY", ""),
			RecaptchaSecret:   getEnv("RECAPTCHA_SECRET_KEY", ""),
			RecaptchaMinScore: clampUnitInterval(getEnvAsFloat("RECAPTCHA_MIN_SCORE", 0.5)),
		},
	}

	if config.Server.Mode == "production" && !config.JWT.CookieSecure {
		log.Warn("CookieSecure is false while running in production mode; cookies will be sent over insecure connections")
	}

	return config, nil
}

func (c *DatabaseConfig) DSN() string {
	// MySQL DSN format: username:password@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		c.User, c.Password, c.Host, c.Port, c.Name)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultValue
}

// getEnvAsFloat reads a float env var, falling back to the default when the
// value is unset or cannot be parsed. Surrounding spaces are ignored.
func getEnvAsFloat(key string, defaultValue float64) float64 {
	valueStr := strings.TrimSpace(getEnv(key, ""))
	if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return value
	}
	return defaultValue
}

// getEnvAsBool reads a boolean env var. "true"/"1" enable it, "false"/"0"
// disable it; anything else (including an unset or empty value) falls back to
// the default. Comparison is case-insensitive and surrounding spaces are
// ignored, so "True" and " 1 " behave as expected.
func getEnvAsBool(key string, defaultValue bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return defaultValue
	}
}

// clampHour keeps an hour-of-day inside 0..23 instead of rejecting it, so a
// mistyped schedule never prevents the application from starting.
func clampHour(hour int) int {
	if hour < 0 {
		return 0
	}
	if hour > 23 {
		return 23
	}
	return hour
}

// clampUnitInterval keeps a probability-like setting inside [0,1] instead of
// rejecting it, so a mistyped threshold never prevents the application from
// starting.
func clampUnitInterval(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// resolveCookieSecure determines the CookieSecure value.
// If JWT_COOKIE_SECURE is explicitly set, that value is used.
// Otherwise, it defaults to true in production mode and false otherwise.
func resolveCookieSecure(serverMode string) bool {
	if v, ok := os.LookupEnv("JWT_COOKIE_SECURE"); ok {
		return v == "true"
	}
	return serverMode == "production"
}

// parseTrustedProxies parses a comma-separated list of CIDRs/IPs.
// Returns nil (not an empty slice) when input is empty, which signals
// "trust no proxies" to Gin's SetTrustedProxies.
func parseTrustedProxies(val string) []string {
	return parseCommaList(val)
}

// parseCommaList splits a comma-separated env value into trimmed, non-empty
// entries; nil when the value is empty.
func parseCommaList(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	entries := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			entries = append(entries, p)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}
