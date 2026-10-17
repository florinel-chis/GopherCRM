package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCommandSupported_SQLiteRejectsDatabaseCommands(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version"} {
		t.Run(command, func(t *testing.T) {
			err := checkCommandSupported(command, config.DriverSQLite)
			require.ErrorIs(t, err, ErrSQLiteUnsupported)
			assert.Equal(t,
				"SQL migrations are MySQL-only; SQLite schemas are created by auto-migration at startup",
				err.Error())
		})
	}
}

func TestCheckCommandSupported_SQLiteAllowsCreate(t *testing.T) {
	assert.NoError(t, checkCommandSupported(commandCreate, config.DriverSQLite))
}

func TestCheckCommandSupported_MySQLAllowsEveryCommand(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version", commandCreate, "bogus"} {
		assert.NoError(t, checkCommandSupported(command, config.DriverMySQL), "command %q", command)
	}
}

func TestMigrationDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "appends with ampersand when query parameters exist",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm?charset=utf8mb4&parseTime=True&loc=Local",
			want: "root:secret@tcp(localhost:3306)/gocrm?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true",
		},
		{
			name: "appends with question mark when no query parameters exist",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm",
			want: "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true",
		},
		{
			name: "leaves DSN untouched if multiStatements is already present",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true&charset=utf8mb4",
			want: "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true&charset=utf8mb4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, migrationDSN(tt.dsn))
		})
	}
}

func TestMigrationFiles_PairsAndCompleteness(t *testing.T) {
	migrateDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(migrateDir)
	require.NoError(t, err)

	upFiles := make(map[string]bool)
	downFiles := make(map[string]bool)
	allSQL := strings.Builder{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		path := filepath.Join(migrateDir, entry.Name())
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotEmpty(t, strings.TrimSpace(string(data)), "%s must not be empty", entry.Name())

		if strings.HasSuffix(entry.Name(), ".up.sql") {
			base := strings.TrimSuffix(entry.Name(), ".up.sql")
			upFiles[base] = true
			allSQL.Write(data)
			allSQL.WriteString("\n")
		} else if strings.HasSuffix(entry.Name(), ".down.sql") {
			base := strings.TrimSuffix(entry.Name(), ".down.sql")
			downFiles[base] = true
		}
	}

	// Every up migration must have a matching down migration and vice versa
	for up := range upFiles {
		assert.True(t, downFiles[up], "missing down migration for %s.up.sql", up)
	}
	for down := range downFiles {
		assert.True(t, upFiles[down], "missing up migration for %s.down.sql", down)
	}

	// Verify all expected tables from models.MigrateDatabase() are created across the up migrations
	expectedTables := []string{
		"users", "customers", "leads", "tickets", "tasks", "api_keys",
		"configurations", "refresh_tokens", "companies", "deals", "deal_stage_changes",
		"labels", "task_labels", "password_reset_tokens", "bulk_operations",
		"bulk_operation_items", "aeo_profiles", "aeo_prompts", "aeo_runs",
		"aeo_answers", "aeo_citations", "forms", "form_submissions", "form_confirmation_tokens",
	}

	combinedSQL := strings.ToLower(allSQL.String())
	for _, table := range expectedTables {
		pattern1 := "create table " + table + " "
		pattern2 := "create table if not exists " + table + " "
		assert.True(t, strings.Contains(combinedSQL, pattern1) || strings.Contains(combinedSQL, pattern2),
			"table %q should be created in migrations", table)
	}
}
