package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-2 (#345): every create path writes its outbox rows in the creating
// transaction, on SQLite and on a disposable TEST_MYSQL_DSN schema (the MySQL
// roots skip without the variable and fail when it is unreachable).

func TestWebhookOutboxAtomicitySQLite(t *testing.T) {
	runWebhookOutboxAtomicity(t, openTicketIntakeDB(t, false))
}

func TestWebhookOutboxAtomicityMySQL(t *testing.T) {
	runWebhookOutboxAtomicity(t, openTicketIntakeDB(t, true))
}

func TestWebhookOutboxFanOutSQLite(t *testing.T) {
	runWebhookOutboxFanOut(t, openTicketIntakeDB(t, false))
}

func TestWebhookOutboxFanOutMySQL(t *testing.T) {
	runWebhookOutboxFanOut(t, openTicketIntakeDB(t, true))
}

// assertSentNothing runs one runtime pass against a consumer and asserts that
// no request was made.
func assertSentNothing(t *testing.T, env *webhookEnv) {
	t.Helper()
	consumer := newWebhookConsumer(t)
	result, err := env.runtime(consumer.guard(nil)).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, result.Attempted, "nothing was claimed")
	assert.Empty(t, consumer.received(), "the runtime sends nothing")
	assert.Zero(t, consumer.dials.Load())
}

func runWebhookOutboxAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	consumerURL := "https://example.com/hook"
	env.endpoint(consumerURL, true, models.WebhookEventLeadCreated, models.WebhookEventCustomerCreated, models.WebhookEventTicketCreated)
	env.endpoint(consumerURL+"/second", true, models.WebhookEventLeadCreated, models.WebhookEventCustomerCreated, models.WebhookEventTicketCreated)
	failing := newFailingWebhookRepository(env.repo, injectedFailure, 0)
	env.wire(failing)
	ctx := context.Background()

	t.Run("LeadService.Create", func(t *testing.T) {
		lead := env.newLead()
		err := env.leads.Create(lead)
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, env.count(&models.Lead{}, "email = ?", lead.Email), "the lead rolled back")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("form create_lead", func(t *testing.T) {
		form := env.leadForm()
		email := env.uniqueEmail("form-lead")
		err := env.submitLead(form, email)
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, env.count(&models.Lead{}, "email = ?", email))
		assert.Zero(t, env.count(&models.FormSubmission{}, "form_id = ?", form.ID), "the submission rolled back too")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("CustomerService.Create", func(t *testing.T) {
		customer := env.newCustomer()
		err := env.customers.Create(customer)
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, customer.ID)
		assert.Zero(t, env.count(&models.Customer{}, "email = ?", customer.Email))
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("LeadService.ConvertToCustomer", func(t *testing.T) {
		lead := env.seedLead()
		_, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, env.count(&models.Customer{}, "email = ?", lead.Email))
		var stored models.Lead
		require.NoError(t, db.First(&stored, lead.ID).Error)
		assert.Nil(t, stored.CustomerID, "the lead is not converted")
		assert.NotEqual(t, models.LeadStatusConverted, stored.Status)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("TicketService.Create", func(t *testing.T) {
		customer := env.seedCustomer()
		err := env.tickets.Create(env.newTicket(customer.ID))
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, env.count(&models.Ticket{}, "customer_id = ?", customer.ID))
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("form ticket intake", func(t *testing.T) {
		customer := env.seedCustomer()
		form := env.ticketForm()
		token := env.pendingTicket(form, customer.Email, "atomic-key-1")
		err := env.forms.ConfirmSubmission(ctx, token)
		require.ErrorIs(t, err, injectedFailure)
		assert.Zero(t, env.count(&models.Ticket{}, "customer_id = ?", customer.ID), "the ticket rolled back")
		assert.Zero(t, env.count(&models.FormTicketKey{}, "form_id = ?", form.ID))
		var submission models.FormSubmission
		require.NoError(t, db.Where("form_id = ?", form.ID).Take(&submission).Error)
		assert.Equal(t, models.FormSubmissionPending, submission.Status, "the confirmation rolled back, token claim included")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("the runtime then sends nothing", func(t *testing.T) {
		assertSentNothing(t, env)
	})

	t.Run("a conversion replayed after a retryable failure commits one row set", func(t *testing.T) {
		// The first attempt writes its rows and then fails with a deadlock: the
		// retry helper rolls the attempt back and replays it.
		deadlock := errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")
		env.wire(newFailingWebhookRepository(env.repo, deadlock, 1))
		lead := env.seedLead()
		customer, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		rows := env.deliveries(models.WebhookEventCustomerCreated, customer.ID)
		require.Len(t, rows, 2, "exactly one row per endpoint")
		assert.Equal(t, rows[0].EventID, rows[1].EventID, "one event")
		assert.EqualValues(t, 1, env.count(&models.Customer{}, "email = ?", lead.Email), "one customer")
		require.NoError(t, db.Where("1 = 1").Delete(&models.WebhookDelivery{}).Error)
	})

	t.Run("without the option every path behaves as before", func(t *testing.T) {
		env.wire(nil)
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		customer := env.newCustomer()
		require.NoError(t, env.customers.Create(customer))
		assert.NotZero(t, customer.ID)
		converted, err := env.leads.ConvertToCustomer(ctx, env.seedLead().ID, &models.Customer{})
		require.NoError(t, err)
		require.NoError(t, env.tickets.Create(env.newTicket(customer.ID)))
		require.NoError(t, env.submitLead(env.leadForm(), env.uniqueEmail("plain")))
		token := env.pendingTicket(env.ticketForm(), customer.Email, "plain-key")
		require.NoError(t, env.forms.ConfirmSubmission(ctx, token))
		assert.NotNil(t, env.newestTicketOf(customer.ID))
		assert.NotZero(t, converted.ID)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

// assertFanOut checks the rows of one event: one per expected endpoint, one
// shared event id, pending, attempts 0, next_attempt_at = occurred_at (UTC).
func assertFanOut(t *testing.T, env *webhookEnv, eventType string, recordID uint, endpoints ...uint) {
	t.Helper()
	rows := env.deliveries(eventType, recordID)
	require.Len(t, rows, len(endpoints), "%s %d", eventType, recordID)
	got := []uint{}
	for _, row := range rows {
		got = append(got, row.EndpointID)
		assert.Equal(t, rows[0].EventID, row.EventID, "one event id")
		assert.Regexp(t, `^evt_[0-9a-f]{32}$`, row.EventID)
		assert.Equal(t, models.WebhookDeliveryPending, row.Status)
		assert.Equal(t, 0, row.Attempts)
		assert.Equal(t, "", row.ErrorCode)
		assert.Nil(t, row.LastAttemptAt)
		assert.Nil(t, row.LastHTTPStatus)
		require.NotNil(t, row.NextAttemptAt)
		assert.True(t, row.NextAttemptAt.Equal(row.OccurredAt), "due at once")
		assert.WithinDuration(t, time.Now(), row.OccurredAt, time.Minute)
	}
	assert.ElementsMatch(t, endpoints, got)
}

func runWebhookOutboxFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	all := []string{models.WebhookEventLeadCreated, models.WebhookEventCustomerCreated, models.WebhookEventTicketCreated}
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, models.WebhookEventLeadCreated)
	env.endpoint("https://example.com/c", false, all...)
	d := env.endpoint("https://example.com/d", true, models.WebhookEventTicketCreated)

	t.Run("LeadService.Create", func(t *testing.T) {
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		assertFanOut(t, env, models.WebhookEventLeadCreated, lead.ID, a.ID, b.ID)
	})

	t.Run("form create_lead: a new lead, never a repeat enquiry", func(t *testing.T) {
		form := env.leadForm()
		email := env.uniqueEmail("enquiry")
		require.NoError(t, env.submitLead(form, email))
		var lead models.Lead
		require.NoError(t, db.Where("email = ?", email).Take(&lead).Error)
		assertFanOut(t, env, models.WebhookEventLeadCreated, lead.ID, a.ID, b.ID)
		before := env.allDeliveries()
		require.NoError(t, env.submitLead(form, email), "the repeat enquiry")
		assert.EqualValues(t, 1, env.count(&models.Lead{}, "email = ?", email), "the same lead")
		assert.Equal(t, before, env.allDeliveries(), "a repeat enquiry enqueues nothing")
	})

	t.Run("CustomerService.Create", func(t *testing.T) {
		customer := env.newCustomer()
		require.NoError(t, env.customers.Create(customer))
		assertFanOut(t, env, models.WebhookEventCustomerCreated, customer.ID, a.ID)
	})

	t.Run("LeadService.ConvertToCustomer", func(t *testing.T) {
		lead := env.seedLead()
		customer, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		assertFanOut(t, env, models.WebhookEventCustomerCreated, customer.ID, a.ID)
		assert.Empty(t, env.deliveries(models.WebhookEventLeadCreated, lead.ID), "a conversion is no lead creation")
	})

	t.Run("conversion retries leave exactly one row set", func(t *testing.T) {
		deadlock := errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")
		env.wire(newFailingWebhookRepository(env.repo, deadlock, 1))
		defer env.wire(env.repo)
		lead := env.seedLead()
		customer, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		assertFanOut(t, env, models.WebhookEventCustomerCreated, customer.ID, a.ID)
	})

	t.Run("TicketService.Create", func(t *testing.T) {
		customer := env.seedCustomer()
		ticket := env.newTicket(customer.ID)
		require.NoError(t, env.tickets.Create(ticket))
		assertFanOut(t, env, models.WebhookEventTicketCreated, ticket.ID, a.ID, d.ID)
	})

	t.Run("form ticket intake: created, never a duplicate", func(t *testing.T) {
		customer := env.seedCustomer()
		form := env.ticketForm()
		token := env.pendingTicket(form, customer.Email, "fan-out-key")
		require.NoError(t, env.forms.ConfirmSubmission(ctx, token))
		ticket := env.newestTicketOf(customer.ID)
		require.NotNil(t, ticket)
		assertFanOut(t, env, models.WebhookEventTicketCreated, ticket.ID, a.ID, d.ID)

		before := env.allDeliveries()
		again := env.pendingTicket(form, customer.Email, "fan-out-key")
		require.NoError(t, env.forms.ConfirmSubmission(ctx, again))
		var submission models.FormSubmission
		require.NoError(t, db.Where("form_id = ?", form.ID).Order("id desc").Take(&submission).Error)
		assert.Equal(t, models.FormTicketOutcomeDuplicate, submission.TicketOutcome)
		assert.Equal(t, before, env.allDeliveries(), "a duplicate enqueues nothing")
	})

	t.Run("an endpoint deactivated by another connection gets no row", func(t *testing.T) {
		late := env.endpoint("https://example.com/late", true, models.WebhookEventLeadCreated)
		// Committed outside the create's transaction (its own connection on
		// MySQL) before the create reads the endpoints.
		require.NoError(t, db.Model(&models.WebhookEndpoint{}).Where("id = ?", late.ID).Update("is_active", false).Error)
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		assertFanOut(t, env, models.WebhookEventLeadCreated, lead.ID, a.ID, b.ID)
	})

	t.Run("an endpoint created inactive gets no row", func(t *testing.T) {
		var inactive []models.WebhookEndpoint
		require.NoError(t, db.Where("is_active = ?", false).Find(&inactive).Error)
		require.NotEmpty(t, inactive)
		for _, endpoint := range inactive {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", endpoint.ID), "endpoint %d", endpoint.ID)
		}
	})

	// The rows are only ever read by the runtime once committed.
	var _ repository.WebhookRepository = env.repo
}
