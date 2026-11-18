package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// setupQuoteResponseTokenDB is the quote repository database plus the token
// table, dropped again afterwards because the database is shared in memory.
func setupQuoteResponseTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupQuoteRepoDB(t)
	require.NoError(t, db.Migrator().DropTable(&models.QuoteResponseToken{}))
	require.NoError(t, db.AutoMigrate(&models.QuoteResponseToken{}))
	t.Cleanup(func() { _ = db.Migrator().DropTable(&models.QuoteResponseToken{}) })
	return db
}

func readQuoteResponseToken(t *testing.T, db *gorm.DB, id uint) models.QuoteResponseToken {
	t.Helper()
	var row models.QuoteResponseToken
	require.NoError(t, db.First(&row, id).Error)
	return row
}

// The read and the claim share their predicates: a row is usable only while
// it is unused, strictly before its expiry (or has none), and its quote is
// live and sent. The claim is one conditional update, so the second of two
// claims finds nothing; an invalidation stamps used_at and leaves decision
// NULL.
func TestQuoteResponseTokenRepository(t *testing.T) {
	db := setupQuoteResponseTokenDB(t)
	quotes := NewQuoteRepository(db)
	repo := NewQuoteResponseTokenRepository(db)
	f := seedQuoteFixture(t, db, "qrt")
	now := time.Now().UTC().Truncate(time.Millisecond)
	later := now.Add(time.Hour)

	newToken := func(t *testing.T, quoteID uint, hash string, expiresAt *time.Time) *models.QuoteResponseToken {
		t.Helper()
		row := &models.QuoteResponseToken{QuoteID: quoteID, TokenHash: hash, ExpiresAt: expiresAt}
		require.NoError(t, repo.Create(row))
		return row
	}
	assertUnusable := func(t *testing.T, row *models.QuoteResponseToken, at time.Time) {
		t.Helper()
		_, err := repo.FindUsableByHash(row.TokenHash, at)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "the read misses: %v", err)
		err = repo.Claim(row.ID, models.QuoteResponseAccept, at)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "the claim matches nothing: %v", err)
	}

	t.Run("the hash is unique and never serialised", func(t *testing.T) {
		sent := f.quote(t, quotes, models.QuoteStatusSent, "Unique")
		newToken(t, sent.ID, "hash-unique", nil)
		require.Error(t, repo.Create(&models.QuoteResponseToken{QuoteID: sent.ID, TokenHash: "hash-unique"}))
	})

	t.Run("a usable row is read without a write and claimed exactly once", func(t *testing.T) {
		sent := f.quote(t, quotes, models.QuoteStatusSent, "Claim")
		row := newToken(t, sent.ID, "hash-claim", &later)
		before := readQuoteResponseToken(t, db, row.ID)

		for i := 0; i < 2; i++ {
			found, err := repo.FindUsableByHash("hash-claim", now)
			require.NoError(t, err)
			assert.Equal(t, row.ID, found.ID)
			assert.Equal(t, sent.ID, found.QuoteID)
		}
		assert.Equal(t, before, readQuoteResponseToken(t, db, row.ID), "the read writes nothing")
		_, err := repo.FindUsableByHash("hash-other", now)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

		require.NoError(t, repo.Claim(row.ID, models.QuoteResponseDecline, now))
		claimed := readQuoteResponseToken(t, db, row.ID)
		require.NotNil(t, claimed.UsedAt)
		assert.WithinDuration(t, now, *claimed.UsedAt, time.Millisecond)
		require.NotNil(t, claimed.Decision)
		assert.Equal(t, models.QuoteResponseDecline, *claimed.Decision)

		assertUnusable(t, row, now)
		again := readQuoteResponseToken(t, db, row.ID)
		assert.Equal(t, models.QuoteResponseDecline, *again.Decision, "a refused claim changes nothing")
	})

	t.Run("the expiry is strict and NULL never expires", func(t *testing.T) {
		sent := f.quote(t, quotes, models.QuoteStatusSent, "Expiry")
		row := newToken(t, sent.ID, "hash-expiry", &later)
		_, err := repo.FindUsableByHash(row.TokenHash, later.Add(-time.Millisecond))
		require.NoError(t, err, "usable until just before the expiry")
		assertUnusable(t, row, later)
		assertUnusable(t, row, later.Add(time.Hour))
		assert.Nil(t, readQuoteResponseToken(t, db, row.ID).UsedAt)

		open := newToken(t, sent.ID, "hash-open", nil)
		_, err = repo.FindUsableByHash(open.TokenHash, now.AddDate(50, 0, 0))
		require.NoError(t, err)
		require.NoError(t, repo.Claim(open.ID, models.QuoteResponseAccept, now.AddDate(50, 0, 0)))
	})

	t.Run("the quote must be live and sent", func(t *testing.T) {
		for _, status := range []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusAccepted, models.QuoteStatusRejected, models.QuoteStatusExpired} {
			quote := f.quote(t, quotes, status, "Status "+string(status))
			row := newToken(t, quote.ID, "hash-"+string(status), nil)
			assertUnusable(t, row, now)
			assert.Nil(t, readQuoteResponseToken(t, db, row.ID).UsedAt, status)
		}
		deleted := f.quote(t, quotes, models.QuoteStatusSent, "Deleted")
		row := newToken(t, deleted.ID, "hash-deleted", nil)
		require.NoError(t, quotes.Delete(deleted.ID))
		assertUnusable(t, row, now)

		orphan := newToken(t, 987654, "hash-orphan", nil)
		assertUnusable(t, orphan, now)
	})

	t.Run("invalidation stamps the quote's unused rows only", func(t *testing.T) {
		sent := f.quote(t, quotes, models.QuoteStatusSent, "Invalidate")
		other := f.quote(t, quotes, models.QuoteStatusSent, "Other")
		first := newToken(t, sent.ID, "hash-inv-1", nil)
		second := newToken(t, sent.ID, "hash-inv-2", &later)
		spent := newToken(t, sent.ID, "hash-inv-3", nil)
		require.NoError(t, repo.Claim(spent.ID, models.QuoteResponseAccept, now.Add(-time.Minute)))
		kept := newToken(t, other.ID, "hash-inv-4", nil)

		require.NoError(t, repo.InvalidateForQuote(sent.ID, now))
		for _, row := range []*models.QuoteResponseToken{first, second} {
			stored := readQuoteResponseToken(t, db, row.ID)
			require.NotNil(t, stored.UsedAt)
			assert.WithinDuration(t, now, *stored.UsedAt, time.Millisecond)
			assert.Nil(t, stored.Decision, "an invalidated row carries no decision")
			assertUnusable(t, row, now)
		}
		stored := readQuoteResponseToken(t, db, spent.ID)
		assert.WithinDuration(t, now.Add(-time.Minute), *stored.UsedAt, time.Millisecond, "a spent row keeps its stamp")
		assert.Equal(t, models.QuoteResponseAccept, *stored.Decision)
		_, err := repo.FindUsableByHash(kept.TokenHash, now)
		require.NoError(t, err, "another quote's row is untouched")
	})

	t.Run("a transaction-bound repository rolls back with it", func(t *testing.T) {
		sent := f.quote(t, quotes, models.QuoteStatusSent, "Tx")
		row := newToken(t, sent.ID, "hash-tx", nil)
		rollback := errors.New("rollback")
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, repo.WithTx(tx).Claim(row.ID, models.QuoteResponseAccept, now))
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		stored := readQuoteResponseToken(t, db, row.ID)
		assert.Nil(t, stored.UsedAt, "the claim is handed back")
		assert.Nil(t, stored.Decision)
	})
}
