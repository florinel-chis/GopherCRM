package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The products and quotes migration (issue #158), on a real MySQL or MariaDB
// server: it applies to a fresh database and to one whose new binary already
// ran AutoMigrate, it produces exactly the schema AutoMigrate produces (so a
// database created either way ends up the same), AutoMigrate on top of it is a
// no-op, applying it a second time is ErrNoChange, and Steps(-1) removes the
// four tables and nothing else. The migration is applied by version, not with
// Up: later migrations (the automation pair of issue #164) sort after it, and
// MigrateDatabase creates their tables through AutoMigrate, which is why the
// rollback is compared against the tables present after that AutoMigrate.
const (
	quotesMigrationVersion    = 20261026120000
	quotesPreviousHeadVersion = 20261019120000
)

var quoteMigrationTables = []string{"products", "quotes", "quote_lines", "quote_status_changes"}

// migrationTestDB is one disposable database on the TEST_MYSQL_DSN server with
// a migrate instance pointed at the repository's migrations directory.
type migrationTestDB struct {
	name string
	sql  *sql.DB
	m    *migrate.Migrate
	dsn  string
}

func openMigrationTestDB(t *testing.T, baseDSN, prefix string) *migrationTestDB {
	t.Helper()
	server, err := sql.Open("mysql", migrationDSN(baseDSN))
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_%s_%d", prefix, time.Now().UnixNano())
	_, err = server.Exec("CREATE DATABASE " + name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS " + name)
		_ = server.Close()
	})

	dsn := baseDSN + name + "?multiStatements=true&parseTime=true"
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	driver, err := mysql.WithInstance(db, &mysql.Config{})
	require.NoError(t, err)
	path, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://"+path, "mysql", driver)
	require.NoError(t, err)
	// Registered after the DROP cleanup, so it runs first: the instance (and
	// the pool it owns) is closed before the database is dropped.
	t.Cleanup(func() { _, _ = m.Close() })
	return &migrationTestDB{name: name, sql: db, m: m, dsn: dsn}
}

// tableSchema is the comparable shape of one table: its columns, its indexes
// and its foreign keys, each rendered as one sorted line per element.
type tableSchema struct {
	Columns     []string
	Indexes     []string
	ForeignKeys []string
}

// snapshotQuoteTables reads the four tables out of INFORMATION_SCHEMA. Names,
// types, nullability, defaults, extras, index membership and referenced
// columns are compared; collation is not, because AutoMigrate inherits the
// database default while the SQL names utf8mb4_unicode_ci explicitly.
func snapshotQuoteTables(t *testing.T, db *sql.DB, schema string) map[string]tableSchema {
	t.Helper()
	out := map[string]tableSchema{}
	for _, table := range quoteMigrationTables {
		var ts tableSchema

		rows, err := db.Query(`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<none>'), EXTRA
			FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, table)
		require.NoError(t, err)
		for rows.Next() {
			var name, typ, nullable, def, extra string
			require.NoError(t, rows.Scan(&name, &typ, &nullable, &def, &extra))
			ts.Columns = append(ts.Columns, fmt.Sprintf("%s %s nullable=%s default=%s %s", name, strings.ToLower(typ), nullable, def, strings.ToLower(extra)))
		}
		require.NoError(t, rows.Err())
		rows.Close()

		rows, err = db.Query(`SELECT INDEX_NAME, NON_UNIQUE, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
			FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? GROUP BY INDEX_NAME, NON_UNIQUE`, schema, table)
		require.NoError(t, err)
		for rows.Next() {
			var name, columns string
			var nonUnique int
			require.NoError(t, rows.Scan(&name, &nonUnique, &columns))
			ts.Indexes = append(ts.Indexes, fmt.Sprintf("%s (%s) non_unique=%d", name, columns, nonUnique))
		}
		require.NoError(t, rows.Err())
		rows.Close()

		rows, err = db.Query(`SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
			FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL`, schema, table)
		require.NoError(t, err)
		for rows.Next() {
			var name, column, refTable, refColumn string
			require.NoError(t, rows.Scan(&name, &column, &refTable, &refColumn))
			ts.ForeignKeys = append(ts.ForeignKeys, fmt.Sprintf("%s %s -> %s.%s", name, column, refTable, refColumn))
		}
		require.NoError(t, rows.Err())
		rows.Close()

		sort.Strings(ts.Columns)
		sort.Strings(ts.Indexes)
		sort.Strings(ts.ForeignKeys)
		out[table] = ts
	}
	return out
}

func listTables(t *testing.T, db *sql.DB, schema string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ?`, schema)
	require.NoError(t, err)
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	sort.Strings(tables)
	return tables
}

func without(tables []string, drop []string) []string {
	dropped := map[string]bool{}
	for _, name := range drop {
		dropped[name] = true
	}
	var kept []string
	for _, name := range tables {
		if !dropped[name] {
			kept = append(kept, name)
		}
	}
	return kept
}

func TestProductsAndQuotesMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The reference: the four tables as GORM AutoMigrate creates them on top
	// of the previous SQL head, which is what a database whose new binary
	// started before the operator ran the migration looks like.
	reference := openMigrationTestDB(t, baseDSN, "quotes_ref")
	require.NoError(t, reference.m.Migrate(quotesPreviousHeadVersion))
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, refDB.AutoMigrate(&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{}))
	want := snapshotQuoteTables(t, reference.sql, reference.name)
	for _, table := range quoteMigrationTables {
		require.NotEmpty(t, want[table].Columns, "AutoMigrate must have created %s", table)
	}
	assert.Contains(t, want["quotes"].ForeignKeys, "fk_quotes_deal deal_id -> deals.id")
	assert.Contains(t, want["quote_lines"].ForeignKeys, "fk_quotes_lines quote_id -> quotes.id")
	assert.Contains(t, want["quote_lines"].ForeignKeys, "fk_quote_lines_product product_id -> products.id")
	assert.Contains(t, want["quote_status_changes"].ForeignKeys, "fk_quote_status_changes_changed_by changed_by_id -> users.id")
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}

	for _, autoMigrateFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "quotes_mig")
			require.NoError(t, tdb.m.Migrate(quotesPreviousHeadVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{}))
				// A row the new binary wrote before the operator migrated: the
				// IF NOT EXISTS must keep the table, and with it the row.
				_, err = tdb.sql.Exec("INSERT INTO products (name, unit_price_cents, currency, is_active) VALUES ('Kept', 100, 'EUR', 1)")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(quotesMigrationVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quotesMigrationVersion), version)

			got := snapshotQuoteTables(t, tdb.sql, tdb.name)
			for _, table := range quoteMigrationTables {
				assert.Equalf(t, want[table], got[table], "%s differs between the SQL migration and AutoMigrate", table)
			}
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM products WHERE name = 'Kept'").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			// The application's own migration on top: succeeds and changes
			// nothing in the four tables. It does create the tables of the
			// migrations that sort after this one (AutoMigrate knows every
			// model), so the rollback below is compared against this list.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, got, snapshotQuoteTables(t, tdb.sql, tdb.name), "MigrateDatabase must not alter the migrated tables")
			tablesMigrated := listTables(t, tdb.sql, tdb.name)
			assert.Subset(t, tablesMigrated, tablesBefore, "MigrateDatabase drops nothing")

			require.ErrorIs(t, tdb.m.Migrate(quotesMigrationVersion), migrate.ErrNoChange)

			// Rollback: the four tables go, every other table stays.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quotesPreviousHeadVersion), version)
			tablesAfter := listTables(t, tdb.sql, tdb.name)
			for _, table := range quoteMigrationTables {
				assert.NotContains(t, tablesAfter, table)
			}
			assert.Equal(t, without(tablesMigrated, quoteMigrationTables), tablesAfter, "Steps(-1) must leave every other table intact")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(quotesMigrationVersion))
			assert.Equal(t, got, snapshotQuoteTables(t, tdb.sql, tdb.name))
		})
	}
}
