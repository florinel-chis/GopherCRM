package repository

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// #343: the repository stores a line quantity as integer thousandths in
// quote_lines.quantity_milli and reads it back exactly, on SQLite and on a
// disposable TEST_MYSQL_DSN schema (skipped without the variable, failing
// when it is unreachable). A freshly migrated schema has no legacy quantity.

func TestQuoteLineQuantityStorageSQLite(t *testing.T) {
	runQuoteLineQuantityStorage(t, openQuoteTaxRepoDB(t, false))
}

func TestQuoteLineQuantityStorageMySQL(t *testing.T) {
	runQuoteLineQuantityStorage(t, openQuoteTaxRepoDB(t, true))
}

func runQuoteLineQuantityStorage(t *testing.T, db *gorm.DB) {
	assert.True(t, db.Migrator().HasColumn("quote_lines", "quantity_milli"))
	assert.False(t, db.Migrator().HasColumn("quote_lines", "quantity"), "a fresh schema has no legacy column")

	repo := NewQuoteRepository(db)
	quote := &models.Quote{Title: "Quantities", Status: models.QuoteStatusDraft, Currency: "EUR", OwnerID: 1}
	require.NoError(t, repo.Create(quote))

	values := []int64{1, 1500, 999_999_999, models.QuoteLineQuantityMilliMax}
	texts := []string{"0.001", "1.5", "999999.999", "1000000"}
	ids := make([]uint, 0, len(values))
	for i, milli := range values {
		line := &models.QuoteLine{QuoteID: quote.ID, ProductID: 1, Position: int64(i + 1), Description: "Widget",
			Quantity: models.QuoteQuantityFromMilli(milli), UnitPriceCents: 1}
		_, line.LineTotalCents, _ = models.ComputeQuoteLineAmounts(line.Quantity, line.UnitPriceCents, 0)
		require.NoError(t, repo.CreateLine(line))
		ids = append(ids, line.ID)
	}

	var raw []int64
	require.NoError(t, db.Raw("SELECT quantity_milli FROM quote_lines WHERE quote_id = ? ORDER BY id", quote.ID).Scan(&raw).Error)
	assert.Equal(t, values, raw, "the column holds the exact thousandths")

	lines, err := repo.ListLines(quote.ID)
	require.NoError(t, err)
	require.Len(t, lines, len(values))
	for i, line := range lines {
		assert.Equal(t, values[i], line.Quantity.Milli())
		assert.Equal(t, texts[i], line.Quantity.String())
		encoded, err := json.Marshal(line)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"quantity":`+texts[i]+`,`)
	}
	for i, id := range ids {
		line, err := repo.GetLine(quote.ID, id)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteQuantityFromMilli(values[i]), line.Quantity)
	}

	// The full-row save writes a new quantity exactly.
	line, err := repo.GetLine(quote.ID, ids[0])
	require.NoError(t, err)
	line.Quantity = models.QuoteQuantityFromMilli(250)
	require.NoError(t, repo.UpdateLine(line))
	stored, err := repo.GetLine(quote.ID, ids[0])
	require.NoError(t, err)
	assert.Equal(t, int64(250), stored.Quantity.Milli())
	assert.Equal(t, "0.25", stored.Quantity.String())

	assert.False(t, db.Migrator().HasColumn("quote_lines", "quantity"), "still no legacy column")
}
