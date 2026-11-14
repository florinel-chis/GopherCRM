package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #237: 20261116120000_add_automation_run_leases adds owner_instance and
// heartbeat_at to automation_runs, each only when missing; the down half drops
// them only when present. No index and no backfill.

const (
	automationRunLeaseVersion         = 20261116120000
	automationRunLeasePreviousVersion = formTicketIntakeVersion
	automationRunLeaseName            = "20261116120000_add_automation_run_leases"
)

var automationRunLeaseColumns = []string{"owner_instance", "heartbeat_at"}

// TestAutomationRunLeaseMigrationFiles needs no database: the pair exists, has
// its headers, every statement is guarded by an INFORMATION_SCHEMA probe, the
// column types match the model, the migration is the newest of the directory
// and latestMigrationVersion names it.
func TestAutomationRunLeaseMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, automationRunLeaseName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, automationRunLeaseName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Add automation run leases"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove automation run leases"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "'alter table automation_runs add column owner_instance varchar(64) null'")
	assert.Contains(t, upSQL, "'alter table automation_runs add column heartbeat_at datetime(3) null'")
	for _, name := range automationRunLeaseColumns {
		assert.Contains(t, upSQL, "table_name = 'automation_runs' and column_name = '"+name+"'), 'select 1', 'alter table", "%s is added only when missing", name)
		assert.Contains(t, downSQL, "table_name = 'automation_runs' and column_name = '"+name+"'), 'alter table automation_runs drop column "+name+"', 'select 1'", "%s is dropped only when present", name)
	}
	assert.Equal(t, 2, strings.Count(upSQL, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(downSQL, "execute stmt;"))
	assert.NotContains(t, upSQL, "update automation_runs", "no backfill")
	assert.NotContains(t, upSQL, "index", "no index")
	assert.NotContains(t, upSQL, "drop ", "the up half drops nothing")
	assert.NotContains(t, downSQL, "add column", "the down half adds nothing")

	// The model declares the same names and column types, hidden from JSON.
	field, ok := reflectField(models.AutomationRun{}, "OwnerInstance")
	require.True(t, ok)
	assert.Contains(t, field, "type:varchar(64)")
	// HeartbeatAt carries no type tag: GORM's MySQL default for a timestamp is
	// datetime(3), which TestAutomationRunLeaseMigration compares with the SQL.
	heartbeat, ok := reflect.TypeOf(models.AutomationRun{}).FieldByName("HeartbeatAt")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeOf(&time.Time{}), heartbeat.Type)
	assert.Equal(t, "-", heartbeat.Tag.Get("json"))

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestFormTicketIntakeMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, automationRunLeaseName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, automationRunLeaseVersion, version)
	assert.Equal(t, automationRunLeaseVersion, latestMigrationVersion)
	assert.Greater(t, automationRunLeaseVersion, automationRunLeasePreviousVersion)
}

// seedAutomationRunRow writes one rule and one running run the up, the down
// and the rollback must keep.
func seedAutomationRunRow(t *testing.T, tdb *migrationTestDB, key string) {
	t.Helper()
	result, err := tdb.sql.Exec("INSERT INTO automation_rules (name, entity_type, trigger_type, enabled, version) VALUES ('Lease rule', 'lead', 'lead_created', 1, 1)")
	require.NoError(t, err)
	ruleID, err := result.LastInsertId()
	require.NoError(t, err)
	_, err = tdb.sql.Exec("INSERT INTO automation_runs (rule_id, rule_version, entity_type, entity_id, dedupe_key, status, next_position, attempts, claimed_at) VALUES (?, 1, 'lead', 7, ?, 'running', 2, 1, NOW(3))", ruleID, key)
	require.NoError(t, err)
}

func TestAutomationRunLeaseMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: AutoMigrate of models.AutomationRun on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "arl_ref")
	require.NoError(t, reference.m.Migrate(automationRunLeasePreviousVersion))
	assert.Empty(t, ownedColumns(snapshotTable(t, reference, "automation_runs"), automationRunLeaseColumns))
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.AutomationRun{}))
	want := ownedColumns(snapshotTable(t, reference, "automation_runs"), automationRunLeaseColumns)
	require.Len(t, want, 2)
	for name, fragment := range map[string]string{"owner_instance": " varchar(64) ", "heartbeat_at": " datetime(3) "} {
		found := ""
		for _, column := range want {
			if strings.HasPrefix(column, name+" ") {
				found = column
			}
		}
		require.NotEmpty(t, found, "AutoMigrate adds %s; got %v", name, want)
		assert.Contains(t, found, fragment, "%s", name)
		assert.Contains(t, found, "nullable=YES", "%s", name)
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "arl_fresh")
		require.NoError(t, tdb.m.Migrate(automationRunLeasePreviousVersion))
		seedAutomationRunRow(t, tdb, "arl-fresh")
		before := snapshotTable(t, tdb, "automation_runs")

		require.NoError(t, tdb.m.Migrate(automationRunLeaseVersion))
		after := snapshotTable(t, tdb, "automation_runs")
		assert.Equal(t, want, ownedColumns(after, automationRunLeaseColumns), "the SQL migration produces what AutoMigrate produces")
		assert.Equal(t, withoutColumns(before, automationRunLeaseColumns), withoutColumns(after, automationRunLeaseColumns), "the rest of automation_runs is untouched")
		var legacy, position, attempts int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_runs WHERE status = 'running' AND owner_instance IS NULL AND heartbeat_at IS NULL AND claimed_at IS NOT NULL").Scan(&legacy))
		assert.Equal(t, 1, legacy, "an existing running row keeps its data and reads NULL")
		require.NoError(t, tdb.sql.QueryRow("SELECT next_position, attempts FROM automation_runs WHERE dedupe_key = 'arl-fresh'").Scan(&position, &attempts))
		assert.Equal(t, 2, position)
		assert.Equal(t, 1, attempts)

		// Down, then up again.
		require.NoError(t, tdb.m.Steps(-1))
		version, dirty, err := tdb.m.Version()
		require.NoError(t, err)
		assert.False(t, dirty)
		assert.Equal(t, uint(automationRunLeasePreviousVersion), version)
		assert.Equal(t, before, snapshotTable(t, tdb, "automation_runs"), "the down half removes exactly what the up added")
		var rows int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_runs").Scan(&rows))
		assert.Equal(t, 1, rows, "rollback keeps the runs")

		require.NoError(t, tdb.m.Migrate(automationRunLeaseVersion))
		assert.Equal(t, want, ownedColumns(snapshotTable(t, tdb, "automation_runs"), automationRunLeaseColumns))
		require.ErrorIs(t, tdb.m.Migrate(automationRunLeaseVersion), migrate.ErrNoChange)

		// The down half is guarded: with a column already gone it still
		// succeeds.
		_, err = tdb.sql.Exec("ALTER TABLE automation_runs DROP COLUMN heartbeat_at")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "the drops run only when present")
		assert.Equal(t, before, snapshotTable(t, tdb, "automation_runs"))
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "arl_mig")
			require.NoError(t, tdb.m.Migrate(automationRunLeasePreviousVersion))
			seedAutomationRunRow(t, tdb, "arl-upgrade")
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.AutomationRun{}))
				_, err = tdb.sql.Exec("UPDATE automation_runs SET owner_instance = 'owner-a', heartbeat_at = NOW(3) WHERE dedupe_key = 'arl-upgrade'")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(automationRunLeaseVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(automationRunLeaseVersion), version)
			assert.Equal(t, want, ownedColumns(snapshotTable(t, tdb, "automation_runs"), automationRunLeaseColumns), "the SQL migration and AutoMigrate produce the same columns")
			if autoMigrateFirst {
				var owner string
				require.NoError(t, tdb.sql.QueryRow("SELECT owner_instance FROM automation_runs WHERE dedupe_key = 'arl-upgrade'").Scan(&owner))
				assert.Equal(t, "owner-a", owner, "a table AutoMigrate extended first keeps its rows")
			}

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, ownedColumns(snapshotTable(t, tdb, "automation_runs"), automationRunLeaseColumns), "MigrateDatabase does not change them")
			require.ErrorIs(t, tdb.m.Migrate(automationRunLeaseVersion), migrate.ErrNoChange)

			// Down, then up again.
			require.NoError(t, tdb.m.Steps(-1))
			assert.Empty(t, ownedColumns(snapshotTable(t, tdb, "automation_runs"), automationRunLeaseColumns))
			var rows int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_runs").Scan(&rows))
			assert.Equal(t, 1, rows, "rollback keeps the runs")
			require.NoError(t, tdb.m.Migrate(automationRunLeaseVersion))
			assert.Equal(t, want, ownedColumns(snapshotTable(t, tdb, "automation_runs"), automationRunLeaseColumns))
		})
	}
}
