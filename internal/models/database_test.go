package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Point the package-level DB to our in-memory database
	origDB := DB
	DB = db
	defer func() { DB = origDB }()

	err = MigrateDatabase()
	require.NoError(t, err)

	// Verify all expected tables exist
	expectedTables := []string{
		"users",
		"leads",
		"customers",
		"tickets",
		"tasks",
		"api_keys",
		"configurations",
		"refresh_tokens",
		"bulk_operations",
		"bulk_operation_items",
	}

	for _, table := range expectedTables {
		assert.True(t, db.Migrator().HasTable(table), "table %s should exist", table)
	}
}

func TestRefreshTokenIsRevokedColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	origDB := DB
	DB = db
	defer func() { DB = origDB }()

	err = MigrateDatabase()
	require.NoError(t, err)

	// Verify the column is named is_revoked (not revoked)
	assert.True(t, db.Migrator().HasColumn(&RefreshToken{}, "is_revoked"),
		"refresh_tokens table should have is_revoked column")
}
