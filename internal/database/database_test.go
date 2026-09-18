package database

import (
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// closeDB releases the pooled connections so the temp directory can be removed
// on Windows-like semantics and the WAL is checkpointed.
func closeDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func TestOpen_SQLiteAppliesPragmas(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "pragmas.db"),
	}

	db, err := Open(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })

	var journalMode string
	require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
	assert.Equal(t, "wal", journalMode)

	var foreignKeys int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	assert.Equal(t, 1, foreignKeys)

	var busyTimeout int
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
	assert.Equal(t, sqliteBusyTimeoutMS, busyTimeout)
}

// TestSQLiteDSN_BusyTimeoutReachesDriver is what makes the busy_timeout
// assertion above meaningful. The driver's own default is 5000, so observing
// 5000 proves nothing on its own; a non-default value can only be explained by
// the DSN pragma being present and honoured.
func TestSQLiteDSN_BusyTimeoutReachesDriver(t *testing.T) {
	const nonDefaultTimeoutMS = 7500
	require.NotEqual(t, nonDefaultTimeoutMS, sqliteBusyTimeoutMS,
		"the probe value must differ from the production one to be falsifiable")

	dsn := sqliteDSN(filepath.Join(t.TempDir(), "busy.db"), nonDefaultTimeoutMS)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })

	var busyTimeout int
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
	assert.Equal(t, nonDefaultTimeoutMS, busyTimeout)
}

func TestOpen_SQLiteSerialisesConnections(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "pool.db"),
	}

	db, err := Open(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })

	sqlDB, err := db.DB()
	require.NoError(t, err)
	assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections,
		"SQLite writes are serialised through a single connection")
}

func TestOpen_SQLiteUsesUTCNowFunc(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "now.db"),
	}

	db, err := Open(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })

	assert.Equal(t, "UTC", db.NowFunc().Location().String())
}

func TestOpen_UnknownDriverRejected(t *testing.T) {
	for _, driver := range []string{"", "postgres"} {
		db, err := Open(&config.DatabaseConfig{Driver: driver})
		assert.Nil(t, db)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mysql")
		assert.Contains(t, err.Error(), "sqlite")
	}
}
