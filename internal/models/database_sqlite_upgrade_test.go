package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
