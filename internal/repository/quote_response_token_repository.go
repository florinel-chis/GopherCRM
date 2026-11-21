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

// quoteResponseClaimClause is what the claim requires of a row: unused, not
// past its expiry, and its quote is live and still sent. It reads no
// customers row: on MySQL and MariaDB under REPEATABLE READ a subquery of an
// UPDATE takes shared locks on the rows it reads, and the claim runs under
// the quote lock, so a customers predicate here would wait on (and deadlock
// with) an erasure that holds the customer row and waits for the quote. The
// service checks the customer with a plain read under the quote lock before
// the claim instead (RespondPublic).
const quoteResponseClaimClause = "used_at IS NULL AND (expires_at IS NULL OR expires_at > ?)" +
	" AND EXISTS (SELECT 1 FROM quotes WHERE quotes.id = quote_response_tokens.quote_id" +
	" AND quotes.status = ? AND quotes.deleted_at IS NULL)"

// quoteResponseReadClause is what makes a row usable for the plain read: the
// claim's predicates plus a live customer of the quote. A plain SELECT takes
// no lock, so the customer predicate costs the read nothing; it also kills a
// token that the erasure of the customer did not stamp because the token was
// inserted after the erasure's read of the token ids.
const quoteResponseReadClause = "used_at IS NULL AND (expires_at IS NULL OR expires_at > ?)" +
	" AND EXISTS (SELECT 1 FROM quotes WHERE quotes.id = quote_response_tokens.quote_id" +
	" AND quotes.status = ? AND quotes.deleted_at IS NULL" +
	" AND EXISTS (SELECT 1 FROM customers WHERE customers.id = quotes.customer_id" +
	" AND customers.deleted_at IS NULL))"

func (r *quoteResponseTokenRepository) Create(token *models.QuoteResponseToken) error {
	return r.db.Create(token).Error
}

// FindUsableByHash returns only a spendable row. A used, expired or unknown
// token, a token whose quote left sent or was deleted, and a token whose
// quote has no customer or whose customer was erased all surface as
// gorm.ErrRecordNotFound, so a caller cannot tell them apart.
func (r *quoteResponseTokenRepository) FindUsableByHash(tokenHash string, now time.Time) (*models.QuoteResponseToken, error) {
	var token models.QuoteResponseToken
	err := r.db.Where("token_hash = ?", tokenHash).
		Where(quoteResponseReadClause, now.UTC(), models.QuoteStatusSent).
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
// row, so a rollback hands the link back. The claim carries the token and
// quote predicates only, not the customer one of the read (see
// quoteResponseClaimClause).
func (r *quoteResponseTokenRepository) Claim(id uint, decision string, now time.Time) error {
	stamp := now.UTC()
	result := r.db.Model(&models.QuoteResponseToken{}).
		Where("id = ?", id).
		Where(quoteResponseClaimClause, stamp, models.QuoteStatusSent).
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

// InvalidateOlderForQuote stamps used_at on the quote's unused rows whose id
// is lower than newestID and leaves decision NULL: the email that carried
// newestID was delivered, so the links of earlier emails die. newestID and
// any higher id stay usable, so of two overlapping emails of one quote the one
// that finishes last never kills the newer link. Same statements and lock
// rules as InvalidateForQuote.
func (r *quoteResponseTokenRepository) InvalidateOlderForQuote(quoteID, newestID uint, now time.Time) error {
	if newestID == 0 {
		return nil
	}
	_, err := invalidateQuoteResponseTokensBelow(r.db, []uint{quoteID}, newestID, now)
	return err
}

// invalidateQuoteResponseTokens is the one statement pair that kills unused
// links, shared by InvalidateForQuote, InvalidateOlderForQuote (with an upper
// id bound) and the erasure of a quote's customer (business_text_erasure.go):
// a plain read of the unused ids of the quotes, then a stamp by primary key
// that still requires used_at IS NULL. It returns the ids it read. The stamp
// may affect fewer rows than were read, zero included: a caller whose
// snapshot predates a committed client answer reads that row as unused, and
// the predicate of the update leaves the answer's used_at and decision alone.
func invalidateQuoteResponseTokens(db *gorm.DB, quoteIDs []uint, now time.Time) ([]uint, error) {
	return invalidateQuoteResponseTokensBelow(db, quoteIDs, 0, now)
}

// invalidateQuoteResponseTokensBelow is invalidateQuoteResponseTokens limited
// to ids lower than belowID; 0 means no bound.
func invalidateQuoteResponseTokensBelow(db *gorm.DB, quoteIDs []uint, belowID uint, now time.Time) ([]uint, error) {
	if len(quoteIDs) == 0 {
		return nil, nil
	}
	var ids []uint
	query := db.Model(&models.QuoteResponseToken{}).
		Where("quote_id IN ? AND used_at IS NULL", quoteIDs)
	if belowID != 0 {
		query = query.Where("id < ?", belowID)
	}
	err := query.Order("id ASC").
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
