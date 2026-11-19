package service

// Concurrency roots for the client response link (#382): two answers with
// one token commit exactly one. The SQLite root runs on the production
// one-connection pool, the MySQL root on a disposable schema with one
// connection per competitor, where the FOR UPDATE read of the quote does the
// serialising. The quote's valid_until is today, so the expiry predicate of
// the claim runs with a non-NULL value on every engine.

import (
	"errors"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQuoteResponseConcurrentSQLite(t *testing.T) {
	runQuoteResponseConcurrencyScenario(t, openQuoteTestSQLite(t))
}

func TestQuoteResponseConcurrentMySQL(t *testing.T) {
	// loc=Local, as the application connects: the DATE of valid_until then
	// keeps the local calendar day.
	runQuoteResponseConcurrencyScenario(t, openQuoteTestMySQLIn(t, 4, time.Local))
}

func runQuoteResponseConcurrencyScenario(t *testing.T, db *gorm.DB) {
	env := newQuoteResponseEnv(t, db)

	t.Run("two answers with one token commit exactly one", func(t *testing.T) {
		outcomes := map[models.QuoteStatus]int{}
		for round := 0; round < quoteRaceRounds; round++ {
			quote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
			token := env.emailForToken(quote.ID)
			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].ExpiresAt, "the claim compares a stored expiry")
			historyBefore := len(env.history(quote.ID))
			dealBefore := env.dealRow()

			// Even rounds race accept against decline, odd rounds the same
			// decision twice (a double click).
			second := models.QuoteResponseDecline
			if round%2 == 1 {
				second = models.QuoteResponseAccept
			}
			firstErr, secondErr := raceTwo(t,
				func() error {
					_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
					return err
				},
				func() error {
					_, err := env.svc.RespondPublic(token, second)
					return err
				},
			)
			winners := 0
			for _, err := range []error{firstErr, secondErr} {
				if err == nil {
					winners++
					continue
				}
				assert.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "the loser gets the generic rejection, got %v", err)
			}
			require.Equal(t, 1, winners, "exactly one answer commits (first=%v second=%v)", firstErr, secondErr)

			wantDecision := models.QuoteResponseAccept
			if firstErr != nil {
				wantDecision = second
			}
			wantStatus := models.QuoteStatusAccepted
			if wantDecision == models.QuoteResponseDecline {
				wantStatus = models.QuoteStatusRejected
			}

			committed := env.assertTotalsMatchLines(quote.ID)
			assert.Equal(t, wantStatus, committed.Status)
			assert.NotNil(t, committed.ClosedAt)
			outcomes[committed.Status]++

			history := env.history(quote.ID)
			require.Len(t, history, historyBefore+1, "exactly one committed history row")
			last := history[len(history)-1]
			require.NotNil(t, last.FromStatus)
			assert.Equal(t, models.QuoteStatusSent, *last.FromStatus)
			assert.Equal(t, wantStatus, last.ToStatus)
			assert.Nil(t, last.ChangedByID, "the committed row of a client answer has no user")
			assert.Equal(t, models.QuoteChangeSourceClient, last.Source)
			assert.Nil(t, last.ChangedBy)

			rows = env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].UsedAt, "the token is spent once")
			require.NotNil(t, rows[0].Decision)
			assert.Equal(t, wantDecision, *rows[0].Decision, "the token records the winner's decision")

			dealAfter := env.dealRow()
			assert.Equal(t, dealBefore.Stage, dealAfter.Stage)
			assert.Equal(t, dealBefore.ClosedAt, dealAfter.ClosedAt)

			_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
			assert.True(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "a later answer is rejected: %v", err)
			assert.Len(t, env.history(quote.ID), historyBefore+1)
		}
		t.Logf("outcomes over %d rounds: %v", quoteRaceRounds, outcomes)
	})

	// Two different quotes share no quotes row, so nothing serialises their
	// mints. An invalidation that locked through the quote_id index took a
	// gap lock on MySQL and MariaDB, and the two inserts then deadlocked
	// (error 1213) on a first email of each quote.
	t.Run("two emails of two quotes at once both mint", func(t *testing.T) {
		for round := 0; round < quoteRaceRounds*4; round++ {
			first := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
			second := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
			env.mailer.reset(nil, nil)

			firstErr, secondErr := raceTwo(t,
				func() error {
					_, err := env.svc.Email(first.ID)
					return err
				},
				func() error {
					_, err := env.svc.Email(second.ID)
					return err
				},
			)
			require.NoErrorf(t, firstErr, "round %d: the first quote's email", round)
			require.NoErrorf(t, secondErr, "round %d: the second quote's email", round)
			require.Len(t, env.mailer.messages(), 2, "both mails went out")
			for _, id := range []uint{first.ID, second.ID} {
				rows := env.tokenRows(id)
				require.Lenf(t, rows, 1, "quote %d has the one row of its email", id)
				assert.Nil(t, rows[0].UsedAt, "and it is unused")
			}

			// Odd rounds email both again at once: each invalidation now
			// stamps an existing row while the other transaction inserts.
			if round%2 == 1 {
				firstErr, secondErr = raceTwo(t,
					func() error {
						_, err := env.svc.Email(first.ID)
						return err
					},
					func() error {
						_, err := env.svc.Email(second.ID)
						return err
					},
				)
				require.NoErrorf(t, firstErr, "round %d: the first quote's second email", round)
				require.NoErrorf(t, secondErr, "round %d: the second quote's second email", round)
				for _, id := range []uint{first.ID, second.ID} {
					require.Lenf(t, env.tokenRows(id), 2, "quote %d has two rows", id)
					require.Lenf(t, env.unusedTokenRows(id), 1, "quote %d has exactly one usable link", id)
				}
			}
		}
	})

	t.Run("a client answer racing a staff transition commits exactly one", func(t *testing.T) {
		for round := 0; round < quoteRaceRounds; round++ {
			quote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
			token := env.emailForToken(quote.ID)
			historyBefore := len(env.history(quote.ID))

			clientErr, staffErr := raceTwo(t,
				func() error {
					_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
					return err
				},
				func() error {
					_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusRejected, env.owner.ID)
					return err
				},
			)
			committed := env.quote(quote.ID)
			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].UsedAt, "the token is dead either way")
			switch {
			case clientErr == nil:
				require.Truef(t, errors.Is(staffErr, apperrors.ErrInvalidQuoteTransition), "the staff move loses: %v", staffErr)
				assert.Equal(t, models.QuoteStatusAccepted, committed.Status)
				require.NotNil(t, rows[0].Decision)
				assert.Equal(t, models.QuoteResponseAccept, *rows[0].Decision)
			default:
				require.NoError(t, staffErr, "one of the two commits")
				require.Truef(t, errors.Is(clientErr, apperrors.ErrQuoteResponseRejected), "the client loses with the generic rejection: %v", clientErr)
				assert.Equal(t, models.QuoteStatusRejected, committed.Status)
				assert.Nil(t, rows[0].Decision, "the staff transition invalidated the token")
			}
			assert.Len(t, env.history(quote.ID), historyBefore+1, "exactly one committed history row")
		}
	})
}
