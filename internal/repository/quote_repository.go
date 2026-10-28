package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type quoteRepository struct {
	db *gorm.DB
}

func NewQuoteRepository(db *gorm.DB) QuoteRepository {
	return &quoteRepository{db: db}
}

// quoteAssociations are the fields never written back from a quote struct: a
// caller that set them for its own use must not have them saved over their
// tables.
var quoteAssociations = []string{"Owner", "Customer", "Deal", "Lines"}

func (r *quoteRepository) Create(quote *models.Quote) error {
	return r.db.Omit(quoteAssociations...).Create(quote).Error
}

// quotePreloads are the associations the detail endpoint and the lists carry.
// Lines are preloaded in display order; a soft-deleted customer is not
// preloaded (GORM's scope), which is how the email refuses an erased
// recipient.
func (r *quoteRepository) withPreloads(db *gorm.DB, lines bool) *gorm.DB {
	db = db.Preload("Owner").Preload("Customer").Preload("Deal")
	if lines {
		db = db.Preload("Lines", func(db *gorm.DB) *gorm.DB {
			return db.Order("position ASC").Order("id ASC")
		})
	}
	return db
}

func (r *quoteRepository) GetByID(id uint) (*models.Quote, error) {
	var quote models.Quote
	if err := r.withPreloads(r.db, true).First(&quote, id).Error; err != nil {
		return nil, err
	}
	return &quote, nil
}

// locking is a fresh session carrying `FOR UPDATE`: Clauses alone returns a
// statement GORM keeps reusing, and its state would leak into the statements
// that follow on the same handle. The SQLite dialector drops the clause.
func (r *quoteRepository) locking() *gorm.DB {
	return r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
}

func (r *quoteRepository) GetByIDForUpdate(id uint) (*models.Quote, error) {
	var quote models.Quote
	if err := r.locking().First(&quote, id).Error; err != nil {
		return nil, err
	}
	return &quote, nil
}

// UpdateDraft writes the quote's editable columns WHERE the stored status is
// still draft: everything a PUT may change plus the links it carries
// unchanged. The totals and the status are deliberately absent — the totals
// are UpdateTotals' and the status is ClaimStatus', each under its own
// condition — and so are the stamps, which only a transition writes. See
// matched for the MySQL changed-rows caveat.
func (r *quoteRepository) UpdateDraft(quote *models.Quote) (int64, error) {
	result := r.db.Model(&models.Quote{}).
		Where("id = ? AND status = ?", quote.ID, models.QuoteStatusDraft).
		Updates(map[string]interface{}{
			"title":       quote.Title,
			"currency":    quote.Currency,
			"valid_until": quote.ValidUntil,
			"notes":       quote.Notes,
			"deal_id":     quote.DealID,
			"customer_id": quote.CustomerID,
			"owner_id":    quote.OwnerID,
			"updated_at":  time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return r.matched(result.RowsAffected, "id = ? AND status = ?", quote.ID, models.QuoteStatusDraft)
}

// UpdateTotals writes the three totals WHERE the status is still draft.
func (r *quoteRepository) UpdateTotals(id uint, subtotal, discount, total int64) (int64, error) {
	result := r.db.Model(&models.Quote{}).
		Where("id = ? AND status = ?", id, models.QuoteStatusDraft).
		Updates(map[string]interface{}{
			"subtotal_cents": subtotal,
			"discount_cents": discount,
			"total_cents":    total,
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return r.matched(result.RowsAffected, "id = ? AND status = ?", id, models.QuoteStatusDraft)
}

// ClaimStatus is the conditional update every transition starts with: it
// matches only while the stored status is still `from`, so of two competing
// transitions exactly one finds the row. The status always changes, so the
// driver's row count is the matched count here.
func (r *quoteRepository) ClaimStatus(id uint, from, to models.QuoteStatus, stamps QuoteStatusStamps) (int64, error) {
	result := r.db.Model(&models.Quote{}).
		Where("id = ? AND status = ?", id, from).
		Updates(map[string]interface{}{
			"status":     to,
			"sent_at":    stamps.SentAt,
			"closed_at":  stamps.ClosedAt,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// emailableStatuses are the statuses a quote may be emailed in.
var emailableStatuses = []models.QuoteStatus{models.QuoteStatusSent, models.QuoteStatusAccepted}

// noNewerRevisionClause is the NOT EXISTS behind StampEmailed: no history row
// of the quote moved it to draft after the marker the sender read. A quote
// revised and re-sent during the send therefore still refuses the stamp; the
// status alone could not tell.
const noNewerRevisionClause = "NOT EXISTS (SELECT 1 FROM quote_status_changes" +
	" WHERE quote_status_changes.quote_id = quotes.id AND quote_status_changes.to_status = ? AND quote_status_changes.id > ?)"

// StampEmailed sets last_emailed_at under the conditions documented on the
// interface. No transaction is needed: the one statement is the whole claim.
func (r *quoteRepository) StampEmailed(id uint, afterHistoryID uint, at time.Time) (int64, error) {
	stamp := at.UTC()
	result := r.db.Model(&models.Quote{}).
		Where("id = ? AND status IN ?", id, emailableStatuses).
		Where(noNewerRevisionClause, models.QuoteStatusDraft, afterHistoryID).
		Updates(map[string]interface{}{
			"last_emailed_at": &stamp,
			"updated_at":      time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > 0 {
		return result.RowsAffected, nil
	}
	return r.matched(0, "id = ? AND status IN ? AND "+noNewerRevisionClause, id, emailableStatuses, models.QuoteStatusDraft, afterHistoryID)
}

// matched turns the driver's row count into the matched-row count of a
// conditional update. MySQL reports rows CHANGED, not rows matched, unless the
// DSN asks otherwise, so an update that writes values the row already holds
// (two stamps in the same millisecond, a line of zero value) reports 0 for a
// row it did match. When that happens the same conditions are re-read on the
// same handle; a row that still satisfies them held exactly the written values
// and the update is as good as applied.
func (r *quoteRepository) matched(rowsAffected int64, condition string, args ...interface{}) (int64, error) {
	if rowsAffected > 0 {
		return rowsAffected, nil
	}
	var count int64
	if err := r.db.Model(&models.Quote{}).Where(condition, args...).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *quoteRepository) Delete(id uint) error {
	result := r.db.Delete(&models.Quote{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// scope applies the filter to a query. Every predicate is a bound parameter;
// the owner predicate is what narrows a sales caller, applied here so that
// both the rows and the total count only what the caller may see.
func (r *quoteRepository) scope(db *gorm.DB, filter QuoteListFilter) *gorm.DB {
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.DealID != nil {
		db = db.Where("deal_id = ?", *filter.DealID)
	}
	if filter.CustomerID != nil {
		db = db.Where("customer_id = ?", *filter.CustomerID)
	}
	if filter.OwnerID != nil {
		db = db.Where("owner_id = ?", *filter.OwnerID)
	}
	return db
}

// List returns one page of quotes without their lines, plus the total matching
// the same filter. The sort column goes through the quotes allowlist; an
// unknown column is an error, never interpolated. The primary key is added as
// a tie-breaker so pages are stable when the sort column repeats.
func (r *quoteRepository) List(offset, limit int, filter QuoteListFilter) ([]models.Quote, int64, error) {
	column, direction, err := utils.ValidateSort("quotes", filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.scope(r.db.Model(&models.Quote{}), filter).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	quotes := []models.Quote{}
	err = r.scope(r.withPreloads(r.db, false), filter).
		Order(column + " " + direction).
		Order("id " + direction).
		Offset(offset).Limit(limit).
		Find(&quotes).Error
	if err != nil {
		return nil, 0, err
	}
	return quotes, total, nil
}

func (r *quoteRepository) CreateLine(line *models.QuoteLine) error {
	return r.db.Omit("Product").Create(line).Error
}

func (r *quoteRepository) GetLine(quoteID, lineID uint) (*models.QuoteLine, error) {
	var line models.QuoteLine
	if err := r.db.Where("quote_id = ?", quoteID).First(&line, lineID).Error; err != nil {
		return nil, err
	}
	return &line, nil
}

// UpdateLine is a full save: Save writes every column, zero values included,
// which is what a discount or a price set back to 0 needs.
func (r *quoteRepository) UpdateLine(line *models.QuoteLine) error {
	return r.db.Omit("Product").Save(line).Error
}

func (r *quoteRepository) DeleteLine(quoteID, lineID uint) error {
	result := r.db.Where("quote_id = ?", quoteID).Delete(&models.QuoteLine{}, lineID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *quoteRepository) ListLines(quoteID uint) ([]models.QuoteLine, error) {
	lines := []models.QuoteLine{}
	err := r.db.Where("quote_id = ?", quoteID).Order("position ASC").Order("id ASC").Find(&lines).Error
	return lines, err
}

func (r *quoteRepository) CountLines(quoteID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.QuoteLine{}).Where("quote_id = ?", quoteID).Count(&count).Error
	return count, err
}

// CountLinesForUpdate reads with `FOR UPDATE` on MySQL. Inside a REPEATABLE
// READ transaction a plain count would answer from the snapshot taken at the
// transaction's first read and miss a line committed since; the locking read
// sees the latest committed rows, which is what the send decision needs.
func (r *quoteRepository) CountLinesForUpdate(quoteID uint) (int64, error) {
	var count int64
	err := r.locking().Model(&models.QuoteLine{}).Where("quote_id = ?", quoteID).Count(&count).Error
	return count, err
}

func (r *quoteRepository) NextLinePosition(quoteID uint) (int64, error) {
	var max struct{ Position *int64 }
	err := r.db.Model(&models.QuoteLine{}).Select("MAX(position) AS position").Where("quote_id = ?", quoteID).Scan(&max).Error
	if err != nil {
		return 0, err
	}
	if max.Position == nil {
		return 1, nil
	}
	return *max.Position + 1, nil
}

func (r *quoteRepository) CreateStatusChange(change *models.QuoteStatusChange) error {
	return r.db.Omit("ChangedBy").Create(change).Error
}

// ListStatusChanges returns the history oldest first: by changed_at, with the
// id as tie-breaker for two rows written in the same instant.
func (r *quoteRepository) ListStatusChanges(quoteID uint) ([]models.QuoteStatusChange, error) {
	changes := []models.QuoteStatusChange{}
	err := r.db.Preload("ChangedBy").
		Where("quote_id = ?", quoteID).
		Order("changed_at ASC").Order("id ASC").
		Find(&changes).Error
	return changes, err
}

func (r *quoteRepository) LatestStatusChangeID(quoteID uint) (uint, error) {
	var latest struct{ ID *uint }
	err := r.db.Model(&models.QuoteStatusChange{}).Select("MAX(id) AS id").Where("quote_id = ?", quoteID).Scan(&latest).Error
	if err != nil {
		return 0, err
	}
	if latest.ID == nil {
		return 0, nil
	}
	return *latest.ID, nil
}

func (r *quoteRepository) WithTx(tx *gorm.DB) QuoteRepository {
	return &quoteRepository{db: tx}
}
