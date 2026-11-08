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

// #278: 20261108120000_add_ticket_sla_fields adds tickets.due_at and
// tickets.first_responded_at as datetime(3) NULL, the type AutoMigrate gives a
// *time.Time on MySQL, each only when missing, and rollback drops exactly those
// two columns. Existing tickets are kept and read NULL: there is no backfill.

const (
	ticketSLAFieldsVersion         = 20261108120000
	ticketSLAFieldsPreviousVersion = 20261107120000
	ticketSLAFieldsName            = "20261108120000_add_ticket_sla_fields"
)

var ticketSLAFieldsColumns = []string{"due_at", "first_responded_at"}

// TestTicketSLAFieldsMigrationFiles needs no database: the pair exists, names
// both columns in both directions and adds no index, key or backfill.
func TestTicketSLAFieldsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, ticketSLAFieldsName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, ticketSLAFieldsName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, column := range ticketSLAFieldsColumns {
		assert.Contains(t, upSQL, fmt.Sprintf("'alter table tickets add column %s datetime(3) null'", column))
		assert.Contains(t, upSQL, fmt.Sprintf("table_name = 'tickets' and column_name = '%s'", column), "the add is guarded")
		assert.Contains(t, downSQL, fmt.Sprintf("'alter table tickets drop column %s'", column))
		assert.Contains(t, downSQL, fmt.Sprintf("table_name = 'tickets' and column_name = '%s'", column), "the drop is guarded")
	}
	assert.NotContains(t, upSQL, "column overdue", "overdue is computed, never stored")
	assert.NotContains(t, upSQL, "create index", "no new index")
	assert.NotContains(t, upSQL, "foreign key", "no new foreign key")
	assert.NotContains(t, upSQL, "create table", "no new table")
	assert.NotContains(t, upSQL, "\nupdate ", "existing rows are not backfilled")
	assert.NotContains(t, downSQL, "drop table", "rollback keeps the rows")
	// The newest-migration assertion moved to TestUserTokenVersionMigrationFiles
	// when 20261109120000_add_user_token_version (#236) followed this pair.
}

func snapshotTicketSLAColumns(t *testing.T, tdb *migrationTestDB) []string {
	t.Helper()
	return ownedColumns(snapshotTable(t, tdb, "tickets"), ticketSLAFieldsColumns)
}

// seedTicketSLARows writes one existing customer and ticket the upgrade must
// keep.
func seedTicketSLARows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT INTO customers (first_name, last_name, email, created_at, updated_at) VALUES ('Kept', 'Customer', 'kept-sla@example.com', NOW(), NOW())",
		"INSERT INTO tickets (title, description, status, priority, customer_id, resolution, created_at, updated_at) " +
			"VALUES ('Kept ticket', 'Kept description', 'in_progress', 'high', 1, 'Kept resolution', '2026-11-01 10:00:00.123', '2026-11-02 11:00:00.456')",
	} {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func assertTicketSLARowsKept(t *testing.T, tdb *migrationTestDB, columns bool) {
	t.Helper()
	var count int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM tickets").Scan(&count))
	assert.Equal(t, 1, count, "the existing ticket is kept")
	var title, description, status, priority, resolution, createdAt, updatedAt string
	require.NoError(t, tdb.sql.QueryRow(`SELECT title, description, status, priority, resolution,
		DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f') FROM tickets`).
		Scan(&title, &description, &status, &priority, &resolution, &createdAt, &updatedAt))
	assert.Equal(t, "Kept ticket", title)
	assert.Equal(t, "Kept description", description)
	assert.Equal(t, "in_progress", status)
	assert.Equal(t, "high", priority)
	assert.Equal(t, "Kept resolution", resolution)
	assert.Equal(t, "2026-11-01 10:00:00.123000", createdAt, "created_at is untouched")
	assert.Equal(t, "2026-11-02 11:00:00.456000", updatedAt, "updated_at is untouched")
	if columns {
		var empty int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM tickets WHERE due_at IS NULL AND first_responded_at IS NULL").Scan(&empty))
		assert.Equal(t, 1, empty, "the existing ticket reads NULL for both new columns (no backfill)")
	}
}

func TestTicketSLAFieldsMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the columns as AutoMigrate adds them on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "ticket_sla_ref")
	require.NoError(t, reference.m.Migrate(ticketSLAFieldsPreviousVersion))
	assert.Empty(t, snapshotTicketSLAColumns(t, reference), "the previous head has no SLA columns")
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.Ticket{}))
	want := snapshotTicketSLAColumns(t, reference)
	require.Len(t, want, len(ticketSLAFieldsColumns))
	for _, line := range want {
		assert.Contains(t, line, "datetime(3)")
		assert.Contains(t, line, "nullable=YES")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "ticket_sla_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTicketSLAColumns(t, tdb), "a fresh SQL schema carries what AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "ticket_sla_mig")
			require.NoError(t, tdb.m.Migrate(ticketSLAFieldsPreviousVersion))
			seedTicketSLARows(t, tdb)
			ticketsBeforeUp := snapshotTable(t, tdb, "tickets")
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Ticket{}))
				assert.Equal(t, want, snapshotTicketSLAColumns(t, tdb))
			}

			require.NoError(t, tdb.m.Migrate(ticketSLAFieldsVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(ticketSLAFieldsVersion), version)
			assert.Equal(t, want, snapshotTicketSLAColumns(t, tdb), "the SQL migration and AutoMigrate must produce the same columns")
			if !autoMigrateFirst {
				assert.Equal(t, ticketsBeforeUp, withoutColumns(snapshotTable(t, tdb, "tickets"), ticketSLAFieldsColumns),
					"the migration adds the two columns and touches nothing else")
			}
			assertTicketSLARowsKept(t, tdb, true)

			// The application's startup on top leaves the columns alone and the
			// migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotTicketSLAColumns(t, tdb), "MigrateDatabase must not alter the columns")
			require.ErrorIs(t, tdb.m.Migrate(ticketSLAFieldsVersion), migrate.ErrNoChange)

			// Rollback removes exactly the two columns and keeps the rows.
			ticketsBefore := snapshotTable(t, tdb, "tickets")
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(ticketSLAFieldsPreviousVersion), version)
			assert.Equal(t, withoutColumns(ticketsBefore, ticketSLAFieldsColumns), snapshotTable(t, tdb, "tickets"),
				"rollback removes the two columns and nothing else")
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			assert.Empty(t, snapshotTicketSLAColumns(t, tdb))
			assertTicketSLARowsKept(t, tdb, false)

			// Reapply.
			require.NoError(t, tdb.m.Migrate(ticketSLAFieldsVersion))
			assert.Equal(t, want, snapshotTicketSLAColumns(t, tdb))
			assertTicketSLARowsKept(t, tdb, true)
		})
	}
}
