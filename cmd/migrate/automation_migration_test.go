package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// AC-17: 20261028120000_add_follow_up_automation is an up/down pair. On
// MySQL, up creates the six tables with the specified columns, unique keys
// and foreign keys — exactly what AutoMigrate produces, so a database created
// either way ends up the same — and down drops them and nothing else.

const (
	automationMigrationVersion    = 20261028120000
	automationPreviousHeadVersion = 20261026120000
	automationMigrationName       = "20261028120000_add_follow_up_automation"
)

var automationMigrationTables = []string{
	"automation_rules", "automation_steps", "automation_events", "automation_runs", "automation_run_steps", "notifications",
}

// TestMigrationAutomationTablesListed needs no database: the pair exists, the
// up file creates the six tables, the down file drops them, and the migration
// file test's expected-table list names all six.
func TestMigrationAutomationTablesListed(t *testing.T) {
	migrateDir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(migrateDir, automationMigrationName+".up.sql"))
	require.NoError(t, err, "the up migration exists")
	down, err := os.ReadFile(filepath.Join(migrateDir, automationMigrationName+".down.sql"))
	require.NoError(t, err, "the down migration exists")

	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))
	for _, table := range automationMigrationTables {
		assert.Containsf(t, upSQL, "create table if not exists "+table+" ", "up creates %s", table)
		assert.Containsf(t, downSQL, "drop table if exists "+table+";", "down drops %s", table)
	}
	assert.Contains(t, upSQL, "unique key idx_automation_runs_dedupe_key (dedupe_key)")
	assert.Contains(t, upSQL, "unique key idx_automation_run_steps_run_position (run_id, position)")
	for _, fk := range []string{
		"fk_automation_rules_created_by", "fk_automation_rules_steps", "fk_automation_runs_rule", "fk_automation_runs_steps", "fk_notifications_user",
	} {
		assert.Containsf(t, upSQL, "constraint "+fk+" foreign key", "up declares %s", fk)
	}
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.NotContains(t, downSQL, "alter table")

	// The migration file test (main_test.go) checks every expected table is
	// created across the up migrations; its list must name the six.
	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	for _, table := range automationMigrationTables {
		assert.Containsf(t, string(source), `"`+table+`"`, "main_test.go's expected-table list names %s", table)
	}
}

// snapshotAutomationTables reads the six tables out of INFORMATION_SCHEMA,
// as snapshotQuoteTables does for the quote tables.
func snapshotAutomationTables(t *testing.T, tdb *migrationTestDB) map[string]tableSchema {
	t.Helper()
	out := map[string]tableSchema{}
	for _, table := range automationMigrationTables {
		out[table] = snapshotTable(t, tdb, table)
	}
	return out
}

// sortSchema orders the lines of a snapshot so two snapshots compare.
func sortSchema(ts *tableSchema) {
	sort.Strings(ts.Columns)
	sort.Strings(ts.Indexes)
	sort.Strings(ts.ForeignKeys)
}

// snapshotTable renders one table's columns, indexes and foreign keys as
// sorted lines, the comparable shape the quotes migration test uses.
func snapshotTable(t *testing.T, tdb *migrationTestDB, table string) tableSchema {
	t.Helper()
	var ts tableSchema
	rows, err := tdb.sql.Query(`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<none>'), EXTRA
		FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, tdb.name, table)
	require.NoError(t, err)
	for rows.Next() {
		var name, typ, nullable, def, extra string
		require.NoError(t, rows.Scan(&name, &typ, &nullable, &def, &extra))
		ts.Columns = append(ts.Columns, fmt.Sprintf("%s %s nullable=%s default=%s %s", name, strings.ToLower(typ), nullable, def, strings.ToLower(extra)))
	}
	require.NoError(t, rows.Err())
	rows.Close()

	rows, err = tdb.sql.Query(`SELECT INDEX_NAME, NON_UNIQUE, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
		FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? GROUP BY INDEX_NAME, NON_UNIQUE`, tdb.name, table)
	require.NoError(t, err)
	for rows.Next() {
		var name, columns string
		var nonUnique int
		require.NoError(t, rows.Scan(&name, &nonUnique, &columns))
		ts.Indexes = append(ts.Indexes, fmt.Sprintf("%s (%s) non_unique=%d", name, columns, nonUnique))
	}
	require.NoError(t, rows.Err())
	rows.Close()

	rows, err = tdb.sql.Query(`SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL`, tdb.name, table)
	require.NoError(t, err)
	for rows.Next() {
		var name, column, refTable, refColumn string
		require.NoError(t, rows.Scan(&name, &column, &refTable, &refColumn))
		ts.ForeignKeys = append(ts.ForeignKeys, fmt.Sprintf("%s %s -> %s.%s", name, column, refTable, refColumn))
	}
	require.NoError(t, rows.Err())
	rows.Close()
	sortSchema(&ts)
	return ts
}

func automationModels() []interface{} {
	return []interface{}{
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{},
		&models.AutomationRun{}, &models.AutomationRunStep{}, &models.Notification{},
	}
}

func TestMigrationAutomationSchema(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The reference: the six tables as AutoMigrate creates them on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "automation_ref")
	require.NoError(t, reference.m.Migrate(automationPreviousHeadVersion))
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, refDB.AutoMigrate(automationModels()...))
	want := snapshotAutomationTables(t, reference)
	for _, table := range automationMigrationTables {
		require.NotEmpty(t, want[table].Columns, "AutoMigrate must have created %s", table)
	}
	assert.Contains(t, want["automation_runs"].Indexes, "idx_automation_runs_dedupe_key (dedupe_key) non_unique=0")
	assert.Contains(t, want["automation_run_steps"].Indexes, "idx_automation_run_steps_run_position (run_id,position) non_unique=0")
	assert.Contains(t, want["automation_steps"].ForeignKeys, "fk_automation_rules_steps rule_id -> automation_rules.id")
	assert.Contains(t, want["automation_runs"].ForeignKeys, "fk_automation_runs_rule rule_id -> automation_rules.id")
	assert.Contains(t, want["automation_run_steps"].ForeignKeys, "fk_automation_runs_steps run_id -> automation_runs.id")
	assert.Contains(t, want["notifications"].ForeignKeys, "fk_notifications_user user_id -> users.id")
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}

	for _, autoMigrateFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "automation_mig")
			require.NoError(t, tdb.m.Migrate(automationPreviousHeadVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(automationModels()...))
				_, err = tdb.sql.Exec("INSERT INTO automation_rules (name, entity_type, trigger_type, enabled, version) VALUES ('Kept', 'lead', 'lead_created', 1, 1)")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Up(), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(automationMigrationVersion), version)

			got := snapshotAutomationTables(t, tdb)
			for _, table := range automationMigrationTables {
				assert.Equalf(t, want[table], got[table], "%s differs between the SQL migration and AutoMigrate", table)
			}
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_rules WHERE name = 'Kept'").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, got, snapshotAutomationTables(t, tdb), "MigrateDatabase must not alter the migrated tables")
			require.ErrorIs(t, tdb.m.Up(), migrate.ErrNoChange)

			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(automationPreviousHeadVersion), version)
			tablesAfter := listTables(t, tdb.sql, tdb.name)
			for _, table := range automationMigrationTables {
				assert.NotContains(t, tablesAfter, table)
			}
			assert.Equal(t, without(tablesBefore, automationMigrationTables), tablesAfter, "Steps(-1) must leave every other table intact")

			require.NoError(t, tdb.m.Up())
			assert.Equal(t, got, snapshotAutomationTables(t, tdb))
		})
	}
}
