package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitDatabase_SQLiteProductionPath exercises the real bootstrap sequence a
// SQLite deployment goes through: InitDatabase (which delegates to
// database.Open) → MigrateDatabase → traffic → CloseDatabase.
func TestInitDatabase_SQLiteProductionPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.db")

	origDB := DB
	t.Cleanup(func() { DB = origDB })

	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   path,
	}))
	require.NotNil(t, DB)
	require.NoError(t, MigrateDatabase())

	user := User{
		Email:     "sqlite-roundtrip@example.com",
		Password:  "not-a-real-hash",
		FirstName: "Ada",
		LastName:  "Lovelace",
		Role:      RoleAdmin,
	}
	require.NoError(t, DB.Create(&user).Error)
	require.NotZero(t, user.ID)

	var found User
	require.NoError(t, DB.First(&found, user.ID).Error)
	assert.Equal(t, "sqlite-roundtrip@example.com", found.Email)
	assert.Equal(t, "Lovelace", found.LastName)

	// journal_mode(WAL) is only observable while the database is open.
	_, err := os.Stat(path + "-wal")
	require.NoError(t, err, "a WAL file must exist while the connection is live")

	require.NoError(t, CloseDatabase())

	_, err = os.Stat(path + "-wal")
	assert.True(t, os.IsNotExist(err),
		"closing the pool must checkpoint and remove the WAL file, got %v", err)
}

// TestCloseDatabase_IsIdempotent covers the shutdown path where more than one
// caller may close: the first close clears the handle, the second does nothing
// rather than operating on an already-closed pool.
func TestCloseDatabase_IsIdempotent(t *testing.T) {
	origDB := DB
	t.Cleanup(func() { DB = origDB })

	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "idempotent.db"),
	}))
	require.NotNil(t, DB)

	require.NoError(t, CloseDatabase())
	assert.Nil(t, DB, "a successful close must clear the global handle")

	assert.NoError(t, CloseDatabase())
	assert.Nil(t, DB)
}

func TestCloseDatabase_NilHandleIsSafe(t *testing.T) {
	origDB := DB
	t.Cleanup(func() { DB = origDB })

	DB = nil
	assert.NoError(t, CloseDatabase())
}
