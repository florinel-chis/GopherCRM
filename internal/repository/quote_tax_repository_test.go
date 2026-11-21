package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
)

// #324: UpdateTotals writes the five totals (subtotal, discount, net total,
// tax, gross) and updated_at WHERE id = ? AND status = 'draft'. It answers 1
// for a draft, also for a repeated identical write (MySQL reports changed
// rows, which matched() re-checks), and 0 for any other status with the row
// unchanged. The SQLite root uses a file-backed database with one connection;
// the MySQL root a disposable schema on TEST_MYSQL_DSN, skipping without the
// variable and failing when it is unreachable.

func TestQuoteRepositoryUpdateTotalsWithTaxSQLite(t *testing.T) {
	runQuoteRepositoryUpdateTotalsWithTax(t, testsupport.Open(t, testsupport.SQLite, "quote_tax_repo", quoteTaxRepoDB))
}

func TestQuoteRepositoryUpdateTotalsWithTaxMySQL(t *testing.T) {
	runQuoteRepositoryUpdateTotalsWithTax(t, testsupport.Open(t, testsupport.MySQL, "quote_tax_repo", quoteTaxRepoDB))
}

// quoteTaxRepoDB migrates the quote tables without their relations: a
// file-backed SQLite database with one connection and foreign keys off, or a
// uniquely named disposable schema on the TEST_MYSQL_DSN server.
var quoteTaxRepoDB = testsupport.Group(
	testsupport.DisableForeignKeyConstraintWhenMigrating(),
	testsupport.OnSQLite(testsupport.File("quote_tax.db", testsupport.BusyTimeout), testsupport.WithoutForeignKeys()),
	testsupport.Models(&models.Quote{}, &models.QuoteLine{}),
)

// quoteTaxRow is the committed quote row rendered for comparison.
func quoteTaxRow(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var rows []map[string]interface{}
	require.NoError(t, db.Table("quotes").Where("id = ?", id).Find(&rows).Error)
	require.Len(t, rows, 1)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	return string(encoded)
}

func runQuoteRepositoryUpdateTotalsWithTax(t *testing.T, db *gorm.DB) {
	repo := NewQuoteRepository(db)
	draft := &models.Quote{Title: "Taxed", Status: models.QuoteStatusDraft, Currency: "EUR", OwnerID: 1}
	require.NoError(t, repo.Create(draft))

	var before models.Quote
	require.NoError(t, db.First(&before, draft.ID).Error)
	assert.Zero(t, before.TaxCents, "a new quote reads tax 0")
	assert.Zero(t, before.GrossTotalCents, "a new quote reads gross 0")

	totals := models.QuoteTotals{Subtotal: 2997, Discount: 375, Total: 2622, Tax: 551, Gross: 3173}
	time.Sleep(5 * time.Millisecond)
	rows, err := repo.UpdateTotals(draft.ID, totals)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows, "a draft matches")

	var stored models.Quote
	require.NoError(t, db.First(&stored, draft.ID).Error)
	assert.Equal(t, int64(2997), stored.SubtotalCents)
	assert.Equal(t, int64(375), stored.DiscountCents)
	assert.Equal(t, int64(2622), stored.TotalCents)
	assert.Equal(t, int64(551), stored.TaxCents)
	assert.Equal(t, int64(3173), stored.GrossTotalCents)
	assert.True(t, stored.UpdatedAt.After(before.UpdatedAt), "updated_at is written")
	assert.Equal(t, models.QuoteStatusDraft, stored.Status)

	// The identical write again still matches the row, whatever the driver
	// reports as changed rows.
	rows, err = repo.UpdateTotals(draft.ID, totals)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows, "a repeated identical write still matches the draft")

	// Back to zero tax: the explicit zeros are written.
	rows, err = repo.UpdateTotals(draft.ID, models.QuoteTotals{Subtotal: 2997, Discount: 375, Total: 2622, Tax: 0, Gross: 2622})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	require.NoError(t, db.First(&stored, draft.ID).Error)
	assert.Zero(t, stored.TaxCents)
	assert.Equal(t, int64(2622), stored.GrossTotalCents)

	for _, status := range []models.QuoteStatus{models.QuoteStatusSent, models.QuoteStatusAccepted, models.QuoteStatusRejected, models.QuoteStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			locked := &models.Quote{Title: "Locked " + string(status), Status: status, Currency: "EUR", OwnerID: 1}
			require.NoError(t, repo.Create(locked))
			require.NoError(t, db.Model(&models.Quote{}).Where("id = ?", locked.ID).Updates(map[string]interface{}{
				"subtotal_cents": 100, "discount_cents": 0, "total_cents": 100, "tax_cents": 21, "gross_total_cents": 121,
			}).Error)
			snapshot := quoteTaxRow(t, db, locked.ID)

			rows, err := repo.UpdateTotals(locked.ID, models.QuoteTotals{Subtotal: 9, Discount: 1, Total: 8, Tax: 2, Gross: 10})
			require.NoError(t, err)
			assert.EqualValues(t, 0, rows, "a %s quote matches nothing", status)
			assert.Equal(t, snapshot, quoteTaxRow(t, db, locked.ID), "the %s row is unchanged", status)
		})
	}
}
