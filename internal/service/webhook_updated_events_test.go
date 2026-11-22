package service

import (
	"context"
	"encoding/json"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The updated events (#408): lead.updated, customer.updated and
// ticket.updated are written in the transaction of the update, of the lead
// assignment and conversion, of the automation assign_owner step and of the
// lead and ticket bulk status batches, one per record whose WebhookState
// changed, on SQLite and on a disposable TEST_MYSQL_DSN schema (the MySQL
// roots skip without the variable and fail when it is unreachable).

func TestWebhookUpdatedFanOutSQLite(t *testing.T) {
	runWebhookUpdatedFanOut(t, openTicketIntakeDB(t, false))
}

func TestWebhookUpdatedFanOutMySQL(t *testing.T) {
	runWebhookUpdatedFanOut(t, openTicketIntakeDB(t, true))
}

func TestWebhookUpdatedAtomicitySQLite(t *testing.T) {
	runWebhookUpdatedAtomicity(t, openTicketIntakeDB(t, false))
}

func TestWebhookUpdatedAtomicityMySQL(t *testing.T) {
	runWebhookUpdatedAtomicity(t, openTicketIntakeDB(t, true))
}

var (
	webhookUpdatedTypes = []string{models.WebhookEventLeadUpdated, models.WebhookEventCustomerUpdated, models.WebhookEventTicketUpdated}
	webhookCreatedTypes = []string{models.WebhookEventLeadCreated, models.WebhookEventCustomerCreated, models.WebhookEventTicketCreated}
)

// storedLead, storedCustomer and storedTicket are fresh reads of the row,
// soft-deleted rows included.
func storedLead(t *testing.T, db *gorm.DB, id uint) models.Lead {
	t.Helper()
	var lead models.Lead
	require.NoError(t, db.Unscoped().First(&lead, id).Error)
	return lead
}

func storedCustomer(t *testing.T, db *gorm.DB, id uint) models.Customer {
	t.Helper()
	var customer models.Customer
	require.NoError(t, db.Unscoped().First(&customer, id).Error)
	return customer
}

func storedTicket(t *testing.T, db *gorm.DB, id uint) models.Ticket {
	t.Helper()
	var ticket models.Ticket
	require.NoError(t, db.Unscoped().First(&ticket, id).Error)
	return ticket
}

// updateTicket reads the ticket through the service, as the handler does,
// applies change and updates it through tickets.
func updateTicket(t *testing.T, env *webhookEnv, tickets TicketService, id uint, change func(*models.Ticket)) error {
	t.Helper()
	ticket, err := env.tickets.GetByID(id)
	require.NoError(t, err)
	change(ticket)
	return tickets.Update(ticket)
}

// updateCustomer reads the customer through the service, applies change and
// updates it.
func updateCustomer(t *testing.T, env *webhookEnv, id uint, change func(*models.Customer)) error {
	t.Helper()
	customer, err := env.customers.GetByID(id)
	require.NoError(t, err)
	change(customer)
	return env.customers.Update(context.Background(), customer)
}

func runWebhookUpdatedFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	six := webhook.AllEventTypes()
	a := env.endpoint("https://example.com/a", true, six...)
	b := env.endpoint("https://example.com/b", true, webhookUpdatedTypes...)
	c := env.endpoint("https://example.com/c", true, webhookCreatedTypes...)
	d := env.endpoint("https://example.com/d", false, six...)
	admin := env.owner.ID
	other := &models.User{Email: env.uniqueEmail("assignee"), Password: "x", FirstName: "As", LastName: "Signee",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(other).Error)

	t.Run("lead update", func(t *testing.T) {
		lead := env.seedLead()
		updated, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": "Changed"})
		require.NoError(t, err)
		assert.Equal(t, "Changed", updated.FirstName)
		assertFanOut(t, env, models.WebhookEventLeadUpdated, lead.ID, a.ID, b.ID)
	})

	t.Run("customer update", func(t *testing.T) {
		customer := env.seedCustomer()
		require.NoError(t, updateCustomer(t, env, customer.ID, func(c *models.Customer) { c.FirstName = "Changed" }))
		assert.Equal(t, "Changed", storedCustomer(t, db, customer.ID).FirstName)
		assertFanOut(t, env, models.WebhookEventCustomerUpdated, customer.ID, a.ID, b.ID)
	})

	t.Run("ticket update", func(t *testing.T) {
		ticket := env.seedTicket(models.TicketStatusOpen)
		require.NoError(t, updateTicket(t, env, env.tickets, ticket.ID, func(tk *models.Ticket) { tk.Priority = models.TicketPriorityHigh }))
		assert.Equal(t, models.TicketPriorityHigh, storedTicket(t, db, ticket.ID).Priority)
		assertFanOut(t, env, models.WebhookEventTicketUpdated, ticket.ID, a.ID, b.ID)
	})

	t.Run("ticket update with webhooks only", func(t *testing.T) {
		// No automation option at all: the transactional path must not touch
		// the absent automation repository.
		tickets := NewTicketService(repository.NewTicketRepository(db), repository.NewCustomerRepository(db),
			repository.NewUserRepository(db), WithTicketWebhooks(webhook.NewEmitter(env.repo), env.txManager))
		ticket := env.seedTicket(models.TicketStatusOpen)
		require.NoError(t, updateTicket(t, env, tickets, ticket.ID, func(tk *models.Ticket) { tk.Status = models.TicketStatusInProgress }))
		assert.Equal(t, models.TicketStatusInProgress, storedTicket(t, db, ticket.ID).Status)
		assertFanOut(t, env, models.WebhookEventTicketUpdated, ticket.ID, a.ID, b.ID)
		assert.Zero(t, env.count(&models.AutomationEvent{}, "entity_type = ? AND entity_id = ?", models.AutomationEntityTicket, ticket.ID),
			"no automation event without the option")
	})

	t.Run("created-only and inactive endpoints get no updated row", func(t *testing.T) {
		for _, endpoint := range []uint{c.ID, d.ID} {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ? AND event_type LIKE ?", endpoint, "%.updated"), "endpoint %d", endpoint)
		}
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "event_type LIKE ?", "%.created"), "an update is no creation")
	})

	t.Run("an unchanged update writes no row", func(t *testing.T) {
		lead := env.seedLead()
		customer := env.seedCustomer()
		ticket := env.seedTicket(models.TicketStatusOpen)
		before := env.allDeliveries()

		_, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": lead.FirstName})
		require.NoError(t, err)
		require.NoError(t, updateCustomer(t, env, customer.ID, func(c *models.Customer) { c.FirstName = customer.FirstName }))
		require.NoError(t, updateTicket(t, env, env.tickets, ticket.ID, func(tk *models.Ticket) { tk.Priority = ticket.Priority }))

		assert.Equal(t, before, env.allDeliveries(), "no row for an update that changed nothing")
		assert.EqualValues(t, 1, env.count(&models.AutomationEvent{}, "entity_type = ? AND entity_id = ? AND event_type = ?",
			models.AutomationEntityLead, lead.ID, models.AutomationEventLeadUpdated), "the automation event is still recorded")
	})

	t.Run("the delivered body is thin", func(t *testing.T) {
		env.clearDeliveries()
		consumer := newWebhookConsumer(t)
		hook := env.endpoint(consumer.url("/thin"), true, models.WebhookEventLeadUpdated)
		defer func() { require.NoError(t, env.endpoints.DeleteEndpoint(ctx, hook.ID)) }()
		lead := env.seedLead()
		_, err := env.leads.Update(lead.ID, map[string]interface{}{"last_name": "Thin"})
		require.NoError(t, err)

		_, err = env.runtime(consumer.guard(nil)).RunOnce(ctx)
		require.NoError(t, err)
		var bodies [][]byte
		for _, request := range consumer.received() {
			if request.Path == "/thin" {
				bodies = append(bodies, request.Body)
			}
		}
		require.Len(t, bodies, 1, "one request to the subscribed endpoint")
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(bodies[0], &body))
		keys := make([]string, 0, len(body))
		for key := range body {
			keys = append(keys, key)
		}
		assert.ElementsMatch(t, []string{"id", "type", "occurred_at", "record_id"}, keys)
		assert.Equal(t, models.WebhookEventLeadUpdated, body["type"])
		assert.EqualValues(t, lead.ID, body["record_id"])
		rows := env.deliveries(models.WebhookEventLeadUpdated, lead.ID)
		require.NotEmpty(t, rows)
		assert.Equal(t, rows[0].EventID, body["id"])
		env.clearDeliveries()
	})

	t.Run("bulk lead status", func(t *testing.T) {
		var ids []uint
		for i := 0; i < 3; i++ {
			ids = append(ids, env.seedLead().ID)
		}
		already := env.newLead()
		already.Status = models.LeadStatusContacted
		require.NoError(t, db.Create(already).Error)

		result, err := env.bulk.BulkSetLeadStatus(ctx, admin, models.RoleAdmin, append(append([]uint{}, ids...), already.ID), models.LeadStatusContacted)
		require.NoError(t, err)
		assert.Equal(t, 4, result.Updated)
		events := map[string]bool{}
		for _, id := range ids {
			assertFanOut(t, env, models.WebhookEventLeadUpdated, id, a.ID, b.ID)
			events[env.deliveries(models.WebhookEventLeadUpdated, id)[0].EventID] = true
			assert.Equal(t, models.LeadStatusContacted, storedLead(t, db, id).Status)
		}
		assert.Len(t, events, 3, "one event id per lead")
		assert.Empty(t, env.deliveries(models.WebhookEventLeadUpdated, already.ID), "a lead already at the status gets none")

		before := env.allDeliveries()
		_, err = env.bulk.BulkSetLeadStatus(ctx, admin, models.RoleAdmin, append(append([]uint{}, ids...), 999999991), models.LeadStatusQualified)
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Equal(t, before, env.allDeliveries(), "a refused batch writes no row")
		for _, id := range ids {
			assert.Equal(t, models.LeadStatusContacted, storedLead(t, db, id).Status, "a refused batch changes no status")
		}
	})

	t.Run("bulk ticket status", func(t *testing.T) {
		var ids []uint
		for i := 0; i < 3; i++ {
			ids = append(ids, env.seedTicket(models.TicketStatusOpen).ID)
		}
		already := env.seedTicket(models.TicketStatusResolved)

		result, err := env.bulk.BulkSetTicketStatus(ctx, admin, models.RoleAdmin, append(append([]uint{}, ids...), already.ID), models.TicketStatusResolved)
		require.NoError(t, err)
		assert.Equal(t, 4, result.Updated)
		events := map[string]bool{}
		for _, id := range ids {
			assertFanOut(t, env, models.WebhookEventTicketUpdated, id, a.ID, b.ID)
			events[env.deliveries(models.WebhookEventTicketUpdated, id)[0].EventID] = true
			assert.Equal(t, models.TicketStatusResolved, storedTicket(t, db, id).Status)
		}
		assert.Len(t, events, 3, "one event id per ticket")
		assert.Empty(t, env.deliveries(models.WebhookEventTicketUpdated, already.ID), "a ticket already at the status gets none")

		before := env.allDeliveries()
		_, err = env.bulk.BulkSetTicketStatus(ctx, admin, models.RoleAdmin, append(append([]uint{}, ids...), 999999991), models.TicketStatusClosed)
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Equal(t, before, env.allDeliveries(), "a refused batch writes no row")
		for _, id := range ids {
			assert.Equal(t, models.TicketStatusResolved, storedTicket(t, db, id).Status, "a refused batch changes no status")
		}
	})

	t.Run("no updated event", func(t *testing.T) {
		env.clearDeliveries()
		erasedLead := env.seedLead()
		require.NoError(t, env.leads.Delete(context.Background(), erasedLead.ID, models.RoleAdmin))
		erasedCustomer := env.seedCustomer()
		require.NoError(t, env.customers.Delete(context.Background(), erasedCustomer.ID))
		deletedTicket := env.seedTicket(models.TicketStatusOpen)
		require.NoError(t, env.tickets.Delete(deletedTicket.ID))

		commented := env.seedTicket(models.TicketStatusOpen)
		require.NoError(t, env.tickets.CreateComment(&models.TicketComment{TicketID: commented.ID, AuthorUserID: admin, Body: "A first response"}))

		assignedCustomer := env.seedCustomer()
		_, err := env.customers.Assign(assignedCustomer.ID, admin)
		require.NoError(t, err)

		assert.Zero(t, env.updatedDeliveries(), "none of these is an update event")
		// The delete and the erasures write their deleted events for a, the
		// one active endpoint subscribed to the deleted types (#531).
		assertFanOut(t, env, models.WebhookEventLeadDeleted, erasedLead.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, erasedCustomer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, deletedTicket.ID, a.ID)
		assert.Equal(t, int64(3), env.allDeliveries(), "the deleted rows only")
	})

	t.Run("lead assign to another owner", func(t *testing.T) {
		env.clearDeliveries()
		lead := env.seedLead()
		assigned, err := env.leads.Assign(ctx, lead.ID, other.ID, LeadViewer{UserID: admin, Role: models.RoleAdmin})
		require.NoError(t, err)
		assert.Equal(t, other.ID, assigned.OwnerID)
		assert.Equal(t, other.ID, storedLead(t, db, lead.ID).OwnerID)
		assertFanOut(t, env, models.WebhookEventLeadUpdated, lead.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries(), "one lead.updated row per subscribed active endpoint")
	})

	t.Run("lead assign to its current owner", func(t *testing.T) {
		env.clearDeliveries()
		lead := env.seedLead()
		_, err := env.leads.Assign(ctx, lead.ID, admin, LeadViewer{UserID: admin, Role: models.RoleAdmin})
		require.NoError(t, err)
		assert.Equal(t, admin, storedLead(t, db, lead.ID).OwnerID)
		assert.Zero(t, env.allDeliveries(), "an assignment that changes nothing writes no row")
	})

	t.Run("lead conversion", func(t *testing.T) {
		env.clearDeliveries()
		lead := env.seedLead()
		converted, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		stored := storedLead(t, db, lead.ID)
		assert.Equal(t, models.LeadStatusConverted, stored.Status)
		require.NotNil(t, stored.CustomerID)
		assert.Equal(t, converted.ID, *stored.CustomerID)
		assertFanOut(t, env, models.WebhookEventLeadUpdated, lead.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventCustomerCreated, converted.ID, a.ID, c.ID)
		assert.Equal(t, int64(4), env.allDeliveries(), "each event fanned out once")
	})

	t.Run("raw event_types bound", func(t *testing.T) {
		raw := func(n int) []string {
			out := make([]string, 0, n)
			for i := 0; i < n; i++ {
				out = append(out, six[i%len(six)])
			}
			return out
		}
		created, err := env.endpoints.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: "https://example.com/twenty", EventTypes: raw(20)})
		require.NoError(t, err, "20 raw entries are accepted")
		stored, err := env.endpoints.GetEndpoint(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookEventTypes{"customer.created", "customer.deleted", "customer.updated", "lead.created", "lead.deleted", "lead.updated", "ticket.created", "ticket.deleted", "ticket.updated"}, stored.EventTypes)
		require.NoError(t, env.endpoints.DeleteEndpoint(ctx, created.ID))

		_, err = env.endpoints.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: "https://example.com/twenty-one", EventTypes: raw(21)})
		var validation *webhook.WebhookValidationError
		require.ErrorAs(t, err, &validation)
		assert.Equal(t, "event_types", validation.Field)
		assert.Equal(t, webhook.WebhookEventTypesRule, validation.Message)
	})

	t.Run("unwired services write no row", func(t *testing.T) {
		env.clearDeliveries()
		env.wire(nil)
		defer env.wire(env.repo)

		lead := env.seedLead()
		_, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": "Unwired"})
		require.NoError(t, err)
		customer := env.seedCustomer()
		require.NoError(t, updateCustomer(t, env, customer.ID, func(c *models.Customer) { c.FirstName = "Unwired" }))
		ticket := env.seedTicket(models.TicketStatusOpen)
		require.NoError(t, updateTicket(t, env, env.tickets, ticket.ID, func(tk *models.Ticket) { tk.Priority = models.TicketPriorityHigh }))
		_, err = env.bulk.BulkSetLeadStatus(ctx, admin, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusQualified)
		require.NoError(t, err)
		_, err = env.bulk.BulkSetTicketStatus(ctx, admin, models.RoleAdmin, []uint{ticket.ID}, models.TicketStatusResolved)
		require.NoError(t, err)

		assert.Equal(t, "Unwired", storedLead(t, db, lead.ID).FirstName)
		assert.Equal(t, models.LeadStatusQualified, storedLead(t, db, lead.ID).Status)
		assert.Equal(t, "Unwired", storedCustomer(t, db, customer.ID).FirstName)
		assert.Equal(t, models.TicketStatusResolved, storedTicket(t, db, ticket.ID).Status)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

func runWebhookUpdatedAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	six := webhook.AllEventTypes()
	env.endpoint("https://example.com/hook", true, six...)
	env.endpoint("https://example.com/hook/second", true, six...)
	ctx := context.Background()
	admin := env.owner.ID

	// The outbox insert writes its rows and then fails: the record write and
	// the rows must roll back together.
	env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))

	t.Run("LeadService.Update", func(t *testing.T) {
		lead := env.seedLead()
		_, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": "Lost"})
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, lead.FirstName, storedLead(t, db, lead.ID).FirstName, "the lead rolled back")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("CustomerService.Update", func(t *testing.T) {
		customer := env.seedCustomer()
		err := updateCustomer(t, env, customer.ID, func(c *models.Customer) { c.FirstName = "Lost" })
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, customer.FirstName, storedCustomer(t, db, customer.ID).FirstName, "the customer rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("TicketService.Update", func(t *testing.T) {
		ticket := env.seedTicket(models.TicketStatusOpen)
		err := updateTicket(t, env, env.tickets, ticket.ID, func(tk *models.Ticket) { tk.Status = models.TicketStatusResolved })
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, models.TicketStatusOpen, storedTicket(t, db, ticket.ID).Status, "the ticket rolled back")
		assert.Zero(t, env.count(&models.AutomationEvent{}, "entity_type = ? AND entity_id = ?", models.AutomationEntityTicket, ticket.ID),
			"its automation events rolled back too")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("BulkSetLeadStatus", func(t *testing.T) {
		first, second := env.seedLead(), env.seedLead()
		_, err := env.bulk.BulkSetLeadStatus(ctx, admin, models.RoleAdmin, []uint{first.ID, second.ID}, models.LeadStatusContacted)
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, models.LeadStatusNew, storedLead(t, db, first.ID).Status)
		assert.Equal(t, models.LeadStatusNew, storedLead(t, db, second.ID).Status)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("BulkSetTicketStatus", func(t *testing.T) {
		first, second := env.seedTicket(models.TicketStatusOpen), env.seedTicket(models.TicketStatusOpen)
		_, err := env.bulk.BulkSetTicketStatus(ctx, admin, models.RoleAdmin, []uint{first.ID, second.ID}, models.TicketStatusResolved)
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, models.TicketStatusOpen, storedTicket(t, db, first.ID).Status)
		assert.Equal(t, models.TicketStatusOpen, storedTicket(t, db, second.ID).Status)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("LeadService.Assign", func(t *testing.T) {
		lead := env.seedLead()
		other := &models.User{Email: env.uniqueEmail("assignee"), Password: "x", FirstName: "As", LastName: "Signee",
			Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(other).Error)
		_, err := env.leads.Assign(ctx, lead.ID, other.ID, LeadViewer{UserID: admin, Role: models.RoleAdmin})
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, admin, storedLead(t, db, lead.ID).OwnerID, "the assignment rolled back")
		assert.Zero(t, env.count(&models.AutomationEvent{}, "entity_type = ? AND entity_id = ?", models.AutomationEntityLead, lead.ID),
			"its automation event rolled back too")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("lead conversion", func(t *testing.T) {
		// customer.created is written first and succeeds; lead.updated is
		// the second insert and fails, so the case shows that it runs inside
		// the conversion attempt.
		failing := newFailingWebhookRepositoryAfter(env.repo, injectedFailure, 1)
		env.wire(failing)
		defer env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))

		lead := env.seedLead()
		customersBefore := env.count(&models.Customer{}, "")
		_, err := env.leads.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, int32(2), failing.calls.Load(), "customer.created succeeded, lead.updated failed")
		assert.Equal(t, customersBefore, env.count(&models.Customer{}, ""), "no customer was created")
		stored := storedLead(t, db, lead.ID)
		assert.Equal(t, models.LeadStatusNew, stored.Status, "the lead is not converted")
		assert.Nil(t, stored.CustomerID)
		assert.Zero(t, env.allDeliveries(), "the customer.created rows rolled back with the attempt")
	})

	t.Run("automation assign_owner step", func(t *testing.T) {
		auto := newAutomationEnv(t, db)
		auto.rebuildWith(WithAutomationWebhooks(webhook.NewEmitter(newFailingWebhookRepository(env.repo, injectedFailure, 0))))
		support := auto.createUser("support", models.RoleSupport, true)
		leadRule := auto.leadRule(models.AutomationEventLeadCreated, assignStep(auto.other.ID))
		ticketRule := auto.createRule(models.AutomationEntityTicket, models.AutomationEventTicketCreated, models.AutomationTrigger{}, assignStep(support.ID))

		lead := env.seedLead()
		leadRun := auto.startEntityRun(leadRule, models.AutomationEntityLead, lead.ID)
		ticket := env.seedTicket(models.TicketStatusOpen)
		ticketRun := auto.startEntityRun(ticketRule, models.AutomationEntityTicket, ticket.ID)
		// The failed steps are the point; what ExecuteDue reports about them
		// is not.
		_ = auto.engine.ExecuteDue(ctx)

		assert.Equal(t, admin, storedLead(t, db, lead.ID).OwnerID, "the lead's owner rolled back")
		assert.Nil(t, storedTicket(t, db, ticket.ID).AssignedToID, "the ticket's assignee rolled back")
		assert.Empty(t, auto.stepsOf(leadRun.ID), "the lead step rolled back")
		assert.Empty(t, auto.stepsOf(ticketRun.ID), "the ticket step rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("the runtime then sends nothing", func(t *testing.T) {
		assertSentNothing(t, env)
	})

	// Refusals, with the real repository: the same error as without
	// webhooks, the record unchanged, no updated row.
	env.wire(env.repo)

	t.Run("reopening a closed ticket", func(t *testing.T) {
		ticket := env.seedTicket(models.TicketStatusClosed)
		err := updateTicket(t, env, env.tickets, ticket.ID, func(tk *models.Ticket) {
			tk.Status = models.TicketStatusOpen
			tk.Title = "Reopened"
		})
		require.ErrorIs(t, err, apperrors.ErrClosedTicketReopen)
		stored := storedTicket(t, db, ticket.ID)
		assert.Equal(t, models.TicketStatusClosed, stored.Status)
		assert.Equal(t, ticket.Title, stored.Title)
		assert.Zero(t, env.updatedDeliveries())
	})

	t.Run("customer update to another customer's email", func(t *testing.T) {
		other := env.seedCustomer()
		customer := env.seedCustomer()
		err := updateCustomer(t, env, customer.ID, func(c *models.Customer) { c.Email = other.Email })
		require.ErrorIs(t, err, apperrors.ErrDuplicateEmail)
		assert.Equal(t, customer.Email, storedCustomer(t, db, customer.ID).Email)
		assert.Zero(t, env.updatedDeliveries())
	})

	t.Run("customer update of a customer that is no longer live", func(t *testing.T) {
		customer := env.seedCustomer()
		stale, err := env.customers.GetByID(customer.ID)
		require.NoError(t, err)
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		erased := storedCustomer(t, db, customer.ID)

		stale.FirstName = "Back"
		err = env.customers.Update(context.Background(), stale)
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		after := storedCustomer(t, db, customer.ID)
		assert.Equal(t, erased.FirstName, after.FirstName, "the erased row is not written back")
		assert.Equal(t, erased.Email, after.Email)
		assert.True(t, after.DeletedAt.Valid, "still deleted")
		assert.Zero(t, env.updatedDeliveries())
	})

	t.Run("lead update with an unknown company_id", func(t *testing.T) {
		lead := env.seedLead()
		unknown := uint(999999991)
		_, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": "Lost", "company_id": &unknown})
		require.ErrorIs(t, err, apperrors.ErrCompanyNotFound)
		stored := storedLead(t, db, lead.ID)
		assert.Equal(t, lead.FirstName, stored.FirstName)
		assert.Nil(t, stored.CompanyID)
		assert.Zero(t, env.updatedDeliveries())
	})

	t.Run("lead update to status converted", func(t *testing.T) {
		lead := env.seedLead()
		_, err := env.leads.Update(lead.ID, map[string]interface{}{"first_name": "Lost", "status": models.LeadStatusConverted})
		var refusal *LeadConversionRuleError
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, "status", refusal.Field)
		stored := storedLead(t, db, lead.ID)
		assert.Equal(t, lead.FirstName, stored.FirstName)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		assert.Zero(t, env.updatedDeliveries())
	})

	t.Run("no row was left by any refusal", func(t *testing.T) {
		// The only rows are the customer.deleted rows of the erasure that
		// prepared "customer update of a customer that is no longer live",
		// one per endpoint (#531).
		assert.Equal(t, int64(2), env.count(&models.WebhookDelivery{}, "event_type = ?", models.WebhookEventCustomerDeleted))
		assert.Equal(t, int64(2), env.allDeliveries())
	})
}

func TestWebhookUpdatedAutomationSQLite(t *testing.T) {
	runWebhookUpdatedAutomation(t, openTicketIntakeDB(t, false))
}

func TestWebhookUpdatedAutomationMySQL(t *testing.T) {
	runWebhookUpdatedAutomation(t, openTicketIntakeDB(t, true))
}

// rebuildWith replaces the engine and the service like rebuild, with the
// engine options given.
func (e *automationEnv) rebuildWith(opts ...AutomationEngineOption) {
	e.engine = NewAutomationEngine(e.repo, e.notifications, e.tasks, e.users, e.mailer, e.txManager, opts...)
	e.engine.SetClock(e.clock.Now)
	e.svc = NewAutomationService(e.repo, e.users, e.engine, e.txManager).(*automationService)
	e.svc.now = e.clock.Now
}

// startEntityRun is startRun for a lead or a ticket: it records the rule's
// trigger event for the record, dispatches it and returns the one run of the
// rule it started, still pending, removing the sibling runs of other rules.
func (e *automationEnv) startEntityRun(rule *models.AutomationRule, entityType string, entityID uint) *models.AutomationRun {
	e.t.Helper()
	before := len(e.runsOf(rule.ID))
	event := e.recordEvent(entityType, entityID, rule.TriggerType, "", "", models.AutomationOriginUser)
	e.dispatch()
	runs := e.runsOf(rule.ID)
	require.Len(e.t, runs, before+1, "the event starts exactly one run of the rule")
	require.NoError(e.t, e.db.
		Where("event_id = ? AND rule_id <> ? AND status = ?", event.ID, rule.ID, models.AutomationRunPending).
		Delete(&models.AutomationRun{}).Error)
	return &runs[len(runs)-1]
}

// runWebhookUpdatedAutomation: the automation assign_owner step (#408)
// writes lead.updated on a lead run and ticket.updated on a ticket run, in the
// step transaction, when it changed the record; a step that sets the owner
// or assignee the record already has, and an engine built without
// WithAutomationWebhooks, write no row.
func runWebhookUpdatedAutomation(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	all := webhook.AllEventTypes()
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, webhookUpdatedTypes...)
	env.endpoint("https://example.com/c", true, webhookCreatedTypes...)
	env.endpoint("https://example.com/d", false, all...)

	auto := newAutomationEnv(t, db)
	auto.rebuildWith(WithAutomationWebhooks(webhook.NewEmitter(env.repo)))
	support := auto.createUser("support", models.RoleSupport, true)
	leadRule := auto.leadRule(models.AutomationEventLeadCreated, assignStep(auto.other.ID))
	ticketRule := auto.createRule(models.AutomationEntityTicket, models.AutomationEventTicketCreated, models.AutomationTrigger{}, assignStep(support.ID))

	runStep := func(t *testing.T, engineEnv *automationEnv, rule *models.AutomationRule, entityType string, id uint) {
		t.Helper()
		run := engineEnv.startEntityRun(rule, entityType, id)
		engineEnv.executeDue()
		assert.Equal(t, models.AutomationRunCompleted, engineEnv.run(run.ID).Status)
		steps := engineEnv.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
	}

	lead := env.seedLead()
	ticket := env.seedTicket(models.TicketStatusOpen)

	t.Run("assign_owner on a lead run", func(t *testing.T) {
		env.clearDeliveries()
		runStep(t, auto, leadRule, models.AutomationEntityLead, lead.ID)
		assert.Equal(t, auto.other.ID, storedLead(t, db, lead.ID).OwnerID)
		assertFanOut(t, env, models.WebhookEventLeadUpdated, lead.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("assign_owner on a ticket run", func(t *testing.T) {
		env.clearDeliveries()
		runStep(t, auto, ticketRule, models.AutomationEntityTicket, ticket.ID)
		stored := storedTicket(t, db, ticket.ID)
		require.NotNil(t, stored.AssignedToID)
		assert.Equal(t, support.ID, *stored.AssignedToID)
		assertFanOut(t, env, models.WebhookEventTicketUpdated, ticket.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("the same target again writes no row", func(t *testing.T) {
		env.clearDeliveries()
		runStep(t, auto, leadRule, models.AutomationEntityLead, lead.ID)
		runStep(t, auto, ticketRule, models.AutomationEntityTicket, ticket.ID)
		assert.Equal(t, auto.other.ID, storedLead(t, db, lead.ID).OwnerID)
		assert.Zero(t, env.allDeliveries(), "an unchanged owner or assignee is no update")
	})

	t.Run("an engine without the option writes no row", func(t *testing.T) {
		env.clearDeliveries()
		plain := newAutomationEnv(t, db)
		freshLead := env.seedLead()
		freshTicket := env.seedTicket(models.TicketStatusOpen)
		runStep(t, plain, leadRule, models.AutomationEntityLead, freshLead.ID)
		runStep(t, plain, ticketRule, models.AutomationEntityTicket, freshTicket.ID)
		assert.Equal(t, auto.other.ID, storedLead(t, db, freshLead.ID).OwnerID, "the step still ran")
		require.NotNil(t, storedTicket(t, db, freshTicket.ID).AssignedToID)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}
