package service

// A response link mailed before the erasure of the quote's customer (#395).
//
// The real chain: QuoteService.Email mints the link, the repository erases
// the customer, and the two public methods are called with the mailed token.
// The erasure stamps the unused token of the customer's quotes in its own
// transaction, so the link gets the one generic rejection while the quote
// stays sent. The race root runs the erasure against a client answer on one
// token: either the answer commits first and stands, or the erasure does and
// the answer is refused.
//
// The SQLite root runs on the production one-connection pool, the MySQL root
// on a disposable schema (TEST_MYSQL_DSN; it skips without it).

import (
	"errors"
	"fmt"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// migrateQuoteErasureSchema adds, to the quote test schema, the credential
// tables the erasure of a customer locks and purges without a table probe.
// Kept here so the shared quote schema stays what the quote flows need.
func migrateQuoteErasureSchema(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{}))
	return db
}

func TestQuoteResponseAfterCustomerErasureSQLite(t *testing.T) {
	runQuoteResponseAfterCustomerErasure(t, migrateQuoteErasureSchema(t, openQuoteTestSQLite(t)))
}

func TestQuoteResponseAfterCustomerErasureMySQL(t *testing.T) {
	// loc=Local, as the application connects (see TestQuoteResponseConcurrentMySQL).
	runQuoteResponseAfterCustomerErasure(t, migrateQuoteErasureSchema(t, openQuoteTestMySQLIn(t, 4, time.Local)))
}

// isLockFailure reports a deadlock (1213) or a lock wait timeout (1205).
func isLockFailure(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func runQuoteResponseAfterCustomerErasure(t *testing.T, db *gorm.DB) {
	env := newQuoteResponseEnv(t, db)
	customers := repository.NewCustomerRepository(db)

	// Every scenario erases its own customer; the quote names no deal, so the
	// env's deal and customer stay as they are for the next one.
	newCustomer := func() *models.Customer {
		customer := &models.Customer{FirstName: "Erased", LastName: "Client",
			Email: fmt.Sprintf("erased-client-%d@example.com", quoteTestSeq.Add(1))}
		require.NoError(t, db.Create(customer).Error)
		return customer
	}
	quoteOf := func(customer *models.Customer, status models.QuoteStatus) *models.Quote {
		return env.newQuote(status, 1, func(q *models.Quote) {
			q.DealID, q.CustomerID, q.ValidUntil = nil, &customer.ID, dateDaysFromNow(t, 0)
		})
	}
	allTokens := func() []models.QuoteResponseToken {
		var rows []models.QuoteResponseToken
		require.NoError(t, db.Order("id ASC").Find(&rows).Error)
		return rows
	}

	t.Run("a link mailed before the erasure is refused after it", func(t *testing.T) {
		customer := newCustomer()
		quote := quoteOf(customer, models.QuoteStatusSent)
		token := env.emailForToken(quote.ID)

		view, err := env.svc.GetPublicResponse(token)
		require.NoError(t, err, "the link works before the erasure")
		require.NotNil(t, view)
		assert.Equal(t, models.QuoteStatusSent, view.Status)
		historyBefore := env.history(quote.ID)

		require.NoError(t, customers.Delete(customer.ID))

		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0].UsedAt, "the erasure stamped the token")
		assert.Nil(t, rows[0].Decision, "the client did not answer")

		before := env.responseState(quote.ID)
		view, err = env.svc.GetPublicResponse(token)
		assert.Nil(t, view)
		assert.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "GetPublicResponse after the erasure: %v", err)
		for _, decision := range []string{models.QuoteResponseAccept, models.QuoteResponseDecline} {
			view, err = env.svc.RespondPublic(token, decision)
			assert.Nil(t, view)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "RespondPublic %s after the erasure: %v", decision, err)
		}
		assert.Equal(t, before, env.responseState(quote.ID), "the three refused calls wrote nothing")

		var committed models.Quote
		require.NoError(t, db.First(&committed, quote.ID).Error)
		assert.Equal(t, models.QuoteStatusSent, committed.Status, "the quote is still sent")
		assert.Nil(t, committed.ClosedAt)
		assert.Equal(t, historyBefore, env.history(quote.ID), "and its history is unchanged")
	})

	t.Run("a customer whose only quote is an unmailed draft is erased without a token row", func(t *testing.T) {
		customer := newCustomer()
		quote := quoteOf(customer, models.QuoteStatusDraft)
		before := allTokens()

		require.NoError(t, customers.Delete(customer.ID))

		assert.Empty(t, env.tokenRows(quote.ID))
		assert.Equal(t, before, allTokens(), "no token row was written or changed")
		var erased models.Customer
		require.NoError(t, db.Unscoped().First(&erased, customer.ID).Error)
		assert.True(t, erased.DeletedAt.Valid)
	})

	// The order the scheduler rarely picks in the race below, pinned: the
	// erasure has stamped the token and holds the quote row when the answer
	// arrives. The answer waits for the commit (for the lock on MySQL, for the
	// one connection on SQLite) and is then refused; it never fails on a lock.
	t.Run("an answer arriving while the erasure is open is refused once it commits", func(t *testing.T) {
		customer := newCustomer()
		quote := quoteOf(customer, models.QuoteStatusSent)
		token := env.emailForToken(quote.ID)
		historyBefore := len(env.history(quote.ID))

		tx := db.Begin()
		require.NoError(t, tx.Error)
		open := true
		t.Cleanup(func() {
			if open {
				_ = tx.Rollback().Error
			}
		})
		require.NoError(t, customers.WithTx(tx).Delete(customer.ID))

		answered := make(chan error, 1)
		go func() {
			_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
			answered <- err
		}()
		require.NoError(t, tx.Commit().Error)
		open = false

		select {
		case err := <-answered:
			require.Falsef(t, isLockFailure(err), "the answer met a deadlock or lock wait: %v", err)
			require.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "the answer is refused with the generic rejection: %v", err)
		case <-time.After(quoteTestTimeout):
			t.Fatal("the answer did not return after the erasure committed")
		}
		var committed models.Quote
		require.NoError(t, db.First(&committed, quote.ID).Error)
		assert.Equal(t, models.QuoteStatusSent, committed.Status)
		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0].UsedAt)
		assert.Nil(t, rows[0].Decision, "the erasure invalidated the token, the client did not answer")
		assert.Len(t, env.history(quote.ID), historyBefore)
	})

	t.Run("the erasure racing a client answer ends in exactly one consistent outcome", func(t *testing.T) {
		outcomes := map[models.QuoteStatus]int{}
		for round := 0; round < quoteRaceRounds; round++ {
			customer := newCustomer()
			quote := quoteOf(customer, models.QuoteStatusSent)
			token := env.emailForToken(quote.ID)
			historyBefore := len(env.history(quote.ID))

			answerErr, eraseErr := raceTwo(t,
				func() error {
					_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
					return err
				},
				func() error { return customers.Delete(customer.ID) },
			)
			require.Falsef(t, isLockFailure(answerErr), "round %d: the answer met a deadlock or lock wait: %v", round, answerErr)
			require.Falsef(t, isLockFailure(eraseErr), "round %d: the erasure met a deadlock or lock wait: %v", round, eraseErr)
			require.NoErrorf(t, eraseErr, "round %d: the erasure commits either way", round)

			var committed models.Quote
			require.NoError(t, db.First(&committed, quote.ID).Error)
			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].UsedAt, "the token is dead either way")
			if answerErr == nil {
				assert.Equal(t, models.QuoteStatusAccepted, committed.Status)
				require.NotNil(t, rows[0].Decision, "the erasure left the answer's row alone")
				assert.Equal(t, models.QuoteResponseAccept, *rows[0].Decision)
				require.NotNil(t, committed.ClosedAt)
				assert.Truef(t, rows[0].UsedAt.Equal(*committed.ClosedAt),
					"used_at is the answer's stamp, not the erasure's (used_at %v, closed_at %v)", rows[0].UsedAt, committed.ClosedAt)
				assert.Len(t, env.history(quote.ID), historyBefore+1, "one history row for the answer")
			} else {
				require.Truef(t, errors.Is(answerErr, apperrors.ErrQuoteResponseRejected), "round %d: a refused answer is the generic rejection: %v", round, answerErr)
				assert.Equal(t, models.QuoteStatusSent, committed.Status)
				assert.Nil(t, committed.ClosedAt)
				assert.Nil(t, rows[0].Decision, "the erasure invalidated the token")
				assert.Len(t, env.history(quote.ID), historyBefore, "a refused answer writes no history")
			}
			outcomes[committed.Status]++

			var erased models.Customer
			require.NoError(t, db.Unscoped().First(&erased, customer.ID).Error)
			assert.True(t, erased.DeletedAt.Valid, "the customer is erased")
			_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "a later answer is refused: %v", err)
		}
		t.Logf("outcomes over %d rounds: %v", quoteRaceRounds, outcomes)
	})
}
