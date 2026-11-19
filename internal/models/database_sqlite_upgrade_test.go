package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file upgrade a populated SQLite file written by the
// previous release through the production bootstrap (InitDatabase →
// MigrateDatabase) and check that the data, the constraints and foreign-key
// enforcement all survive. The v1.2.0 schema and its rows live in
// testdata/sqlite-v1.2.0-schema.sql.

// upgradeFixtureCounts is the row count of every populated fixture table; the
// upgrade must leave each of them exactly as it found it.
var upgradeFixtureCounts = map[string]int64{
	"users":          1,
	"customers":      2,
	"leads":          2,
	"tickets":        1,
	"tasks":          2,
	"labels":         1,
	"task_labels":    1,
	"api_keys":       1,
	"refresh_tokens": 1,
}

// schemaEntry is one row of sqlite_master, enough to compare two schemas.
type schemaEntry struct {
	Type    string
	Name    string
	TblName string
	SQL     string
}

// openV120Fixture opens a fresh temp file through the production path and
// loads the v1.2.0 schema and data into it. The global DB points at the file
// until the test ends.
func openV120Fixture(t *testing.T) {
	t.Helper()

	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})

	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "crm-v1.2.0.db"),
	}))
	execSQLFile(t, filepath.Join("testdata", "sqlite-v1.2.0-schema.sql"))
}

// execSQLFile runs a `;`-terminated, one-statement-per-line-or-block SQL file
// statement by statement, skipping `--` comments.
func execSQLFile(t *testing.T, path string) {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var stmt strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		stmt.WriteString(line)
		stmt.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			require.NoError(t, DB.Exec(stmt.String()).Error, "fixture statement: %s", stmt.String())
			stmt.Reset()
		}
	}
	require.Empty(t, strings.TrimSpace(stmt.String()), "fixture ends with an unterminated statement")
}

func schemaSnapshot(t *testing.T) []schemaEntry {
	t.Helper()
	var entries []schemaEntry
	require.NoError(t, DB.Raw(
		"SELECT type, name, tbl_name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name",
	).Scan(&entries).Error)
	return entries
}

func tableSQL(t *testing.T, table string) string {
	t.Helper()
	var sql string
	require.NoError(t, DB.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table,
	).Scan(&sql).Error)
	return sql
}

func indexSQL(t *testing.T, table, index string) string {
	t.Helper()
	var sql string
	require.NoError(t, DB.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name = ?", table, index,
	).Scan(&sql).Error)
	return sql
}

// assertUpgradedV120 holds everything a successful upgrade of the fixture must
// leave behind, whichever state the file started in.
func assertUpgradedV120(t *testing.T) {
	t.Helper()

	// The file is consistent and enforcement is on for the application.
	var violations []map[string]interface{}
	require.NoError(t, DB.Raw("PRAGMA foreign_key_check").Scan(&violations).Error)
	assert.Empty(t, violations, "foreign_key_check must find nothing after the upgrade")

	var integrity string
	require.NoError(t, DB.Raw("PRAGMA integrity_check").Scan(&integrity).Error)
	assert.Equal(t, "ok", integrity)

	var foreignKeys int
	require.NoError(t, DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	assert.Equal(t, 1, foreignKeys, "foreign keys must be enforced again once the migration is over")

	// Not a single row was lost or gained.
	for table, want := range upgradeFixtureCounts {
		var got int64
		require.NoError(t, DB.Table(table).Count(&got).Error)
		assert.Equal(t, want, got, "row count of %s", table)
	}

	var convertedCustomerID *uint
	require.NoError(t, DB.Raw("SELECT customer_id FROM leads WHERE id = 2").Scan(&convertedCustomerID).Error)
	require.NotNil(t, convertedCustomerID, "the converted lead must keep its customer link")
	assert.Equal(t, uint(2), *convertedCustomerID)

	// The company link arrived on both tables: column, constraint and index.
	for _, table := range []string{"customers", "leads"} {
		assert.True(t, DB.Migrator().HasColumn(table, "company_id"), "%s.company_id", table)

		ddl := tableSQL(t, table)
		assert.Contains(t, ddl,
			"CONSTRAINT `fk_"+table+"_company_record` FOREIGN KEY (`company_id`) REFERENCES `companies`(`id`)",
			"%s must carry the company foreign key, got DDL: %s", table, ddl)

		assert.NotEmpty(t, indexSQL(t, table, "idx_"+table+"_company_id"),
			"idx_%s_company_id must exist", table)
		assert.NotEmpty(t, indexSQL(t, table, "idx_"+table+"_deleted_at"),
			"idx_%s_deleted_at must survive the rebuild", table)
	}
	assert.Contains(t, indexSQL(t, "customers", "idx_customers_email"), "UNIQUE",
		"the unique email index must survive the rebuild")

	// The deals tables are created on the populated file, with their own
	// foreign keys. The one existing table given a foreign key to them is tasks
	// (rebuilt for fk_tasks_deal): deals point at companies, customers, leads
	// and users, never the other way round.
	for _, table := range []string{"deals", "deal_stage_changes"} {
		assert.True(t, DB.Migrator().HasTable(table), "%s must exist after the upgrade", table)
	}
	assert.Contains(t, tableSQL(t, "deals"), "CONSTRAINT `fk_deals_owner` FOREIGN KEY (`owner_id`) REFERENCES `users`(`id`)")
	assert.Contains(t, tableSQL(t, "deal_stage_changes"), "CONSTRAINT `fk_deal_stage_changes_changed_by` FOREIGN KEY (`changed_by_id`) REFERENCES `users`(`id`)")
	for _, table := range []string{"users", "companies", "customers", "leads", "tickets"} {
		assert.NotContains(t, tableSQL(t, table), "REFERENCES `deals`", "%s must not have gained a foreign key to deals", table)
	}
	assert.Contains(t, tableSQL(t, "tasks"), "CONSTRAINT `fk_tasks_deal` FOREIGN KEY (`deal_id`) REFERENCES `deals`(`id`)",
		"tasks must have gained its foreign key to deals")

	// A second migration on the upgraded file is a no-op.
	before := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, before, schemaSnapshot(t), "a repeated migration must not change the schema")
}

// TestMigrateDatabase_SQLiteUpgradeFromV120 is the upgrade every SQLite
// deployment created with v1.2.0 goes through on its first start of this
// release. The fixture holds tickets and tasks that reference customers, a
// task that references a lead and a lead converted to a customer, so the
// rebuild of `customers` and `leads` (needed to add the company foreign key)
// runs against referenced rows.
func TestMigrateDatabase_SQLiteUpgradeFromV120(t *testing.T) {
	openV120Fixture(t)

	require.NoError(t, MigrateDatabase())

	assertUpgradedV120(t)
}

// TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120 starts from what a
// failed first start leaves behind: `companies` exists and
// `customers.company_id` exists without its constraint and index (the ALTER
// TABLE ran outside the transaction that then failed). The next start must
// complete the upgrade rather than fail again or leave the link half built.
func TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120(t *testing.T) {
	openV120Fixture(t)
	execSQLFile(t, filepath.Join("testdata", "sqlite-v1.2.0-half-applied.sql"))

	require.NoError(t, MigrateDatabase())

	assertUpgradedV120(t)
}

// TestTaskDealLinkSQLiteUpgradeFromV120 is the SQLite half of the task-to-deal
// link (#240): SQLite installs never run the SQL migration, so AutoMigrate adds
// tasks.deal_id, its index and its foreign key, which rebuilds tasks while
// task_labels references its rows. Every existing task stays, unlinked, and a
// deal link is enforced afterwards.
func TestTaskDealLinkSQLiteUpgradeFromV120(t *testing.T) {
	openV120Fixture(t)
	require.False(t, DB.Migrator().HasColumn("tasks", "deal_id"), "the v1.2.0 fixture has no deal link")

	require.NoError(t, MigrateDatabase())

	assertUpgradedV120(t)

	assert.True(t, DB.Migrator().HasColumn("tasks", "deal_id"), "tasks.deal_id")
	assert.NotEmpty(t, indexSQL(t, "tasks", "idx_tasks_deal_id"), "idx_tasks_deal_id must exist")
	assert.NotEmpty(t, indexSQL(t, "tasks", "idx_tasks_deleted_at"), "idx_tasks_deleted_at must survive the rebuild")
	assert.Contains(t, tableSQL(t, "tasks"), "CONSTRAINT `fk_tasks_deal` FOREIGN KEY (`deal_id`) REFERENCES `deals`(`id`)")

	var linked int64
	require.NoError(t, DB.Table("tasks").Where("deal_id IS NOT NULL").Count(&linked).Error)
	assert.Zero(t, linked, "no existing task is backfilled")

	var dangling struct{ ID uint }
	require.NoError(t, DB.Raw("SELECT id FROM tasks ORDER BY id LIMIT 1").Scan(&dangling).Error)
	require.NotZero(t, dangling.ID)
	err := DB.Exec("UPDATE tasks SET deal_id = 999999 WHERE id = ?", dangling.ID).Error
	require.Error(t, err, "a deal_id naming no deal must be refused")
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")
}

// TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade proves that
// whatever the migration does to foreign-key enforcement on its way, the
// application ends up with it switched on: a dangling reference is rejected.
func TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade(t *testing.T) {
	openV120Fixture(t)
	require.NoError(t, MigrateDatabase())

	err := DB.Exec(
		"INSERT INTO tickets (title, description, status, priority, customer_id) VALUES (?, ?, 'open', 'medium', 999999)",
		"dangling", "must be rejected",
	).Error
	require.Error(t, err, "a ticket on a non-existent customer must be rejected")
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")

	var count int64
	require.NoError(t, DB.Table("tickets").Count(&count).Error)
	assert.Equal(t, upgradeFixtureCounts["tickets"], count, "the rejected ticket must not be stored")
}

// TestQuoteHistorySourceSQLiteUpgrade is the SQLite half of the quote history
// source (#400): SQLite installs never run the SQL migration, so AutoMigrate
// relaxes quote_status_changes.changed_by_id (a table rebuild) and adds
// source. The file holds users, quotes and the history table in the shape the
// previous release created it, with one staff row. The row stays with its
// user and reads source staff, the index and the foreign key to users
// survive, and a client answer without a user is accepted afterwards.
func TestQuoteHistorySourceSQLiteUpgrade(t *testing.T) {
	openV120Fixture(t)
	require.NoError(t, MigrateDatabase())

	// Put the history table back into its previous shape.
	require.NoError(t, DB.Exec("DROP TABLE `quote_status_changes`").Error)
	require.NoError(t, DB.Exec("CREATE TABLE `quote_status_changes` (`id` integer PRIMARY KEY AUTOINCREMENT,`quote_id` integer NOT NULL,"+
		"`from_status` varchar(20),`to_status` varchar(20) NOT NULL,`changed_by_id` integer NOT NULL,`changed_at` datetime NOT NULL,"+
		"CONSTRAINT `fk_quote_status_changes_changed_by` FOREIGN KEY (`changed_by_id`) REFERENCES `users`(`id`))").Error)
	require.NoError(t, DB.Exec("CREATE INDEX `idx_quote_status_changes_quote_id` ON `quote_status_changes`(`quote_id`)").Error)
	require.False(t, DB.Migrator().HasColumn("quote_status_changes", "source"), "the previous shape has no source")

	var userID uint
	require.NoError(t, DB.Raw("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&userID).Error)
	require.NotZero(t, userID)
	quote := Quote{Title: "Upgrade", Status: QuoteStatusSent, Currency: "EUR", OwnerID: userID}
	require.NoError(t, DB.Create(&quote).Error)
	require.NoError(t, DB.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, changed_at) VALUES (?, NULL, 'draft', ?, '2026-11-01 10:00:00')",
		quote.ID, userID).Error)
	require.Error(t, DB.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, changed_at) VALUES (?, 'draft', 'sent', NULL, '2026-11-01 10:05:00')",
		quote.ID).Error, "the previous shape needs a user")

	require.NoError(t, MigrateDatabase())

	assertUpgradedV120(t)

	// The existing row: kept, same user, source staff.
	var rows []QuoteStatusChange
	require.NoError(t, DB.Order("id ASC").Find(&rows).Error)
	require.Len(t, rows, 1, "the existing row is kept")
	require.NotNil(t, rows[0].ChangedByID)
	assert.Equal(t, userID, *rows[0].ChangedByID, "the existing row keeps its user")
	assert.Equal(t, QuoteChangeSourceStaff, rows[0].Source, "the existing row reads source staff")
	assert.Equal(t, QuoteStatusDraft, rows[0].ToStatus)
	assert.Nil(t, rows[0].FromStatus)

	// The schema: nullable actor, source, the index and the foreign key.
	ddl := tableSQL(t, "quote_status_changes")
	assert.NotContains(t, ddl, "`changed_by_id` integer NOT NULL", "changed_by_id is nullable, got DDL: %s", ddl)
	assert.Contains(t, ddl, "`changed_by_id` integer")
	assert.Contains(t, ddl, "`source` varchar(20) NOT NULL DEFAULT \"staff\"")
	assert.Contains(t, ddl, "CONSTRAINT `fk_quote_status_changes_changed_by` FOREIGN KEY (`changed_by_id`) REFERENCES `users`(`id`)")
	assert.NotEmpty(t, indexSQL(t, "quote_status_changes", "idx_quote_status_changes_quote_id"), "idx_quote_status_changes_quote_id must survive the rebuild")

	// A client answer has no user; a user that does not exist is refused.
	from := QuoteStatusSent
	client := QuoteStatusChange{QuoteID: quote.ID, FromStatus: &from, ToStatus: QuoteStatusAccepted, Source: QuoteChangeSourceClient, ChangedAt: rows[0].ChangedAt.Add(time.Hour)}
	require.NoError(t, DB.Create(&client).Error, "a client row without a user is accepted")
	var stored QuoteStatusChange
	require.NoError(t, DB.Preload("ChangedBy").First(&stored, client.ID).Error)
	assert.Nil(t, stored.ChangedByID)
	assert.Nil(t, stored.ChangedBy)
	assert.Equal(t, QuoteChangeSourceClient, stored.Source)

	err := DB.Exec("INSERT INTO quote_status_changes (quote_id, from_status, to_status, changed_by_id, source, changed_at) VALUES (?, 'draft', 'sent', 999999, 'staff', '2026-11-01 11:00:00')", quote.ID).Error
	require.Error(t, err, "a changed_by_id naming no user must be refused")
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")

	var violations []map[string]interface{}
	require.NoError(t, DB.Raw("PRAGMA foreign_key_check").Scan(&violations).Error)
	assert.Empty(t, violations, "foreign_key_check must find nothing")
}
