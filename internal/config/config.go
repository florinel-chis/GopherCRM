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
			Port:           getEnvAsInt("SERVER_PORT", 8080),
			Mode:           getEnv("SERVER_MODE", "development"),
			TrustedProxies: parseTrustedProxies(getEnv("TRUSTED_PROXIES", "")),
		},
		JWT: JWTConfig{
			Secret:             jwtSecret,
			ExpiryHours:        getEnvAsInt("JWT_EXPIRY_HOURS", 24),
			AccessTokenMinutes: getEnvAsInt("JWT_ACCESS_TOKEN_MINUTES", 15),
			// REFRESH_TOKEN_EXPIRY_DAYS is the canonical variable; the older
			// JWT_REFRESH_TOKEN_DAYS is honoured as a fallback for existing
			// environments. Default is 30 days.
			RefreshTokenDays: getEnvAsInt("REFRESH_TOKEN_EXPIRY_DAYS", getEnvAsInt("JWT_REFRESH_TOKEN_DAYS", 30)),
			CookieSameSite:     getEnv("JWT_COOKIE_SAMESITE", "Lax"),
			CookieDomain:       getEnv("JWT_COOKIE_DOMAIN", ""),
			CookieSecure:       resolveCookieSecure(getEnv("SERVER_MODE", "development")),
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
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	proxies := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) == 0 {
		return nil
	}
	return proxies
}