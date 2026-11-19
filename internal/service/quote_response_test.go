package service

// The client response link of a quote email (#382): the read that never
// spends the token, the decision that claims it and moves the quote in one
// transaction, the rollback that hands the link back, and every way a link
// dies. The roots run on the one-connection SQLite pool; the concurrency
// roots are in quote_response_regression_test.go.

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	quoteResponseTestBaseURL = "https://crm.example.test"
	quoteResponseTestSecret  = "quote-response-test-secret-0123456789"
	quoteResponseLinkPrefix  = quoteResponseTestBaseURL + "/quote-response?token="
)

// responseService wires a quote service with response links over the given
// quote repository; the base URL carries a trailing slash, which the link
// must not double.
func (e *quoteTestEnv) responseService(quotes repository.QuoteRepository) *quoteService {
	return NewQuoteServiceWithResponseLinks(quotes, repository.NewProductRepository(e.db), repository.NewDealRepository(e.db),
		repository.NewCustomerRepository(e.db), repository.NewUserRepository(e.db), nil, e.mailer,
		utils.NewTransactionManager(e.db), repository.NewQuoteResponseTokenRepository(e.db),
		quoteResponseTestBaseURL+"/", quoteResponseTestSecret).(*quoteService)
}

// newQuoteResponseEnv is newQuoteTestEnv with the service that mints links.
func newQuoteResponseEnv(t *testing.T, db *gorm.DB) *quoteTestEnv {
	t.Helper()
	env := newQuoteTestEnv(t, db)
	env.svc = env.responseService(env.repo)
	return env
}

// responseTokenFromBody extracts the plain token from the link of a mail body.
func responseTokenFromBody(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, quoteResponseLinkPrefix)
	require.GreaterOrEqual(t, i, 0, "the mail carries a response link")
	fields := strings.Fields(body[i+len(quoteResponseLinkPrefix):])
	require.NotEmpty(t, fields)
	require.NotEmpty(t, fields[0])
	return fields[0]
}

// emailForToken emails the quote and returns the token of the link it mailed.
func (e *quoteTestEnv) emailForToken(id uint) string {
	e.t.Helper()
	e.mailer.reset(nil, nil)
	_, err := e.svc.Email(id)
	require.NoError(e.t, err)
	messages := e.mailer.messages()
	require.Len(e.t, messages, 1)
	return responseTokenFromBody(e.t, messages[0].Body)
}

// tokenRows are the committed response tokens of the quote, oldest first.
func (e *quoteTestEnv) tokenRows(quoteID uint) []models.QuoteResponseToken {
	e.t.Helper()
	var rows []models.QuoteResponseToken
	require.NoError(e.t, e.db.Where("quote_id = ?", quoteID).Order("id ASC").Find(&rows).Error)
	return rows
}

// unusedTokenRows are the quote's rows with used_at NULL.
func (e *quoteTestEnv) unusedTokenRows(quoteID uint) []models.QuoteResponseToken {
	e.t.Helper()
	var unused []models.QuoteResponseToken
	for _, row := range e.tokenRows(quoteID) {
		if row.UsedAt == nil {
			unused = append(unused, row)
		}
	}
	return unused
}

// responseState is everything a refused response must leave unchanged: the
// quote and its lines, its history, its token rows (hash included) and its
// deal, rendered for comparison.
func (e *quoteTestEnv) responseState(quoteID uint) string {
	e.t.Helper()
	var tokens, history, deals []map[string]interface{}
	require.NoError(e.t, e.db.Table("quote_response_tokens").Where("quote_id = ?", quoteID).Order("id").Find(&tokens).Error)
	require.NoError(e.t, e.db.Table("quote_status_changes").Where("quote_id = ?", quoteID).Order("id").Find(&history).Error)
	require.NoError(e.t, e.db.Table("deals").Where("id = ?", e.deal.ID).Find(&deals).Error)
	// Unscoped: a soft-deleted quote is still part of the state.
	var quotes []map[string]interface{}
	require.NoError(e.t, e.db.Table("quotes").Where("id = ?", quoteID).Find(&quotes).Error)
	encoded, err := json.Marshal(map[string]interface{}{"quote": quotes, "tokens": tokens, "history": history, "deal": deals})
	require.NoError(e.t, err)
	return string(encoded)
}

func (e *quoteTestEnv) dealRow() models.Deal {
	e.t.Helper()
	var deal models.Deal
	require.NoError(e.t, e.db.First(&deal, e.deal.ID).Error)
	return deal
}

// assertResponseRejected calls both public methods with the token and checks
// that each answers the one rejection and that nothing was written.
func (e *quoteTestEnv) assertResponseRejected(quoteID uint, token, why string) {
	e.t.Helper()
	before := e.responseState(quoteID)
	view, err := e.svc.GetPublicResponse(token)
	assert.Nil(e.t, view, why)
	assert.Truef(e.t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "GetPublicResponse, %s: %v", why, err)
	for _, decision := range []string{models.QuoteResponseAccept, models.QuoteResponseDecline} {
		view, err = e.svc.RespondPublic(token, decision)
		assert.Nil(e.t, view, why)
		assert.Truef(e.t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "RespondPublic %s, %s: %v", decision, why, err)
	}
	assert.Equal(e.t, before, e.responseState(quoteID), "a refused response writes nothing (%s)", why)
}

// assertNoTokenInLogs checks the captured debug-level log output.
func assertNoTokenInLogs(t *testing.T, logs string, tokens ...string) {
	t.Helper()
	require.NotEmpty(t, logs, "the calls were logged")
	for _, token := range tokens {
		require.NotEmpty(t, token)
		assert.NotContains(t, logs, token, "no log line carries the plain token")
		assert.NotContains(t, logs, hashOpaqueToken(token, quoteResponseTestSecret), "nor its hash")
	}
	assert.NotContains(t, logs, "quote-response", "no log line carries the link")
}

func TestQuoteResponseView(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteResponseEnv(t, db)
	output, _ := capturePrivacyLogs(t)

	quote := env.sentQuoteWithLines(t)
	token := env.emailForToken(quote.ID)
	rowsBefore := env.tokenRows(quote.ID)
	require.Len(t, rowsBefore, 1)

	t.Run("a valid token shows exactly the public view and is not spent", func(t *testing.T) {
		stateBefore := env.responseState(quote.ID)
		for i := 0; i < 2; i++ {
			view, err := env.svc.GetPublicResponse(token)
			require.NoError(t, err)
			assert.Equal(t, quote.Number(), view.Number)
			assert.Equal(t, models.QuoteStatusSent, view.Status)
			assert.Equal(t, "EUR", view.Currency)
			require.NotNil(t, view.ValidUntil)
			assert.Equal(t, *models.FormatDealDate(quote.ValidUntil), *view.ValidUntil)
			require.Len(t, view.Lines, 2)
			assert.Equal(t, models.QuotePublicLine{Description: "Widget", Quantity: models.QuoteQuantityFromUnits(2),
				UnitPriceCents: 1000, DiscountBP: 1250, LineTotalCents: 1750}, view.Lines[0])
			assert.Equal(t, int64(999), view.Lines[1].UnitPriceCents)
			assert.Equal(t, int64(2999), view.SubtotalCents)
			assert.Equal(t, int64(250), view.DiscountCents)
			assert.Equal(t, int64(2749), view.TotalCents)
			assert.Equal(t, quote.TaxCents, view.TaxCents)
			assert.Equal(t, quote.GrossTotalCents, view.GrossTotalCents)

			// The serialised view: the ten keys, and nothing that names the
			// title, the notes, the owner, the customer, the deal or an id.
			encoded, err := json.Marshal(view)
			require.NoError(t, err)
			var keys map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &keys))
			names := make([]string, 0, len(keys))
			for key := range keys {
				names = append(names, key)
			}
			sort.Strings(names)
			assert.Equal(t, []string{"currency", "discount_cents", "gross_total_cents", "lines", "number", "status",
				"subtotal_cents", "tax_cents", "total_cents", "valid_until"}, names)
			for _, private := range []string{"Engine licences", "Delivery within two weeks", env.customer.FirstName, env.customer.Email,
				`"id"`, "owner", "customer", "deal", "product", "notes", "title"} {
				assert.NotContains(t, string(encoded), private)
			}
		}
		assert.Equal(t, stateBefore, env.responseState(quote.ID), "two reads write nothing")
		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].UsedAt, "used_at stays NULL")
		assert.Nil(t, rows[0].Decision)
	})

	t.Run("an unknown or empty token is rejected", func(t *testing.T) {
		for _, bad := range []string{"", "not-a-token", token + "x", token[:len(token)-1], hashOpaqueToken(token, quoteResponseTestSecret)} {
			view, err := env.svc.GetPublicResponse(bad)
			assert.Nil(t, view)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "%q: %v", bad, err)
		}
		_, err := env.svc.GetPublicResponse(token)
		require.NoError(t, err, "the real token is still usable")
	})

	t.Run("an expired token is rejected", func(t *testing.T) {
		expired := env.sentQuoteWithLines(t)
		expiredToken := env.emailForToken(expired.ID)
		_, err := env.svc.GetPublicResponse(expiredToken)
		require.NoError(t, err)
		env.svc.now = func() time.Time { return time.Now().AddDate(0, 0, 32) }
		defer func() { env.svc.now = time.Now }()
		env.assertResponseRejected(expired.ID, expiredToken, "past valid_until")
		assertNoTokenInLogs(t, output.String(), expiredToken)
	})

	t.Run("a used token is rejected", func(t *testing.T) {
		_, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
		require.NoError(t, err)
		env.assertResponseRejected(quote.ID, token, "already used")
	})

	t.Run("a service without response links rejects both methods", func(t *testing.T) {
		plain := env.service(env.repo)
		other := env.sentQuoteWithLines(t)
		otherToken := env.emailForToken(other.ID)
		_, err := plain.GetPublicResponse(otherToken)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "%v", err)
		_, err = plain.RespondPublic(otherToken, models.QuoteResponseAccept)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "%v", err)
		_, err = env.svc.GetPublicResponse(otherToken)
		require.NoError(t, err, "and spends nothing")
		assertNoTokenInLogs(t, output.String(), otherToken)
	})

	assertNoTokenInLogs(t, output.String(), token)
}

func TestQuoteResponseDecision(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteResponseEnv(t, db)
	output, hook := capturePrivacyLogs(t)

	for decision, want := range map[string]models.QuoteStatus{
		models.QuoteResponseAccept:  models.QuoteStatusAccepted,
		models.QuoteResponseDecline: models.QuoteStatusRejected,
	} {
		decision, want := decision, want
		t.Run(decision, func(t *testing.T) {
			quote := env.sentQuoteWithLines(t)
			require.NotNil(t, quote.SentAt)
			token := env.emailForToken(quote.ID)
			emailed := env.quote(quote.ID)
			dealBefore := env.dealRow()
			historyBefore := env.history(quote.ID)
			clock := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Millisecond)
			env.svc.now = func() time.Time { return clock }
			defer func() { env.svc.now = time.Now }()

			// A decision outside the two is a validation failure and leaves
			// the link usable.
			view, err := env.svc.RespondPublic(token, "maybe")
			assert.Nil(t, view)
			require.True(t, errors.Is(err, apperrors.ErrValidation), "%v", err)
			assert.False(t, errors.Is(err, apperrors.ErrQuoteResponseRejected))
			require.Len(t, env.unusedTokenRows(quote.ID), 1, "the token stays usable")

			view, err = env.svc.RespondPublic(token, decision)
			require.NoError(t, err)
			assert.Equal(t, want, view.Status, "the answer carries the new status")
			assert.Equal(t, quote.Number(), view.Number)
			assert.Len(t, view.Lines, 2)

			committed := env.quote(quote.ID)
			assert.Equal(t, want, committed.Status)
			require.NotNil(t, committed.ClosedAt, "closed_at is set")
			assert.WithinDuration(t, clock, *committed.ClosedAt, time.Millisecond)
			require.NotNil(t, committed.SentAt)
			assert.WithinDuration(t, *quote.SentAt, *committed.SentAt, time.Millisecond, "sent_at is kept")
			require.NotNil(t, committed.LastEmailedAt)
			assert.WithinDuration(t, *emailed.LastEmailedAt, *committed.LastEmailedAt, time.Millisecond)
			assert.Equal(t, quote.TotalCents, committed.TotalCents)

			history := env.history(quote.ID)
			require.Len(t, history, len(historyBefore)+1, "exactly one new history row")
			last := history[len(history)-1]
			require.NotNil(t, last.FromStatus)
			assert.Equal(t, models.QuoteStatusSent, *last.FromStatus)
			assert.Equal(t, want, last.ToStatus)
			assert.Nil(t, last.ChangedByID, "the history row of a client answer has no user")
			assert.Equal(t, models.QuoteChangeSourceClient, last.Source)
			assert.Nil(t, last.ChangedBy, "no user is preloaded for a client answer")
			for _, earlier := range history[:len(history)-1] {
				assert.Equal(t, models.QuoteChangeSourceStaff, earlier.Source, "the staff rows read as before")
				assert.NotNil(t, earlier.ChangedByID)
			}
			assert.WithinDuration(t, clock, last.ChangedAt, time.Millisecond)

			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].UsedAt, "the token is spent")
			assert.WithinDuration(t, clock, *rows[0].UsedAt, time.Millisecond)
			require.NotNil(t, rows[0].Decision)
			assert.Equal(t, decision, *rows[0].Decision)

			dealAfter := env.dealRow()
			assert.Equal(t, dealBefore.Stage, dealAfter.Stage, "the deal's stage is unchanged")
			assert.Equal(t, dealBefore.ClosedAt, dealAfter.ClosedAt, "and so is its closed_at")
			assert.Equal(t, dealBefore.UpdatedAt, dealAfter.UpdatedAt, "the deal row is not written")

			// The token is single use: a second answer, either way, and a
			// read are the rejection and write nothing.
			env.assertResponseRejected(quote.ID, token, "second use")

			var answered bool
			for _, entry := range hook.AllEntries() {
				if entry.Message == "Quote answered through the client response link" && entry.Data["quote_id"] == quote.ID {
					answered = true
				}
			}
			assert.True(t, answered, "the success is logged with the quote id")
			assertNoTokenInLogs(t, output.String(), token)
		})
	}
}

// failOnceHistoryRepository refuses the first history write it is asked for
// and passes the later ones on, so the same service can fail and then succeed.
type failOnceHistoryRepository struct {
	repository.QuoteRepository
	failed *atomic.Bool
}

func (r *failOnceHistoryRepository) WithTx(tx *gorm.DB) repository.QuoteRepository {
	return &failOnceHistoryRepository{QuoteRepository: r.QuoteRepository.WithTx(tx), failed: r.failed}
}

func (r *failOnceHistoryRepository) CreateStatusChange(change *models.QuoteStatusChange) error {
	if r.failed.CompareAndSwap(false, true) {
		return errors.New("injected: history write failed")
	}
	return r.QuoteRepository.CreateStatusChange(change)
}

// The history insert is the last write of the transaction. When it fails,
// the token claim and the status claim before it roll back: the quote is
// still sent, the token unused, and the same link then works.
func TestQuoteResponseRollbackKeepsLink(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteResponseEnv(t, db)

	quote := env.sentQuoteWithLines(t)
	token := env.emailForToken(quote.ID)
	before := env.responseState(quote.ID)
	historyBefore := len(env.history(quote.ID))

	failed := &atomic.Bool{}
	failing := env.responseService(&failOnceHistoryRepository{QuoteRepository: env.repo, failed: failed})

	view, err := failing.RespondPublic(token, models.QuoteResponseAccept)
	require.Error(t, err)
	assert.Nil(t, view)
	require.True(t, failed.Load(), "the history write was reached, after both claims")
	assert.False(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "a storage failure is not the generic rejection: %v", err)
	assert.NotContains(t, err.Error(), token)

	assert.Equal(t, before, env.responseState(quote.ID), "everything rolled back")
	assert.Equal(t, models.QuoteStatusSent, env.quote(quote.ID).Status)
	assert.Nil(t, env.quote(quote.ID).ClosedAt)
	assert.Len(t, env.history(quote.ID), historyBefore, "no history row was added")
	rows := env.tokenRows(quote.ID)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].UsedAt, "the claim was handed back")
	assert.Nil(t, rows[0].Decision)

	_, err = env.svc.GetPublicResponse(token)
	require.NoError(t, err, "the link is still shown")
	view, err = failing.RespondPublic(token, models.QuoteResponseAccept)
	require.NoError(t, err, "and the same token then succeeds")
	assert.Equal(t, models.QuoteStatusAccepted, view.Status)
	assert.Equal(t, models.QuoteStatusAccepted, env.quote(quote.ID).Status)
	assert.Len(t, env.history(quote.ID), historyBefore+1)
	rows = env.tokenRows(quote.ID)
	require.NotNil(t, rows[0].UsedAt)
	assert.Equal(t, models.QuoteResponseAccept, *rows[0].Decision)
}

func TestQuoteResponseDeadLinks(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteResponseEnv(t, db)

	// The link works through the whole valid_until day in the server's local
	// zone and dies at the first instant of the next day.
	t.Run("the valid_until day boundary, in local time", func(t *testing.T) {
		for _, decision := range []string{models.QuoteResponseAccept, models.QuoteResponseDecline} {
			quote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 3) })
			token := env.emailForToken(quote.ID)
			day := *env.quote(quote.ID).ValidUntil
			lastSecond := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.Local)
			nextDay := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.Local)

			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].ExpiresAt)
			assert.True(t, rows[0].ExpiresAt.Equal(nextDay), "expires_at %s is the start of the next local day %s", rows[0].ExpiresAt, nextDay)

			env.svc.now = func() time.Time { return nextDay }
			env.assertResponseRejected(quote.ID, token, "00:00:00 local on the day after valid_until")
			env.svc.now = func() time.Time { return nextDay.Add(48 * time.Hour) }
			env.assertResponseRejected(quote.ID, token, "two days after valid_until")

			env.svc.now = func() time.Time { return lastSecond }
			_, err := env.svc.GetPublicResponse(token)
			require.NoError(t, err, "23:59:59 local on the valid_until day still shows the quote")
			view, err := env.svc.RespondPublic(token, decision)
			require.NoError(t, err, "and still takes the answer")
			assert.NotEqual(t, models.QuoteStatusSent, view.Status)
			env.svc.now = time.Now
		}
	})

	// The expiry is the local day after valid_until whatever location the
	// stored date was read back in (MySQL without loc=Local reads a DATE as
	// UTC midnight).
	t.Run("the expiry follows the calendar date of valid_until", func(t *testing.T) {
		assert.Nil(t, quoteResponseExpiry(nil))
		want := time.Date(2027, time.March, 1, 0, 0, 0, 0, time.Local)
		for _, loc := range []*time.Location{time.UTC, time.Local, time.FixedZone("far-east", 11*3600), time.FixedZone("far-west", -9*3600)} {
			validUntil := time.Date(2027, time.February, 28, 0, 0, 0, 0, loc)
			expiry := quoteResponseExpiry(&validUntil)
			require.NotNil(t, expiry)
			assert.True(t, expiry.Equal(want), "%s: %s", loc, expiry)
			assert.Equal(t, time.UTC, expiry.Location(), "stored in UTC")
		}
	})

	t.Run("a quote without valid_until keeps its link while it is sent", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1)
		require.Nil(t, quote.ValidUntil)
		token := env.emailForToken(quote.ID)
		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].ExpiresAt, "no expiry without valid_until")

		env.svc.now = func() time.Time { return time.Now().AddDate(10, 0, 0) }
		defer func() { env.svc.now = time.Now }()
		_, err := env.svc.GetPublicResponse(token)
		require.NoError(t, err, "ten years on the link still shows the quote")
		view, err := env.svc.RespondPublic(token, models.QuoteResponseAccept)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteStatusAccepted, view.Status)
	})

	t.Run("a staff status change away from sent kills the link", func(t *testing.T) {
		for _, to := range []models.QuoteStatus{models.QuoteStatusAccepted, models.QuoteStatusRejected, models.QuoteStatusExpired, models.QuoteStatusDraft} {
			quote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 5) })
			token := env.emailForToken(quote.ID)
			_, err := env.svc.GetPublicResponse(token)
			require.NoError(t, err)

			_, err = env.svc.ChangeStatus(quote.ID, to, env.owner.ID)
			require.NoError(t, err)
			rows := env.tokenRows(quote.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].UsedAt, "%s: the transition stamped the token", to)
			assert.Nil(t, rows[0].Decision, "%s: an invalidated row carries no decision", to)
			env.assertResponseRejected(quote.ID, token, "after sent→"+string(to))
			assert.Equal(t, to, env.quote(quote.ID).Status)
		}
	})

	t.Run("a link stays dead after draft and sent again until the next email", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1)
		token := env.emailForToken(quote.ID)
		for _, to := range []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusSent} {
			_, err := env.svc.ChangeStatus(quote.ID, to, env.owner.ID)
			require.NoError(t, err)
		}
		require.Equal(t, models.QuoteStatusSent, env.quote(quote.ID).Status)
		env.assertResponseRejected(quote.ID, token, "sent again without a new email")

		fresh := env.emailForToken(quote.ID)
		require.NotEqual(t, token, fresh)
		env.assertResponseRejected(quote.ID, token, "the old link after a new email")
		view, err := env.svc.RespondPublic(fresh, models.QuoteResponseDecline)
		require.NoError(t, err, "the link of the new email works")
		assert.Equal(t, models.QuoteStatusRejected, view.Status)
	})

	t.Run("a deleted quote kills the link", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1)
		token := env.emailForToken(quote.ID)
		// An admin may delete a quote that is not a draft.
		require.NoError(t, env.svc.Delete(quote.ID, false))
		env.assertResponseRejected(quote.ID, token, "the quote was deleted")
		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].UsedAt, "the token row is unchanged")
		var stored models.Quote
		require.NoError(t, db.Unscoped().First(&stored, quote.ID).Error)
		assert.Equal(t, models.QuoteStatusSent, stored.Status, "the deleted row is still sent, so only deleted_at refuses it")
	})
}
