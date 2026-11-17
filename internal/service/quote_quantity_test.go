package service

// Regression roots for fractional quote line quantities (#343). They run the
// real service over the real repositories on in-memory SQLite with the
// production one-connection pool and, for the *MySQL root, on a disposable
// TEST_MYSQL_DSN schema (skipped without the variable, failing when it is
// unreachable). Every assertion reads committed rows outside any transaction.

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

func milliPtr(milli int64) *models.QuoteQuantity {
	q := models.QuoteQuantityFromMilli(milli)
	return &q
}

// AC-6: a quantity outside 1..10^9 thousandths, built without validation, is
// refused by AddLine and UpdateLine with ErrValidation before any transaction
// starts; a merged line whose rounded gross exceeds 10^12 is refused inside
// the transaction. Neither changes a line, a total or a history row.
func TestQuoteFractionalQuantityBoundsRejectedWithoutWrites(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	counter := &atomic.Int64{}
	svc := env.service(&txCountingQuoteRepository{QuoteRepository: env.repo, count: counter})

	quote := env.newQuote(models.QuoteStatusDraft, 1)
	lineID := quote.Lines[0].ID
	before := env.snapshot(quote.ID)
	historyBefore := len(env.history(quote.ID))

	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "want a validation failure, got %v", err)
		assert.Falsef(t, errors.Is(err, apperrors.ErrQuoteLocked), "a bound failure is not a lock: %v", err)
	}

	for _, milli := range []int64{0, -1, models.QuoteLineQuantityMilliMax + 1, -models.QuoteLineQuantityMilliMax} {
		counter.Store(0)
		_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(milli)})
		refused(t, err)
		assert.Zerof(t, counter.Load(), "add %d: no transaction started", milli)

		_, err = svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{Quantity: milliPtr(milli)})
		refused(t, err)
		assert.Zerof(t, counter.Load(), "update %d: no transaction started", milli)
		assert.Equal(t, before, env.snapshot(quote.ID), "%d: no row of quotes or quote_lines changes", milli)
	}

	// The bounds themselves are fine.
	for _, milli := range []int64{models.QuoteLineQuantityMilliMin, models.QuoteLineQuantityMilliMax} {
		probe := env.newQuote(models.QuoteStatusDraft, 0)
		_, err := svc.AddLine(probe.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(milli), UnitPriceCents: int64Ptr(1)})
		require.NoErrorf(t, err, "%d thousandths", milli)
		assert.Equal(t, milli, env.lines(probe.ID)[0].Quantity.Milli())
		env.assertTotalsMatchLines(probe.ID)
	}

	// A rounded gross above 10^12: 1.5 x 666666666667 is 10^12 + 0.5, which
	// rounds to 10^12 + 1. The add and the merged update are both refused.
	_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: int64Ptr(666_666_666_667)})
	refused(t, err)
	assert.Equal(t, before, env.snapshot(quote.ID))
	_, err = svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{Quantity: milliPtr(1000), UnitPriceCents: int64Ptr(666_666_666_667)})
	require.NoError(t, err, "1 x 666666666667 is within the cap")
	afterPrice := env.snapshot(quote.ID)
	_, err = svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{Quantity: milliPtr(1500)})
	refused(t, err)
	assert.Equal(t, afterPrice, env.snapshot(quote.ID), "the merged line is refused and nothing changes")
	// One thousandth less fits: 1.499 x 666666666667 = 999333333333.833.
	_, err = svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{Quantity: milliPtr(1499)})
	require.NoError(t, err)
	line := env.lines(quote.ID)[0]
	assert.Equal(t, int64(999_333_333_334), line.LineTotalCents)
	env.assertTotalsMatchLines(quote.ID)

	assert.Len(t, env.history(quote.ID), historyBefore, "no line write appends history")
}

// AC-7: fractional lines store the exact quantity_milli, the half-up line
// total and tax, and quote totals that are the sums of the rounded line
// values; a gross total above 10^12 rolls the whole write back.
func TestQuoteFractionalQuantityTotalsSQLite(t *testing.T) {
	runQuoteFractionalQuantityTotals(t, openQuoteTestSQLite(t))
}

func TestQuoteFractionalQuantityTotalsMySQL(t *testing.T) {
	runQuoteFractionalQuantityTotals(t, openQuoteTestMySQL(t, 2))
}

func storedQuantityMilli(t *testing.T, db *gorm.DB, quoteID uint) []int64 {
	t.Helper()
	var values []int64
	require.NoError(t, db.Raw("SELECT quantity_milli FROM quote_lines WHERE quote_id = ? ORDER BY id", quoteID).Scan(&values).Error)
	return values
}

func runQuoteFractionalQuantityTotals(t *testing.T, db *gorm.DB) {
	env := newQuoteTestEnv(t, db)
	quote := env.newQuote(models.QuoteStatusDraft, 0)
	assert.False(t, db.Migrator().HasColumn("quote_lines", "quantity"), "no legacy column")

	// 1.5 x 9.99 at 12.5 percent off, 21 percent tax.
	created, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: int64Ptr(999), DiscountBP: 1250, TaxRateBP: 2100})
	require.NoError(t, err)
	require.Len(t, created.Lines, 1)
	assert.Equal(t, int64(1500), created.Lines[0].Quantity.Milli(), "the response carries the exact quantity")
	// 0.25 x 3.33 and 0.005 x 1.00, untaxed.
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(250), UnitPriceCents: int64Ptr(333)})
	require.NoError(t, err)
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(5), UnitPriceCents: int64Ptr(100)})
	require.NoError(t, err)

	assert.Equal(t, []int64{1500, 250, 5}, storedQuantityMilli(t, db, quote.ID))
	lines := env.lines(quote.ID)
	require.Len(t, lines, 3)
	assert.Equal(t, int64(1311), lines[0].LineTotalCents, "1.5 x 999 x 0.875 = 1311.1875")
	assert.Equal(t, int64(275), lines[0].TaxCents, "1311 x 21 percent = 275.31")
	assert.Equal(t, int64(83), lines[1].LineTotalCents, "0.25 x 333 = 83.25")
	assert.Equal(t, int64(1), lines[2].LineTotalCents, "0.005 x 100 = 0.5 rounds up")
	committed := env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(1499+83+1), committed.SubtotalCents, "the sum of the rounded line gross values")
	assert.Equal(t, int64(1311+83+1), committed.TotalCents)
	assert.Equal(t, int64(1583-1395), committed.DiscountCents)
	assert.Equal(t, int64(275), committed.TaxCents)
	assert.Equal(t, int64(1395+275), committed.GrossTotalCents)

	// Edit the first line to 2.
	_, err = env.svc.UpdateLine(quote.ID, lines[0].ID, models.QuoteLinePatch{Quantity: milliPtr(2000)})
	require.NoError(t, err)
	assert.Equal(t, []int64{2000, 250, 5}, storedQuantityMilli(t, db, quote.ID))
	edited := env.lines(quote.ID)[0]
	assert.Equal(t, int64(1748), edited.LineTotalCents, "2 x 999 x 0.875 = 1748.25")
	assert.Equal(t, int64(367), edited.TaxCents)
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(1998+83+1), committed.SubtotalCents)
	assert.Equal(t, int64(1748+83+1), committed.TotalCents)
	assert.Equal(t, int64(367), committed.TaxCents)
	assert.Equal(t, int64(1832+367), committed.GrossTotalCents)

	// The exact boundary values survive the round trip.
	_, err = env.svc.UpdateLine(quote.ID, lines[2].ID, models.QuoteLinePatch{Quantity: milliPtr(models.QuoteLineQuantityMilliMax - 1)})
	require.NoError(t, err)
	assert.Equal(t, []int64{2000, 250, models.QuoteLineQuantityMilliMax - 1}, storedQuantityMilli(t, db, quote.ID))
	assert.Equal(t, int64(100_000_000), env.lines(quote.ID)[2].LineTotalCents, "999999.999 x 1.00 = 99999999.9 cents, rounded half up")
	env.assertTotalsMatchLines(quote.ID)

	// A gross total above 10^12 rolls the whole write back: this line's own
	// gross (999999999999) is within the cap, the quote's is not.
	atCap := env.snapshot(quote.ID)
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: int64Ptr(666_666_666_666)})
	require.Error(t, err)
	assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "%v", err)
	assert.Equal(t, atCap, env.snapshot(quote.ID), "no line was inserted and no total changed")
	assert.Len(t, env.lines(quote.ID), 3)
	_, err = env.svc.UpdateLine(quote.ID, lines[1].ID, models.QuoteLinePatch{Quantity: milliPtr(1500), UnitPriceCents: int64Ptr(666_666_666_666)})
	require.Error(t, err)
	assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "%v", err)
	assert.Equal(t, atCap, env.snapshot(quote.ID), "the edit is rolled back")
}

// AC-8: the email prints the quantity without trailing zeros; a whole
// quantity renders exactly as before.
func TestQuoteEmailFractionalQuantity(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	env.mailer.reset(nil, nil)

	quote := env.newQuote(models.QuoteStatusDraft, 0)
	_, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: int64Ptr(999), DiscountBP: 1250, TaxRateBP: 2100})
	require.NoError(t, err)
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromMilli(1)})
	require.NoError(t, err)
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(3), UnitPriceCents: int64Ptr(999)})
	require.NoError(t, err)
	_, err = env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
	require.NoError(t, err)

	_, err = env.svc.Email(quote.ID)
	require.NoError(t, err)
	messages := env.mailer.messages()
	require.Len(t, messages, 1)
	body := messages[0].Body
	assert.Equal(t, RenderQuoteEmail(env.quote(quote.ID)), body)
	bodyLines := strings.Split(body, "\n")
	assert.Contains(t, bodyLines, "1. Widget | quantity 1.5 x 9.99 EUR | discount 12.50% | line total 13.11 EUR | tax 21.00% 2.75 EUR")
	assert.Contains(t, bodyLines, "2. Widget | quantity 0.001 x 10.00 EUR | discount 0.00% | line total 0.01 EUR | tax 0.00% 0.00 EUR")
	assert.Contains(t, bodyLines, "3. Widget | quantity 3 x 9.99 EUR | discount 0.00% | line total 29.97 EUR | tax 0.00% 0.00 EUR",
		"a whole quantity renders as before")
	assert.NotContains(t, body, "1.500")

	rendered := RenderQuoteEmail(&models.Quote{
		BaseModel: models.BaseModel{ID: 7}, Title: "T", Currency: "EUR",
		Lines: []models.QuoteLine{
			{Description: "A", Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: 999, LineTotalCents: 1499},
			{Description: "B", Quantity: models.QuoteQuantityFromMilli(1), UnitPriceCents: 1000, LineTotalCents: 1},
			{Description: "C", Quantity: models.QuoteQuantityFromUnits(3), UnitPriceCents: 999, LineTotalCents: 2997},
		},
	})
	assert.Contains(t, rendered, "1. A | quantity 1.5 x 9.99 EUR |")
	assert.Contains(t, rendered, "2. B | quantity 0.001 x 10.00 EUR |")
	assert.Contains(t, rendered, "3. C | quantity 3 x 9.99 EUR |")
}
