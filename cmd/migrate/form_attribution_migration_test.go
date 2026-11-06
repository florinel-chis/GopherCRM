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

// #220: 20261107120000_add_form_attribution adds the six attribution columns
// of form_submissions and the nullable automation_events.form_id and
// submission_id under the names and types AutoMigrate gives them, each only
// when missing, and rollback removes exactly those eight columns.

const (
	formAttributionVersion         = 20261107120000
	formAttributionPreviousVersion = 20261106120000
	formAttributionName            = "20261107120000_add_form_attribution"
)

var (
	formAttributionSubmissionColumns = []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "referrer_host"}
	formAttributionEventColumns      = []string{"form_id", "submission_id"}
)

// TestFormAttributionMigrationFiles needs no database: the pair exists, names
// every column in both directions, and adds no index, key or backfill.
func TestFormAttributionMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, formAttributionName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, formAttributionName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, column := range formAttributionSubmissionColumns {
		width := "100"
		if column == "referrer_host" {
			width = "255"
		}
		assert.Contains(t, upSQL, fmt.Sprintf("'alter table form_submissions add column %s varchar(%s) not null default \\'\\''", column, width))
		assert.Contains(t, upSQL, fmt.Sprintf("table_name = 'form_submissions' and column_name = '%s'", column), "the add is guarded")
		assert.Contains(t, downSQL, fmt.Sprintf("'alter table form_submissions drop column %s'", column))
	}
	for _, column := range formAttributionEventColumns {
		assert.Contains(t, upSQL, fmt.Sprintf("'alter table automation_events add column %s bigint unsigned null'", column))
		assert.Contains(t, upSQL, fmt.Sprintf("table_name = 'automation_events' and column_name = '%s'", column), "the add is guarded")
		assert.Contains(t, downSQL, fmt.Sprintf("'alter table automation_events drop column %s'", column))
	}

	assert.NotContains(t, upSQL, "create index", "no new index")
	assert.NotContains(t, upSQL, "foreign key", "no new foreign key")
	assert.NotContains(t, upSQL, "create table", "no new table")
	assert.NotContains(t, upSQL, "\nupdate ", "existing rows are not backfilled")
	assert.NotContains(t, downSQL, "drop table", "rollback keeps the rows")
}

// formAttributionObjects is the part of the two tables the migration owns.
type formAttributionObjects struct {
	Submissions []string
	Events      []string
}

func ownedColumns(schema tableSchema, owned []string) []string {
	out := []string{}
	for _, line := range schema.Columns {
		for _, column := range owned {
			if strings.HasPrefix(line, column+" ") {
				out = append(out, line)
			}
		}
	}
	return out
}

func withoutColumns(schema tableSchema, owned []string) tableSchema {
	out := tableSchema{Indexes: schema.Indexes, ForeignKeys: schema.ForeignKeys}
	for _, line := range schema.Columns {
		keep := true
		for _, column := range owned {
			if strings.HasPrefix(line, column+" ") {
				keep = false
			}
		}
		if keep {
			out.Columns = append(out.Columns, line)
		}
	}
	return out
}

func snapshotFormAttributionObjects(t *testing.T, tdb *migrationTestDB) formAttributionObjects {
	t.Helper()
	return formAttributionObjects{
		Submissions: ownedColumns(snapshotTable(t, tdb, "form_submissions"), formAttributionSubmissionColumns),
		Events:      ownedColumns(snapshotTable(t, tdb, "automation_events"), formAttributionEventColumns),
	}
}

// seedFormAttributionRows writes one existing submission and one existing
// event the upgrade must keep.
func seedFormAttributionRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT INTO forms (name, public_id, status, submit_action, create_lead) VALUES ('Kept form', 'kept-form-public-id', 'published', 'message', 0)",
		"INSERT INTO form_submissions (form_id, data, email, status) VALUES (1, '{}', 'kept@example.com', 'received')",
		"INSERT INTO automation_events (entity_type, entity_id, event_type, origin, created_at) VALUES ('lead', 1, 'lead_created', 'form', NOW())",
	} {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func assertFormAttributionRowsKept(t *testing.T, tdb *migrationTestDB, columns bool) {
	t.Helper()
	var submissions, events int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM form_submissions").Scan(&submissions))
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_events").Scan(&events))
	assert.Equal(t, 1, submissions, "the existing submission is kept")
	assert.Equal(t, 1, events, "the existing event is kept")
	if columns {
		var empty, unlinked int
		require.NoError(t, tdb.sql.QueryRow(`SELECT COUNT(*) FROM form_submissions WHERE utm_source = '' AND utm_medium = ''
			AND utm_campaign = '' AND utm_term = '' AND utm_content = '' AND referrer_host = ''`).Scan(&empty))
		assert.Equal(t, 1, empty, "the existing submission reads as unattributed")
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM automation_events WHERE form_id IS NULL AND submission_id IS NULL").Scan(&unlinked))
		assert.Equal(t, 1, unlinked, "the existing event carries no form ids")
	}
}

func TestFormAttributionMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the columns as AutoMigrate adds them on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "form_attr_ref")
	require.NoError(t, reference.m.Migrate(formAttributionPreviousVersion))
	before := snapshotFormAttributionObjects(t, reference)
	assert.Empty(t, before.Submissions, "the previous head has no attribution columns")
	assert.Empty(t, before.Events, "the previous head has no event form ids")
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.FormSubmission{}, &models.AutomationEvent{}))
	want := snapshotFormAttributionObjects(t, reference)
	require.Len(t, want.Submissions, len(formAttributionSubmissionColumns))
	require.Len(t, want.Events, len(formAttributionEventColumns))
	for _, line := range want.Submissions {
		assert.Contains(t, line, "nullable=NO")
		if strings.HasPrefix(line, "referrer_host ") {
			assert.Contains(t, line, "varchar(255)")
		} else {
			assert.Contains(t, line, "varchar(100)")
		}
	}
	for _, line := range want.Events {
		assert.Contains(t, line, "bigint unsigned")
		assert.Contains(t, line, "nullable=YES")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "form_attr_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotFormAttributionObjects(t, tdb), "a fresh SQL schema carries what AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "form_attr_mig")
			require.NoError(t, tdb.m.Migrate(formAttributionPreviousVersion))
			seedFormAttributionRows(t, tdb)
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.FormSubmission{}, &models.AutomationEvent{}))
				assert.Equal(t, want, snapshotFormAttributionObjects(t, tdb))
			}

			require.NoError(t, tdb.m.Migrate(formAttributionVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formAttributionVersion), version)
			assert.Equal(t, want, snapshotFormAttributionObjects(t, tdb), "the SQL migration and AutoMigrate must produce the same columns")
			assertFormAttributionRowsKept(t, tdb, true)

			// The application's startup on top leaves the columns alone and the
			// migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotFormAttributionObjects(t, tdb), "MigrateDatabase must not alter the columns")
			require.ErrorIs(t, tdb.m.Migrate(formAttributionVersion), migrate.ErrNoChange)

			// Rollback removes exactly the eight columns and keeps the rows.
			submissionsBefore := snapshotTable(t, tdb, "form_submissions")
			eventsBefore := snapshotTable(t, tdb, "automation_events")
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formAttributionPreviousVersion), version)
			assert.Equal(t, withoutColumns(submissionsBefore, formAttributionSubmissionColumns), snapshotTable(t, tdb, "form_submissions"),
				"rollback removes the six submission columns and nothing else")
			assert.Equal(t, withoutColumns(eventsBefore, formAttributionEventColumns), snapshotTable(t, tdb, "automation_events"),
				"rollback removes the two event columns and nothing else")
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			assertFormAttributionRowsKept(t, tdb, false)

			// Reapply.
			require.NoError(t, tdb.m.Migrate(formAttributionVersion))
			assert.Equal(t, want, snapshotFormAttributionObjects(t, tdb))
			assertFormAttributionRowsKept(t, tdb, true)
		})
	}
}
