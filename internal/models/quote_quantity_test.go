package models

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// #343: quote line quantities are exact decimals with at most three
// fractional digits, from 0.001 to 1,000,000, carried as thousandths.

func TestQuoteQuantityParse(t *testing.T) {
	for _, tc := range []struct {
		literal string
		milli   int64
	}{
		{"1", 1000},
		{"2", 2000},
		{"1.5", 1500},
		{"0.25", 250},
		{"0.001", 1},
		{"1e-3", 1},
		{"1E-3", 1},
		{"1.5000", 1500},
		{"1000000", QuoteLineQuantityMilliMax},
		{"1000000.000", QuoteLineQuantityMilliMax},
		{"1e6", QuoteLineQuantityMilliMax},
		{"1E+6", QuoteLineQuantityMilliMax},
		{"999999.999", 999_999_999},
		{"2.50e1", 25_000},
		{"100e-2", 1000},
		{"0.0010", 1},
		{"1e03", 1_000_000},
	} {
		t.Run("accepts "+tc.literal, func(t *testing.T) {
			q, err := ParseQuoteQuantity(tc.literal)
			require.NoError(t, err)
			assert.Equal(t, tc.milli, q.Milli())

			var decoded QuoteQuantity
			require.NoError(t, json.Unmarshal([]byte(tc.literal), &decoded), "UnmarshalJSON agrees")
			assert.Equal(t, tc.milli, decoded.Milli())
		})
	}

	// A literal of exactly 64 bytes is still parsed; 65 bytes is refused
	// before any digit is converted, whatever its value.
	at64 := "1." + strings.Repeat("0", 62)
	require.Len(t, at64, 64)
	q, err := ParseQuoteQuantity(at64)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), q.Milli())

	for _, literal := range []string{
		"0", "-0", "0.0", "-0.000", "0e5", "-1", "-1.5", "-0.001",
		"0.0005", "1.0001", "0.0001", "1.00001",
		"1000000.001", "1000001", "1e7", "1.0000001e6", "1e030",
		"1e400", "1e-400", "1e31", "1e-31", "9e99999999999",
		"1." + strings.Repeat("0", 63),
		strings.Repeat("9", 65),
	} {
		t.Run("refuses "+truncate(literal), func(t *testing.T) {
			_, err := ParseQuoteQuantity(literal)
			require.Error(t, err)
			var quantityErr *QuoteQuantityError
			assert.Truef(t, errors.As(err, &quantityErr), "%q: want *QuoteQuantityError, got %T %v", literal, err, err)
			assert.Contains(t, err.Error(), QuoteQuantityBoundsMessage)

			var decoded QuoteQuantity
			err = json.Unmarshal([]byte(literal), &decoded)
			require.Error(t, err)
			assert.Truef(t, errors.As(err, &quantityErr), "%q through UnmarshalJSON: %T %v", literal, err, err)
		})
	}

	// Not a JSON number: an error, but never a QuoteQuantityError.
	for _, literal := range []string{
		`"1.5"`, `"2"`, "true", "false", "null", "[1]", "{}", `{"q":1}`, "",
		"abc", "+1", "01", "1.", ".5", "1e", "--1", "1.5x", "0x10",
		`"` + strings.Repeat("1", 70) + `"`,
	} {
		t.Run("not a number "+truncate(literal), func(t *testing.T) {
			_, err := ParseQuoteQuantity(literal)
			require.Error(t, err)
			var quantityErr *QuoteQuantityError
			assert.Falsef(t, errors.As(err, &quantityErr), "%q is not a number, not a bound failure", literal)
		})
	}

	// Through a struct field, as the handler binds it.
	type body struct {
		Quantity *QuoteQuantity `json:"quantity"`
	}
	for _, raw := range []string{`{"quantity":"1.5"}`, `{"quantity":true}`, `{"quantity":[1]}`, `{"quantity":{"a":1}}`} {
		var b body
		err := json.Unmarshal([]byte(raw), &b)
		require.Error(t, err, raw)
		var quantityErr *QuoteQuantityError
		assert.Falsef(t, errors.As(err, &quantityErr), "%s", raw)
	}
	for _, raw := range []string{`{"quantity":0}`, `{"quantity":1.0005}`, `{"quantity":1e400}`} {
		var b body
		err := json.Unmarshal([]byte(raw), &b)
		var quantityErr *QuoteQuantityError
		assert.Truef(t, errors.As(err, &quantityErr), "%s: %v", raw, err)
	}
	var absent, null body
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	assert.Nil(t, absent.Quantity)
	require.NoError(t, json.Unmarshal([]byte(`{"quantity":null}`), &null))
	assert.Nil(t, null.Quantity, "null leaves the pointer nil")
	var exact body
	require.NoError(t, json.Unmarshal([]byte(`{"quantity":1.5}`), &exact))
	require.NotNil(t, exact.Quantity)
	assert.Equal(t, int64(1500), exact.Quantity.Milli())

	// The unchecked constructors and the bounds.
	assert.Equal(t, int64(-1), QuoteQuantityFromMilli(-1).Milli())
	assert.Equal(t, int64(3000), QuoteQuantityFromUnits(3).Milli())
	assert.False(t, QuoteQuantityFromMilli(0).InBounds())
	assert.True(t, QuoteQuantityFromMilli(1).InBounds())
	assert.True(t, QuoteQuantityFromMilli(QuoteLineQuantityMilliMax).InBounds())
	assert.False(t, QuoteQuantityFromMilli(QuoteLineQuantityMilliMax+1).InBounds())
	assert.Equal(t, int64(1000), QuoteQuantityScale)
	assert.Equal(t, int64(1), QuoteLineQuantityMilliMin)
	assert.Equal(t, int64(1_000_000_000), QuoteLineQuantityMilliMax)
}

func truncate(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

func TestQuoteQuantityJSON(t *testing.T) {
	for _, tc := range []struct {
		milli int64
		text  string
	}{
		{1500, "1.5"},
		{2000, "2"},
		{1, "0.001"},
		{10, "0.01"},
		{250, "0.25"},
		{1010, "1.01"},
		{999_999_999, "999999.999"},
		{1_000_000_000, "1000000"},
		{0, "0"},
		{-1500, "-1.5"},
	} {
		q := QuoteQuantityFromMilli(tc.milli)
		encoded, err := json.Marshal(q)
		require.NoError(t, err)
		assert.Equal(t, tc.text, string(encoded), "%d thousandths", tc.milli)
		assert.Equal(t, tc.text, q.String())
	}

	// A whole number sent in round-trips byte-identical.
	for _, literal := range []string{"1", "2", "37", "999999", "1000000"} {
		var q QuoteQuantity
		require.NoError(t, json.Unmarshal([]byte(literal), &q))
		encoded, err := json.Marshal(q)
		require.NoError(t, err)
		assert.Equal(t, literal, string(encoded))
	}

	// A line and a quote carry the quantity as a bare number, never a string.
	line := QuoteLine{ID: 1, QuoteID: 2, Quantity: QuoteQuantityFromMilli(1500), UnitPriceCents: 999}
	encoded, err := json.Marshal(line)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	assert.Equal(t, "1.5", string(fields["quantity"]))
	_, legacy := fields["quantity_milli"]
	assert.False(t, legacy, "the JSON key is quantity")

	encoded, err = json.Marshal(Quote{BaseModel: BaseModel{ID: 2}, Lines: []QuoteLine{
		{ID: 1, Quantity: QuoteQuantityFromUnits(2)},
		{ID: 2, Quantity: QuoteQuantityFromMilli(1)},
	}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"quantity":2,`)
	assert.Contains(t, string(encoded), `"quantity":0.001,`)
	assert.NotContains(t, string(encoded), `"quantity":"`)

	// A line decodes the number back exactly.
	var decoded QuoteLine
	require.NoError(t, json.Unmarshal([]byte(`{"id":5,"quantity":2.5}`), &decoded))
	assert.Equal(t, int64(2500), decoded.Quantity.Milli())
}

func TestQuoteLineAmounts(t *testing.T) {
	// The worked cases.
	for _, tc := range []struct {
		name                string
		milli, price, bp    int64
		wantGross, wantLine int64
	}{
		{"1.5 x 9.99 at 12.5 percent", 1500, 999, 1250, 1499, 1311},
		{"0.005 x 1.00 rounds half up", 5, 100, 0, 1, 1},
		{"0.333 x 1.00", 333, 100, 0, 33, 33},
		{"0.004 x 1.00 rounds down", 4, 100, 0, 0, 0},
		{"0.25 x 3.33", 250, 333, 0, 83, 83},
		{"the line total is rounded once from the exact product", 1500, 999, 5000, 1499, 749},
		{"a full discount", 1500, 999, 10000, 1499, 0},
		{"free", 1500, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gross, lineTotal, ok := ComputeQuoteLineAmounts(QuoteQuantityFromMilli(tc.milli), tc.price, tc.bp)
			assert.True(t, ok)
			assert.Equal(t, tc.wantGross, gross, "gross")
			assert.Equal(t, tc.wantLine, lineTotal, "line total")
		})
	}

	// Parity with the integer formula for whole quantities.
	compared := 0
	for _, quantity := range []int64{1, 3, 7, 999_999, 1_000_000} {
		for _, price := range []int64{0, 1, 999, 1_000_000, 1_000_000_000_000} {
			for _, bp := range []int64{0, 1, 1250, 5000, 9999, 10000} {
				if price != 0 && quantity > QuoteAmountCentsMax/price {
					continue // gross above 10^12: outside the sample set
				}
				wantGross, wantLine := ComputeQuoteLineTotal(quantity, price, bp)
				gross, lineTotal, ok := ComputeQuoteLineAmounts(QuoteQuantityFromUnits(quantity), price, bp)
				require.Truef(t, ok, "%d x %d at %d", quantity, price, bp)
				assert.Equalf(t, wantGross, gross, "gross of %d x %d at %d", quantity, price, bp)
				assert.Equalf(t, wantLine, lineTotal, "line total of %d x %d at %d", quantity, price, bp)
				compared++
			}
		}
	}
	assert.Greater(t, compared, 100)

	// No overflow at the extremes, exact results.
	gross, lineTotal, ok := ComputeQuoteLineAmounts(QuoteQuantityFromMilli(QuoteLineQuantityMilliMax), QuoteAmountCentsMax, 0)
	assert.False(t, ok, "10^18 is above the cap")
	assert.Equal(t, int64(1_000_000_000_000_000_000), gross)
	assert.Equal(t, int64(1_000_000_000_000_000_000), lineTotal)
	gross, lineTotal, ok = ComputeQuoteLineAmounts(QuoteQuantityFromMilli(QuoteLineQuantityMilliMax), QuoteAmountCentsMax, 1250)
	assert.False(t, ok)
	assert.Equal(t, int64(1_000_000_000_000_000_000), gross)
	assert.Equal(t, int64(875_000_000_000_000_000), lineTotal)
	gross, lineTotal, ok = ComputeQuoteLineAmounts(QuoteQuantityFromMilli(QuoteLineQuantityMilliMax-1), QuoteAmountCentsMax-1, 9999)
	assert.False(t, ok)
	// (10^9-1)(10^12-1) = 999999998999000000001 thousandths of a cent.
	// Line total: that product × (10000-9999) / 10^7 = 99999999899900.0000001,
	// rounded half up to 99999999899900.
	assert.Equal(t, int64(999_999_998_999_000_000), gross)
	assert.Equal(t, int64(99_999_999_899_900), lineTotal)

	// The cap is on the rounded gross.
	_, _, ok = ComputeQuoteLineAmounts(QuoteQuantityFromUnits(1), QuoteAmountCentsMax, 0)
	assert.True(t, ok, "exactly 10^12")
	_, _, ok = ComputeQuoteLineAmounts(QuoteQuantityFromMilli(1001), QuoteAmountCentsMax, 0)
	assert.False(t, ok)
	gross, _, ok = ComputeQuoteLineAmounts(QuoteQuantityFromMilli(1500), 666_666_666_666, 0)
	assert.True(t, ok)
	assert.Equal(t, int64(999_999_999_999), gross)
	gross, _, ok = ComputeQuoteLineAmounts(QuoteQuantityFromMilli(1500), 666_666_666_667, 0)
	assert.False(t, ok, "10^12 + 0.5 rounds up to 10^12 + 1")
	assert.Equal(t, int64(1_000_000_000_001), gross)

	// Invalid inputs are reported, never computed through a wrap-around.
	for _, tc := range []struct {
		milli, price, bp int64
	}{{-1, 100, 0}, {1000, -1, 0}, {1000, 100, -1}, {1000, 100, 10001}, {1 << 62, 1 << 62, 0}} {
		_, _, ok := ComputeQuoteLineAmounts(QuoteQuantityFromMilli(tc.milli), tc.price, tc.bp)
		assert.Falsef(t, ok, "%v", tc)
	}

	// ComputeQuoteLineTotal keeps its signature and results.
	gross, lineTotal = ComputeQuoteLineTotal(3, 999, 1250)
	assert.Equal(t, int64(2997), gross)
	assert.Equal(t, int64(2622), lineTotal)

	// The quote totals sum the rounded line gross values and line totals.
	lines := []QuoteLine{
		{Quantity: QuoteQuantityFromMilli(1500), UnitPriceCents: 999, DiscountBP: 1250},
		{Quantity: QuoteQuantityFromMilli(5), UnitPriceCents: 100},
		{Quantity: QuoteQuantityFromMilli(333), UnitPriceCents: 100},
	}
	for i := range lines {
		_, lines[i].LineTotalCents, _ = ComputeQuoteLineAmounts(lines[i].Quantity, lines[i].UnitPriceCents, lines[i].DiscountBP)
	}
	subtotal, discount, total := ComputeQuoteTotals(lines)
	assert.Equal(t, int64(1499+1+33), subtotal)
	assert.Equal(t, int64(1311+1+33), total)
	assert.Equal(t, subtotal-total, discount)
	totals := SumQuoteTotals(lines)
	assert.Equal(t, subtotal, totals.Subtotal)
	assert.Equal(t, total, totals.Total)
}

// quoteLineWriteCapture records every executed statement (GORM's Raw
// callback, which Exec and the migrator's DDL go through) that writes the
// quote_lines table or its schema.
type quoteLineWriteCapture struct {
	mu   sync.Mutex
	seen []string
}

var quoteLineWritePattern = regexp.MustCompile("(?is)^\\s*(UPDATE|ALTER|CREATE|DROP|INSERT|DELETE)\\b.*quote_lines")

func (c *quoteLineWriteCapture) record(tx *gorm.DB) {
	sql := tx.Statement.SQL.String()
	if quoteLineWritePattern.MatchString(sql) {
		c.mu.Lock()
		c.seen = append(c.seen, sql)
		c.mu.Unlock()
	}
}

func (c *quoteLineWriteCapture) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.seen
	c.seen = nil
	return out
}

func quoteLineIndexNames(t *testing.T) []string {
	t.Helper()
	var names []string
	require.NoError(t, DB.Raw("SELECT name FROM pragma_index_list('quote_lines')").Scan(&names).Error)
	return names
}

func quoteLineForeignTables(t *testing.T) []string {
	t.Helper()
	var tables []string
	require.NoError(t, DB.Raw("SELECT \"table\" FROM pragma_foreign_key_list('quote_lines')").Scan(&tables).Error)
	return tables
}

type quoteQuantityUpgradeRow struct {
	ID             uint
	QuantityMilli  int64
	LineTotalCents int64
	TaxCents       int64
	UpdatedAt      time.Time
}

// TestQuoteQuantitySQLiteUpgrade: SQLite never runs the SQL migrations, so
// MigrateDatabase upgrades a quote_lines table that still has the whole-unit
// quantity: it backfills quantity_milli = quantity x 1000 in chunks and drops
// the column with the native ALTER TABLE ... DROP COLUMN, without a table
// rebuild, so the indexes and foreign keys survive. A second run issues no
// UPDATE and no DDL on quote_lines.
func TestQuoteQuantitySQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "quote-quantity-upgrade.db"),
	}))
	require.NoError(t, MigrateDatabase())
	assert.False(t, DB.Migrator().HasColumn("quote_lines", "quantity"), "a fresh schema has no legacy column")

	// Turn quote_lines back into its pre-#343 shape.
	for _, stmt := range []string{
		"ALTER TABLE quote_lines ADD COLUMN quantity bigint NOT NULL DEFAULT 0",
		"ALTER TABLE quote_lines DROP COLUMN quantity_milli",
	} {
		require.NoError(t, DB.Exec(stmt).Error, stmt)
	}
	require.True(t, DB.Migrator().HasColumn("quote_lines", "quantity"))
	require.False(t, DB.Migrator().HasColumn("quote_lines", "quantity_milli"))
	indexes := quoteLineIndexNames(t)
	require.Contains(t, indexes, "idx_quote_lines_quote_id")
	require.Contains(t, indexes, "idx_quote_lines_product_id")
	foreign := quoteLineForeignTables(t)
	require.Contains(t, foreign, "quotes")
	require.Contains(t, foreign, "products")

	owner := User{Email: "quote-quantity-upgrade@example.com", Password: "x", FirstName: "Q", LastName: "Q", Role: RoleSales, IsActive: true}
	require.NoError(t, DB.Create(&owner).Error)
	product := Product{Name: "Widget", SKU: "W-Q", UnitPriceCents: 999, Currency: "EUR", IsActive: true}
	require.NoError(t, DB.Create(&product).Error)
	stamp := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	require.NoError(t, DB.Exec(
		"INSERT INTO quotes (created_at, updated_at, title, status, currency, subtotal_cents, discount_cents, total_cents, tax_cents, gross_total_cents, notes, owner_id) VALUES (?, ?, 'Legacy', 'draft', 'EUR', 4321, 21, 4300, 903, 5203, '', ?)",
		stamp, stamp, owner.ID).Error)
	var quoteID uint
	require.NoError(t, DB.Raw("SELECT id FROM quotes WHERE title = 'Legacy'").Scan(&quoteID).Error)
	legacyQuantities := []int64{1, 3, QuoteLineQuantityMax}
	for i, quantity := range legacyQuantities {
		require.NoError(t, DB.Exec(
			"INSERT INTO quote_lines (created_at, updated_at, quote_id, product_id, position, description, quantity, unit_price_cents, discount_bp, line_total_cents, tax_rate_bp, tax_cents) VALUES (?, ?, ?, ?, ?, 'Widget', ?, 1, 0, ?, 2100, ?)",
			stamp, stamp, quoteID, product.ID, i+1, quantity, quantity, 7+i).Error)
	}
	// 520 more lines, so the conversion takes two chunks.
	require.NoError(t, DB.Exec(
		"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 520) "+
			"INSERT INTO quote_lines (created_at, updated_at, quote_id, product_id, position, description, quantity, unit_price_cents, discount_bp, line_total_cents, tax_rate_bp, tax_cents) "+
			"SELECT ?, ?, ?, ?, i + 3, 'Bulk', 2, 100, 0, 200, 0, 0 FROM n",
		stamp, stamp, quoteID, product.ID).Error)

	readLines := func() []quoteQuantityUpgradeRow {
		var rows []quoteQuantityUpgradeRow
		require.NoError(t, DB.Raw("SELECT id, quantity_milli, line_total_cents, tax_cents, updated_at FROM quote_lines ORDER BY id").Scan(&rows).Error)
		return rows
	}
	var linesBefore []quoteQuantityUpgradeRow
	require.NoError(t, DB.Raw("SELECT id, quantity * 1000 AS quantity_milli, line_total_cents, tax_cents, updated_at FROM quote_lines ORDER BY id").Scan(&linesBefore).Error)
	require.Len(t, linesBefore, 523)
	var quoteBefore Quote
	require.NoError(t, DB.First(&quoteBefore, quoteID).Error)

	capture := &quoteLineWriteCapture{}
	require.NoError(t, DB.Callback().Raw().After("gorm:raw").Register("test:quote_quantity_capture_raw", capture.record))
	require.NoError(t, DB.Callback().Update().After("gorm:update").Register("test:quote_quantity_capture_update", capture.record))

	require.NotPanics(t, func() { require.NoError(t, MigrateDatabase()) })
	first := capture.take()
	var drops, updates int
	for _, stmt := range first {
		assert.NotContainsf(t, stmt, "quote_lines__temp", "no table rebuild: %s", stmt)
		assert.Falsef(t, regexp.MustCompile("(?i)^\\s*(DROP\\s+TABLE|CREATE\\s+TABLE)").MatchString(stmt), "no table rebuild: %s", stmt)
		if strings.TrimSpace(stmt) == "ALTER TABLE quote_lines DROP COLUMN quantity" {
			drops++
		}
		if regexp.MustCompile("(?i)^\\s*UPDATE\\s+quote_lines\\s+SET\\s+quantity_milli").MatchString(stmt) {
			updates++
		}
	}
	assert.Equal(t, 1, drops, "the native drop runs once: %v", first)
	assert.Equal(t, 2, updates, "523 rows convert in two chunks of at most 500")

	assert.False(t, DB.Migrator().HasColumn("quote_lines", "quantity"), "the legacy column is gone")
	assert.True(t, DB.Migrator().HasColumn("quote_lines", "quantity_milli"))
	indexes = quoteLineIndexNames(t)
	assert.Contains(t, indexes, "idx_quote_lines_quote_id")
	assert.Contains(t, indexes, "idx_quote_lines_product_id")
	foreign = quoteLineForeignTables(t)
	assert.Contains(t, foreign, "quotes")
	assert.Contains(t, foreign, "products")

	linesAfter := readLines()
	require.Len(t, linesAfter, len(linesBefore))
	for i := range linesAfter {
		assert.Equal(t, linesBefore[i].QuantityMilli, linesAfter[i].QuantityMilli, "line %d: quantity x 1000", linesAfter[i].ID)
		assert.Equal(t, linesBefore[i].LineTotalCents, linesAfter[i].LineTotalCents)
		assert.Equal(t, linesBefore[i].TaxCents, linesAfter[i].TaxCents)
		assert.Truef(t, linesAfter[i].UpdatedAt.Equal(stamp), "line %d: updated_at is not touched (%s)", linesAfter[i].ID, linesAfter[i].UpdatedAt)
	}
	assert.Equal(t, int64(1000), linesAfter[0].QuantityMilli)
	assert.Equal(t, int64(3000), linesAfter[1].QuantityMilli)
	assert.Equal(t, QuoteLineQuantityMilliMax, linesAfter[2].QuantityMilli)

	var quoteAfter Quote
	require.NoError(t, DB.First(&quoteAfter, quoteID).Error)
	assert.Equal(t, quoteBefore.SubtotalCents, quoteAfter.SubtotalCents)
	assert.Equal(t, quoteBefore.DiscountCents, quoteAfter.DiscountCents)
	assert.Equal(t, quoteBefore.TotalCents, quoteAfter.TotalCents)
	assert.Equal(t, quoteBefore.TaxCents, quoteAfter.TaxCents)
	assert.Equal(t, quoteBefore.GrossTotalCents, quoteAfter.GrossTotalCents)
	assert.True(t, quoteAfter.UpdatedAt.Equal(quoteBefore.UpdatedAt))
	var second QuoteLine
	require.NoError(t, DB.First(&second, linesAfter[1].ID).Error)
	assert.Equal(t, QuoteQuantityFromUnits(3), second.Quantity, "the model reads the converted value")

	// A second run issues nothing on quote_lines and leaves the schema alone.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Empty(t, capture.take(), "the second MigrateDatabase issues no UPDATE and no DDL on quote_lines")
	assert.Equal(t, schema, schemaSnapshot(t))
}
