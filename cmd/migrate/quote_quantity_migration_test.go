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

// #343: 20261118120000_store_quote_line_quantity_milli adds
// quote_lines.quantity_milli bigint NOT NULL DEFAULT 0 when missing, copies
// quantity x 1000 into it where it is still 0 while the legacy quantity
// exists, and drops quantity when present. The down half re-adds quantity
// bigint NOT NULL (no default), sets it to GREATEST(1, (quantity_milli + 500)
// DIV 1000) and drops quantity_milli. Every step is guarded by an
// INFORMATION_SCHEMA probe.

const (
	quoteQuantityVersion         = 20261118120000
	quoteQuantityPreviousVersion = quoteTaxVersion
	quoteQuantityName            = "20261118120000_store_quote_line_quantity_milli"
)

// quoteQuantityColumns are the quote_lines columns the migration owns: the
// one it adds and the legacy one it drops.
var quoteQuantityColumns = []string{"quantity_milli", "quantity"}

// withoutQuoteQuantityColumns removes the columns of this migration from a
// snapshot of the quote tables, for the roots of older migrations: their
// AutoMigrate reference carries quantity_milli, and a schema migrated only up
// to their version carries the legacy quantity.
func withoutQuoteQuantityColumns(tables map[string]tableSchema) map[string]tableSchema {
	out := make(map[string]tableSchema, len(tables))
	for table, schema := range tables {
		if table == "quote_lines" {
			out[table] = withoutColumns(schema, quoteQuantityColumns)
			continue
		}
		out[table] = schema
	}
	return out
}

// TestQuoteQuantityMigrationFiles needs no database: the pair exists, has its
// headers, every ADD, UPDATE, MODIFY and DROP is guarded by an
// INFORMATION_SCHEMA probe, each half has exactly one UPDATE and no index, the
// model declares the same column, the migration is the newest of the
// directory and latestMigrationVersion names it.
func TestQuoteQuantityMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, quoteQuantityName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, quoteQuantityName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Store quote line quantities in thousandths"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Restore whole quote line quantities"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	probe := func(column string) string {
		return fmt.Sprintf("table_name = 'quote_lines' and column_name = '%s')", column)
	}
	assert.Contains(t, upSQL, probe("quantity_milli")+", 'select 1', 'alter table quote_lines add column quantity_milli bigint not null default 0'",
		"quantity_milli is added only when missing")
	assert.Contains(t, upSQL, probe("quantity")+", 'update quote_lines set quantity_milli = quantity * 1000 where quantity_milli = 0', 'select 1'",
		"the backfill runs only while quantity exists and only on rows still at 0")
	assert.Contains(t, upSQL, probe("quantity")+", 'alter table quote_lines drop column quantity', 'select 1'",
		"quantity is dropped only when present")
	assert.Equal(t, 3, strings.Count(upSQL, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upSQL, "update quote_lines"), "exactly one UPDATE in the up half")
	assert.Equal(t, 1, strings.Count(upSQL, "add column"))
	assert.Equal(t, 1, strings.Count(upSQL, "drop column"))
	assert.NotContains(t, upSQL, "updated_at =", "the backfill does not touch updated_at")

	assert.Contains(t, downSQL, probe("quantity")+", 'select 1', 'alter table quote_lines add column quantity bigint not null default 0'",
		"quantity is re-added only when missing")
	assert.Contains(t, downSQL, probe("quantity_milli")+", 'update quote_lines set quantity = greatest(1, (quantity_milli + 500) div 1000)', 'select 1'",
		"the restore rounds half up with a floor of 1, only while quantity_milli exists")
	assert.Contains(t, downSQL, probe("quantity")+", 'alter table quote_lines modify column quantity bigint not null', 'select 1'",
		"the restored column has no default, as 20261026120000 created it")
	assert.Contains(t, downSQL, probe("quantity_milli")+", 'alter table quote_lines drop column quantity_milli', 'select 1'",
		"quantity_milli is dropped only when present")
	assert.Equal(t, 4, strings.Count(downSQL, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downSQL, "update quote_lines"), "exactly one UPDATE in the down half")

	// Every statement that changes the schema or a row sits inside a probe.
	for name, text := range map[string]string{"up": upSQL, "down": downSQL} {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "--") {
				continue
			}
			if strings.Contains(line, "alter table") || strings.Contains(line, "update ") {
				assert.Truef(t, strings.HasPrefix(line, "set @preparedstatement = (select if(exists(select 1 from information_schema.columns where table_schema = database() and table_name = 'quote_lines'"),
					"%s: unguarded statement %q", name, line)
			}
		}
		for _, forbidden := range []string{"create index", "add index", "unique", "add key", "drop table"} {
			assert.NotContainsf(t, text, forbidden, "%s half", name)
		}
	}

	// The model declares the same column.
	tag, ok := reflectField(models.QuoteLine{}, "Quantity")
	require.True(t, ok)
	assert.Contains(t, tag, "column:quantity_milli;type:bigint;not null;default:0")

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestQuoteTaxMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, quoteQuantityName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, quoteQuantityVersion, version)
	assert.Equal(t, quoteQuantityVersion, latestMigrationVersion)
	assert.Greater(t, quoteQuantityVersion, quoteQuantityPreviousVersion)
}

// quoteQuantitySeed is what seedQuoteQuantityRows wrote: the line ids and the
// whole quantity of each.
type quoteQuantitySeed struct {
	lineIDs    []int64
	quantities []int64
}

// seedQuoteQuantityRows writes an owner through GORM, then a product, a quote
// and three lines in the pre-#343 shape (whole quantity column).
func seedQuoteQuantityRows(t *testing.T, tdb *migrationTestDB, tag string) quoteQuantitySeed {
	t.Helper()
	appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	pool, err := appDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	owner := models.User{Email: "quote-quantity-" + tag + "@example.com", Password: "x", FirstName: "Q", LastName: "Q", Role: models.RoleSales, IsActive: true}
	require.NoError(t, appDB.Create(&owner).Error)
	_, err = tdb.sql.Exec("INSERT INTO products (name, unit_price_cents, currency, is_active) VALUES ('Widget', 999, 'EUR', 1)")
	require.NoError(t, err)
	var productID int64
	require.NoError(t, tdb.sql.QueryRow("SELECT MAX(id) FROM products").Scan(&productID))
	result, err := tdb.sql.Exec("INSERT INTO quotes (created_at, updated_at, title, status, currency, subtotal_cents, discount_cents, total_cents, owner_id) VALUES ('2026-10-30 10:00:00.000', '2026-10-30 10:00:00.000', ?, 'draft', 'EUR', 0, 0, 0, ?)", "Quantities "+tag, owner.ID)
	require.NoError(t, err)
	quoteID, err := result.LastInsertId()
	require.NoError(t, err)

	seed := quoteQuantitySeed{quantities: []int64{1, 3, models.QuoteLineQuantityMax}}
	for i, quantity := range seed.quantities {
		result, err := tdb.sql.Exec("INSERT INTO quote_lines (created_at, updated_at, quote_id, product_id, position, description, quantity, unit_price_cents, discount_bp, line_total_cents) VALUES ('2026-10-30 10:00:00.000', '2026-10-30 10:00:00.000', ?, ?, ?, 'Widget', ?, 1, 0, ?)",
			quoteID, productID, i+1, quantity, quantity)
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		seed.lineIDs = append(seed.lineIDs, id)
	}
	return seed
}

// lineQuantityValues reads one column of every seeded line, in id order,
// with updated_at, which no migration step may touch.
func lineQuantityValues(t *testing.T, tdb *migrationTestDB, column string) []int64 {
	t.Helper()
	rows, err := tdb.sql.Query("SELECT " + column + ", DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s'), line_total_cents FROM quote_lines ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var value, total int64
		var updated string
		require.NoError(t, rows.Scan(&value, &updated, &total))
		assert.Equal(t, "2026-10-30 10:00:00", updated, "updated_at is not touched")
		out = append(out, value)
	}
	require.NoError(t, rows.Err())
	return out
}

func timesThousand(values []int64) []int64 {
	out := make([]int64, len(values))
	for i, v := range values {
		out[i] = v * models.QuoteQuantityScale
	}
	return out
}

func runMigrateDatabase(t *testing.T, dsn string) {
	t.Helper()
	appDB, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	pool, err := appDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	models.DB = appDB
	require.NoError(t, models.MigrateDatabase())
}

func TestQuoteQuantityMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the quote_lines schema models.MigrateDatabase
	// (AutoMigrate plus migrateLegacyQuoteLineQuantity) produces on top of the
	// previous SQL head. Plain AutoMigrate is not the reference: it never
	// drops quantity.
	reference := openMigrationTestDB(t, baseDSN, "qqty_ref")
	require.NoError(t, reference.m.Migrate(quoteQuantityPreviousVersion))
	legacy := snapshotTable(t, reference, "quote_lines")
	require.Len(t, ownedColumns(legacy, []string{"quantity"}), 1, "the previous head has the whole-unit column")
	require.Empty(t, ownedColumns(legacy, []string{"quantity_milli"}))
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, "quote_lines")
	assert.Empty(t, ownedColumns(want, []string{"quantity"}), "MigrateDatabase drops the legacy column")
	milli := ownedColumns(want, []string{"quantity_milli"})
	require.Len(t, milli, 1)
	assert.Contains(t, milli[0], " bigint")
	assert.Contains(t, milli[0], "nullable=NO default=0")
	assert.Equal(t, legacy.Indexes, want.Indexes, "the indexes are unchanged")
	assert.Equal(t, legacy.ForeignKeys, want.ForeignKeys, "the foreign keys are unchanged")
	assert.Equal(t, withoutColumns(legacy, quoteQuantityColumns), withoutColumns(want, quoteQuantityColumns), "no other column changes")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "qqty_fresh")
		require.NoError(t, tdb.m.Migrate(quoteQuantityPreviousVersion))
		require.NoError(t, tdb.m.Migrate(quoteQuantityVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, legacy, snapshotTable(t, tdb, "quote_lines"), "the down restores the previous schema")

		seed := seedQuoteQuantityRows(t, tdb, "fresh")
		require.NoError(t, tdb.m.Migrate(quoteQuantityVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
		assert.Equal(t, timesThousand(seed.quantities), lineQuantityValues(t, tdb, "quantity_milli"), "every row is backfilled quantity x 1000")
		require.ErrorIs(t, tdb.m.Migrate(quoteQuantityVersion), migrate.ErrNoChange)

		// MigrateDatabase on top changes neither the schema nor a value.
		runMigrateDatabase(t, tdb.dsn)
		assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
		assert.Equal(t, timesThousand(seed.quantities), lineQuantityValues(t, tdb, "quantity_milli"))

		// Fractional values written by the new binary: the down rounds them
		// half up to whole units with a floor of 1.
		for id, value := range map[int64]int64{seed.lineIDs[0]: 1500, seed.lineIDs[1]: 1} {
			_, err := tdb.sql.Exec("UPDATE quote_lines SET quantity_milli = ? WHERE id = ?", value, id)
			require.NoError(t, err)
		}
		require.NoError(t, tdb.m.Steps(-1))
		version, dirty, err := tdb.m.Version()
		require.NoError(t, err)
		assert.False(t, dirty)
		assert.Equal(t, uint(quoteQuantityPreviousVersion), version)
		assert.Equal(t, legacy, snapshotTable(t, tdb, "quote_lines"), "the down restores quantity bigint NOT NULL with no default")
		assert.Equal(t, []int64{2, 1, models.QuoteLineQuantityMax}, lineQuantityValues(t, tdb, "quantity"))

		// Up after down restores whole values exactly.
		require.NoError(t, tdb.m.Migrate(quoteQuantityVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
		assert.Equal(t, []int64{2000, 1000, models.QuoteLineQuantityMilliMax}, lineQuantityValues(t, tdb, "quantity_milli"))

		// The down half is guarded: with quantity already restored by hand it
		// still succeeds.
		_, err = tdb.sql.Exec("ALTER TABLE quote_lines ADD COLUMN quantity bigint NOT NULL DEFAULT 0")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "every step of the down is guarded")
		assert.Equal(t, legacy, snapshotTable(t, tdb, "quote_lines"))
	})

	for _, mode := range []string{"AutoMigrateFirst=false", "AutoMigrateFirst=true", "AutoMigrateOnly"} {
		mode := mode
		t.Run("Upgrade/"+mode, func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "qqty_mig")
			require.NoError(t, tdb.m.Migrate(quoteQuantityPreviousVersion))
			seed := seedQuoteQuantityRows(t, tdb, "upgrade")

			switch mode {
			case "AutoMigrateFirst=true":
				// A new binary started before the operator migrated:
				// AutoMigrate followed by MigrateDatabase already converted
				// and dropped the column.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
			case "AutoMigrateOnly":
				// AutoMigrate alone added quantity_milli (all 0) and kept quantity.
				appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
				require.NoError(t, err)
				require.NoError(t, appDB.AutoMigrate(&models.QuoteLine{}))
				if pool, err := appDB.DB(); err == nil {
					_ = pool.Close()
				}
				require.Len(t, ownedColumns(snapshotTable(t, tdb, "quote_lines"), quoteQuantityColumns), 2)
			}

			require.NoError(t, tdb.m.Migrate(quoteQuantityVersion), "the migration must apply (%s)", mode)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(quoteQuantityVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"), "the SQL migration yields the MigrateDatabase reference")
			assert.Equal(t, timesThousand(seed.quantities), lineQuantityValues(t, tdb, "quantity_milli"))

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"), "MigrateDatabase is idempotent on top")
			assert.Equal(t, timesThousand(seed.quantities), lineQuantityValues(t, tdb, "quantity_milli"))
			require.ErrorIs(t, tdb.m.Migrate(quoteQuantityVersion), migrate.ErrNoChange)

			// Down, then up again.
			require.NoError(t, tdb.m.Steps(-1))
			assert.Empty(t, ownedColumns(snapshotTable(t, tdb, "quote_lines"), []string{"quantity_milli"}))
			assert.Equal(t, seed.quantities, lineQuantityValues(t, tdb, "quantity"), "the down restores the whole values")
			require.NoError(t, tdb.m.Migrate(quoteQuantityVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, "quote_lines"))
			assert.Equal(t, timesThousand(seed.quantities), lineQuantityValues(t, tdb, "quantity_milli"))
		})
	}
}
