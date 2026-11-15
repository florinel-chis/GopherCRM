package models

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// #324: per-line tax rate in basis points, a stored per-line tax rounded half
// up, and the quote's tax and gross totals.

func TestQuoteLineTaxCompute(t *testing.T) {
	for _, tc := range []struct {
		name            string
		lineTotal, rate int64
		want            int64
	}{
		{"21 percent of 10.00", 1000, 2100, 210},
		{"half a cent rounds up", 1, 5000, 1},
		{"just under half a cent rounds down", 1, 4999, 0},
		{"15 percent of 3.33", 333, 1500, 50},
		{"0 bp is no tax", 123456, 0, 0},
		{"0 bp on zero", 0, 0, 0},
		{"10000 bp is the line total", 98765, 10000, 98765},
		{"the bound at 100 percent", QuoteAmountCentsMax, 10000, QuoteAmountCentsMax},
		{"the quantity 3 x 9.99 at 12.5 percent off, 21 percent", 2622, 2100, 551},
		{"8x10^11 at 25 percent", 800_000_000_000, 2500, 200_000_000_000},
		{"8x10^11 + 1 at 25 percent", 800_000_000_001, 2500, 200_000_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeQuoteLineTax(tc.lineTotal, tc.rate)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, (tc.lineTotal*tc.rate+5000)/10000, got, "the formula of the contract")
		})
	}
}

func TestQuoteTotalsWithTax(t *testing.T) {
	assert.Equal(t, QuoteTotals{}, SumQuoteTotals(nil), "no lines, all zero")

	lines := []QuoteLine{
		{Quantity: 3, UnitPriceCents: 999, DiscountBP: 1250, TaxRateBP: 2100},
		{Quantity: 1, UnitPriceCents: 1000, DiscountBP: 0, TaxRateBP: 0},
		{Quantity: 7, UnitPriceCents: 1000, DiscountBP: 10000, TaxRateBP: 2100},
	}
	for i := range lines {
		_, lines[i].LineTotalCents = ComputeQuoteLineTotal(lines[i].Quantity, lines[i].UnitPriceCents, lines[i].DiscountBP)
		lines[i].TaxCents = ComputeQuoteLineTax(lines[i].LineTotalCents, lines[i].TaxRateBP)
	}
	require.Equal(t, int64(2622), lines[0].LineTotalCents)
	require.Equal(t, int64(551), lines[0].TaxCents)

	totals := SumQuoteTotals(lines)
	subtotal, discount, total := ComputeQuoteTotals(lines)
	assert.Equal(t, subtotal, totals.Subtotal, "subtotal exactly as ComputeQuoteTotals")
	assert.Equal(t, discount, totals.Discount, "discount exactly as ComputeQuoteTotals")
	assert.Equal(t, total, totals.Total, "net total exactly as ComputeQuoteTotals")
	assert.Equal(t, int64(551+0+0), totals.Tax)
	assert.Equal(t, totals.Total+totals.Tax, totals.Gross)
	assert.Equal(t, int64(2622+1000+551), totals.Gross)

	// The tax is the sum of the stored line taxes, each rounded on its own:
	// two lines of 0.01 at 50 percent round to 0.01 each, 0.02 in all, where
	// rounding the summed 0.02 at 50 percent would give 0.01.
	perLine := []QuoteLine{
		{Quantity: 1, UnitPriceCents: 1, LineTotalCents: 1, TaxRateBP: 5000, TaxCents: ComputeQuoteLineTax(1, 5000)},
		{Quantity: 1, UnitPriceCents: 1, LineTotalCents: 1, TaxRateBP: 5000, TaxCents: ComputeQuoteLineTax(1, 5000)},
	}
	pinned := SumQuoteTotals(perLine)
	assert.Equal(t, int64(2), pinned.Tax, "per-line rounding")
	assert.Equal(t, int64(2), pinned.Total)
	assert.Equal(t, int64(4), pinned.Gross)

	// The stored line tax is trusted, as the stored line total is.
	stored := SumQuoteTotals([]QuoteLine{{Quantity: 1, UnitPriceCents: 100, LineTotalCents: 100, TaxRateBP: 2100, TaxCents: 7}})
	assert.Equal(t, int64(7), stored.Tax)
	assert.Equal(t, int64(107), stored.Gross)
}

func TestQuoteTaxJSON(t *testing.T) {
	quote := Quote{
		BaseModel: BaseModel{ID: 42}, Title: "Taxed", Status: QuoteStatusDraft, Currency: "EUR",
		SubtotalCents: 2997, DiscountCents: 375, TotalCents: 2622, TaxCents: 551, GrossTotalCents: 3173,
		Lines: []QuoteLine{{ID: 1, QuoteID: 42, Quantity: 3, UnitPriceCents: 999, DiscountBP: 1250, LineTotalCents: 2622, TaxRateBP: 2100, TaxCents: 551}},
	}
	encoded, err := json.Marshal(quote)
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	for key, want := range map[string]string{"total_cents": "2622", "tax_cents": "551", "gross_total_cents": "3173", "subtotal_cents": "2997", "discount_cents": "375"} {
		assert.Equal(t, want, string(decoded[key]), "quote %s is an integer", key)
	}
	var lines []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded["lines"], &lines))
	require.Len(t, lines, 1)
	assert.Equal(t, "2100", string(lines[0]["tax_rate_bp"]))
	assert.Equal(t, "551", string(lines[0]["tax_cents"]))
	assert.Equal(t, "2622", string(lines[0]["line_total_cents"]))

	// A quote without tax still carries the keys, as zero integers.
	encoded, err = json.Marshal(Quote{BaseModel: BaseModel{ID: 1}, Title: "Bare", Lines: []QuoteLine{{ID: 2}}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"tax_cents":0`)
	assert.Contains(t, string(encoded), `"gross_total_cents":0`)
	assert.Contains(t, string(encoded), `"tax_rate_bp":0`)

	// The swagger tags: the computed values are read-only, the rate bounded.
	tag := func(typ reflect.Type, name, key string) string {
		field, ok := typ.FieldByName(name)
		require.Truef(t, ok, "%s has no %s", typ.Name(), name)
		return field.Tag.Get(key)
	}
	quoteType, lineType := reflect.TypeOf(Quote{}), reflect.TypeOf(QuoteLine{})
	assert.Equal(t, "true", tag(quoteType, "TaxCents", "readonly"))
	assert.Equal(t, "true", tag(quoteType, "GrossTotalCents", "readonly"))
	assert.Equal(t, "true", tag(lineType, "TaxCents", "readonly"))
	assert.Equal(t, "0", tag(lineType, "TaxRateBP", "minimum"))
	assert.Equal(t, "10000", tag(lineType, "TaxRateBP", "maximum"))
	assert.Empty(t, tag(lineType, "TaxRateBP", "readonly"), "the rate is writable")
	assert.Equal(t, "total_cents", tag(quoteType, "TotalCents", "json"), "total_cents keeps its key")
}

func openQuoteTaxSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quote_tax_schema.db")), &gorm.Config{
		Logger: logger.Discard, DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Quote{}, &QuoteLine{}))
	return db
}

func sqliteColumnsOf(t *testing.T, db *gorm.DB, table string) map[string]sqliteColumn {
	t.Helper()
	var columns []sqliteColumn
	require.NoError(t, db.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table).Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	for _, column := range columns {
		byName[column.Name] = column
	}
	return byName
}

func assertQuoteTaxColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	for table, names := range map[string][]string{"quotes": {"tax_cents", "gross_total_cents"}, "quote_lines": {"tax_rate_bp", "tax_cents"}} {
		columns := sqliteColumnsOf(t, db, table)
		for _, name := range names {
			column, ok := columns[name]
			require.Truef(t, ok, "%s.%s exists", table, name)
			assert.Equalf(t, 1, column.NotNull, "%s.%s is NOT NULL", table, name)
			require.NotNilf(t, column.Dflt, "%s.%s has a default", table, name)
			assert.Equalf(t, "0", strings.Trim(*column.Dflt, "'\""), "%s.%s defaults to 0", table, name)
			assert.Equalf(t, "integer", strings.ToLower(column.Type), "%s.%s is an integer", table, name)
		}
	}
}

func TestQuoteTaxColumnsSchemaSQLite(t *testing.T) {
	db := openQuoteTaxSchemaDB(t)
	assertQuoteTaxColumns(t, db)

	// An explicit 0 persists on create, and so does a value written back to 0
	// through Save (the full-row write the line update uses).
	quote := Quote{Title: "Zero", Currency: "EUR", OwnerID: 1}
	require.NoError(t, db.Create(&quote).Error)
	line := QuoteLine{QuoteID: quote.ID, ProductID: 1, Position: 1, Description: "x", Quantity: 1, UnitPriceCents: 100, LineTotalCents: 100}
	require.NoError(t, db.Create(&line).Error)
	var readQuote Quote
	require.NoError(t, db.First(&readQuote, quote.ID).Error)
	assert.Zero(t, readQuote.TaxCents)
	assert.Zero(t, readQuote.GrossTotalCents)
	var readLine QuoteLine
	require.NoError(t, db.First(&readLine, line.ID).Error)
	assert.Zero(t, readLine.TaxRateBP)
	assert.Zero(t, readLine.TaxCents)

	line.TaxRateBP, line.TaxCents = 2100, 21
	require.NoError(t, db.Save(&line).Error)
	require.NoError(t, db.First(&readLine, line.ID).Error)
	assert.Equal(t, int64(2100), readLine.TaxRateBP)
	assert.Equal(t, int64(21), readLine.TaxCents)
	line.TaxRateBP, line.TaxCents = 0, 0
	require.NoError(t, db.Save(&line).Error)
	require.NoError(t, db.First(&readLine, line.ID).Error)
	assert.Zero(t, readLine.TaxRateBP, "a rate set back to 0 is written")
	assert.Zero(t, readLine.TaxCents)

	// A non-zero value is written on create as well.
	taxed := QuoteLine{QuoteID: quote.ID, ProductID: 1, Position: 2, Description: "y", Quantity: 1, UnitPriceCents: 1000, LineTotalCents: 1000, TaxRateBP: 2100, TaxCents: 210}
	require.NoError(t, db.Create(&taxed).Error)
	// A fresh destination: GORM adds a populated struct's primary key to the
	// WHERE clause, so re-using readLine would query two ids at once.
	var readTaxed QuoteLine
	require.NoError(t, db.First(&readTaxed, taxed.ID).Error)
	assert.Equal(t, int64(2100), readTaxed.TaxRateBP)
	assert.Equal(t, int64(210), readTaxed.TaxCents)
}

// quoteUpdateCapture records every statement that updates the quotes table.
type quoteUpdateCapture struct {
	mu   sync.Mutex
	seen []string
}

var quoteUpdatePattern = regexp.MustCompile("(?i)^\\s*UPDATE\\s+[`\"]?quotes[`\"]?\\s")

func (c *quoteUpdateCapture) record(tx *gorm.DB) {
	sql := tx.Statement.SQL.String()
	if quoteUpdatePattern.MatchString(sql) {
		c.mu.Lock()
		c.seen = append(c.seen, sql)
		c.mu.Unlock()
	}
}

func (c *quoteUpdateCapture) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.seen
	c.seen = nil
	return out
}

// TestQuoteTaxSQLiteUpgrade: SQLite never runs the SQL migrations, so
// MigrateDatabase on a schema written before #324 adds the four columns NOT
// NULL DEFAULT 0 and backfills gross_total_cents = total_cents + tax_cents,
// soft-deleted quotes included, without touching updated_at; a second run
// issues no UPDATE of quotes at all.
func TestQuoteTaxSQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "quote-tax-upgrade.db"),
	}))
	require.NoError(t, MigrateDatabase())

	// Turn the schema back into its pre-#324 shape.
	for _, stmt := range []string{
		"ALTER TABLE quotes DROP COLUMN gross_total_cents",
		"ALTER TABLE quotes DROP COLUMN tax_cents",
		"ALTER TABLE quote_lines DROP COLUMN tax_cents",
		"ALTER TABLE quote_lines DROP COLUMN tax_rate_bp",
	} {
		require.NoError(t, DB.Exec(stmt).Error, stmt)
	}
	for table, names := range map[string][]string{"quotes": {"tax_cents", "gross_total_cents"}, "quote_lines": {"tax_rate_bp", "tax_cents"}} {
		for _, name := range names {
			require.False(t, DB.Migrator().HasColumn(table, name), "the fixture predates %s.%s", table, name)
		}
	}

	owner := User{Email: "quote-tax-upgrade@example.com", Password: "x", FirstName: "Q", LastName: "T", Role: RoleSales, IsActive: true}
	require.NoError(t, DB.Create(&owner).Error)
	product := Product{Name: "Widget", SKU: "W-1", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(t, DB.Create(&product).Error)

	stamp := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	deletedAt := stamp.Add(time.Hour)
	type legacyQuote struct {
		title   string
		status  QuoteStatus
		total   int64
		deleted *time.Time
	}
	legacy := []legacyQuote{
		{"Legacy draft", QuoteStatusDraft, 2997, nil},
		{"Legacy accepted", QuoteStatusAccepted, 875, nil},
		{"Legacy empty", QuoteStatusDraft, 0, nil},
		{"Legacy deleted", QuoteStatusSent, 1234, &deletedAt},
	}
	ids := make([]uint, 0, len(legacy))
	for _, q := range legacy {
		require.NoError(t, DB.Exec(
			"INSERT INTO quotes (created_at, updated_at, deleted_at, title, status, currency, subtotal_cents, discount_cents, total_cents, notes, owner_id) VALUES (?, ?, ?, ?, ?, 'EUR', ?, 0, ?, '', ?)",
			stamp, stamp, q.deleted, q.title, q.status, q.total, q.total, owner.ID,
		).Error)
		var id uint
		require.NoError(t, DB.Raw("SELECT id FROM quotes WHERE title = ?", q.title).Scan(&id).Error)
		ids = append(ids, id)
		require.NoError(t, DB.Exec(
			"INSERT INTO quote_lines (created_at, updated_at, quote_id, product_id, position, description, quantity, unit_price_cents, discount_bp, line_total_cents) VALUES (?, ?, ?, ?, 1, 'Widget', 1, ?, 0, ?)",
			stamp, stamp, id, product.ID, q.total, q.total,
		).Error)
	}

	capture := &quoteUpdateCapture{}
	require.NoError(t, DB.Callback().Raw().After("gorm:raw").Register("test:quote_tax_capture_raw", capture.record))
	require.NoError(t, DB.Callback().Update().After("gorm:update").Register("test:quote_tax_capture_update", capture.record))

	require.NoError(t, MigrateDatabase())
	assertQuoteTaxColumns(t, DB)
	backfills := capture.take()
	assert.Len(t, backfills, 1, "one chunked backfill UPDATE for the four legacy quotes: %v", backfills)

	var quotes []Quote
	require.NoError(t, DB.Unscoped().Where("id IN ?", ids).Order("id").Find(&quotes).Error)
	require.Len(t, quotes, len(legacy), "every legacy quote survives, the soft-deleted one included")
	for i, quote := range quotes {
		assert.Equal(t, legacy[i].total, quote.TotalCents, "quote %d keeps its total", quote.ID)
		assert.Zero(t, quote.TaxCents, "quote %d reads tax 0", quote.ID)
		assert.Equal(t, quote.TotalCents, quote.GrossTotalCents, "quote %d reads gross = total", quote.ID)
		assert.Truef(t, quote.UpdatedAt.Equal(stamp), "quote %d: updated_at is not touched (%s)", quote.ID, quote.UpdatedAt)
	}
	assert.True(t, quotes[3].DeletedAt.Valid, "the soft-deleted quote stays soft-deleted")

	var lines []QuoteLine
	require.NoError(t, DB.Where("quote_id IN ?", ids).Order("id").Find(&lines).Error)
	require.Len(t, lines, len(legacy))
	for _, line := range lines {
		assert.Zero(t, line.TaxRateBP, "line %d reads rate 0", line.ID)
		assert.Zero(t, line.TaxCents, "line %d reads tax 0", line.ID)
		assert.Truef(t, line.UpdatedAt.Equal(stamp), "line %d: updated_at is not touched", line.ID)
	}

	// A second run on the backfilled database writes nothing to quotes.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Empty(t, capture.take(), "the second MigrateDatabase issues no UPDATE of quotes")
	assert.Equal(t, schema, schemaSnapshot(t), "and leaves the schema alone")

	// A quote whose gross disagrees again (written by hand) is repaired once.
	require.NoError(t, DB.Exec("UPDATE quotes SET gross_total_cents = 0 WHERE id = ?", ids[0]).Error)
	capture.take()
	require.NoError(t, MigrateDatabase())
	assert.Len(t, capture.take(), 1)
	var repaired Quote
	require.NoError(t, DB.First(&repaired, ids[0]).Error)
	assert.Equal(t, repaired.TotalCents, repaired.GrossTotalCents)
}
