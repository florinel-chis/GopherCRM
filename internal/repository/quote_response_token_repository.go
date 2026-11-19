package repository

import (
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type quoteResponseTokenRepository struct {
	db *gorm.DB
}

func NewQuoteResponseTokenRepository(db *gorm.DB) QuoteResponseTokenRepository {
	return &quoteResponseTokenRepository{db: db}
}

// quoteResponseUsableClause is what makes a row spendable, shared by the read
// and the claim so the two cannot disagree: unused, not past its expiry, and
// its quote is live and still sent.
const quoteResponseUsableClause = "used_at IS NULL AND (expires_at IS NULL OR expires_at > ?)" +
	" AND EXISTS (SELECT 1 FROM quotes WHERE quotes.id = quote_response_tokens.quote_id" +
	" AND quotes.status = ? AND quotes.deleted_at IS NULL)"

func (r *quoteResponseTokenRepository) Create(token *models.QuoteResponseToken) error {
	return r.db.Create(token).Error
}

// FindUsableByHash returns only a spendable row. A used, expired or unknown
// token and a token whose quote left sent or was deleted all surface as
// gorm.ErrRecordNotFound, so a caller cannot tell them apart.
func (r *quoteResponseTokenRepository) FindUsableByHash(tokenHash string, now time.Time) (*models.QuoteResponseToken, error) {
	var token models.QuoteResponseToken
	err := r.db.Where("token_hash = ?", tokenHash).
		Where(quoteResponseUsableClause, now.UTC(), models.QuoteStatusSent).
		First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// Claim spends a row with one conditional update: every predicate is part of
// the statement, so of two callers that both read the token as spendable only
// one stamps it, and the other affects no row. Exactly one affected row is
// the only success; zero is gorm.ErrRecordNotFound. The service runs the
// claim inside the transaction that moves the quote and writes the history
// row, so a rollback hands the link back.
func (r *quoteResponseTokenRepository) Claim(id uint, decision string, now time.Time) error {
	stamp := now.UTC()
	result := r.db.Model(&models.QuoteResponseToken{}).
		Where("id = ?", id).
		Where(quoteResponseUsableClause, stamp, models.QuoteStatusSent).
		Updates(map[string]interface{}{
			"used_at":    &stamp,
			"decision":   decision,
			"updated_at": stamp,
		})
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return gorm.ErrRecordNotFound
	default:
		// The primary key makes this unreachable; it is still refused rather
		// than reported as a single-use claim.
		return fmt.Errorf("quote response token claim affected %d rows", result.RowsAffected)
	}
}

// InvalidateForQuote stamps used_at on the quote's unused rows and leaves
// decision NULL: a newer email or a staff status change killed the link, the
// client did not answer. The erasure of the quote's customer stamps the same
// way through the shared helper below (#395).
//
// It reads the ids with a plain SELECT and stamps them by primary key, and
// issues no UPDATE when there is none. An UPDATE filtered on quote_id would
// go through idx_quote_response_tokens_quote_id and, under REPEATABLE READ on
// MySQL and MariaDB, lock the gap around the quote's entries (always on a
// first email, which has no row yet); two transactions of two different
// quotes can hold the same gap, and each one's insert then waits on the
// other's: a deadlock (error 1213). Primary-key record locks leave the gaps
// free. The plain read is complete because the caller holds the lock on the
// quotes row before the first read of its transaction, and every writer of
// the quote's token rows takes that lock first.
func (r *quoteResponseTokenRepository) InvalidateForQuote(quoteID uint, now time.Time) error {
	_, err := invalidateQuoteResponseTokens(r.db, []uint{quoteID}, now)
	return err
}

// invalidateQuoteResponseTokens is the one statement pair that kills unused
// links, shared by InvalidateForQuote and the erasure of a quote's customer
// (business_text_erasure.go): a plain read of the unused ids of the quotes,
// then a stamp by primary key that still requires used_at IS NULL. It returns
// the ids it read. The stamp may affect fewer rows than were read, zero
// included: a caller whose snapshot predates a committed client answer reads
// that row as unused, and the predicate of the update leaves the answer's
// used_at and decision alone.
func invalidateQuoteResponseTokens(db *gorm.DB, quoteIDs []uint, now time.Time) ([]uint, error) {
	if len(quoteIDs) == 0 {
		return nil, nil
	}
	var ids []uint
	err := db.Model(&models.QuoteResponseToken{}).
		Where("quote_id IN ? AND used_at IS NULL", quoteIDs).
		Order("id ASC").
		Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	stamp := now.UTC()
	err = db.Model(&models.QuoteResponseToken{}).
		Where("id IN ? AND used_at IS NULL", ids).
		Updates(map[string]interface{}{
			"used_at":    &stamp,
			"updated_at": stamp,
		}).Error
	return ids, err
}

func (r *quoteResponseTokenRepository) WithTx(tx *gorm.DB) QuoteResponseTokenRepository {
	return &quoteResponseTokenRepository{db: tx}
}
