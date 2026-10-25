package service

// Concurrency roots for the quote transitions (issue #158, AC-12): two
// competing transitions commit exactly one; a line write racing the send is
// either included in the sent quote or refused; and no committed sent quote
// ever has zero lines or totals that disagree with its lines. The SQLite root
// runs on the production one-connection pool, the MySQL root on a disposable
// schema with one connection per competitor, where the FOR UPDATE reads do
// the serialising.

import (
	"errors"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// quoteRaceRounds is how many times each race is run: the interleaving is
// the scheduler's, so each run may land on either side.
const quoteRaceRounds = 6

// raceTwo starts both calls behind one barrier and returns their errors; it
// fails the test if either has not returned within the bounded wait.
func raceTwo(t *testing.T, first, second func() error) (error, error) {
	t.Helper()
	var start, done sync.WaitGroup
	start.Add(1)
	errs := make([]error, 2)
	for i, fn := range []func() error{first, second} {
		done.Add(1)
		go func(i int, fn func() error) {
			defer done.Done()
			start.Wait()
			errs[i] = fn()
		}(i, fn)
	}
	start.Done()
	finished := make(chan struct{})
	go func() {
		done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(quoteTestTimeout):
		t.Fatal("the racing calls did not finish in time")
	}
	return errs[0], errs[1]
}

func TestQuoteStatusConcurrentSQLite(t *testing.T) {
	runQuoteConcurrencyScenarios(t, openQuoteTestSQLite(t))
}

func TestQuoteStatusConcurrentMySQL(t *testing.T) {
	runQuoteConcurrencyScenarios(t, openQuoteTestMySQL(t, 4))
}

func runQuoteConcurrencyScenarios(t *testing.T, db *gorm.DB) {
	env := newQuoteTestEnv(t, db)
	other := env.createProduct("Gadget", 2500, "EUR")

	t.Run("accepted versus rejected commits exactly one", func(t *testing.T) {
		outcomes := map[models.QuoteStatus]int{}
		for round := 0; round < quoteRaceRounds; round++ {
			quote := env.newQuote(models.QuoteStatusSent, 1)
			historyBefore := len(env.history(quote.ID))

			acceptErr, rejectErr := raceTwo(t,
				func() error {
					_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusAccepted, env.owner.ID)
					return err
				},
				func() error {
					_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusRejected, env.owner.ID)
					return err
				},
			)
			winners := 0
			for _, err := range []error{acceptErr, rejectErr} {
				if err == nil {
					winners++
					continue
				}
				assert.Truef(t, errors.Is(err, apperrors.ErrInvalidQuoteTransition), "the loser gets INVALID_STATUS_TRANSITION, got %v", err)
			}
			require.Equal(t, 1, winners, "exactly one transition commits (accept=%v reject=%v)", acceptErr, rejectErr)

			committed := env.assertTotalsMatchLines(quote.ID)
			switch {
			case acceptErr == nil:
				assert.Equal(t, models.QuoteStatusAccepted, committed.Status)
			default:
				assert.Equal(t, models.QuoteStatusRejected, committed.Status)
			}
			outcomes[committed.Status]++
			assert.NotNil(t, committed.ClosedAt)
			history := env.history(quote.ID)
			require.Len(t, history, historyBefore+1, "exactly one new history row")
			assert.Equal(t, committed.Status, history[len(history)-1].ToStatus)
			require.NotNil(t, history[len(history)-1].FromStatus)
			assert.Equal(t, models.QuoteStatusSent, *history[len(history)-1].FromStatus)
		}
		t.Logf("outcomes over %d rounds: %v", quoteRaceRounds, outcomes)
	})

	t.Run("a line add racing the send is included or refused", func(t *testing.T) {
		included, refused := 0, 0
		for round := 0; round < quoteRaceRounds; round++ {
			quote := env.newQuote(models.QuoteStatusDraft, 1)

			addErr, sendErr := raceTwo(t,
				func() error {
					_, err := env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: other.ID, Quantity: 3, DiscountBP: 1250})
					return err
				},
				func() error {
					_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
					return err
				},
			)
			require.NoError(t, sendErr, "the quote has a line, so the send always goes through")
			committed := env.assertTotalsMatchLines(quote.ID)
			assert.Equal(t, models.QuoteStatusSent, committed.Status)
			lines := env.lines(quote.ID)
			if addErr == nil {
				included++
				assert.Len(t, lines, 2, "a line that committed before the claim is part of the sent quote")
				_, lineTotal := models.ComputeQuoteLineTotal(3, 2500, 1250)
				assert.Equal(t, int64(2*1000)+lineTotal, committed.TotalCents)
			} else {
				refused++
				assert.Truef(t, errors.Is(addErr, apperrors.ErrQuoteLocked), "a line add after the claim is refused as locked, got %v", addErr)
				assert.Len(t, lines, 1)
			}
			// The last history row is the send, whichever side the line landed.
			history := env.history(quote.ID)
			assert.Equal(t, models.QuoteStatusSent, history[len(history)-1].ToStatus)
		}
		t.Logf("line add versus send over %d rounds: included=%d refused=%d", quoteRaceRounds, included, refused)
	})

	t.Run("deleting the only line racing the send", func(t *testing.T) {
		deletedFirst, sentFirst := 0, 0
		for round := 0; round < quoteRaceRounds; round++ {
			quote := env.newQuote(models.QuoteStatusDraft, 1)
			lineID := quote.Lines[0].ID

			deleteErr, sendErr := raceTwo(t,
				func() error { _, err := env.svc.DeleteLine(quote.ID, lineID); return err },
				func() error {
					_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
					return err
				},
			)
			committed := env.assertTotalsMatchLines(quote.ID)
			lines := env.lines(quote.ID)
			switch {
			case deleteErr == nil:
				deletedFirst++
				require.Error(t, sendErr)
				assert.Truef(t, errors.Is(sendErr, apperrors.ErrQuoteHasNoLines), "the send after the delete finds no lines, got %v", sendErr)
				assert.Equal(t, models.QuoteStatusDraft, committed.Status, "the claim was rolled back")
				assert.Nil(t, committed.SentAt)
				assert.Empty(t, lines)
				assert.Zero(t, committed.TotalCents)
				assert.Len(t, env.history(quote.ID), 1, "no history row for the rolled-back send")
			default:
				sentFirst++
				assert.Truef(t, errors.Is(deleteErr, apperrors.ErrQuoteLocked), "a delete after the send is refused as locked, got %v", deleteErr)
				require.NoError(t, sendErr)
				assert.Equal(t, models.QuoteStatusSent, committed.Status)
				assert.Len(t, lines, 1)
				assert.Len(t, env.history(quote.ID), 2)
			}
		}
		t.Logf("delete versus send over %d rounds: deleted first=%d sent first=%d", quoteRaceRounds, deletedFirst, sentFirst)
	})

	// Whatever happened above, no committed sent quote has zero lines and
	// every quote's totals equal the sum of its lines.
	var quotes []models.Quote
	require.NoError(t, db.Find(&quotes).Error)
	require.NotEmpty(t, quotes)
	for _, quote := range quotes {
		env.assertTotalsMatchLines(quote.ID)
	}
}
