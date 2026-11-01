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

// #240: 20261101120000_add_task_deal_link adds tasks.deal_id, idx_tasks_deal_id
// and fk_tasks_deal under the names AutoMigrate gives them, each only when
// missing, and rollback removes exactly those three.

const (
	taskDealLinkVersion         = 20261101120000
	taskDealLinkPreviousVersion = 20261031120000
	taskDealLinkName            = "20261101120000_add_task_deal_link"

	taskDealIndexLine = "idx_tasks_deal_id (deal_id) non_unique=1"
	taskDealFKLine    = "fk_tasks_deal deal_id -> deals.id"
)

// TestTaskDealLinkMigrationFiles needs no database: the pair exists and names
// the three objects in both directions.
func TestTaskDealLinkMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, taskDealLinkName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, taskDealLinkName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "'alter table tasks add column deal_id bigint unsigned null'")
	assert.Contains(t, upSQL, "'create index idx_tasks_deal_id on tasks (deal_id)'")
	assert.Contains(t, upSQL, "'alter table tasks add constraint fk_tasks_deal foreign key (deal_id) references deals (id)'")
	assert.Contains(t, downSQL, "'alter table tasks drop foreign key fk_tasks_deal'")
	assert.Contains(t, downSQL, "'drop index idx_tasks_deal_id on tasks'")
	assert.Contains(t, downSQL, "'alter table tasks drop column deal_id'")

	assert.NotContains(t, upSQL, "\nupdate ", "existing tasks are not backfilled")
	assert.NotContains(t, downSQL, "drop table", "rollback keeps the rows")

	// Index before foreign key, else InnoDB makes an implicit index of its own.
	assert.Less(t, strings.Index(upSQL, "'create index idx_tasks_deal_id"), strings.Index(upSQL, "'alter table tasks add constraint fk_tasks_deal"))
	// Foreign key first on the way down: the column cannot go while it is referenced.
	assert.Less(t, strings.Index(downSQL, "'alter table tasks drop foreign key"), strings.Index(downSQL, "'alter table tasks drop column"))
}

// taskDealObjects is the part of the tasks table the migration owns. Every index
// over the column counts, so an implicit extra one breaks parity.
type taskDealObjects struct {
	Columns     []string
	Indexes     []string
	ForeignKeys []string
}

func snapshotTaskDealObjects(t *testing.T, tdb *migrationTestDB) taskDealObjects {
	t.Helper()
	schema := snapshotTable(t, tdb, "tasks")
	objects := taskDealObjects{Columns: []string{}, Indexes: []string{}, ForeignKeys: []string{}}
	for _, line := range schema.Columns {
		if strings.HasPrefix(line, "deal_id ") {
			objects.Columns = append(objects.Columns, line)
		}
	}
	for _, line := range schema.Indexes {
		if strings.Contains(line, "(deal_id)") {
			objects.Indexes = append(objects.Indexes, line)
		}
	}
	for _, line := range schema.ForeignKeys {
		if strings.Contains(line, " deal_id ") {
			objects.ForeignKeys = append(objects.ForeignKeys, line)
		}
	}
	return objects
}

// withoutTaskDealObjects is a snapshot of tasks minus the three objects.
func withoutTaskDealObjects(schema tableSchema) tableSchema {
	out := tableSchema{}
	for _, line := range schema.Columns {
		if !strings.HasPrefix(line, "deal_id ") {
			out.Columns = append(out.Columns, line)
		}
	}
	for _, line := range schema.Indexes {
		if !strings.Contains(line, "(deal_id)") {
			out.Indexes = append(out.Indexes, line)
		}
	}
	for _, line := range schema.ForeignKeys {
		if !strings.Contains(line, " deal_id ") {
			out.ForeignKeys = append(out.ForeignKeys, line)
		}
	}
	return out
}

// seedTaskDealRows writes two existing tasks the upgrade must keep.
func seedTaskDealRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT INTO users (email, password, first_name, last_name, role, is_active) VALUES ('task-owner@example.com', 'x', 'O', 'W', 'sales', 1)",
		"INSERT INTO tasks (title, status, priority, assigned_to_id) VALUES ('Kept one', 'pending', 'medium', 1)",
		"INSERT INTO tasks (title, status, priority, assigned_to_id) VALUES ('Kept two', 'completed', 'high', 1)",
	} {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func assertTasksKept(t *testing.T, tdb *migrationTestDB, linkColumn bool) {
	t.Helper()
	var rows int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&rows))
	assert.Equal(t, 2, rows, "every existing task is kept")
	if linkColumn {
		var unlinked int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM tasks WHERE deal_id IS NULL").Scan(&unlinked))
		assert.Equal(t, 2, unlinked, "every existing task stays unlinked")
	}
}

func TestTaskDealLinkMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the three objects as AutoMigrate adds them on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "task_deal_ref")
	require.NoError(t, reference.m.Migrate(taskDealLinkPreviousVersion))
	assert.Empty(t, snapshotTaskDealObjects(t, reference).Columns, "the previous head has no deal link")
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.Task{}))
	want := snapshotTaskDealObjects(t, reference)
	require.Len(t, want.Columns, 1)
	assert.Contains(t, want.Columns[0], "nullable=YES")
	assert.Contains(t, want.Columns[0], "unsigned")
	assert.Equal(t, []string{taskDealIndexLine}, want.Indexes)
	assert.Equal(t, []string{taskDealFKLine}, want.ForeignKeys)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "task_deal_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTaskDealObjects(t, tdb), "a fresh SQL schema carries what AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "task_deal_mig")
			require.NoError(t, tdb.m.Migrate(taskDealLinkPreviousVersion))
			seedTaskDealRows(t, tdb)
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Task{}))
				assert.Equal(t, want, snapshotTaskDealObjects(t, tdb))
			}

			require.NoError(t, tdb.m.Migrate(taskDealLinkVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(taskDealLinkVersion), version)

			assert.Equal(t, want, snapshotTaskDealObjects(t, tdb), "the SQL migration and AutoMigrate must produce the same objects")
			assertTasksKept(t, tdb, true)

			// The foreign key is live: a dangling id is refused.
			_, err = tdb.sql.Exec("UPDATE tasks SET deal_id = 999999 WHERE title = 'Kept one'")
			assert.Error(t, err, "a deal_id naming no deal is refused")

			// The application's own startup on top changes nothing and the
			// migration is not applied twice.
			got := snapshotTable(t, tdb, "tasks")
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, got, snapshotTable(t, tdb, "tasks"), "MigrateDatabase must not alter the migrated table")
			require.ErrorIs(t, tdb.m.Migrate(taskDealLinkVersion), migrate.ErrNoChange)

			// Rollback removes exactly the three objects and keeps the rows.
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(taskDealLinkPreviousVersion), version)
			assert.Equal(t, withoutTaskDealObjects(got), snapshotTable(t, tdb, "tasks"), "rollback removes the three objects and nothing else")
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			assertTasksKept(t, tdb, false)

			// Reapply.
			require.NoError(t, tdb.m.Migrate(taskDealLinkVersion))
			assert.Equal(t, want, snapshotTaskDealObjects(t, tdb))
			assertTasksKept(t, tdb, true)
		})
	}
}
