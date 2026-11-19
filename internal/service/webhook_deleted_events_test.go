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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The deleted events (#437): lead.deleted, customer.deleted and
// ticket.deleted are written inside the transaction that deletes (the ticket
// delete) or erases (every erasure unit, cascades included), only for the
// active endpoints subscribed to the deleted type that hold a created or
// updated delivery row of the record, on SQLite and on a disposable
// TEST_MYSQL_DSN schema (the MySQL roots skip without the variable and fail
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
	require.NoError(e.t, e.customers.Create(customer))
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

// announce writes a created or updated delivery row of the record for an
// endpoint directly, in the shape the outbox writes (no runtime runs here).
func (e *webhookEnv) announce(endpointID uint, eventType string, recordID uint) {
	e.t.Helper()
	eventID, err := newWebhookEventID()
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
	nine := models.WebhookEventTypeValues
	require.Len(t, nine, 9)
	announcing := append(append([]string{}, webhookCreatedTypes...), webhookUpdatedTypes...)
	// a: everything. b: the deleted types only, so it never announces a
	// record. c: announces but is not subscribed to a deleted type. d:
	// inactive (its announcements are written directly).
	a := env.endpoint("https://example.com/a", true, nine...)
	b := env.endpoint("https://example.com/b", true, webhookDeletedTypes...)
	c := env.endpoint("https://example.com/c", true, announcing...)
	d := env.endpoint("https://example.com/d", false, nine...)
	admin := models.RoleAdmin

	t.Run("lead erasure", func(t *testing.T) {
		lead := env.announcedLead()
		env.announce(d.ID, models.WebhookEventLeadCreated, lead.ID)
		assert.Len(t, env.deliveries(models.WebhookEventLeadCreated, lead.ID), 3, "a, c and d announced the lead")
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+1, env.deletedRows())
	})

	t.Run("customer erasure", func(t *testing.T) {
		customer := env.announcedCustomer()
		env.announce(d.ID, models.WebhookEventCustomerCreated, customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.customers.Delete(customer.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assert.Equal(t, before+1, env.deletedRows())
	})

	t.Run("ticket delete", func(t *testing.T) {
		ticket := env.announcedTicket()
		env.announce(d.ID, models.WebhookEventTicketCreated, ticket.ID)
		before := env.deletedRows()
		require.NoError(t, env.tickets.Delete(ticket.ID))
		assert.True(t, storedTicket(t, db, ticket.ID).DeletedAt.Valid)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, ticket.ID, a.ID)
		assert.Equal(t, before+1, env.deletedRows())
	})

	t.Run("an updated delivery announces too", func(t *testing.T) {
		lead := env.seedLead()
		env.announce(a.ID, models.WebhookEventLeadUpdated, lead.ID)
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+1, env.deletedRows())
	})

	t.Run("unannounced records get no deleted event", func(t *testing.T) {
		before := env.deletedRows()
		lead := env.seedLead()
		customer := env.seedCustomer()
		ticket := env.seedTicket(models.TicketStatusOpen)
		// Another entity's announcement of the same id does not count (ids
		// of different tables may coincide, so only distinct ones are used),
		// nor does an announcement by an endpoint not subscribed to deleted.
		if customer.ID != lead.ID {
			env.announce(a.ID, models.WebhookEventCustomerCreated, lead.ID)
		}
		if ticket.ID != customer.ID {
			env.announce(a.ID, models.WebhookEventTicketUpdated, customer.ID)
		}
		if lead.ID != ticket.ID {
			env.announce(a.ID, models.WebhookEventLeadCreated, ticket.ID)
		}
		env.announce(c.ID, models.WebhookEventLeadCreated, lead.ID)
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		require.NoError(t, env.customers.Delete(customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		assert.Equal(t, before, env.deletedRows())
	})

	t.Run("erasure is indistinguishable from delete", func(t *testing.T) {
		lead, customer := env.convertedLead()
		ticket := env.announcedTicket()
		require.NoError(t, env.customers.Delete(customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		erased := env.deliveries(models.WebhookEventLeadDeleted, lead.ID)
		deleted := env.deliveries(models.WebhookEventTicketDeleted, ticket.ID)
		require.Len(t, erased, 1)
		require.Len(t, deleted, 1)
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
			body, err := WebhookBody(&row)
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
		body, err := WebhookBody(&erased[0])
		require.NoError(t, err)
		for _, personal := range []string{lead.Email, lead.FirstName, lead.LastName, lead.Phone} {
			require.NotEmpty(t, personal)
			assert.NotContains(t, string(body), personal)
		}
	})

	t.Run("cascade from the customer", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		require.NoError(t, env.customers.Delete(customer.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("cascade from the converted lead", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("cascade from the login, ungoverned", func(t *testing.T) {
		lead, customer := env.convertedLead()
		login := env.customerLogin(customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.users.Delete(login.ID))
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("cascade from the login, governed", func(t *testing.T) {
		lead, customer := env.convertedLead()
		login := env.customerLogin(customer.ID)
		before := env.deletedRows()
		require.NoError(t, env.governedUsers.DeleteAs(ctx, Actor{UserID: env.owner.ID, AuthMethod: AuthMethodBearer}, login.ID))
		assert.True(t, storedCustomer(t, db, customer.ID).DeletedAt.Valid)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	t.Run("a customer soft-deleted earlier gets no deleted event", func(t *testing.T) {
		lead, customer := env.convertedLead()
		// A legacy soft delete, before deletion became an erasure.
		require.NoError(t, db.Delete(&models.Customer{}, customer.ID).Error)
		before := env.deletedRows()
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Empty(t, env.deliveries(models.WebhookEventCustomerDeleted, customer.ID))
		assert.Equal(t, before+1, env.deletedRows())
		// The legacy row was still scrubbed.
		assert.NotEqual(t, customer.Email, storedCustomer(t, db, customer.ID).Email)
	})

	t.Run("a refused non-admin erasure writes nothing", func(t *testing.T) {
		lead, customer := env.convertedLead()
		before := env.deletedRows()
		err := env.leads.Delete(lead.ID, models.RoleSales)
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
		require.NoError(t, env.leads.Delete(lead.ID, admin))
		require.NoError(t, env.customers.Delete(customer.ID))
		require.NoError(t, env.tickets.Delete(ticket.ID))
		before := env.deletedRows()
		assert.ErrorIs(t, env.leads.Delete(lead.ID, admin), apperrors.ErrNotFound)
		assert.ErrorIs(t, env.customers.Delete(customer.ID), apperrors.ErrNotFound)
		assert.ErrorIs(t, env.tickets.Delete(ticket.ID), apperrors.ErrNotFound)
		assert.Equal(t, before, env.deletedRows())
		// Erasing the soft-deleted rows again at the repository (a repeated
		// erasure re-scrubs them) writes no event either.
		require.NoError(t, repository.NewLeadRepository(db, repository.WithLeadErasureListener(NewWebhookErasureListener(env.repo))).Delete(lead.ID))
		require.NoError(t, repository.NewCustomerRepository(db, repository.WithCustomerErasureListener(NewWebhookErasureListener(env.repo))).Delete(customer.ID))
		assert.Equal(t, before, env.deletedRows())
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventTicketDeleted, ticket.ID, a.ID)
	})

	t.Run("a restarted erasure commits one row set", func(t *testing.T) {
		lead, customer := env.convertedLead()
		real := NewWebhookErasureListener(env.repo)
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
		require.NoError(t, env.customers.Delete(customer.ID))
		assert.Equal(t, int32(2), calls.Load(), "the restart called the listener again")
		assertFanOut(t, env, models.WebhookEventCustomerDeleted, customer.ID, a.ID)
		assertFanOut(t, env, models.WebhookEventLeadDeleted, lead.ID, a.ID)
		assert.Equal(t, before+2, env.deletedRows())
	})

	assert.Empty(t, env.deliveriesOf(b.ID), "b never announced a record")
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
	env.endpoint("https://example.com/all", true, models.WebhookEventTypeValues...)
	admin := models.RoleAdmin

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
		real := NewWebhookErasureListener(env.repo)
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
			err := env.leads.Delete(lead.ID, admin)
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
			err := env.customers.Delete(customer.ID)
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
			err := env.users.Delete(login.ID)
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
			assert.ErrorIs(t, env.customers.Delete(customer.ID), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)

			assert.ErrorIs(t, env.leads.Delete(lead.ID, admin), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)

			login := env.customerLogin(customer.ID)
			assert.ErrorIs(t, env.users.Delete(login.ID), afterEnqueue)
			assert.ErrorIs(t, env.governedUsers.DeleteAs(ctx, Actor{UserID: env.owner.ID, AuthMethod: AuthMethodBearer}, login.ID), afterEnqueue)
			assertLeadLive(t, db, lead)
			assertCustomerLive(t, db, customer)
		})
		assert.Equal(t, before, env.deletedRows())
	})
}
