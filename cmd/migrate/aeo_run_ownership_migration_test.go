package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// #229: 20261114120000_add_aeo_run_ownership adds owner_instance, heartbeat_at,
// running_lock and schedule_slot to aeo_runs and the unique indexes
// idx_aeo_runs_running_lock and idx_aeo_runs_schedule_slot, each only when
// missing; the down half drops them only when present.

const (
	aeoRunOwnershipVersion         = 20261114120000
	aeoRunOwnershipPreviousVersion = ticketMailDeliveriesVersion
	aeoRunOwnershipName            = "20261114120000_add_aeo_run_ownership"
)

var (
	aeoRunOwnershipColumnNames = []string{"owner_instance", "heartbeat_at", "running_lock", "schedule_slot"}
	aeoRunOwnershipIndexNames  = []string{"idx_aeo_runs_running_lock", "idx_aeo_runs_schedule_slot"}
)

// TestAEORunOwnershipMigrationFiles needs no database: both halves exist, every
// statement is guarded by an INFORMATION_SCHEMA probe, the column types match
// what AutoMigrate produces for models.AEORun, and the migration is the newest
// of the directory.
func TestAEORunOwnershipMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, aeoRunOwnershipName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, aeoRunOwnershipName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, fragment := range []string{
		"alter table aeo_runs add column owner_instance varchar(64) null",
		"alter table aeo_runs add column heartbeat_at datetime(3) null",
		"alter table aeo_runs add column running_lock tinyint unsigned null",
		"alter table aeo_runs add column schedule_slot varchar(20) null",
		"create unique index idx_aeo_runs_running_lock on aeo_runs (running_lock)",
		"create unique index idx_aeo_runs_schedule_slot on aeo_runs (schedule_slot)",
	} {
		assert.Containsf(t, upSQL, fragment, "up declares %s", fragment)
	}
	for _, name := range aeoRunOwnershipColumnNames {
		assert.Contains(t, upSQL, "column_name = '"+name+"'), 'select 1', 'alter table", "%s is added only when missing", name)
		assert.Contains(t, downSQL, "column_name = '"+name+"'), 'alter table aeo_runs drop column "+name+"', 'select 1'", "%s is dropped only when present", name)
	}
	for _, name := range aeoRunOwnershipIndexNames {
		assert.Contains(t, upSQL, "index_name = '"+name+"'), 'select 1', 'create unique index "+name, "%s is created only when missing", name)
		assert.Contains(t, downSQL, "index_name = '"+name+"'), 'drop index "+name+" on aeo_runs', 'select 1'", "%s is dropped only when present", name)
	}
	assert.Equal(t, 6, strings.Count(upSQL, "execute stmt;"))
	assert.Equal(t, 6, strings.Count(downSQL, "execute stmt;"))
	assert.NotContains(t, upSQL, "update aeo_runs", "no backfill")
	assert.NotContains(t, upSQL, "drop ", "the up half drops nothing")
	assert.NotContains(t, downSQL, "add column", "the down half adds nothing")
	// The indexes go before their columns in the down half.
	assert.Less(t, strings.Index(downSQL, "drop index idx_aeo_runs_running_lock"), strings.Index(downSQL, "drop column running_lock"))
	assert.Less(t, strings.Index(downSQL, "drop index idx_aeo_runs_schedule_slot"), strings.Index(downSQL, "drop column schedule_slot"))

	// The model declares the same names and column types as the migration
	// (an explicit varchar, so SQLite does not fall back to text).
	field, ok := reflectField(models.AEORun{}, "OwnerInstance")
	require.True(t, ok)
	assert.Contains(t, field, "type:varchar(64)")
	field, ok = reflectField(models.AEORun{}, "RunningLock")
	require.True(t, ok)
	assert.Contains(t, field, "uniqueIndex:idx_aeo_runs_running_lock")
	field, ok = reflectField(models.AEORun{}, "ScheduleSlot")
	require.True(t, ok)
	assert.Contains(t, field, "type:varchar(20)")
	assert.Contains(t, field, "uniqueIndex:idx_aeo_runs_schedule_slot")

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestFormTicketIntakeMigrationFiles (#213).
	version, err := strconv.Atoi(strings.SplitN(aeoRunOwnershipName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, aeoRunOwnershipVersion, version)
	assert.Less(t, aeoRunOwnershipVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Greater(t, aeoRunOwnershipVersion, aeoRunOwnershipPreviousVersion)
}

// reflectField returns the gorm tag of a model field.
func reflectField(value interface{}, name string) (string, bool) {
	field, ok := reflect.TypeOf(value).FieldByName(name)
	if !ok {
		return "", false
	}
	return field.Tag.Get("gorm"), true
}

// ownershipSlice keeps the parts of an aeo_runs snapshot this migration owns.
func ownershipSlice(schema tableSchema) tableSchema {
	var out tableSchema
	for _, column := range schema.Columns {
		for _, name := range aeoRunOwnershipColumnNames {
			if strings.HasPrefix(column, name+" ") {
				out.Columns = append(out.Columns, column)
			}
		}
	}
	for _, index := range schema.Indexes {
		for _, name := range aeoRunOwnershipIndexNames {
			if strings.HasPrefix(index, name+" ") {
				out.Indexes = append(out.Indexes, index)
			}
		}
	}
	return out
}

// otherSlice keeps everything of an aeo_runs snapshot this migration does not
// own, so up and down can be shown to leave it alone.
func otherSlice(schema tableSchema) tableSchema {
	owned := ownershipSlice(schema)
	return tableSchema{
		Columns:     without(schema.Columns, owned.Columns),
		Indexes:     without(schema.Indexes, owned.Indexes),
		ForeignKeys: schema.ForeignKeys,
	}
}

func TestAEORunOwnershipMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: AutoMigrate of models.AEORun on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "aeoown_ref")
	require.NoError(t, reference.m.Migrate(aeoRunOwnershipPreviousVersion))
	assert.Empty(t, ownershipSlice(snapshotTable(t, reference, "aeo_runs")).Columns)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.AEORun{}))
	want := ownershipSlice(snapshotTable(t, reference, "aeo_runs"))
	require.Len(t, want.Columns, 4)
	// MariaDB reports a display width (tinyint(3) unsigned), MySQL 8 does not.
	for name, fragments := range map[string][]string{
		"owner_instance": {" varchar(64) ", "nullable=YES"},
		"heartbeat_at":   {" datetime(3) ", "nullable=YES"},
		"running_lock":   {" tinyint", " unsigned ", "nullable=YES"},
		"schedule_slot":  {" varchar(20) ", "nullable=YES"},
	} {
		found := ""
		for _, column := range want.Columns {
			if strings.HasPrefix(column, name+" ") {
				found = column
			}
		}
		require.NotEmpty(t, found, "AutoMigrate adds %s; got %v", name, want.Columns)
		for _, fragment := range fragments {
			assert.Contains(t, found, fragment, "%s", name)
		}
	}
	assert.Contains(t, want.Indexes, "idx_aeo_runs_running_lock (running_lock) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_aeo_runs_schedule_slot (schedule_slot) non_unique=0")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "aeoown_fresh")
		require.NoError(t, tdb.m.Migrate(aeoRunOwnershipPreviousVersion))
		before := snapshotTable(t, tdb, "aeo_runs")
		_, err := tdb.sql.Exec("INSERT INTO aeo_runs (`trigger`, status, started_at, total_queries, failed_queries) VALUES ('manual', 'running', NOW(), 2, 0)")
		require.NoError(t, err)

		require.NoError(t, tdb.m.Migrate(aeoRunOwnershipVersion))
		after := snapshotTable(t, tdb, "aeo_runs")
		assert.Equal(t, want, ownershipSlice(after), "the SQL migration produces what AutoMigrate produces")
		assert.Equal(t, otherSlice(before), otherSlice(after), "the rest of aeo_runs is untouched")
		var legacyHeartbeats int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM aeo_runs WHERE status = 'running' AND heartbeat_at IS NULL").Scan(&legacyHeartbeats))
		assert.Equal(t, 1, legacyHeartbeats, "a legacy running row survives the up with a NULL heartbeat")
		assertOwnershipIndexesEnforced(t, tdb)

		// Down, then up again.
		require.NoError(t, tdb.m.Steps(-1))
		version, dirty, err := tdb.m.Version()
		require.NoError(t, err)
		assert.False(t, dirty)
		assert.Equal(t, uint(aeoRunOwnershipPreviousVersion), version)
		assert.Equal(t, before, snapshotTable(t, tdb, "aeo_runs"), "the down half removes exactly what the up added")
		var rows int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM aeo_runs").Scan(&rows))
		assert.Positive(t, rows, "rollback keeps the runs")

		require.NoError(t, tdb.m.Migrate(aeoRunOwnershipVersion))
		assert.Equal(t, want, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")))
		require.ErrorIs(t, tdb.m.Migrate(aeoRunOwnershipVersion), migrate.ErrNoChange)

		// The down half is guarded: with a column and an index already gone it
		// still succeeds.
		_, err = tdb.sql.Exec("DROP INDEX idx_aeo_runs_schedule_slot ON aeo_runs")
		require.NoError(t, err)
		_, err = tdb.sql.Exec("ALTER TABLE aeo_runs DROP COLUMN schedule_slot")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "the drops run only when present")
		assert.Equal(t, before, snapshotTable(t, tdb, "aeo_runs"))
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "aeoown_mig")
			require.NoError(t, tdb.m.Migrate(aeoRunOwnershipPreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.AEORun{}))
				_, err = tdb.sql.Exec("INSERT INTO aeo_runs (`trigger`, status, started_at, total_queries, failed_queries, owner_instance, heartbeat_at, running_lock) VALUES ('manual', 'running', NOW(), 2, 0, 'owner-a', NOW(), 1)")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(aeoRunOwnershipVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(aeoRunOwnershipVersion), version)
			assert.Equal(t, want, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")), "the SQL migration and AutoMigrate produce the same columns and indexes")
			if autoMigrateFirst {
				var owner string
				require.NoError(t, tdb.sql.QueryRow("SELECT owner_instance FROM aeo_runs WHERE running_lock = 1").Scan(&owner))
				assert.Equal(t, "owner-a", owner, "a table AutoMigrate extended first keeps its rows")
			}

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")), "MigrateDatabase does not change them")
			assertOwnershipIndexesEnforced(t, tdb)

			// Down, then up again.
			require.NoError(t, tdb.m.Steps(-1))
			assert.Empty(t, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")).Columns)
			assert.Empty(t, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")).Indexes)
			require.NoError(t, tdb.m.Migrate(aeoRunOwnershipVersion))
			assert.Equal(t, want, ownershipSlice(snapshotTable(t, tdb, "aeo_runs")))
		})
	}
}

// assertOwnershipIndexesEnforced proves both indexes are unique and admit
// several NULLs.
func assertOwnershipIndexesEnforced(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	_, err := tdb.sql.Exec("UPDATE aeo_runs SET running_lock = NULL, status = 'failed'")
	require.NoError(t, err)
	insert := "INSERT INTO aeo_runs (`trigger`, status, started_at, total_queries, failed_queries, running_lock, schedule_slot) VALUES ('scheduled', ?, NOW(), 1, 0, ?, ?)"
	_, err = tdb.sql.Exec(insert, "completed", nil, nil)
	require.NoError(t, err)
	_, err = tdb.sql.Exec(insert, "completed", nil, nil)
	require.NoError(t, err, "NULL running_lock and NULL schedule_slot coexist")
	_, err = tdb.sql.Exec(insert, "running", 1, "2031-01-01T06:00Z")
	require.NoError(t, err)
	_, err = tdb.sql.Exec(insert, "running", 1, "2031-01-02T06:00Z")
	require.Error(t, err, "a second running_lock = 1 is refused")
	_, err = tdb.sql.Exec(insert, "completed", nil, "2031-01-01T06:00Z")
	require.Error(t, err, "a second row of one slot is refused")
	_, err = tdb.sql.Exec("UPDATE aeo_runs SET running_lock = NULL, status = 'failed' WHERE running_lock = 1")
	require.NoError(t, err)
}
