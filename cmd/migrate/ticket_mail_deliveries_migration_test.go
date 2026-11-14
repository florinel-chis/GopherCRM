package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

// #219: 20261113120000_add_ticket_mail_deliveries creates ticket_mail_deliveries
// only when it is missing, with the columns and indexes AutoMigrate produces
// (no deleted_at, no foreign key); rollback drops that table and nothing else.

const (
	ticketMailDeliveriesVersion         = 20261113120000
	ticketMailDeliveriesPreviousVersion = productExternalIDsVersion
	ticketMailDeliveriesName            = "20261113120000_add_ticket_mail_deliveries"
	ticketMailDeliveriesTable           = "ticket_mail_deliveries"
)

// TestTicketMailDeliveriesMigrationFiles needs no database: the pair exists,
// declares the table as the model does, is the newest migration of the
// directory, and latestMigrationVersion names it.
func TestTicketMailDeliveriesMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, ticketMailDeliveriesName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, ticketMailDeliveriesName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "create table if not exists ticket_mail_deliveries (", "the table is created only when missing")
	for _, column := range []string{
		"id bigint unsigned not null auto_increment",
		"created_at datetime(3) null",
		"updated_at datetime(3) null",
		"ticket_id bigint unsigned not null",
		"comment_id bigint unsigned not null",
		"customer_id bigint unsigned not null",
		"status varchar(20) not null",
		"error_code varchar(50) not null default ''",
		"sent_at datetime(3) null",
		"primary key (id)",
		"unique key idx_ticket_mail_deliveries_comment_id (comment_id)",
		"key idx_ticket_mail_deliveries_ticket_id (ticket_id)",
		"key idx_ticket_mail_deliveries_customer_id (customer_id)",
	} {
		assert.Containsf(t, upSQL, column, "up declares %s", column)
	}
	body := strings.SplitN(upSQL, "create table", 2)[1]
	assert.NotContains(t, body, "deleted_at", "delivery records are hard-deleted")
	assert.NotContains(t, body, "foreign key", "no foreign key")
	assert.Equal(t, 1, strings.Count(body, "unique key"), "the comment index is the only unique one")
	for _, forbidden := range []string{"email", "address", "subject", "title", "body"} {
		assert.NotContains(t, body, forbidden, "the table holds no %s", forbidden)
	}
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.Contains(t, downSQL, "drop table if exists ticket_mail_deliveries;")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops one table")

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestAEORunOwnershipMigrationFiles (#229).
	version, err := strconv.Atoi(strings.SplitN(ticketMailDeliveriesName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, ticketMailDeliveriesVersion, version)
	assert.Greater(t, ticketMailDeliveriesVersion, ticketMailDeliveriesPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+ticketMailDeliveriesTable+`"`, "main_test.go's expected-table list names the table")
}

// insertMigrationDeliveries writes two delivery rows and refuses a second row
// of the same comment.
func insertMigrationDeliveries(t *testing.T, db *sql.DB, commentBase int64) {
	t.Helper()
	insert := "INSERT INTO ticket_mail_deliveries (ticket_id, comment_id, customer_id, status) VALUES (?, ?, ?, 'pending')"
	_, err := db.Exec(insert, 1, commentBase, 3)
	require.NoError(t, err)
	_, err = db.Exec(insert, 1, commentBase+1, 3)
	require.NoError(t, err)
	_, err = db.Exec(insert, 2, commentBase, 4)
	require.Error(t, err, "a second delivery of the same comment is refused")
	var errorCode string
	require.NoError(t, db.QueryRow("SELECT error_code FROM ticket_mail_deliveries WHERE comment_id = ?", commentBase).Scan(&errorCode))
	assert.Equal(t, "", errorCode, "error_code defaults to ''")
}

func TestTicketMailDeliveriesMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as MigrateDatabase creates it on top of
	// the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "tmd_ref")
	require.NoError(t, reference.m.Migrate(ticketMailDeliveriesPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), ticketMailDeliveriesTable)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	models.DB = refDB
	require.NoError(t, models.MigrateDatabase())
	want := snapshotTable(t, reference, ticketMailDeliveriesTable)
	require.NotEmpty(t, want.Columns)
	assert.Contains(t, want.Indexes, "idx_ticket_mail_deliveries_comment_id (comment_id) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_ticket_mail_deliveries_ticket_id (ticket_id) non_unique=1")
	assert.Contains(t, want.Indexes, "idx_ticket_mail_deliveries_customer_id (customer_id) non_unique=1")
	assert.Empty(t, want.ForeignKeys, "no foreign key")
	for _, column := range want.Columns {
		assert.False(t, strings.HasPrefix(column, "deleted_at "), "no deleted_at column")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "tmd_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, ticketMailDeliveriesTable), "a fresh SQL schema carries what MigrateDatabase creates")
		insertMigrationDeliveries(t, tdb.sql, 100)

		// Back to this migration first: newer migrations (#229) sort after it.
		require.NoError(t, tdb.m.Migrate(ticketMailDeliveriesVersion))
		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, []string{ticketMailDeliveriesTable}), listTables(t, tdb.sql, tdb.name),
			"rollback drops ticket_mail_deliveries and nothing else")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, ticketMailDeliveriesTable))

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), ticketMailDeliveriesTable)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "tmd_mig")
			require.NoError(t, tdb.m.Migrate(ticketMailDeliveriesPreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.TicketMailDelivery{}))
				_, err = tdb.sql.Exec("INSERT INTO ticket_mail_deliveries (ticket_id, comment_id, customer_id, status, created_at, updated_at) VALUES (9, 9000, 9, 'sent', NOW(), NOW())")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(ticketMailDeliveriesVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(ticketMailDeliveriesVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, ticketMailDeliveriesTable), "the SQL migration and AutoMigrate must produce the same table")
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM ticket_mail_deliveries WHERE comment_id = 9000").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotTable(t, tdb, ticketMailDeliveriesTable), "MigrateDatabase must not change the table's shape")
			insertMigrationDeliveries(t, tdb.sql, 200)
			require.ErrorIs(t, tdb.m.Migrate(ticketMailDeliveriesVersion), migrate.ErrNoChange)

			// Single-step rollback, then reapply.
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(ticketMailDeliveriesPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, []string{ticketMailDeliveriesTable}), listTables(t, tdb.sql, tdb.name),
				"rollback drops ticket_mail_deliveries and nothing else")

			require.NoError(t, tdb.m.Migrate(ticketMailDeliveriesVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, ticketMailDeliveriesTable))
			insertMigrationDeliveries(t, tdb.sql, 300)
		})
	}
}
