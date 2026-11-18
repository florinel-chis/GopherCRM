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
// client did not answer.
func (r *quoteResponseTokenRepository) InvalidateForQuote(quoteID uint, now time.Time) error {
	stamp := now.UTC()
	return r.db.Model(&models.QuoteResponseToken{}).
		Where("quote_id = ? AND used_at IS NULL", quoteID).
		Updates(map[string]interface{}{
			"used_at":    &stamp,
			"updated_at": stamp,
		}).Error
}

func (r *quoteResponseTokenRepository) WithTx(tx *gorm.DB) QuoteResponseTokenRepository {
	return &quoteResponseTokenRepository{db: tx}
}
