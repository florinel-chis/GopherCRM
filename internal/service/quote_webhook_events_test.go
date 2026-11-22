package service

// The quote events (#518): quote.created, quote.updated and quote.deleted are
// written by QuoteService inside the writing transaction, one pending row per
// active subscribed endpoint, on SQLite and on a disposable TEST_MYSQL_DSN
// schema opened with loc=Local (the MySQL roots skip without the variable and
// fail when it is unreachable). quote.updated follows the WebhookState rule
// for Update and UpdateLine, and is written by every AddLine, DeleteLine and
// committed transition (ChangeStatus, RespondPublic). quote.deleted follows
// the #531 rule: every active endpoint subscribed to it, whether or not it
// received an earlier event for the quote. Email writes none.

import (
	"context"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQuoteWebhookFanOutSQLite(t *testing.T) {
	runQuoteWebhookFanOut(t, openTicketIntakeDB(t, false))
}

func TestQuoteWebhookFanOutMySQL(t *testing.T) {
	runQuoteWebhookFanOut(t, openDealWebhookMySQLDB(t))
}

func TestQuoteWebhookAtomicitySQLite(t *testing.T) {
	runQuoteWebhookAtomicity(t, openTicketIntakeDB(t, false))
}

func TestQuoteWebhookAtomicityMySQL(t *testing.T) {
	runQuoteWebhookAtomicity(t, openDealWebhookMySQLDB(t))
}

// quoteWebhookTypes are the three quote event types.
var quoteWebhookTypes = []string{"quote.created", "quote.updated", "quote.deleted"}

// storedWebhookQuote is a fresh read of the row, soft-deleted included.
func storedWebhookQuote(t *testing.T, db *gorm.DB, id uint) *models.Quote {
	t.Helper()
	var quote models.Quote
	require.NoError(t, db.Unscoped().First(&quote, id).Error)
	return &quote
}

// storedWebhookQuoteLines are the committed lines of a quote, in id order.
func storedWebhookQuoteLines(t *testing.T, db *gorm.DB, quoteID uint) []models.QuoteLine {
	t.Helper()
	var lines []models.QuoteLine
	require.NoError(t, db.Where("quote_id = ?", quoteID).Order("id").Find(&lines).Error)
	return lines
}

// seedQuoteProduct writes an active EUR product directly.
func (e *webhookEnv) seedQuoteProduct() *models.Product {
	e.t.Helper()
	n := webhookTestSeq.Add(1)
	product := &models.Product{Name: fmt.Sprintf("Webhook product %d", n), SKU: fmt.Sprintf("WH-%d", n),
		UnitPriceCents: 1500, Currency: "EUR", IsActive: true}
	require.NoError(e.t, e.db.Create(product).Error)
	return product
}

// newWebhookQuote is a draft for a fresh customer, owned by the env owner.
func (e *webhookEnv) newWebhookQuote() *models.Quote {
	customer := e.seedCustomer()
	return &models.Quote{Title: fmt.Sprintf("Webhook quote %d", webhookTestSeq.Add(1)), Currency: "EUR",
		CustomerID: &customer.ID, OwnerID: e.owner.ID, Notes: "n"}
}

// seedQuote creates a draft through svc with `lines` lines of product, and
// moves it to sent when sent is true.
func (e *webhookEnv) seedQuote(svc QuoteService, product *models.Product, lines int, sent bool) *models.Quote {
	e.t.Helper()
	quote := e.newWebhookQuote()
	require.NoError(e.t, svc.Create(quote, e.owner.ID, models.RoleAdmin))
	for i := 0; i < lines; i++ {
		_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: models.QuoteQuantityFromUnits(int64(i + 1))})
		require.NoError(e.t, err)
	}
	if sent {
		_, err := svc.ChangeStatus(quote.ID, models.QuoteStatusSent, e.owner.ID)
		require.NoError(e.t, err)
	}
	return quote
}

// quoteAsHandlerSends is the quote PUT /quotes/{id} hands the service for a
// body that repeats the stored values: the service's read with the preloaded
// copies dropped, as the handler drops them.
func quoteAsHandlerSends(t *testing.T, svc QuoteService, id uint) *models.Quote {
	t.Helper()
	quote, err := svc.GetByID(id)
	require.NoError(t, err)
	quote.Owner, quote.Customer, quote.Deal, quote.Lines = nil, nil, nil, nil
	return quote
}

// quoteResponseToken emails the sent quote through svc and returns the token
// of the link it mailed.
func (e *webhookEnv) quoteResponseToken(svc QuoteService, quoteID uint) string {
	e.t.Helper()
	before := len(e.mailer.messages())
	_, err := svc.Email(quoteID)
	require.NoError(e.t, err)
	messages := e.mailer.messages()
	require.Len(e.t, messages, before+1)
	return responseTokenFromBody(e.t, messages[len(messages)-1].Body)
}

func runQuoteWebhookFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	all := webhook.AllEventTypes()
	require.Len(t, all, 21)
	var others []string
	for _, eventType := range all {
		if len(eventType) < 6 || eventType[:6] != "quote." {
			others = append(others, eventType)
		}
	}
	require.Len(t, others, 18)
	// a: everything. b: the quote types only. c: every type but the quote
	// ones. d: inactive with everything.
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, quoteWebhookTypes...)
	c := env.endpoint("https://example.com/c", true, others...)
	d := env.endpoint("https://example.com/d", false, all...)
	product := env.seedQuoteProduct()

	// start clears the outbox of the shared database and checks it is empty.
	start := func(t *testing.T) {
		t.Helper()
		env.clearDeliveries()
		require.Zero(t, env.allDeliveries())
	}
	// updatedOnce asserts one quote.updated per subscribed endpoint and
	// nothing else.
	updatedOnce := func(t *testing.T, quoteID uint) {
		t.Helper()
		assertFanOut(t, env, "quote.updated", quoteID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries(), "no other row")
	}

	t.Run("the endpoint with every type is stored with the twenty-one sorted types", func(t *testing.T) {
		stored, err := env.endpoints.GetEndpoint(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookEventTypes(all), stored.EventTypes)
		assert.Equal(t, models.WebhookEventTypes{"company.created", "company.deleted", "company.updated",
			"customer.created", "customer.deleted", "customer.updated", "deal.created", "deal.deleted", "deal.updated",
			"lead.created", "lead.deleted", "lead.updated", "quote.created", "quote.deleted", "quote.updated",
			"task.created", "task.deleted", "task.updated", "ticket.created", "ticket.deleted", "ticket.updated"}, stored.EventTypes)
	})

	t.Run("Create writes quote.created", func(t *testing.T) {
		start(t)
		quote := env.newWebhookQuote()
		require.NoError(t, env.quotes.Create(quote, env.owner.ID, models.RoleAdmin))
		require.NotZero(t, quote.ID)
		assertFanOut(t, env, "quote.created", quote.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		for _, row := range env.deliveries("quote.created", quote.ID) {
			assert.Equal(t, quote.ID, row.RecordID, "the record id is the quote id")
			assert.NotEqual(t, c.ID, row.EndpointID)
			assert.NotEqual(t, d.ID, row.EndpointID)
		}
	})

	t.Run("an update with the stored values writes no row", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, false)
		// A stored valid_until, set as the handler parses it.
		withDate := quoteAsHandlerSends(t, env.quotes, quote.ID)
		date := dealCloseDate(t, "2099-12-31")
		withDate.ValidUntil = date
		require.NoError(t, env.quotes.Update(withDate))

		start(t)
		same := quoteAsHandlerSends(t, env.quotes, quote.ID)
		require.NotNil(t, same.ValidUntil)
		require.NotZero(t, same.TotalCents, "the totals are read")
		require.NoError(t, env.quotes.Update(same))
		assert.Zero(t, env.allDeliveries(), "the stored values are no update")

		// The body repeats the date as a string, parsed again.
		reparsed := quoteAsHandlerSends(t, env.quotes, quote.ID)
		reparsed.ValidUntil = dealCloseDate(t, "2099-12-31")
		require.NoError(t, env.quotes.Update(reparsed))
		assert.Zero(t, env.allDeliveries(), "the same date parsed again is no update")
	})

	t.Run("an update that changes notes writes quote.updated", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 0, false)
		start(t)
		changed := quoteAsHandlerSends(t, env.quotes, quote.ID)
		changed.Notes = "changed notes"
		require.NoError(t, env.quotes.Update(changed))
		assert.Equal(t, "changed notes", storedWebhookQuote(t, db, quote.ID).Notes)
		updatedOnce(t, quote.ID)
	})

	t.Run("the line writes", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 0, false)

		start(t)
		added, err := env.quotes.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: models.QuoteQuantityFromUnits(2)})
		require.NoError(t, err)
		require.Len(t, added.Lines, 1)
		updatedOnce(t, quote.ID)
		line := added.Lines[0]

		start(t)
		quantity, discount, price := line.Quantity, line.DiscountBP, line.UnitPriceCents
		description, position, tax := line.Description, line.Position, line.TaxRateBP
		_, err = env.quotes.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{Quantity: &quantity, DiscountBP: &discount,
			UnitPriceCents: &price, Description: &description, Position: &position, TaxRateBP: &tax})
		require.NoError(t, err)
		assert.Zero(t, env.allDeliveries(), "a patch that repeats the stored values is no update")
		_, err = env.quotes.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{})
		require.NoError(t, err)
		assert.Zero(t, env.allDeliveries(), "an empty patch is no update")

		start(t)
		more := models.QuoteQuantityFromUnits(5)
		_, err = env.quotes.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{Quantity: &more})
		require.NoError(t, err)
		assert.Equal(t, more, storedWebhookQuoteLines(t, db, quote.ID)[0].Quantity)
		updatedOnce(t, quote.ID)

		start(t)
		_, err = env.quotes.DeleteLine(quote.ID, line.ID)
		require.NoError(t, err)
		assert.Empty(t, storedWebhookQuoteLines(t, db, quote.ID))
		updatedOnce(t, quote.ID)
	})

	t.Run("each transition writes one quote.updated", func(t *testing.T) {
		transition := func(t *testing.T, quoteID uint, to models.QuoteStatus) {
			t.Helper()
			start(t)
			_, err := env.quotes.ChangeStatus(quoteID, to, env.owner.ID)
			require.NoError(t, err)
			assert.Equal(t, to, storedWebhookQuote(t, db, quoteID).Status)
			updatedOnce(t, quoteID)
		}
		first := env.seedQuote(env.quotes, product, 1, false)
		transition(t, first.ID, models.QuoteStatusSent)
		transition(t, first.ID, models.QuoteStatusAccepted)

		rejected := env.seedQuote(env.quotes, product, 1, true)
		transition(t, rejected.ID, models.QuoteStatusRejected)

		expired := env.seedQuote(env.quotes, product, 1, true)
		transition(t, expired.ID, models.QuoteStatusExpired)

		back := env.seedQuote(env.quotes, product, 1, true)
		transition(t, back.ID, models.QuoteStatusDraft)
	})

	t.Run("a refused transition writes no row", func(t *testing.T) {
		empty := env.seedQuote(env.quotes, product, 0, false)
		start(t)
		_, err := env.quotes.ChangeStatus(empty.ID, models.QuoteStatusSent, env.owner.ID)
		require.ErrorIs(t, err, apperrors.ErrQuoteHasNoLines)
		assert.Equal(t, models.QuoteStatusDraft, storedWebhookQuote(t, db, empty.ID).Status, "the claim rolled back")
		assert.Zero(t, env.allDeliveries())

		draft := env.seedQuote(env.quotes, product, 1, false)
		start(t)
		_, err = env.quotes.ChangeStatus(draft.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.ErrorIs(t, err, apperrors.ErrInvalidQuoteTransition)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Email writes no row", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, true)
		start(t)
		env.quoteResponseToken(env.quotes, quote.ID)
		require.NotNil(t, storedWebhookQuote(t, db, quote.ID).LastEmailedAt, "the email was stamped")
		assert.Zero(t, env.allDeliveries(), "an email is no change to the quote")
	})

	t.Run("a client accept writes one quote.updated", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, true)
		token := env.quoteResponseToken(env.quotes, quote.ID)
		start(t)
		_, err := env.quotes.RespondPublic(token, models.QuoteResponseAccept)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteStatusAccepted, storedWebhookQuote(t, db, quote.ID).Status)
		updatedOnce(t, quote.ID)
	})

	t.Run("a client decline writes one quote.updated", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, true)
		token := env.quoteResponseToken(env.quotes, quote.ID)
		start(t)
		_, err := env.quotes.RespondPublic(token, models.QuoteResponseDecline)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteStatusRejected, storedWebhookQuote(t, db, quote.ID).Status)
		updatedOnce(t, quote.ID)

		start(t)
		_, err = env.quotes.RespondPublic(token, models.QuoteResponseAccept)
		require.ErrorIs(t, err, apperrors.ErrQuoteResponseRejected, "the spent link")
		assert.Zero(t, env.allDeliveries(), "a refused answer writes no row")
	})

	t.Run("Delete writes quote.deleted for every subscribed endpoint", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, false)
		// e subscribes after the quote's earlier events: it never received
		// one, and gets the deleted event all the same (#531).
		e := env.endpoint("https://example.com/e", true, "quote.deleted")
		t.Cleanup(func() { require.NoError(t, env.endpoints.DeleteEndpoint(ctx, e.ID)) })
		start(t)
		require.NoError(t, env.quotes.Delete(quote.ID, true))
		assert.True(t, storedWebhookQuote(t, db, quote.ID).DeletedAt.Valid, "soft-deleted")
		assertFanOut(t, env, "quote.deleted", quote.ID, a.ID, b.ID, e.ID)
		assert.Equal(t, int64(3), env.allDeliveries(), "no other row")
		for _, row := range env.deliveries("quote.deleted", quote.ID) {
			assert.Equal(t, quote.ID, row.RecordID)
		}

		start(t)
		err := env.quotes.Delete(quote.ID, false)
		require.ErrorIs(t, err, apperrors.ErrQuoteNotFound, "already deleted")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("an admin deletes a sent quote", func(t *testing.T) {
		quote := env.seedQuote(env.quotes, product, 1, true)
		start(t)
		require.NoError(t, env.quotes.Delete(quote.ID, false))
		assertFanOut(t, env, "quote.deleted", quote.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("a refused delete writes no row", func(t *testing.T) {
		sent := env.seedQuote(env.quotes, product, 1, true)
		start(t)
		require.ErrorIs(t, env.quotes.Delete(sent.ID, true), apperrors.ErrQuoteLocked)
		assert.False(t, storedWebhookQuote(t, db, sent.ID).DeletedAt.Valid, "the quote stays live")
		assert.Zero(t, env.allDeliveries())

		require.ErrorIs(t, env.quotes.Delete(999999, false), apperrors.ErrQuoteNotFound)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("an unwired quote service writes no row", func(t *testing.T) {
		plain := newWebhookEnv(t, db, nil)
		start(t)
		quote := plain.seedQuote(plain.quotes, product, 2, false)
		changed := quoteAsHandlerSends(t, plain.quotes, quote.ID)
		changed.Notes = "renamed"
		require.NoError(t, plain.quotes.Update(changed))
		line := storedWebhookQuoteLines(t, db, quote.ID)[0]
		more := models.QuoteQuantityFromUnits(9)
		_, err := plain.quotes.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{Quantity: &more})
		require.NoError(t, err)
		_, err = plain.quotes.DeleteLine(quote.ID, line.ID)
		require.NoError(t, err)
		_, err = plain.quotes.ChangeStatus(quote.ID, models.QuoteStatusSent, plain.owner.ID)
		require.NoError(t, err)
		token := plain.quoteResponseToken(plain.quotes, quote.ID)
		_, err = plain.quotes.RespondPublic(token, models.QuoteResponseAccept)
		require.NoError(t, err)
		require.NoError(t, plain.quotes.Delete(quote.ID, false))
		assert.True(t, storedWebhookQuote(t, db, quote.ID).DeletedAt.Valid)
		require.ErrorIs(t, plain.quotes.Delete(quote.ID, false), apperrors.ErrQuoteNotFound)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

// runQuoteWebhookAtomicity: an outbox insert that fails after its rows were
// written rolls the quote write back with them.
func runQuoteWebhookAtomicity(t *testing.T, db *gorm.DB) {
	// setup writes through an unwired service; env through the failing outbox.
	setup := newWebhookEnv(t, db, nil)
	env := newWebhookEnv(t, db, nil)
	all := webhook.AllEventTypes()
	env.endpoint("https://example.com/hook", true, all...)
	env.endpoint("https://example.com/hook/second", true, all...)
	env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))
	product := setup.seedQuoteProduct()

	t.Run("Create", func(t *testing.T) {
		quotes := env.count(&models.Quote{}, "")
		changes := env.count(&models.QuoteStatusChange{}, "")
		quote := env.newWebhookQuote()
		require.ErrorIs(t, env.quotes.Create(quote, env.owner.ID, models.RoleAdmin), injectedFailure)
		assert.Equal(t, quotes, env.count(&models.Quote{}, ""), "no quote row")
		assert.Equal(t, changes, env.count(&models.QuoteStatusChange{}, ""), "no history row")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("Update", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		before := storedWebhookQuote(t, db, quote.ID)
		changed := quoteAsHandlerSends(t, env.quotes, quote.ID)
		changed.Title = "Lost title"
		changed.Notes = "lost notes"
		require.ErrorIs(t, env.quotes.Update(changed), injectedFailure)
		assert.Equal(t, before.WebhookState(), storedWebhookQuote(t, db, quote.ID).WebhookState(), "the quote rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("AddLine", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		before := storedWebhookQuote(t, db, quote.ID)
		_, err := env.quotes.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: models.QuoteQuantityFromUnits(3)})
		require.ErrorIs(t, err, injectedFailure)
		assert.Len(t, storedWebhookQuoteLines(t, db, quote.ID), 1, "no line added")
		assert.Equal(t, before.WebhookState(), storedWebhookQuote(t, db, quote.ID).WebhookState(), "the totals rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("UpdateLine", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		before := storedWebhookQuote(t, db, quote.ID)
		line := storedWebhookQuoteLines(t, db, quote.ID)[0]
		more := models.QuoteQuantityFromUnits(7)
		_, err := env.quotes.UpdateLine(quote.ID, line.ID, models.QuoteLinePatch{Quantity: &more})
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, line.WebhookState(), storedWebhookQuoteLines(t, db, quote.ID)[0].WebhookState(), "the line rolled back")
		assert.Equal(t, before.WebhookState(), storedWebhookQuote(t, db, quote.ID).WebhookState(), "the totals rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("DeleteLine", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		line := storedWebhookQuoteLines(t, db, quote.ID)[0]
		_, err := env.quotes.DeleteLine(quote.ID, line.ID)
		require.ErrorIs(t, err, injectedFailure)
		assert.Len(t, storedWebhookQuoteLines(t, db, quote.ID), 1, "the line stays")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("ChangeStatus", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		changes := env.count(&models.QuoteStatusChange{}, "quote_id = ?", quote.ID)
		_, err := env.quotes.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
		require.ErrorIs(t, err, injectedFailure)
		stored := storedWebhookQuote(t, db, quote.ID)
		assert.Equal(t, models.QuoteStatusDraft, stored.Status, "the claim rolled back")
		assert.Nil(t, stored.SentAt)
		assert.Equal(t, changes, env.count(&models.QuoteStatusChange{}, "quote_id = ?", quote.ID), "no history row")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("RespondPublic", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, true)
		token := setup.quoteResponseToken(setup.quotes, quote.ID)
		_, err := env.quotes.RespondPublic(token, models.QuoteResponseAccept)
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, models.QuoteStatusSent, storedWebhookQuote(t, db, quote.ID).Status, "the status rolled back")
		var tokens []models.QuoteResponseToken
		require.NoError(t, db.Where("quote_id = ?", quote.ID).Find(&tokens).Error)
		require.Len(t, tokens, 1)
		assert.Nil(t, tokens[0].UsedAt, "the token is still unused")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Delete", func(t *testing.T) {
		quote := setup.seedQuote(setup.quotes, product, 1, false)
		require.ErrorIs(t, env.quotes.Delete(quote.ID, true), injectedFailure)
		assert.False(t, storedWebhookQuote(t, db, quote.ID).DeletedAt.Valid, "the quote stays live")
		assert.Zero(t, env.allDeliveries())
	})
}
