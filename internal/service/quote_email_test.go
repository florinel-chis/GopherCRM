package service

// The quote email (issue #158, AC-15): one Send to the customer's current
// address with the fixed subject and a plaintext rendering of the quote; the
// refusals that send nothing; a mailer failure that stamps nothing; the
// conditional stamp that a revision during the send defeats; and logs that
// carry ids only.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	_, err := e.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: e.product.ID, Quantity: models.QuoteQuantityFromUnits(2), DiscountBP: 1250})
	require.NoError(t, err)
	_, err = e.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: e.product.ID, Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: int64Ptr(999)})
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
		assert.Equal(t, QuoteEmailSubject(quote.ID, DefaultMailProductName), mail.Subject)
		for _, fragment := range []string{
			fmt.Sprintf("Quote Q-%06d", quote.ID),
			"Title: Engine licences",
			"Currency: EUR",
			"Valid until: " + *models.FormatDealDate(quote.ValidUntil),
			"1. Widget | quantity 2 x 10.00 EUR | discount 12.50% | line total 17.50 EUR | tax 0.00% 0.00 EUR",
			"2. Widget | quantity 1 x 9.99 EUR | discount 0.00% | line total 9.99 EUR | tax 0.00% 0.00 EUR",
			"Subtotal: 29.99 EUR",
			"Discount: 2.50 EUR",
			"Net total: 27.49 EUR",
			"Tax: 0.00 EUR",
			"Gross total: 27.49 EUR",
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
			_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(5)})
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
		assert.Contains(t, messages[0].Body, "Net total: "+FormatMinorUnits(staleTotal, "EUR"), "with the rendering read before the revision")

		committed := env.quote(quote.ID)
		assert.Nil(t, committed.LastEmailedAt, "last_emailed_at stays unchanged")
		assert.Equal(t, models.QuoteStatusSent, committed.Status)
		assert.Greater(t, committed.TotalCents, staleTotal, "the committed quote carries the revision")

		// Emailed again, with nothing racing, the fresh rendering is stamped.
		env.mailer.reset(nil, nil)
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.NotNil(t, emailed.LastEmailedAt)
		assert.Contains(t, env.mailer.messages()[0].Body, "Net total: "+FormatMinorUnits(committed.TotalCents, "EUR"))
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

// flippingReadRepository runs change once, right after the service's plain
// read of the quote, so the change commits between that read and the
// transaction that mints the response link.
type flippingReadRepository struct {
	repository.QuoteRepository
	change func()
	fired  bool
}

func (r *flippingReadRepository) GetByID(id uint) (*models.Quote, error) {
	quote, err := r.QuoteRepository.GetByID(id)
	if err == nil && !r.fired {
		r.fired = true
		r.change()
	}
	return quote, err
}

// The email of a sent quote carries a single-use client response link
// (#382): one usable token row per quote, only its HMAC-SHA256 hash stored,
// earlier rows stamped, the plain token in no log line and no error text. An
// accepted quote and a sent quote past its valid_until are emailed without a
// link, and a quote that changed between the read and the mint is refused
// before anything is sent.
func TestQuoteEmailResponseLink(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteResponseEnv(t, db)
	output, _ := capturePrivacyLogs(t)

	t.Run("a sent quote is mailed with a link whose hash is the only stored form", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, nil)
		output.Reset()
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		require.NotNil(t, emailed.LastEmailedAt)

		messages := env.mailer.messages()
		require.Len(t, messages, 1)
		body := messages[0].Body
		token := responseTokenFromBody(t, body)
		assert.Contains(t, body, quoteResponseTestBaseURL+"/quote-response?token="+token)
		assert.NotContains(t, body, "//quote-response", "a trailing slash of APP_BASE_URL is not doubled")
		assert.Equal(t, 1, strings.Count(body, "quote-response?token="), "one link")
		assert.True(t, strings.HasPrefix(body, RenderQuoteEmail(quote)), "the rendering of the quote comes first, unchanged")
		assert.True(t, strings.HasSuffix(strings.TrimSpace(body), token), "the link is the final block")
		assert.GreaterOrEqual(t, len(token), 43, "a 256-bit token")

		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].UsedAt)
		assert.Nil(t, rows[0].Decision)
		mac := hmac.New(sha256.New, []byte(quoteResponseTestSecret))
		mac.Write([]byte(token))
		assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), rows[0].TokenHash, "token_hash is the HMAC-SHA256 hex of the mailed token")
		assert.NotEqual(t, token, rows[0].TokenHash)
		require.NotNil(t, rows[0].ExpiresAt, "the quote has a valid_until")
		day := *quote.ValidUntil
		assert.True(t, rows[0].ExpiresAt.Equal(time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.Local)))

		logs := output.String()
		require.NotEmpty(t, logs, "the send is logged at debug level")
		assert.NotContains(t, logs, token, "no log line carries the plain token")
		assert.NotContains(t, logs, rows[0].TokenHash)
		assert.NotContains(t, logs, "quote-response")

		// A second email mints a new token and stamps the first.
		second := env.emailForToken(quote.ID)
		require.NotEqual(t, token, second)
		rows = env.tokenRows(quote.ID)
		require.Len(t, rows, 2)
		require.NotNil(t, rows[0].UsedAt, "the earlier unused row is stamped")
		assert.Nil(t, rows[0].Decision, "without a decision")
		unused := env.unusedTokenRows(quote.ID)
		require.Len(t, unused, 1, "exactly one usable row")
		assert.Equal(t, hashOpaqueToken(second, quoteResponseTestSecret), unused[0].TokenHash)
		_, err = env.svc.GetPublicResponse(token)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "the first link is dead: %v", err)
		_, err = env.svc.GetPublicResponse(second)
		require.NoError(t, err)
	})

	t.Run("an accepted quote is mailed without a link and stores no row", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.NoError(t, err)
		env.mailer.reset(nil, nil)
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.NotNil(t, emailed.LastEmailedAt)
		messages := env.mailer.messages()
		require.Len(t, messages, 1)
		assert.Equal(t, RenderQuoteEmail(env.quote(quote.ID)), messages[0].Body, "the body is the rendering alone")
		assert.NotContains(t, messages[0].Body, "quote-response")
		assert.Empty(t, env.tokenRows(quote.ID))
	})

	t.Run("a sent quote past its valid_until is mailed without a link", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, -1) })
		env.mailer.reset(nil, nil)
		_, err := env.svc.Email(quote.ID)
		require.NoError(t, err, "the quote is still emailable, as before")
		messages := env.mailer.messages()
		require.Len(t, messages, 1)
		assert.NotContains(t, messages[0].Body, "quote-response", "a link that would be dead on arrival is not mailed")
		assert.Empty(t, env.tokenRows(quote.ID), "and nothing is stored for it")

		// On the valid_until day itself the link is still minted.
		today := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
		token := env.emailForToken(today.ID)
		_, err = env.svc.GetPublicResponse(token)
		require.NoError(t, err)
	})

	t.Run("a quote without valid_until gets a link without an expiry", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1)
		env.emailForToken(quote.ID)
		rows := env.tokenRows(quote.ID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].ExpiresAt)
	})

	t.Run("a failed send keeps the token out of the error and the logs", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		var mailed string
		env.mailer.reset(nil, func(_, _, body string) error {
			mailed = body
			return errors.New("451 4.3.0 temporary failure")
		})
		output.Reset()
		_, err := env.svc.Email(quote.ID)
		require.Error(t, err)
		require.True(t, errors.Is(err, apperrors.ErrMailDelivery), "%v", err)
		token := responseTokenFromBody(t, mailed)
		assert.NotContains(t, err.Error(), token, "the wrapped delivery error does not carry the token")
		assert.NotContains(t, err.Error(), "quote-response")
		assert.NotContains(t, output.String(), token)
		assert.NotContains(t, output.String(), "quote-response")
		assert.Empty(t, env.mailer.messages())
		assert.Nil(t, env.quote(quote.ID).LastEmailedAt)
		// The stored hash stays: its plain value was never delivered, and the
		// next email stamps it.
		require.Len(t, env.unusedTokenRows(quote.ID), 1)
		env.emailForToken(quote.ID)
		assert.Len(t, env.tokenRows(quote.ID), 2)
		assert.Len(t, env.unusedTokenRows(quote.ID), 1)
	})

	t.Run("a quote that left sent before the mint is refused without a token or a send", func(t *testing.T) {
		for _, to := range []models.QuoteStatus{models.QuoteStatusAccepted, models.QuoteStatusDraft} {
			quote := env.sentQuoteWithLines(t)
			hooked := &flippingReadRepository{QuoteRepository: env.repo}
			hooked.change = func() {
				_, err := env.svc.ChangeStatus(quote.ID, to, env.owner.ID)
				require.NoError(t, err)
			}
			env.mailer.reset(nil, nil)
			_, err := env.responseService(hooked).Email(quote.ID)
			require.Error(t, err, to)
			assert.Truef(t, errors.Is(err, apperrors.ErrQuoteNotSent), "%s: %v", to, err)
			require.True(t, hooked.fired)
			assert.Empty(t, env.mailer.messages(), "%s: nothing is sent", to)
			assert.Empty(t, env.tokenRows(quote.ID), "%s: no token row", to)
			assert.Nil(t, env.quote(quote.ID).LastEmailedAt)
		}
	})

	t.Run("a quote revised and re-sent before the mint is refused without a token or a send", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		hooked := &flippingReadRepository{QuoteRepository: env.repo}
		hooked.change = func() {
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusDraft, env.owner.ID)
			require.NoError(t, err)
			_, err = env.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteQuantityFromUnits(5)})
			require.NoError(t, err)
			_, err = env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
			require.NoError(t, err)
		}
		env.mailer.reset(nil, nil)
		_, err := env.responseService(hooked).Email(quote.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteChangedDuringSend), "the old rendering must not go out with a live link: %v", err)
		assert.Empty(t, env.mailer.messages(), "nothing is sent")
		assert.Empty(t, env.tokenRows(quote.ID), "no token row")
		assert.Equal(t, models.QuoteStatusSent, env.quote(quote.ID).Status)

		// Emailed again, with nothing racing, the revised quote gets its link.
		token := env.emailForToken(quote.ID)
		view, err := env.svc.GetPublicResponse(token)
		require.NoError(t, err)
		assert.Len(t, view.Lines, 3)
	})

	t.Run("a revision during the send leaves a link that the revision already killed", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		var mailed string
		env.mailer.reset(nil, func(_, _, body string) error {
			mailed = body
			if _, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusDraft, env.owner.ID); err != nil {
				return err
			}
			_, err := env.svc.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
			return err
		})
		_, err := env.svc.Email(quote.ID)
		require.True(t, errors.Is(err, apperrors.ErrQuoteChangedDuringSend), "%v", err)
		env.assertResponseRejected(quote.ID, responseTokenFromBody(t, mailed), "the link mailed before the revision")
	})

	t.Run("a service without response links mails no link", func(t *testing.T) {
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, nil)
		_, err := env.service(env.repo).Email(quote.ID)
		require.NoError(t, err)
		messages := env.mailer.messages()
		require.Len(t, messages, 1)
		assert.Equal(t, RenderQuoteEmail(quote), messages[0].Body)
		assert.Empty(t, env.tokenRows(quote.ID))
	})
}

// The quote email attaches the PDF when PDF output is on and the mailer can
// carry one (#384); a failed or late render sends the same mail without it,
// stamps as before, returns no error, and logs nothing personal.
func TestQuoteEmailPDFAttachment(t *testing.T) {
	t.Run("attached", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
		env, attaching := newQuotePDFEnv(t, renderer, 5*time.Second)
		quote := env.sentQuoteWithLines(t)
		attaching.resetAll()

		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		require.NotNil(t, emailed.LastEmailedAt)

		require.Len(t, env.mailer.messages(), 1, "one mail")
		sent := attaching.attached()
		require.Len(t, sent, 1, "through SendWithAttachment, with exactly one attachment")
		mail := sent[0]
		assert.Equal(t, env.customer.Email, mail.To)
		assert.Equal(t, QuoteEmailSubject(quote.ID, DefaultMailProductName), mail.Subject)
		assert.Equal(t, fmt.Sprintf("Quote-Q-%06d.pdf", quote.ID), mail.Attachment.Filename)
		assert.Equal(t, "application/pdf", mail.Attachment.ContentType)
		assert.Equal(t, quotePDFTestBytes, mail.Attachment.Data, "the renderer's bytes")

		// The text body is the one a service without PDF output sends: the
		// rendering, then the response link.
		assert.True(t, strings.HasPrefix(mail.Body, RenderQuoteEmail(env.quote(quote.ID))), "the unchanged rendering")
		token := responseTokenFromBody(t, mail.Body)
		assert.Equal(t, RenderQuoteEmail(env.quote(quote.ID))+renderQuoteResponseLink(quoteResponseLinkPrefix+token), mail.Body)
		assert.NotContains(t, mail.Body, "quote-render", "the render URL is not part of the mail")
		assert.EqualValues(t, 1, renderer.calls.Load())
	})

	t.Run("a mailer without attachment support sends the plain mail and renders nothing", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
		env, attaching := newQuotePDFEnv(t, renderer, 5*time.Second)
		env.svc.mailer = env.mailer // Mailer only
		quote := env.sentQuoteWithLines(t)
		attaching.resetAll()

		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		require.NotNil(t, emailed.LastEmailedAt)
		assert.Len(t, env.mailer.messages(), 1)
		assert.Empty(t, attaching.attached())
		assert.Zero(t, renderer.calls.Load())
	})

	fallback := func(t *testing.T, renderer *fakeQuotePDFRenderer, bound time.Duration, wantReason string) {
		env, attaching := newQuotePDFEnv(t, renderer, bound)
		quote := env.sentQuoteWithLines(t)
		attaching.resetAll()
		output, hook := capturePrivacyLogs(t)

		started := time.Now()
		emailed, err := env.svc.Email(quote.ID)
		elapsed := time.Since(started)
		require.NoError(t, err, "a failed render is not an error of the email")
		require.NotNil(t, emailed.LastEmailedAt, "last_emailed_at is stamped")
		assert.NotNil(t, env.quote(quote.ID).LastEmailedAt)
		assert.Less(t, elapsed, bound+3*time.Second)

		messages := env.mailer.messages()
		require.Len(t, messages, 1, "the mail is still sent once")
		assert.Empty(t, attaching.attached(), "without an attachment")
		assert.Equal(t, env.customer.Email, messages[0].To)
		assert.True(t, strings.HasPrefix(messages[0].Body, RenderQuoteEmail(env.quote(quote.ID))))
		responseTokenFromBody(t, messages[0].Body)

		urls := renderer.pageURLs()
		require.Len(t, urls, 1)
		credential := quoteRenderTokenFromURL(urls[0])
		require.NotEmpty(t, credential)
		logs := output.String()
		for _, needle := range []string{urls[0], credential, "quote-render", env.customer.Email, env.customer.FirstName, env.customer.LastName} {
			assert.NotContains(t, logs, needle)
		}

		var warned []*logrus.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Quote emailed without a PDF attachment" {
				warned = append(warned, entry)
			}
		}
		require.Len(t, warned, 1, "one warn line for the fallback")
		assert.Equal(t, logrus.WarnLevel, warned[0].Level)
		assert.Equal(t, quote.ID, warned[0].Data["quote_id"])
		assert.Equal(t, env.customer.ID, warned[0].Data["customer_id"])
		assert.Equal(t, wantReason, warned[0].Data["reason"])
	}

	t.Run("renderer error", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{redeem: true, err: errors.New("browser crashed")}
		fallback(t, renderer, 5*time.Second, quotePDFReasonRenderer)
	})

	t.Run("timeout", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true, block: make(chan struct{})}
		defer close(renderer.block)
		fallback(t, renderer, 300*time.Millisecond, quotePDFReasonTimeout)
	})

	t.Run("PDF off logs the fallback once and sends through Send", func(t *testing.T) {
		env := newQuoteResponseEnv(t, openQuoteTestSQLite(t))
		quote := env.sentQuoteWithLines(t)
		env.mailer.reset(nil, nil)
		_, hook := capturePrivacyLogs(t)
		_, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.Len(t, env.mailer.messages(), 1)
		var reasons []interface{}
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Quote emailed without a PDF attachment" {
				reasons = append(reasons, entry.Data["reason"])
				assert.Equal(t, quote.ID, entry.Data["quote_id"])
			}
		}
		assert.Equal(t, []interface{}{"pdf_off"}, reasons)
	})

	t.Run("a refused email renders nothing", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
		env, attaching := newQuotePDFEnv(t, renderer, 5*time.Second)
		draft := env.newQuote(models.QuoteStatusDraft, 1)
		attaching.resetAll()
		_, err := env.svc.Email(draft.ID)
		require.ErrorIs(t, err, apperrors.ErrQuoteNotSent)
		assert.Zero(t, renderer.calls.Load())
		assert.Empty(t, env.mailer.messages())
	})
}
