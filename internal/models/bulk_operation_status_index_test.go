package models

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBulkOperationStatusIndexSQLite is the SQLite half of #196: the
// stale-pending sweep reads its candidates through a non-unique index on
// bulk_operations(status). SQLite installs never run the SQL migration, so
// MigrateDatabase must create it on a fresh database and on one migrated
// before this change, and a second run must change nothing.
func TestBulkOperationStatusIndexSQLite(t *testing.T) {
	assertIndex := func(t *testing.T) {
		t.Helper()
		require.True(t, DB.Migrator().HasIndex(&BulkOperation{}, "idx_bulk_operations_status"))
		sql := indexSQL(t, "bulk_operations", "idx_bulk_operations_status")
		require.NotEmpty(t, sql)
		assert.NotContains(t, strings.ToUpper(sql), "UNIQUE", "the index is not unique")
		assert.Contains(t, sql, "`status`")

		var columns []string
		require.NoError(t, DB.Raw("SELECT name FROM pragma_index_info('idx_bulk_operations_status') ORDER BY seqno").
			Scan(&columns).Error)
		assert.Equal(t, []string{"status"}, columns, "a single-column index on status")
	}

	t.Run("fresh database", func(t *testing.T) {
		origDB := DB
		t.Cleanup(func() {
			require.NoError(t, CloseDatabase())
			DB = origDB
		})
		require.NoError(t, InitDatabase(&config.DatabaseConfig{
			Driver: config.DriverSQLite,
			Path:   filepath.Join(t.TempDir(), "bulk-status-index.db"),
		}))
		require.NoError(t, MigrateDatabase())
		assertIndex(t)

		schema := schemaSnapshot(t)
		require.NoError(t, MigrateDatabase())
		assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase leaves the schema alone")
	})

	t.Run("database migrated before this change", func(t *testing.T) {
		openV120Fixture(t)
		require.True(t, DB.Migrator().HasTable("bulk_operations"), "the fixture holds the table")
		require.False(t, DB.Migrator().HasIndex(&BulkOperation{}, "idx_bulk_operations_status"),
			"the fixture predates the index")
		require.NoError(t, DB.Exec("INSERT INTO bulk_operations (created_at, updated_at, user_id, resource_type, type, status, total_items) "+
			"SELECT CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, id, 'leads', 'action', 'pending', 1 FROM users ORDER BY id LIMIT 1").Error)

		require.NoError(t, MigrateDatabase())
		assertUpgradedV120(t)
		assertIndex(t)

		var pending int64
		require.NoError(t, DB.Table("bulk_operations").Where("status = ?", "pending").Count(&pending).Error)
		assert.EqualValues(t, 1, pending, "existing rows survive the index build")

		schema := schemaSnapshot(t)
		require.NoError(t, MigrateDatabase())
		assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase leaves the schema alone")
	})
}
