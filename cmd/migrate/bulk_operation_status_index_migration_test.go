package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #196: 20261110120000_add_bulk_operation_status_index adds the non-unique
// index idx_bulk_operations_status on bulk_operations(status), the one
// AutoMigrate creates from the model tag, only when it is missing; rollback
// drops it only when it is present. No row changes.

const (
	bulkStatusIndexVersion         = 20261110120000
	bulkStatusIndexPreviousVersion = 20261109120000
	bulkStatusIndexName            = "20261110120000_add_bulk_operation_status_index"
	bulkStatusIndexLine            = "idx_bulk_operations_status (status) non_unique=1"
)

// TestBulkOperationStatusIndexMigrationFiles needs no database: the pair
// exists, creates and drops exactly the guarded non-unique index, changes no
// row, and is the newest migration of the directory.
func TestBulkOperationStatusIndexMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, bulkStatusIndexName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, bulkStatusIndexName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "'create index idx_bulk_operations_status on bulk_operations (status)'")
	assert.Contains(t, upSQL, "table_name = 'bulk_operations' and index_name = 'idx_bulk_operations_status'", "the create is guarded")
	assert.Contains(t, downSQL, "'drop index idx_bulk_operations_status on bulk_operations'")
	assert.Contains(t, downSQL, "table_name = 'bulk_operations' and index_name = 'idx_bulk_operations_status'", "the drop is guarded")
	for _, unique := range []string{"create unique", "unique index", "unique key", "add unique"} {
		assert.NotContains(t, upSQL, unique, "the index is not unique")
	}
	assert.NotContains(t, upSQL, "\nupdate ", "no row is changed")
	assert.NotContains(t, upSQL, "alter table", "no column changes")
	assert.NotContains(t, upSQL, "create table", "no new table")
	assert.NotContains(t, downSQL, "drop table", "rollback keeps the rows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, bulkStatusIndexName+".up.sql", newest, "the newest migration")
	assert.Equal(t, bulkStatusIndexVersion, latestMigrationVersion)
}

// bulkStatusIndexes returns the status index lines of bulk_operations.
func bulkStatusIndexes(t *testing.T, tdb *migrationTestDB) []string {
	t.Helper()
	out := []string{}
	for _, line := range snapshotTable(t, tdb, "bulk_operations").Indexes {
		if strings.Contains(line, "(status)") {
			out = append(out, line)
		}
	}
	return out
}

func withoutBulkStatusIndex(schema tableSchema) tableSchema {
	out := tableSchema{Columns: schema.Columns, ForeignKeys: schema.ForeignKeys, Indexes: []string{}}
	for _, line := range schema.Indexes {
		if !strings.Contains(line, "(status)") {
			out.Indexes = append(out.Indexes, line)
		}
	}
	return out
}

// seedBulkOperationRows writes rows the upgrade and the rollback must keep.
func seedBulkOperationRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT INTO users (email, password, first_name, last_name, role, is_active) VALUES ('bulk-owner@example.com', 'x', 'B', 'O', 'admin', 1)",
		"INSERT INTO bulk_operations (user_id, resource_type, type, status, total_items, created_at, updated_at) " +
			"SELECT id, 'leads', 'action', 'pending', 2, '2026-11-01 10:00:00', '2026-11-01 10:00:00' FROM users WHERE email = 'bulk-owner@example.com'",
		"INSERT INTO bulk_operations (user_id, resource_type, type, status, total_items, created_at, updated_at) " +
			"SELECT id, 'tasks', 'action', 'completed', 1, '2026-11-01 10:00:00', '2026-11-01 10:00:00' FROM users WHERE email = 'bulk-owner@example.com'",
	} {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func assertBulkOperationRowsKept(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	var pending, completed int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM bulk_operations WHERE status = 'pending'").Scan(&pending))
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM bulk_operations WHERE status = 'completed'").Scan(&completed))
	assert.Equal(t, 1, pending, "rows keep their status")
	assert.Equal(t, 1, completed)
}

func TestBulkOperationStatusIndexMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the index as AutoMigrate adds it on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "bulk_status_ref")
	require.NoError(t, reference.m.Migrate(bulkStatusIndexPreviousVersion))
	assert.Empty(t, bulkStatusIndexes(t, reference), "the previous head has no status index")
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.User{}, &models.BulkOperation{}))
	want := bulkStatusIndexes(t, reference)
	require.Equal(t, []string{bulkStatusIndexLine}, want, "AutoMigrate creates one non-unique single-column index")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "bulk_status_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, bulkStatusIndexes(t, tdb), "a fresh SQL schema carries the index AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "bulk_status_mig")
			require.NoError(t, tdb.m.Migrate(bulkStatusIndexPreviousVersion))
			seedBulkOperationRows(t, tdb)
			before := snapshotTable(t, tdb, "bulk_operations")
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.User{}, &models.BulkOperation{}))
				assert.Equal(t, want, bulkStatusIndexes(t, tdb))
			}

			require.NoError(t, tdb.m.Migrate(bulkStatusIndexVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(bulkStatusIndexVersion), version)
			assert.Equal(t, want, bulkStatusIndexes(t, tdb), "the SQL migration and AutoMigrate must produce the same index")
			if !autoMigrateFirst {
				assert.Equal(t, withoutBulkStatusIndex(before), withoutBulkStatusIndex(snapshotTable(t, tdb, "bulk_operations")),
					"the migration adds the index and touches nothing else")
			}
			assertBulkOperationRowsKept(t, tdb)

			// The application's startup on top leaves the index alone and the
			// migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, bulkStatusIndexes(t, tdb), "MigrateDatabase must not alter the index")
			require.ErrorIs(t, tdb.m.Migrate(bulkStatusIndexVersion), migrate.ErrNoChange)

			// Rollback removes exactly the index and keeps the rows.
			schemaBefore := snapshotTable(t, tdb, "bulk_operations")
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(bulkStatusIndexPreviousVersion), version)
			after := snapshotTable(t, tdb, "bulk_operations")
			assert.Empty(t, bulkStatusIndexes(t, tdb), "rollback removes the index")
			assert.Equal(t, withoutBulkStatusIndex(schemaBefore), withoutBulkStatusIndex(after),
				"rollback removes the index and nothing else")
			assert.Len(t, after.Indexes, len(schemaBefore.Indexes)-1)
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			assertBulkOperationRowsKept(t, tdb)

			// The down half is guarded: rolling back over a schema without the
			// index succeeds and drops nothing.
			require.NoError(t, tdb.m.Migrate(bulkStatusIndexVersion))
			_, err = tdb.sql.Exec("DROP INDEX idx_bulk_operations_status ON bulk_operations")
			require.NoError(t, err)
			require.NoError(t, tdb.m.Steps(-1), "the drop runs only when the index is present")
			assert.Empty(t, bulkStatusIndexes(t, tdb))

			// Reapply.
			require.NoError(t, tdb.m.Migrate(bulkStatusIndexVersion))
			assert.Equal(t, want, bulkStatusIndexes(t, tdb))
			assertBulkOperationRowsKept(t, tdb)
		})
	}
}
