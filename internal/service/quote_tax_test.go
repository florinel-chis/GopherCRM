package service

// Regression roots for the per-line tax rate and the tax and gross totals of
// a quote (#324). They run the real service over the real repositories on
// in-memory SQLite with the production one-connection pool and, for the
// *MySQL roots, on a disposable TEST_MYSQL_DSN schema (skipped without the
// variable, failing when it is unreachable). Every assertion reads committed
// rows outside any transaction.

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
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// txCountingQuoteRepository counts how often the service binds the quote
// repository to a transaction: WithTx is called exactly once per transaction
// the service starts, so zero means no transaction began.
type txCountingQuoteRepository struct {
	repository.QuoteRepository
	count *atomic.Int64
}

func (r *txCountingQuoteRepository) WithTx(tx *gorm.DB) repository.QuoteRepository {
	r.count.Add(1)
	return r.QuoteRepository.WithTx(tx)
}

// AC-2: a tax_rate_bp outside 0..10000 is refused on AddLine and UpdateLine
// with ErrValidation before any transaction starts, on a draft and on a sent
// quote alike; nothing of the quote, its lines or its totals changes.
func TestQuoteTaxRateBoundsRejectedWithoutWrites(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	counter := &atomic.Int64{}
	svc := env.service(&txCountingQuoteRepository{QuoteRepository: env.repo, count: counter})

	for _, status := range []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusSent} {
		quote := env.newQuote(status, 1)
		lineID := quote.Lines[0].ID
		before := env.snapshot(quote.ID)
		for _, rate := range []int64{-1, models.QuoteTaxRateBPMax + 1, -10000, 1_000_000} {
			counter.Store(0)
			_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), TaxRateBP: rate})
			require.Error(t, err)
			assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "%s add %d: %v", status, rate, err)
			assert.Falsef(t, errors.Is(err, apperrors.ErrQuoteLocked), "%s add %d: a bound failure is not a lock", status, rate)
			assert.Zerof(t, counter.Load(), "%s add %d: no transaction started", status, rate)

			_, err = svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{TaxRateBP: int64Ptr(rate)})
			require.Error(t, err)
			assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "%s update %d: %v", status, rate, err)
			assert.Zerof(t, counter.Load(), "%s update %d: no transaction started", status, rate)
			assert.Equal(t, before, env.snapshot(quote.ID), "%s %d: no row of quotes or quote_lines changes", status, rate)
		}
	}

	// The bounds themselves are accepted on a draft, and they did start a
	// transaction: the counter does see one.
	quote := env.newQuote(models.QuoteStatusDraft, 0)
	counter.Store(0)
	_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), TaxRateBP: models.QuoteTaxRateBPMax})
	require.NoError(t, err)
	assert.EqualValues(t, 1, counter.Load())
	line := env.lines(quote.ID)[0]
	assert.Equal(t, int64(10000), line.TaxRateBP)
	assert.Equal(t, line.LineTotalCents, line.TaxCents, "100 percent tax is the line total")
	_, err = svc.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(0)})
	require.NoError(t, err)
	env.assertTotalsMatchLines(quote.ID)
}

// AC-4: the stored line tax_rate_bp and tax_cents and the quote's tax_cents
// and gross_total_cents follow every line write: an add, a patch of the rate
// alone, a rate set back to 0, a patch of another field, and a delete.
func TestQuoteLineTaxRoundingSQLite(t *testing.T) {
	runQuoteLineTaxRounding(t, openQuoteTestSQLite(t))
}

func TestQuoteLineTaxRoundingMySQL(t *testing.T) {
	runQuoteLineTaxRounding(t, openQuoteTestMySQL(t, 2))
}

func runQuoteLineTaxRounding(t *testing.T, db *gorm.DB) {
	env := newQuoteTestEnv(t, db)
	quote := env.newQuote(models.QuoteStatusDraft, 0)

	// quantity 3, unit 9.99, 12.5 percent off, 21 percent tax.
	created, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(3), UnitPriceCents: int64Ptr(999), DiscountBP: 1250, TaxRateBP: 2100})
	require.NoError(t, err)
	assert.Equal(t, int64(551), created.TaxCents, "the response carries the recomputed tax")
	assert.Equal(t, int64(3173), created.GrossTotalCents)
	lines := env.lines(quote.ID)
	require.Len(t, lines, 1)
	first := lines[0]
	assert.Equal(t, int64(2622), first.LineTotalCents)
	assert.Equal(t, int64(2100), first.TaxRateBP)
	assert.Equal(t, int64(551), first.TaxCents)
	committed := env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(2997), committed.SubtotalCents)
	assert.Equal(t, int64(375), committed.DiscountCents)
	assert.Equal(t, int64(2622), committed.TotalCents)
	assert.Equal(t, int64(551), committed.TaxCents)
	assert.Equal(t, int64(3173), committed.GrossTotalCents)

	// Two lines of 0.01 at 50 percent: each rounds up on its own.
	for i := 0; i < 2; i++ {
		_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(1), TaxRateBP: 5000})
		require.NoError(t, err)
	}
	lines = env.lines(quote.ID)
	require.Len(t, lines, 3)
	assert.Equal(t, int64(1), lines[1].TaxCents)
	assert.Equal(t, int64(1), lines[2].TaxCents)
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(2624), committed.TotalCents)
	assert.Equal(t, int64(553), committed.TaxCents, "per-line rounding: 551 + 1 + 1")
	assert.Equal(t, int64(3177), committed.GrossTotalCents)

	// A patch of the rate alone recomputes the line and the totals.
	_, err = env.svc.UpdateLine(quote.ID, first.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(1500)})
	require.NoError(t, err)
	patched := env.lines(quote.ID)[0]
	assert.Equal(t, int64(1500), patched.TaxRateBP)
	assert.Equal(t, int64(393), patched.TaxCents, "(2622 x 1500 + 5000) / 10000")
	assert.Equal(t, int64(2622), patched.LineTotalCents, "the net line total is unchanged")
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(395), committed.TaxCents)
	assert.Equal(t, int64(2624+395), committed.GrossTotalCents)

	// Back to 0: the explicit zero is stored.
	_, err = env.svc.UpdateLine(quote.ID, first.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(0)})
	require.NoError(t, err)
	patched = env.lines(quote.ID)[0]
	assert.Zero(t, patched.TaxRateBP)
	assert.Zero(t, patched.TaxCents)
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(2), committed.TaxCents)
	assert.Equal(t, int64(2626), committed.GrossTotalCents)

	// A patch without the rate keeps it and recomputes the tax on the new
	// line total.
	_, err = env.svc.UpdateLine(quote.ID, first.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(2100)})
	require.NoError(t, err)
	_, err = env.svc.UpdateLine(quote.ID, first.ID, models.QuoteLinePatch{Quantity: quantityPtr(1)})
	require.NoError(t, err)
	patched = env.lines(quote.ID)[0]
	assert.Equal(t, int64(2100), patched.TaxRateBP, "an absent rate keeps the stored one")
	assert.Equal(t, int64(874), patched.LineTotalCents)
	assert.Equal(t, models.ComputeQuoteLineTax(874, 2100), patched.TaxCents)
	assert.Equal(t, int64(184), patched.TaxCents)
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(186), committed.TaxCents)

	// A delete recomputes without the line.
	_, err = env.svc.DeleteLine(quote.ID, lines[2].ID)
	require.NoError(t, err)
	committed = env.assertTotalsMatchLines(quote.ID)
	assert.Equal(t, int64(185), committed.TaxCents)
	assert.Equal(t, int64(875), committed.TotalCents)
	assert.Equal(t, int64(875+185), committed.GrossTotalCents)

	var sum int64
	for _, line := range env.lines(quote.ID) {
		sum += line.LineTotalCents
	}
	assert.Equal(t, sum, committed.TotalCents, "total_cents is still the sum of the line totals")
}

// AC-5: the 10^12 cap is on the gross total. Exactly 10^12 commits; one cent
// over is ErrValidation and leaves the quote row and every line row as they
// were, whether the write is an add, a price patch or a patch of the rate.
func TestQuoteGrossCapRefusalWritesNothingSQLite(t *testing.T) {
	runQuoteGrossCapRefusal(t, openQuoteTestSQLite(t))
}

func TestQuoteGrossCapRefusalWritesNothingMySQL(t *testing.T) {
	runQuoteGrossCapRefusal(t, openQuoteTestMySQL(t, 2))
}

func runQuoteGrossCapRefusal(t *testing.T, db *gorm.DB) {
	env := newQuoteTestEnv(t, db)
	const unit = int64(800_000_000_000)

	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "want a validation failure, got %v", err)
		assert.Falsef(t, errors.Is(err, apperrors.ErrQuoteLocked), "a cap failure is not a lock: %v", err)
	}

	// Exactly at the cap: 8x10^11 net plus 2x10^11 tax.
	capped := env.newQuote(models.QuoteStatusDraft, 0)
	_, err := env.svc.AddLine(capped.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(unit), TaxRateBP: 2500})
	require.NoError(t, err, "a gross total of exactly 10^12 commits")
	committed := env.assertTotalsMatchLines(capped.ID)
	assert.Equal(t, unit, committed.TotalCents)
	assert.Equal(t, int64(200_000_000_000), committed.TaxCents)
	assert.Equal(t, models.QuoteAmountCentsMax, committed.GrossTotalCents)
	line := env.lines(capped.ID)[0]
	atCap := env.snapshot(capped.ID)

	// One cent more on the same line: the tax rounds to the same 2x10^11,
	// the gross is 10^12 + 1.
	require.Equal(t, int64(200_000_000_000), models.ComputeQuoteLineTax(unit+1, 2500))
	_, err = env.svc.UpdateLine(capped.ID, line.ID, models.QuoteLinePatch{UnitPriceCents: int64Ptr(unit + 1)})
	refused(t, err)
	assert.Equal(t, atCap, env.snapshot(capped.ID), "the price patch wrote nothing")

	// A rate patch alone over the cap is refused the same way.
	_, err = env.svc.UpdateLine(capped.ID, line.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(2501)})
	refused(t, err)
	assert.Equal(t, atCap, env.snapshot(capped.ID), "the rate patch wrote nothing")

	// Another line, however small, is refused once the gross is at the cap.
	_, err = env.svc.AddLine(capped.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(1)})
	refused(t, err)
	assert.Equal(t, atCap, env.snapshot(capped.ID), "no line was inserted")
	assert.Len(t, env.lines(capped.ID), 1)

	// The same add with 8x10^11 + 1 on a fresh quote: nothing is inserted.
	fresh := env.newQuote(models.QuoteStatusDraft, 0)
	freshBefore := env.snapshot(fresh.ID)
	_, err = env.svc.AddLine(fresh.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(unit + 1), TaxRateBP: 2500})
	refused(t, err)
	assert.Equal(t, freshBefore, env.snapshot(fresh.ID))
	assert.Empty(t, env.lines(fresh.ID))
	freshQuote := env.quote(fresh.ID)
	assert.Zero(t, freshQuote.TotalCents)
	assert.Zero(t, freshQuote.TaxCents)
	assert.Zero(t, freshQuote.GrossTotalCents)

	// The net total alone at the cap is fine while untaxed.
	untaxed := env.newQuote(models.QuoteStatusDraft, 0)
	_, err = env.svc.AddLine(untaxed.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax)})
	require.NoError(t, err)
	assert.Equal(t, models.QuoteAmountCentsMax, env.assertTotalsMatchLines(untaxed.ID).GrossTotalCents)
	untaxedLine := env.lines(untaxed.ID)[0]
	untaxedBefore := env.snapshot(untaxed.ID)
	_, err = env.svc.UpdateLine(untaxed.ID, untaxedLine.ID, models.QuoteLinePatch{TaxRateBP: int64Ptr(1)})
	refused(t, err)
	assert.Equal(t, untaxedBefore, env.snapshot(untaxed.ID), "taxing a net total at the cap is refused and writes nothing")
}

// AC-6: on a quote in sent, accepted, rejected or expired status, an add
// with a tax rate and a patch of the rate alone are ErrQuoteLocked and write
// nothing.
func TestQuoteTaxRateNonDraftRefusedSQLite(t *testing.T) {
	runQuoteTaxRateNonDraftRefused(t, openQuoteTestSQLite(t))
}

func TestQuoteTaxRateNonDraftRefusedMySQL(t *testing.T) {
	runQuoteTaxRateNonDraftRefused(t, openQuoteTestMySQL(t, 2))
}

func runQuoteTaxRateNonDraftRefused(t *testing.T, db *gorm.DB) {
	env := newQuoteTestEnv(t, db)
	for _, status := range []models.QuoteStatus{models.QuoteStatusSent, models.QuoteStatusAccepted, models.QuoteStatusRejected, models.QuoteStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			quote := env.newQuote(status, 1)
			lineID := quote.Lines[0].ID
			before := env.snapshot(quote.ID)

			_, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), TaxRateBP: 2100})
			require.Error(t, err)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteLocked), "add on %s: %v", status, err)
			assert.Equal(t, before, env.snapshot(quote.ID), "the add on a %s quote wrote nothing", status)

			_, err = env.svc.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{TaxRateBP: int64Ptr(2100)})
			require.Error(t, err)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteLocked), "rate patch on %s: %v", status, err)
			assert.Equal(t, before, env.snapshot(quote.ID), "the rate patch on a %s quote wrote nothing", status)
			assert.Zero(t, env.lines(quote.ID)[0].TaxRateBP)
			assert.Len(t, env.lines(quote.ID), 1)
		})
	}
}

// AC-12: the plaintext email ends every line with its tax rate and tax, also
// at 0 percent, and the totals block reads Subtotal, Discount, Net total, Tax
// and Gross total, in that order, with the currency.
func TestQuoteEmailShowsTax(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	env.mailer.reset(nil, nil)

	quote := env.newQuote(models.QuoteStatusDraft, 0)
	_, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(3), UnitPriceCents: int64Ptr(999), DiscountBP: 1250, TaxRateBP: 2100})
	require.NoError(t, err)
	_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(999)})
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
	assert.Contains(t, bodyLines, "1. Widget | quantity 3 x 9.99 EUR | discount 12.50% | line total 26.22 EUR | tax 21.00% 5.51 EUR")
	assert.Contains(t, bodyLines, "2. Widget | quantity 1 x 9.99 EUR | discount 0.00% | line total 9.99 EUR | tax 0.00% 0.00 EUR",
		"a 0 bp line still shows its tax")

	block := []string{
		"Subtotal: 39.96 EUR",
		"Discount: 3.75 EUR",
		"Net total: 36.21 EUR",
		"Tax: 5.51 EUR",
		"Gross total: 41.72 EUR",
	}
	last := -1
	for _, want := range block {
		index := -1
		for i, line := range bodyLines {
			if line == want {
				index = i
				break
			}
		}
		require.GreaterOrEqualf(t, index, 0, "the body has the line %q:\n%s", want, body)
		assert.Greaterf(t, index, last, "%q comes after the previous totals line", want)
		last = index
	}
	assert.NotContains(t, bodyLines, "Total: 36.21 EUR", "the net total is labelled Net total")

	// The rendering reads the stored values, not a recomputation.
	rendered := RenderQuoteEmail(&models.Quote{
		BaseModel: models.BaseModel{ID: 7}, Title: "T", Currency: "RON",
		SubtotalCents: 1000, DiscountCents: 0, TotalCents: 1000, TaxCents: 190, GrossTotalCents: 1190,
		Lines: []models.QuoteLine{{Description: "D", Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: 1000, LineTotalCents: 1000, TaxRateBP: 1900, TaxCents: 190}},
	})
	assert.Contains(t, rendered, "1. D | quantity 1 x 10.00 RON | discount 0.00% | line total 10.00 RON | tax 19.00% 1.90 RON\n")
	assert.Contains(t, rendered, "Net total: 10.00 RON\nTax: 1.90 RON\nGross total: 11.90 RON\n")
}
