package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #224: 20261031120000_retire_inert_configurations deletes the two seeded
// settings nothing read. It is the newest migration, so the fresh and upgrade
// roots in main_test.go also check that it ran.

const (
	latestMigrationVersion      = 20261031120000
	retireConfigurationsMigName = "20261031120000_retire_inert_configurations"
)

// TestRetireInertConfigurationsMigrationFiles needs no database: the pair
// names exactly the keys the application retires, in both directions.
func TestRetireInertConfigurationsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, retireConfigurationsMigName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, retireConfigurationsMigName+".down.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, models.RetiredConfigurationKeys)
	for _, key := range models.RetiredConfigurationKeys {
		assert.Contains(t, string(up), "'"+key+"'")
		assert.Contains(t, string(down), "('"+key+"'")
	}
	assert.Contains(t, strings.ToUpper(string(up)), "DELETE FROM CONFIGURATIONS")
	assert.Contains(t, strings.ToUpper(string(down)), "INSERT IGNORE INTO CONFIGURATIONS")
}

func assertRetiredConfigurations(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	args := make([]interface{}, len(models.RetiredConfigurationKeys))
	for i, key := range models.RetiredConfigurationKeys {
		args[i] = key
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	var count int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM configurations WHERE config_key IN ("+placeholders+")", args...,
	).Scan(&count))
	assert.Equal(t, want, count, "retired configuration rows")
}
