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

// #216: 20261106120000_add_form_mail_deliveries creates form_mail_deliveries
// with the columns and indexes AutoMigrate gives it, only when the table is
// missing, and rollback drops that table and nothing else.

const (
	formMailDeliveriesVersion         = 20261106120000
	formMailDeliveriesPreviousVersion = 20261101120000
	formMailDeliveriesName            = "20261106120000_add_form_mail_deliveries"
	formMailDeliveriesTable           = "form_mail_deliveries"
)

// TestFormMailDeliveriesMigrationFiles needs no database: the pair exists,
// creates and drops the table with its indexes, and the completeness and CI lists name it.
func TestFormMailDeliveriesMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, formMailDeliveriesName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, formMailDeliveriesName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "create table if not exists form_mail_deliveries (")
	for _, index := range []string{
		"key idx_form_mail_deliveries_deleted_at (deleted_at)",
		"key idx_form_mail_deliveries_submission_id (submission_id)",
		"key idx_form_mail_deliveries_form_id (form_id)",
		"key idx_form_mail_deliveries_status (status)",
	} {
		assert.Containsf(t, upSQL, index, "up declares %s", index)
	}
	for _, column := range []string{
		"submission_id bigint unsigned not null", "form_id bigint unsigned not null",
		"kind varchar(20) not null", "status varchar(20) not null", "error_code varchar(50) null",
		"recipient_hash varchar(64) null", "attempts bigint not null default 0",
		"last_attempt_at datetime(3) null", "sent_at datetime(3) null",
	} {
		assert.Containsf(t, upSQL, column, "up declares %s", column)
	}
	for _, forbidden := range []string{"email", "subject", "body", "address"} {
		assert.NotContainsf(t, strings.SplitN(upSQL, "create table", 2)[1], forbidden, "the table stores no %s", forbidden)
	}
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.NotContains(t, strings.SplitN(upSQL, "create table", 2)[1], "foreign key", "the table declares no foreign key")
	assert.Contains(t, downSQL, "drop table if exists form_mail_deliveries;")
	assert.NotContains(t, downSQL, "alter table")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops one table")

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+formMailDeliveriesTable+`"`, "main_test.go's expected-table list names the table")
}

func TestFormMailDeliveriesMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as AutoMigrate creates it on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "form_mail_ref")
	require.NoError(t, reference.m.Migrate(formMailDeliveriesPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), formMailDeliveriesTable)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.FormMailDelivery{}))
	want := snapshotTable(t, reference, formMailDeliveriesTable)
	require.NotEmpty(t, want.Columns)
	assert.Contains(t, want.Indexes, "idx_form_mail_deliveries_submission_id (submission_id) non_unique=1")
	assert.Contains(t, want.Indexes, "idx_form_mail_deliveries_status (status) non_unique=1")
	assert.Empty(t, want.ForeignKeys)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "form_mail_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, formMailDeliveriesTable), "a fresh SQL schema carries what AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), formMailDeliveriesTable)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "form_mail_mig")
			require.NoError(t, tdb.m.Migrate(formMailDeliveriesPreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.FormMailDelivery{}))
				_, err = tdb.sql.Exec("INSERT INTO form_mail_deliveries (submission_id, form_id, kind, status, attempts) VALUES (1, 1, 'notification', 'failed', 1)")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(formMailDeliveriesVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formMailDeliveriesVersion), version)
			got := snapshotTable(t, tdb, formMailDeliveriesTable)
			assert.Equal(t, want, got, "the SQL migration and AutoMigrate must produce the same table")
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM form_mail_deliveries").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			// The application's own startup on top leaves the table alone, and
			// the migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, got, snapshotTable(t, tdb, formMailDeliveriesTable), "MigrateDatabase must not alter the table")
			require.ErrorIs(t, tdb.m.Migrate(formMailDeliveriesVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formMailDeliveriesPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, []string{formMailDeliveriesTable}), listTables(t, tdb.sql, tdb.name),
				"rollback drops form_mail_deliveries and nothing else")

			require.NoError(t, tdb.m.Migrate(formMailDeliveriesVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, formMailDeliveriesTable))
		})
	}
}
