package service

// The quote email (issue #158, AC-15): one Send to the customer's current
// address with the fixed subject and a plaintext rendering of the quote; the
// refusals that send nothing; a mailer failure that stamps nothing; the
// conditional stamp that a revision during the send defeats; and logs that
// carry ids only.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentQuoteWithLines is a quote taken through the real path — create, two
// lines, draft→sent — with a validity date, priced 2 × 10.00 at 12.5% off and
// 1 × 9.99.
func (e *quoteTestEnv) sentQuoteWithLines(t *testing.T) *models.Quote {
	t.Helper()
	quote := e.newQuote(models.QuoteStatusDraft, 0, func(q *models.Quote) {
		q.ValidUntil = dateDaysFromNow(t, 30)
		q.Notes = "Delivery within two weeks of acceptance."
	})
	_, err := e.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: e.product.ID, Quantity: 2, DiscountBP: 1250})
	require.NoError(t, err)
	_, err = e.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: e.product.ID, Quantity: 1, UnitPriceCents: int64Ptr(999)})
	require.NoError(t, err)
	_, err = e.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, e.owner.ID)
	require.NoError(t, err)
	return e.quote(quote.ID)
}

func TestQuoteEmailDelivery(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)

	t.Run("a sent quote is rendered and sent once to the customer's current address", func(t *testing.T) {
		env.mailer.reset(nil, nil)
		quote := env.sentQuoteWithLines(t)
		// The address as it is now, not as it was when the quote was written.
		current := fmt.Sprintf("current-%d@example.com", quote.ID)
		require.NoError(t, db.Model(&models.Customer{}).Where("id = ?", env.customer.ID).Update("email", current).Error)

		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		require.NotNil(t, emailed.LastEmailedAt, "the response carries the stamp")
		assert.NotNil(t, env.quote(quote.ID).LastEmailedAt, "and so does the committed row")

		messages := env.mailer.messages()
		require.Len(t, messages, 1, "Mailer.Send is called exactly once")
		mail := messages[0]
		assert.Equal(t, current, mail.To)
		assert.Equal(t, fmt.Sprintf("Quote Q-%06d from GopherCRM", quote.ID), mail.Subject)
		assert.Equal(t, QuoteEmailSubject(quote.ID), mail.Subject)
		for _, fragment := range []string{
			fmt.Sprintf("Quote Q-%06d", quote.ID),
			"Title: Engine licences",
			"Currency: EUR",
			"Valid until: " + *models.FormatDealDate(quote.ValidUntil),
			"1. Widget | quantity 2 x 10.00 EUR | discount 12.50% | line total 17.50 EUR",
			"2. Widget | quantity 1 x 9.99 EUR | discount 0.00% | line total 9.99 EUR",
			"Subtotal: 29.99 EUR",
			"Discount: 2.50 EUR",
			"Total: 27.49 EUR",
			"Delivery within two weeks of acceptance.",
		} {
			assert.Contains(t, mail.Body, fragment)
		}
		assert.Equal(t, int64(2999), quote.SubtotalCents)
		assert.Equal(t, int64(250), quote.DiscountCents)
		assert.Equal(t, int64(2749), quote.TotalCents)

		// An accepted quote can be emailed too.
		_, err = env.svc.ChangeStatus(quote.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.NoError(t, err)
		_, err = env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.Len(t, env.mailer.messages(), 2)
	})

	t.Run("a draft, rejected or expired quote is refused without a send", func(t *testing.T) {
		for _, status := range []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusRejected, models.QuoteStatusExpired} {
			env.mailer.reset(nil, nil)
			quote := env.newQuote(status, 1)
			_, err := env.svc.Email(quote.ID)
			require.Error(t, err, status)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteNotSent), "%s: %v", status, err)
			assert.Empty(t, env.mailer.messages(), "nothing is sent for a %s quote", status)
			assert.Nil(t, env.quote(quote.ID).LastEmailedAt)
		}
	})

	t.Run("no live recipient is refused without a send", func(t *testing.T) {
		// A quote without a customer: a deal with only a company, say.
		env.mailer.reset(nil, nil)
		orphanDeal := &models.Deal{Title: "No customer", Stage: models.DealStageProposal, Probability: 40, Currency: "EUR", OwnerID: env.owner.ID}
		require.NoError(t, db.Create(orphanDeal).Error)
		noCustomer := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.DealID = &orphanDeal.ID; q.CustomerID = nil })
		require.Nil(t, noCustomer.CustomerID)
		_, err := env.svc.Email(noCustomer.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteNoRecipient), "%v", err)

		// A customer without an address.
		blank := &models.Customer{FirstName: "No", LastName: "Address", Email: fmt.Sprintf("blank-%d@example.com", noCustomer.ID)}
		require.NoError(t, db.Create(blank).Error)
		require.NoError(t, db.Model(&models.Customer{}).Where("id = ?", blank.ID).Update("email", "  ").Error)
		blankQuote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.DealID = nil; q.CustomerID = &blank.ID })
		_, err = env.svc.Email(blankQuote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteNoRecipient), "%v", err)

		// A soft-deleted customer.
		gone := &models.Customer{FirstName: "Soft", LastName: "Deleted", Email: fmt.Sprintf("gone-%d@example.com", noCustomer.ID)}
		require.NoError(t, db.Create(gone).Error)
		goneQuote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.DealID = nil; q.CustomerID = &gone.ID })
		require.NoError(t, db.Delete(gone).Error)
		_, err = env.svc.Email(goneQuote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteNoRecipient), "%v", err)

		// An erased customer (the production delete).
		erased := &models.Customer{FirstName: "Era", LastName: "Sed", Email: fmt.Sprintf("erased-%d@example.com", noCustomer.ID)}
		require.NoError(t, db.Create(erased).Error)
		erasedQuote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.DealID = nil; q.CustomerID = &erased.ID })
		require.NoError(t, repository.NewCustomerRepository(db).Delete(erased.ID))
		_, err = env.svc.Email(erasedQuote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteNoRecipient), "%v", err)
		assert.Equal(t, &erased.ID, env.quote(erasedQuote.ID).CustomerID, "the quote keeps its link")

		assert.Empty(t, env.mailer.messages(), "no mail goes out without a live recipient")
	})

	t.Run("a mailer failure leaves last_emailed_at unchanged", func(t *testing.T) {
		env.mailer.reset(errors.New("dial tcp 127.0.0.1:25: connection refused"), nil)
		quote := env.sentQuoteWithLines(t)
		_, err := env.svc.Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrMailDelivery), "%v", err)
		assert.Nil(t, env.quote(quote.ID).LastEmailedAt)
		assert.Empty(t, env.mailer.messages())

		// Without a mailer at all the call is a delivery failure as well.
		none := env.service(env.repo)
		none.mailer = nil
		_, err = none.Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrMailDelivery), "%v", err)
		assert.Nil(t, env.quote(quote.ID).LastEmailedAt)
	})
}

// No log line written during the call carries the recipient address, the
// customer's name or the body — on success, on a transport failure whose
// error text quotes the address, and on the refusals.
func TestQuoteEmailLogsOmitPersonalData(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	output, hook := capturePrivacyLogs(t)

	quote := env.sentQuoteWithLines(t)
	recipient := env.customer.Email
	forbidden := []string{recipient, env.customer.FirstName, env.customer.LastName, "Engine licences", "Delivery within two weeks", "Widget", "17.50 EUR"}

	output.Reset()
	env.mailer.reset(nil, nil)
	_, err := env.svc.Email(quote.ID)
	require.NoError(t, err)
	success := output.String()
	require.NotEmpty(t, success, "the success is logged")
	for _, needle := range forbidden {
		assert.NotContains(t, success, needle)
	}
	var emailedEntry *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Quote emailed" {
			emailedEntry = entry
		}
	}
	require.NotNil(t, emailedEntry, "the success line is written")
	assert.Equal(t, quote.ID, emailedEntry.Data["quote_id"], "the log carries the quote id")
	assert.Equal(t, env.customer.ID, emailedEntry.Data["customer_id"], "and the customer id")
	for key := range emailedEntry.Data {
		assert.NotContains(t, []string{"to", "email", "recipient", "body", "subject"}, key, "no address or body field on the log line")
	}

	output.Reset()
	env.mailer.reset(fmt.Errorf("550 5.1.1 <%s>: recipient rejected", recipient), nil)
	_, err = env.svc.Email(quote.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), recipient, "the returned error keeps the transport's text for errors.Is and the handler")
	failure := output.String()
	require.NotEmpty(t, failure)
	for _, needle := range forbidden {
		assert.NotContains(t, failure, needle, "a transport error quoting the address must not be logged")
	}

	output.Reset()
	env.mailer.reset(nil, nil)
	draft := env.newQuote(models.QuoteStatusDraft, 1)
	_, err = env.svc.Email(draft.ID)
	require.Error(t, err)
	gone := &models.Customer{FirstName: env.customer.FirstName, LastName: env.customer.LastName, Email: "second-" + recipient}
	require.NoError(t, db.Create(gone).Error)
	goneQuote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.DealID = nil; q.CustomerID = &gone.ID })
	require.NoError(t, db.Delete(gone).Error)
	_, err = env.svc.Email(goneQuote.ID)
	require.Error(t, err)
	refusals := output.String()
	for _, needle := range forbidden {
		assert.NotContains(t, refusals, needle)
	}
	assert.False(t, strings.Contains(refusals, "second-"+recipient))
}

// The stamp is a conditional update keyed on the latest history id read
// before the send: a revision to draft that commits while the mail is in
// flight — even one followed by a fresh send — defeats it, and the call is
// answered with a conflict while last_emailed_at stays unchanged.
func TestQuoteEmailRevisedDuringSend(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)

	t.Run("sent->draft during the send", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, func(string, string, string) error {
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusDraft, env.owner.ID)
			return err
		})
		_, err := env.svc.Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteChangedDuringSend), "%v", err)
		assert.Len(t, env.mailer.messages(), 1, "the mail did go out")
		committed := env.quote(quote.ID)
		assert.Nil(t, committed.LastEmailedAt, "last_emailed_at stays unchanged")
		assert.Equal(t, models.QuoteStatusDraft, committed.Status)
	})

	t.Run("revised and re-sent during the send", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, func(string, string, string) error {
			if _, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusDraft, env.owner.ID); err != nil {
				return err
			}
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
			return err
		})
		_, err := env.svc.Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteChangedDuringSend), "the status alone would pass; the history id tells: %v", err)
		committed := env.quote(quote.ID)
		assert.Nil(t, committed.LastEmailedAt)
		assert.Equal(t, models.QuoteStatusSent, committed.Status)

		// Emailed again, with nothing racing, the fresh send is stamped.
		env.mailer.reset(nil, nil)
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.NotNil(t, emailed.LastEmailedAt)
	})

	t.Run("acceptance during the send is not a revision", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, func(string, string, string) error {
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusAccepted, env.owner.ID)
			return err
		})
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteStatusAccepted, emailed.Status)
		assert.NotNil(t, emailed.LastEmailedAt)
	})

	t.Run("an older revision does not block a later send", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		for _, to := range []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusSent} {
			_, err := env.svc.ChangeStatus(quote.ID, to, env.owner.ID)
			require.NoError(t, err)
		}
		env.mailer.reset(nil, nil)
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err, "the draft row is older than the marker read before the send")
		assert.NotNil(t, emailed.LastEmailedAt)
	})

	// The marker is read before the quote. A revision that commits between
	// the two reads — sent→draft, a new line, draft→sent — leaves a stale
	// rendering in the mail, and the stamp must refuse it exactly as it
	// refuses a revision during the send. With the reads the other way round
	// the marker would be past the revision and the stale mail would be
	// recorded as the quote's last email.
	t.Run("a revision between the marker read and the quote read is refused", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		staleTotal := quote.TotalCents
		hooked := &revisingReadRepository{QuoteRepository: env.repo}
		hooked.revise = func() {
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusDraft, env.owner.ID)
			require.NoError(t, err)
			_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: 5})
			require.NoError(t, err)
			_, err = env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
			require.NoError(t, err)
		}
		env.mailer.reset(nil, nil)
		racy := env.service(hooked)

		_, err := racy.Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteChangedDuringSend), "the stale rendering must not be stamped: %v", err)
		require.True(t, hooked.fired, "the revision must have committed right after the quote was read")

		messages := env.mailer.messages()
		require.Len(t, messages, 1, "the mail did go out")
		assert.Contains(t, messages[0].Body, "Total: "+FormatMinorUnits(staleTotal, "EUR"), "with the rendering read before the revision")

		committed := env.quote(quote.ID)
		assert.Nil(t, committed.LastEmailedAt, "last_emailed_at stays unchanged")
		assert.Equal(t, models.QuoteStatusSent, committed.Status)
		assert.Greater(t, committed.TotalCents, staleTotal, "the committed quote carries the revision")

		// Emailed again, with nothing racing, the fresh rendering is stamped.
		env.mailer.reset(nil, nil)
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.NotNil(t, emailed.LastEmailedAt)
		assert.Contains(t, env.mailer.messages()[0].Body, "Total: "+FormatMinorUnits(committed.TotalCents, "EUR"))
	})
}

// revisingReadRepository runs revise once, right after the first plain read of
// the quote returns, standing in for a writer that committed between the
// service's read of the history marker and its read of the quote. The read
// holds no transaction, so the revision commits through the ordinary service.
type revisingReadRepository struct {
	repository.QuoteRepository
	revise func()
	fired  bool
}

func (r *revisingReadRepository) GetByID(id uint) (*models.Quote, error) {
	quote, err := r.QuoteRepository.GetByID(id)
	if err == nil && !r.fired {
		r.fired = true
		r.revise()
	}
	return quote, err
}
