package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The deleted events (#437): lead.deleted, customer.deleted and
// ticket.deleted are written inside the transaction that deletes (the ticket
// delete) or erases (every erasure unit, cascades included), for every active
// endpoint subscribed to the deleted type, whether or not that endpoint
// received an earlier event for the record (#531), on SQLite and on a
// disposable TEST_MYSQL_DSN schema (the MySQL roots skip without the variable and fail
// when it is unreachable).

func TestWebhookDeletedFanOutSQLite(t *testing.T) {
	runWebhookDeletedFanOut(t, openTicketIntakeDB(t, false))
}

func TestWebhookDeletedFanOutMySQL(t *testing.T) {
	runWebhookDeletedFanOut(t, openTicketIntakeDB(t, true))
}

func TestWebhookDeletedAtomicitySQLite(t *testing.T) {
	runWebhookDeletedAtomicity(t, openTicketIntakeDB(t, false))
}

func TestWebhookDeletedAtomicityMySQL(t *testing.T) {
	runWebhookDeletedAtomicity(t, openTicketIntakeDB(t, true))
}

var webhookDeletedTypes = []string{models.WebhookEventLeadDeleted, models.WebhookEventCustomerDeleted, models.WebhookEventTicketDeleted}

// deletedRows counts the rows of every *.deleted event type.
func (e *webhookEnv) deletedRows() int64 {
	e.t.Helper()
	return e.count(&models.WebhookDelivery{}, "event_type LIKE ?", "%.deleted")
}

// announcedLead creates a lead through the wired service (lead.created).
func (e *webhookEnv) announcedLead() *models.Lead {
	e.t.Helper()
	lead := e.newLead()
	lead.Phone = "+40 700 111 222"
	require.NoError(e.t, e.leads.Create(lead))
	return lead
}

// announcedCustomer creates a customer through the wired service
// (customer.created).
func (e *webhookEnv) announcedCustomer() *models.Customer {
	e.t.Helper()
	customer := e.newCustomer()
	require.NoError(e.t, e.customers.Create(context.Background(), customer))
	return customer
}

// announcedTicket creates a ticket through the wired service (ticket.created).
func (e *webhookEnv) announcedTicket() *models.Ticket {
	e.t.Helper()
	ticket := e.newTicket(e.seedCustomer().ID)
	require.NoError(e.t, e.tickets.Create(ticket))
	return ticket
}

// convertedLead creates a lead and converts it through the wired services:
// lead.created and customer.created are announced.
func (e *webhookEnv) convertedLead() (*models.Lead, *models.Customer) {
	e.t.Helper()
	lead := e.announcedLead()
	customer, err := e.leads.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
	require.NoError(e.t, err)
	return lead, customer
}

// customerLogin links a new customer-role account to the customer.
func (e *webhookEnv) customerLogin(customerID uint) *models.User {
	e.t.Helper()
	login := &models.User{Email: e.uniqueEmail("login"), Password: "x", FirstName: "Log", LastName: "In",
		Role: models.RoleCustomer, IsActive: true}
	require.NoError(e.t, e.db.Create(login).Error)
	require.NoError(e.t, e.db.Model(&models.Customer{}).Where("id = ?", customerID).Update("user_id", login.ID).Error)
	return login
}

// announce writes a delivery row of the record for an endpoint directly, in
// the shape the outbox writes (no runtime runs here). Earlier rows do not
// decide who receives a deleted event; the subtests use them to show that.
func (e *webhookEnv) announce(endpointID uint, eventType string, recordID uint) {
	e.t.Helper()
	eventID, err := webhook.NewEventID()
	require.NoError(e.t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := models.WebhookDelivery{EndpointID: endpointID, EventID: eventID, EventType: eventType, RecordID: recordID,
		OccurredAt: now, Status: models.WebhookDeliveryPending, NextAttemptAt: &now}
	require.NoError(e.t, e.db.Create(&row).Error)
}

// assertLive asserts the lead or customer is live and keeps its address.
func assertLeadLive(t *testing.T, db *gorm.DB, lead *models.Lead) {
	t.Helper()
	stored := storedLead(t, db, lead.ID)
	assert.False(t, stored.DeletedAt.Valid, "lead %d stays live", lead.ID)
	assert.Equal(t, lead.Email, stored.Email, "lead %d keeps its address", lead.ID)
}

func assertCustomerLive(t *testing.T, db *gorm.DB, customer *models.Customer) {
	t.Helper()
	stored := storedCustomer(t, db, customer.ID)
	assert.False(t, stored.DeletedAt.Valid, "customer %d stays live", customer.ID)
	assert.Equal(t, customer.Email, stored.Email, "customer %d keeps its address", customer.ID)
}

func runWebhookDeletedFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	allTypes := webhook.AllEventTypes()
	require.Len(t, allTypes, 18)
	announcing := append(append([]string{}, webhookCreatedTypes...), webhookUpdatedTypes...)
	// a: everything. b: the deleted types only, so it never receives a
	// created or updated event, yet receives every deleted event. c: not
	// subscribed to a deleted type. d: inactive (its earlier rows are written
	// directly); it is the check that the outbox still filters on is_active.
	a := env.endpoint("https://example.com/a", true, allTypes...)
	b := env.endpoint("https://example.com/b", true, webhookDeletedTypes...)
	c := env.endpoint("https://example.com/c", true, announcing...)
	d := env.endpoint("https://example.com/d", false, allTypes...)
	admin := models.PermissionScopeAll

	t.Run("lead erasure", func(t *testing.T) {
		lead := env.announcedLead()
		env.announce(d.ID, models.WebhookEventLeadCreated, lead.ID)
		assert.Len(t, env.deliveries(models.WebhookEventLeadCreated, lead.ID), 3, "a, c and d hold lead.created")
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("customer erasure", func(t *testing.T) {
		customer := env.announcedCustomer()
		env.announce(d.ID, models.WebhookEventCustomerCreated, customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("ticket delete", func(t *testing.T) {
		ticket := env.announcedTicket()
		env.announce(d.ID, models.WebhookEventTicketCreated, ticket.ID)
		before := env.deletedRows()
		require.NoError(t, env.tickets.Delete(ticket.ID))
		assert.True(t, storedTicket(t, db, ticket.ID).DeletedAt.Valid)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, ticket.ID, a.ID, b.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("a record with an earlier updated delivery", func(t *testing.T) {
		lead := env.seedLead()
		env.announce(a.ID, models.WebhookEventLeadUpdated, lead.ID)
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("records no endpoint received an event for", func(t *testing.T) {
		before := env.deletedRows()
		lead := env.seedLead()
		customer := env.seedCustomer()
		ticket := env.seedTicket(models.TicketStatusOpen)
		assert.Empty(t, env.deliveries(models.WebhookEventLeadCreated, lead.ID))
		assert.Empty(t, env.deliveries(models.WebhookEventCustomerCreated, customer.ID))
		assert.Empty(t, env.deliveries(models.WebhookEventTicketCreated, ticket.ID))
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, ticket.ID, a.ID, b.ID)
		assert.Equal(t, before+6, env.deletedRows())
	})

	t.Run("a record whose earlier deliveries were pruned", func(t *testing.T) {
		lead := env.announcedLead()
		created := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
		require.Len(t, created, 2, "a and c hold lead.created")
		now := time.Now().UTC()
		old := now.Add(-webhook.WebhookDeliveryRetention - time.Hour)
		require.NoError(t, db.Model(&models.WebhookDelivery{}).
			Where("event_type = ? AND record_id = ?", models.WebhookEventLeadCreated, lead.ID).
			UpdateColumns(map[string]interface{}{"status": models.WebhookDeliverySucceeded, "updated_at": old}).Error)
		pruned, err := env.repo.PruneFinished(ctx, now.Add(-webhook.WebhookDeliveryRetention))
		require.NoError(t, err)
		assert.GreaterOrEqual(t, pruned, int64(2))
		require.Empty(t, env.deliveries(models.WebhookEventLeadCreated, lead.ID), "the lead.created rows were pruned")
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("erasure is indistinguishable from delete", func(t *testing.T) {
		lead, customer := env.convertedLead()
		ticket := env.announcedTicket()
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		erased := env.deliveries(models.WebhookEventLeadDeleted, lead.ID)
		deleted := env.deliveries(models.WebhookEventTicketDeleted, ticket.ID)
		require.Len(t, erased, 2, "a and b")
		require.Len(t, deleted, 2, "a and b")
		for _, row := range []models.WebhookDelivery{erased[0], deleted[0]} {
			assert.Equal(t, models.WebhookDeliveryPending, row.Status)
			assert.Equal(t, 0, row.Attempts)
			assert.Equal(t, "", row.ErrorCode)
			require.NotNil(t, row.NextAttemptAt)
			assert.True(t, row.NextAttemptAt.Equal(row.OccurredAt))
		}
		assert.Equal(t, erased[0].Status, deleted[0].Status)
		assert.Equal(t, erased[0].Attempts, deleted[0].Attempts)
		assert.Equal(t, erased[0].ErrorCode, deleted[0].ErrorCode)
		assert.Equal(t, erased[0].LastAttemptAt, deleted[0].LastAttemptAt)
		assert.Equal(t, erased[0].LastHTTPStatus, deleted[0].LastHTTPStatus)

		for _, row := range []models.WebhookDelivery{erased[0], deleted[0]} {
			body, err := webhook.WebhookBody(&row)
			require.NoError(t, err)
			var decoded map[string]interface{}
			require.NoError(t, json.Unmarshal(body, &decoded))
			keys := make([]string, 0, len(decoded))
			for key := range decoded {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			assert.Equal(t, []string{"id", "occurred_at", "record_id", "type"}, keys)
			assert.Equal(t, row.EventType, decoded["type"])
		}
		body, err := webhook.WebhookBody(&erased[0])
		require.NoError(t, err)
		for _, personal := range []string{lead.Email, lead.FirstName, lead.LastName, lead.Phone} {
			require.NotEmpty(t, personal)
			assert.NotContains(t, string(body), personal)
		}
	})

	t.Run("cascade from the customer", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+4, env.deletedRows())
	})

	t.Run("cascade from the converted lead", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+4, env.deletedRows())
	})

	t.Run("cascade from the login, ungoverned", func(t *testing.T) {
		lead, customer := env.convertedLead()
		login := env.customerLogin(customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.users.Delete(context.Background(), login.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+4, env.deletedRows())
	})

	t.Run("cascade from the login, governed", func(t *testing.T) {
		lead, customer := env.convertedLead()
		login := env.customerLogin(customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.governedUsers.DeleteAs(ctx, Actor{UserID: env.owner.ID, AuthMethod: AuthMethodBearer}, login.ID))
		assert.True(t, storedCustomer(t, db, customer.ID).DeletedAt.Valid)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+4, env.deletedRows())
	})

	t.Run("a customer soft-deleted earlier gets no deleted event", func(t *testing.T) {
		lead, customer := env.convertedLead()
		// A legacy soft delete, before deletion became an erasure.
		require.NoError(t, db.Delete(&models.Customer{}, customer.ID).Error)
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Empty(t, env.deliveries(models.WebhookEventCustomerDeleted, customer.ID))
		assert.Equal(t, before+2, env.deletedRows())
		// The legacy row was still scrubbed.
		assert.NotEqual(t, customer.Email, storedCustomer(t, db, customer.ID).Email)
	})

	t.Run("a refused non-admin erasure writes nothing", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		err := env.leads.Delete(context.Background(), lead.ID, models.PermissionScope(""))
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.ErrForbidden)
		assert.Equal(t, before, env.deletedRows())
		assertLeadLive(t, db, lead)
		assertCustomerLive(t, db, customer)
	})

	t.Run("a second delete writes nothing", func(t *testing.T) {
		lead := env.announcedLead()
		customer := env.announcedCustomer()
		ticket := env.announcedTicket()
		require.NoError(t, env.leads.Delete(context.Background(), lead.ID, admin))
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		before := env.deletedRows()
		assert.ErrorIs(t, env.leads.Delete(context.Background(), lead.ID, admin), apperrors.ErrNotFound)
		assert.ErrorIs(t, env.customers.Delete(context.Background(), customer.ID), apperrors.ErrNotFound)
		assert.ErrorIs(t, env.tickets.Delete(ticket.ID), apperrors.ErrNotFound)
		assert.Equal(t, before, env.deletedRows())
		// Erasing the soft-deleted rows again at the repository (a repeated
		// erasure re-scrubs them) writes no event either.
		require.NoError(t, repository.NewLeadRepository(db, repository.WithLeadErasureListener(webhook.NewErasureListener(webhook.NewEmitter(env.repo)))).Delete(context.Background(), lead.ID))
		require.NoError(t, repository.NewCustomerRepository(db, repository.WithCustomerErasureListener(webhook.NewErasureListener(webhook.NewEmitter(env.repo)))).Delete(context.Background(), customer.ID))
		assert.Equal(t, before, env.deletedRows())
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, ticket.ID, a.ID, b.ID)
	})

	t.Run("a restarted erasure commits one row set", func(t *testing.T) {
		lead, customer := env.convertedLead()
		real := webhook.NewErasureListener(webhook.NewEmitter(env.repo))
		var calls atomic.Int32
		env.erasureListener = func(tx *gorm.DB, erased repository.ErasedRecords) error {
			if err := real(tx, erased); err != nil {
				return err
			}
			if calls.Add(1) == 1 {
				// The first attempt wrote its rows and then restarts.
				return repository.ErrErasureGraphChanged
			}
			return nil
		}
		env.wire(env.repo)
		defer func() {
			env.erasureListener = nil
			env.wire(env.repo)
		}()
		before := env.deletedRows()
		require.NoError(t, env.customers.Delete(context.Background(), customer.ID))
		assert.Equal(t, int32(2), calls.Load(), "the restart called the listener again")
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID, b.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID, b.ID)
		assert.Equal(t, before+4, env.deletedRows())
	})

	// b holds deleted rows only, and at least one of each deleted type.
	bTypes := map[string]int{}
	for _, row := range env.deliveriesOf(b.ID) {
		bTypes[row.EventType]++
	}
	for eventType := range bTypes {
		assert.Contains(t, webhookDeletedTypes, eventType, "b holds only deleted rows")
	}
	for _, eventType := range webhookDeletedTypes {
		assert.Positive(t, bTypes[eventType], "b holds %s rows", eventType)
	}
	assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ? AND event_type LIKE ?", c.ID, "%.deleted"))
	assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ? AND event_type LIKE ?", d.ID, "%.deleted"))
}

// deliveriesOf returns every row of one endpoint.
func (e *webhookEnv) deliveriesOf(endpointID uint) []models.WebhookDelivery {
	e.t.Helper()
	var rows []models.WebhookDelivery
	require.NoError(e.t, e.db.Where("endpoint_id = ?", endpointID).Order("id").Find(&rows).Error)
	return rows
}

func runWebhookDeletedAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	env.endpoint("https://example.com/all", true, webhook.AllEventTypes()...)
	admin := models.PermissionScopeAll

	// withFailingOutbox runs fn with an outbox whose insert writes the rows
	// and then fails, and rewires the real one afterwards.
	withFailingOutbox := func(fn func()) {
		env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))
		defer env.wire(env.repo)
		fn()
	}
	// withFailingListener runs fn with an erasure listener that enqueues
	// through the real outbox and then fails inside the same unit.
	afterEnqueue := errors.New("injected failure after the enqueue")
	withFailingListener := func(fn func()) {
		real := webhook.NewErasureListener(webhook.NewEmitter(env.repo))
		env.erasureListener = func(tx *gorm.DB, erased repository.ErasedRecords) error {
			if err := real(tx, erased); err != nil {
				return err
			}
			var written int64
			require.NoError(t, tx.Model(&models.WebhookDelivery{}).Where("event_type LIKE ?", "%.deleted").Count(&written).Error)
			assert.Positive(t, written, "the rows were written inside the unit")
			return afterEnqueue
		}
		env.wire(env.repo)
		defer func() {
			env.erasureListener = nil
			env.wire(env.repo)
		}()
		fn()
	}

	t.Run("ticket delete, outbox failure", func(t *testing.T) {
		ticket := env.announcedTicket()
		before := env.deletedRows()
		withFailingOutbox(func() {
			err := env.tickets.Delete(ticket.ID)
			require.Error(t, err)
			assert.ErrorIs(t, err, injectedFailure)
		})
		assert.False(t, storedTicket(t, db, ticket.ID).DeletedAt.Valid, "the ticket stays live")
		assert.Equal(t, before, env.deletedRows())
		// The bound repository path committed nothing either: a retry works.
		require.NoError(t, env.tickets.Delete(ticket.ID))
		assert.Equal(t, before+1, env.deletedRows())
	})

	t.Run("ticket delete, failure after the soft delete", func(t *testing.T) {
		ticket := env.announcedTicket()
		before := env.deletedRows()
		const name = "test:fail_ticket_soft_delete"
		require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "tickets" {
				_ = tx.AddError(afterEnqueue)
			}
		}))
		err := env.tickets.Delete(ticket.ID)
		require.NoError(t, db.Callback().Delete().Remove(name))
		require.Error(t, err)
		assert.False(t, storedTicket(t, db, ticket.ID).DeletedAt.Valid, "the ticket stays live")
		assert.Equal(t, before, env.deletedRows())
	})

	t.Run("lead erasure, outbox failure", func(t *testing.T) {
		lead := env.announcedLead()
		before := env.deletedRows()
		withFailingOutbox(func() {
			err := env.leads.Delete(context.Background(), lead.ID, admin)
			require.Error(t, err)
			assert.ErrorIs(t, err, injectedFailure)
		})
		assertLeadLive(t, db, lead)
		assert.Equal(t, before, env.deletedRows())
	})

	t.Run("customer erasure, outbox failure", func(t *testing.T) {
		customer := env.announcedCustomer()
		before := env.deletedRows()
		withFailingOutbox(func() {
			err := env.customers.Delete(context.Background(), customer.ID)
			require.Error(t, err)
			assert.ErrorIs(t, err, injectedFailure)
		})
		assertCustomerLive(t, db, customer)
		assert.Equal(t, before, env.deletedRows())
	})

	t.Run("user erasure, outbox failure", func(t *testing.T) {
		lead, customer := env.convertedLead()
		login := env.customerLogin(customer.ID)
		before := env.deletedRows()
		withFailingOutbox(func() {
			err := env.users.Delete(context.Background(), login.ID)
			require.Error(t, err)
			assert.ErrorIs(t, err, injectedFailure)
			err = env.governedUsers.DeleteAs(ctx, Actor{UserID: env.owner.ID, AuthMethod: AuthMethodBearer}, login.ID)
			require.Error(t, err)
			assert.ErrorIs(t, err, injectedFailure)
		})
		assertLeadLive(t, db, lead)
		assertCustomerLive(t, db, customer)
		var stored models.User
		require.NoError(t, db.Unscoped().First(&stored, login.ID).Error)
		assert.False(t, stored.DeletedAt.Valid)
		assert.Equal(t, login.Email, stored.Email)
		assert.Equal(t, before, env.deletedRows())
	})

	t.Run("cascade erasures, failure after the enqueue", func(t *testing.T) {
		before := env.deletedRows()
		withFailingListener(func() {
			lead, customer := env.convertedLead()
			assert.ErrorIs(t, env.customers.Delete(context.Background(), customer.ID), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)

			assert.ErrorIs(t, env.leads.Delete(context.Background(), lead.ID, admin), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)

			login := env.customerLogin(customer.ID)
			assert.ErrorIs(t, env.users.Delete(context.Background(), login.ID), afterEnqueue)
			assert.ErrorIs(t, env.governedUsers.DeleteAs(ctx, Actor{UserID: env.owner.ID, AuthMethod: AuthMethodBearer}, login.ID), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)
		})
		assert.Equal(t, before, env.deletedRows())
	})
}
