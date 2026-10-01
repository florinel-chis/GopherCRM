package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin only the plumbing: LOG_LEVEL must reach cfg.Database so
// database.Open can pick the SQL logger level from it. How database.Open maps
// each level is covered in internal/database/logging_test.go, not here.
//
// withCleanEnv does not clear LOG_LEVEL, so each test sets (or unsets) it via
// t.Setenv, which restores the caller's value when the test ends and keeps a
// developer shell's LOG_LEVEL from leaking in.

func TestLoad_LogLevelReachesDatabaseConfig(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "trace"} {
		t.Run(level, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", level)
			withCleanEnv(t, map[string]string{
				"JWT_SECRET": validSecret(),
			}, func() {
				cfg, err := Load()
				require.NoError(t, err)
				assert.Equal(t, level, cfg.Database.LogLevel,
					"LOG_LEVEL=%s should be copied into cfg.Database.LogLevel", level)
				assert.Equal(t, cfg.Logging.Level, cfg.Database.LogLevel,
					"the database and application loggers should read the same LOG_LEVEL")
			})
		})
	}
}

func TestLoad_LogLevelDatabaseDefault(t *testing.T) {
	// t.Setenv registers the restore; the unset makes LOG_LEVEL truly absent.
	t.Setenv("LOG_LEVEL", "")
	require.NoError(t, os.Unsetenv("LOG_LEVEL"))

	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "info", cfg.Database.LogLevel,
			"with LOG_LEVEL unset the database log level should default to info")
		assert.Equal(t, cfg.Logging.Level, cfg.Database.LogLevel,
			"the database default should match the application logger default")
	})
}
