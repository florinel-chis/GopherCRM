package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Database DatabaseConfig
	Server   ServerConfig
	JWT      JWTConfig
	Logging  LoggingConfig
	API      APIConfig
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
	Port int
	Mode string
}

type JWTConfig struct {
	Secret      string
	ExpiryHours int
}

type LoggingConfig struct {
	Level  string
	Format string
}

type APIConfig struct {
	Prefix string
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("error loading .env file: %w", err)
		}
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
			Port: getEnvAsInt("SERVER_PORT", 8080),
			Mode: getEnv("SERVER_MODE", "development"),
		},
		JWT: JWTConfig{
			Secret:      getEnv("JWT_SECRET", "default-secret-change-this"),
			ExpiryHours: getEnvAsInt("JWT_EXPIRY_HOURS", 24),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
		API: APIConfig{
			Prefix: getEnv("API_PREFIX", "/api/v1"),
		},
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