package main

import (
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

// #324: 20261117120000_add_quote_tax adds quote_lines.tax_rate_bp and
// tax_cents and quotes.tax_cents and gross_total_cents, each bigint NOT NULL
// DEFAULT 0 and each only when missing, then backfills gross_total_cents =
// total_cents + tax_cents in one UPDATE. The down half drops the four columns,
// each only when present. No index.

const (
	quoteTaxVersion         = 20261117120000
	quoteTaxPreviousVersion = automationRunLeaseVersion
	quoteTaxName            = "20261117120000_add_quote_tax"
)

// quoteTaxColumns are the columns the migration owns, per table.
var quoteTaxColumns = map[string][]string{
	"quotes":      {"tax_cents", "gross_total_cents"},
	"quote_lines": {"tax_rate_bp", "tax_cents"},
}

// withoutQuoteTaxColumns removes the columns of this migration from a
// snapshot of the quote tables, for the roots of older migrations whose
// AutoMigrate reference now carries them.
func withoutQuoteTaxColumns(tables map[string]tableSchema) map[string]tableSchema {
	out := make(map[string]tableSchema, len(tables))
	for table, schema := range tables {
		if owned, ok := quoteTaxColumns[table]; ok {
			out[table] = withoutColumns(schema, owned)
			continue
		}
		out[table] = schema
	}
	return out
}

// TestQuoteTaxMigrationFiles needs no database: the pair exists, has its
// headers, every ADD and DROP is guarded by an INFORMATION_SCHEMA probe, the
// up half has exactly one backfill UPDATE and no index, and the model declares
// the same columns. It is no longer the newest migration (#343).
func TestQuoteTaxMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, quoteTaxName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, quoteTaxName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Add quote tax"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove quote tax"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for table, columns := range quoteTaxColumns {
		for _, name := range columns {
			assert.Contains(t, upSQL, fmt.Sprintf("'alter table %s add column %s bigint not null default 0'", table, name))
			assert.Contains(t, upSQL, fmt.Sprintf("table_name = '%s' and column_name = '%s'), 'select 1', 'alter table %s add column %s", table, name, table, name),
				"%s.%s is added only when missing", table, name)
			assert.Contains(t, downSQL, fmt.Sprintf("table_name = '%s' and column_name = '%s'), 'alter table %s drop column %s', 'select 1'", table, name, table, name),
				"%s.%s is dropped only when present", table, name)
		}
	}
	assert.Equal(t, 4, strings.Count(upSQL, "execute stmt;"))
	assert.Equal(t, 4, strings.Count(downSQL, "execute stmt;"))
	assert.Equal(t, 4, strings.Count(upSQL, "add column"))
	assert.Equal(t, 4, strings.Count(downSQL, "drop column"))

	assert.Equal(t, 1, strings.Count(upSQL, "update "), "exactly one backfill UPDATE")
	assert.Contains(t, upSQL, "update quotes set gross_total_cents = total_cents + tax_cents where gross_total_cents <> total_cents + tax_cents;")
	assert.NotContains(t, upSQL, "updated_at =", "the backfill does not touch updated_at")
	for _, forbidden := range []string{"create index", "add index", "unique", "add key", "drop "} {
		assert.NotContains(t, upSQL, forbidden)
	}
	assert.NotContains(t, downSQL, "add column", "the down half adds nothing")
	assert.NotContains(t, downSQL, "update ", "the down half writes no row")

	// The model declares the same names and column definitions.
	for _, tc := range []struct {
		model  interface{}
		field  string
		column string
	}{
		{models.Quote{}, "TaxCents", ""},
		{models.Quote{}, "GrossTotalCents", ""},
		{models.QuoteLine{}, "TaxRateBP", "column:tax_rate_bp"},
		{models.QuoteLine{}, "TaxCents", ""},
	} {
		tag, ok := reflectField(tc.model, tc.field)
		require.Truef(t, ok, "%T.%s", tc.model, tc.field)
		assert.Containsf(t, tag, "not null;default:0", "%T.%s", tc.model, tc.field)
		if tc.column != "" {
			assert.Contains(t, tag, tc.column)
		}
		assert.NotContainsf(t, tag, "type:", "%T.%s takes GORM's int64 type, bigint on MySQL", tc.model, tc.field)
	}

	// The version is the one its file names, after the previous head; the
	// newest-migration assertion has moved to TestQuoteQuantityMigrationFiles
	// (#343).
	version, err := strconv.Atoi(strings.SplitN(quoteTaxName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, quoteTaxVersion, version)
	assert.Greater(t, quoteTaxVersion, quoteTaxPreviousVersion)
	assert.Greater(t, latestMigrationVersion, quoteTaxVersion, "a later migration is the newest")
}

// quoteTaxSchema is the part of the quote tables this migration owns.
func quoteTaxSchema(t *testing.T, tdb *migrationTestDB) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for table, columns := range quoteTaxColumns {
		out[table] = ownedColumns(snapshotTable(t, tdb, table), columns)
	}
	return out
}

// quoteTaxRest is everything of the quote tables this migration does not own.
// The quantity columns of the later 20261118120000 migration (#343) are left
// out too: AutoMigrate adds quantity_milli and MigrateDatabase drops quantity.
func quoteTaxRest(t *testing.T, tdb *migrationTestDB) map[string]tableSchema {
	t.Helper()
	out := map[string]tableSchema{}
	for table, columns := range quoteTaxColumns {
		out[table] = withoutColumns(snapshotTable(t, tdb, table), columns)
	}
	return withoutQuoteQuantityColumns(out)
}

// seedQuoteTaxRows writes an owner and a product through GORM, then a live and
// a soft-deleted quote with one line each in the pre-#324 shape.
func seedQuoteTaxRows(t *testing.T, tdb *migrationTestDB, tag string) {
	t.Helper()
	appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	pool, err := appDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	owner := models.User{Email: "quote-tax-" + tag + "@example.com", Password: "x", FirstName: "Q", LastName: "T", Role: models.RoleSales, IsActive: true}
	require.NoError(t, appDB.Create(&owner).Error)
	_, err = tdb.sql.Exec("INSERT INTO products (name, unit_price_cents, currency, is_active) VALUES ('Widget', 999, 'EUR', 1)")
	require.NoError(t, err)
	var productID uint
	require.NoError(t, tdb.sql.QueryRow("SELECT MAX(id) FROM products").Scan(&productID))
	for _, q := range []struct {
		title   string
		deleted string
	}{{"Kept " + tag, "NULL"}, {"Deleted " + tag, "'2026-10-31 09:00:00.000'"}} {
		result, err := tdb.sql.Exec(fmt.Sprintf("INSERT INTO quotes (created_at, updated_at, deleted_at, title, status, currency, subtotal_cents, discount_cents, total_cents, owner_id) VALUES ('2026-10-30 10:00:00.000', '2026-10-30 10:00:00.000', %s, ?, 'sent', 'EUR', 2997, 375, 2622, ?)", q.deleted), q.title, owner.ID)
		require.NoError(t, err)
		quoteID, err := result.LastInsertId()
		require.NoError(t, err)
		_, err = tdb.sql.Exec("INSERT INTO quote_lines (created_at, updated_at, quote_id, product_id, position, description, quantity, unit_price_cents, discount_bp, line_total_cents) VALUES ('2026-10-30 10:00:00.000', '2026-10-30 10:00:00.000', ?, ?, 1, 'Widget', 3, 999, 1250, 2622)", quoteID, productID)
		require.NoError(t, err)
	}
}

// assertQuoteTaxRows checks every seeded row after the up: gross = total + tax
// (tax 0 unless wantTax), updated_at untouched, lines at rate and tax 0 unless
// wantTax.
func assertQuoteTaxRows(t *testing.T, tdb *migrationTestDB, wantTax int64, wantRate int64) {
	t.Helper()
	rows, err := tdb.sql.Query("SELECT id, total_cents, tax_cents, gross_total_cents, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') FROM quotes ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, total, tax, gross int64
		var updated string
		require.NoError(t, rows.Scan(&id, &total, &tax, &gross, &updated))
		assert.Equal(t, wantTax, tax, "quote %d tax", id)
		assert.Equal(t, total+tax, gross, "quote %d reads gross = total + tax", id)
		assert.Equal(t, "2026-10-30 10:00:00", updated, "quote %d: updated_at is not touched", id)
		count++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 2, count, "both quotes, the soft-deleted one included, are kept")

	var lines, matching int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM quote_lines").Scan(&lines))
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM quote_lines WHERE tax_rate_bp = ? AND tax_cents = ?", wantRate, wantTax).Scan(&matching))
	assert.Equal(t, 2, lines)
	assert.Equal(t, lines, matching, "every line reads rate %d and tax %d", wantRate, wantTax)
}

func countQuoteRows(t *testing.T, tdb *migrationTestDB) (quotes, lines int) {
	t.Helper()
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM quotes").Scan(&quotes))
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM quote_lines").Scan(&lines))
	return quotes, lines
}

func TestQuoteTaxMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: AutoMigrate of models.Quote and models.QuoteLine on
	// top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "qtax_ref")
	require.NoError(t, reference.m.Migrate(quoteTaxPreviousVersion))
	for table, columns := range quoteTaxSchema(t, reference) {
		assert.Empty(t, columns, "%s predates the tax columns", table)
	}
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.Quote{}, &models.QuoteLine{}))
	want := quoteTaxSchema(t, reference)
	for table, columns := range quoteTaxColumns {
		require.Len(t, want[table], len(columns), "AutoMigrate adds the %s columns: %v", table, want[table])
		for _, column := range want[table] {
			assert.Contains(t, column, " bigint", column)
			assert.Contains(t, column, "nullable=NO default=0", column)
		}
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "qtax_fresh")
		require.NoError(t, tdb.m.Migrate(quoteTaxPreviousVersion))
		empty := quoteTaxRest(t, tdb)
		require.NoError(t, tdb.m.Migrate(quoteTaxVersion), "the up applies on an empty database")
		assert.Equal(t, want, quoteTaxSchema(t, tdb))
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, empty, quoteTaxRest(t, tdb))
		for table, columns := range quoteTaxSchema(t, tdb) {
			assert.Empty(t, columns, "the down drops the %s columns", table)
		}

		seedQuoteTaxRows(t, tdb, "fresh")
		before := map[string]tableSchema{}
		for table := range quoteTaxColumns {
			before[table] = snapshotTable(t, tdb, table)
		}
		quotesBefore, linesBefore := countQuoteRows(t, tdb)

		require.NoError(t, tdb.m.Migrate(quoteTaxVersion))
		assert.Equal(t, want, quoteTaxSchema(t, tdb), "the SQL migration produces what AutoMigrate produces")
		assert.Equal(t, empty, quoteTaxRest(t, tdb), "the rest of the quote tables is untouched")
		assertQuoteTaxRows(t, tdb, 0, 0)

		// Down, then up again.
		require.NoError(t, tdb.m.Steps(-1))
		version, dirty, err := tdb.m.Version()
		require.NoError(t, err)
		assert.False(t, dirty)
		assert.Equal(t, uint(quoteTaxPreviousVersion), version)
		for table := range quoteTaxColumns {
			assert.Equal(t, before[table], snapshotTable(t, tdb, table), "the down half removes exactly the four columns of %s", table)
		}
		quotesAfter, linesAfter := countQuoteRows(t, tdb)
		assert.Equal(t, quotesBefore, quotesAfter, "rollback keeps the quotes")
		assert.Equal(t, linesBefore, linesAfter, "rollback keeps the lines")

		require.NoError(t, tdb.m.Migrate(quoteTaxVersion))
		assert.Equal(t, want, quoteTaxSchema(t, tdb))
		assertQuoteTaxRows(t, tdb, 0, 0)
		require.ErrorIs(t, tdb.m.Migrate(quoteTaxVersion), migrate.ErrNoChange)

		// The down half is guarded: with a column already gone it still
		// succeeds.
		_, err = tdb.sql.Exec("ALTER TABLE quotes DROP COLUMN gross_total_cents")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "the drops run only when present")
		for table := range quoteTaxColumns {
			assert.Equal(t, before[table], snapshotTable(t, tdb, table))
		}
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "qtax_mig")
			require.NoError(t, tdb.m.Migrate(quoteTaxPreviousVersion))
			seedQuoteTaxRows(t, tdb, "upgrade")
			rest := quoteTaxRest(t, tdb)
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			wantTax, wantRate := int64(0), int64(0)
			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Quote{}, &models.QuoteLine{}))
				// A new binary started before the operator migrated and wrote
				// tax on the existing rows; gross is still the column default.
				_, err = tdb.sql.Exec("UPDATE quote_lines SET tax_rate_bp = 2100, tax_cents = 551")
				require.NoError(t, err)
				_, err = tdb.sql.Exec("UPDATE quotes SET tax_cents = 551, updated_at = '2026-10-30 10:00:00.000'")
				require.NoError(t, err)
				wantTax, wantRate = 551, 2100
			}

			require.NoError(t, tdb.m.Migrate(quoteTaxVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteTaxVersion), version)
			assert.Equal(t, want, quoteTaxSchema(t, tdb), "the SQL migration and AutoMigrate produce the same columns")
			assert.Equal(t, rest, quoteTaxRest(t, tdb))
			assertQuoteTaxRows(t, tdb, wantTax, wantRate)

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, quoteTaxSchema(t, tdb), "MigrateDatabase does not change them")
			assertQuoteTaxRows(t, tdb, wantTax, wantRate)
			require.ErrorIs(t, tdb.m.Migrate(quoteTaxVersion), migrate.ErrNoChange)

			// Down, then up again.
			quotesBefore, linesBefore := countQuoteRows(t, tdb)
			require.NoError(t, tdb.m.Steps(-1))
			for table, columns := range quoteTaxSchema(t, tdb) {
				assert.Empty(t, columns, "the down drops the %s columns", table)
			}
			quotesAfter, linesAfter := countQuoteRows(t, tdb)
			assert.Equal(t, quotesBefore, quotesAfter, "rollback keeps the quotes")
			assert.Equal(t, linesBefore, linesAfter, "rollback keeps the lines")
			require.NoError(t, tdb.m.Migrate(quoteTaxVersion))
			assert.Equal(t, want, quoteTaxSchema(t, tdb))
			assertQuoteTaxRows(t, tdb, 0, 0)
		})
	}
}
