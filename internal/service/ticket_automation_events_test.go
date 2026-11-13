package service

// AC-1 and AC-14 of #242: with WithTicketAutomationEvents wired, a ticket
// create inserts the ticket and one ticket_created event in one transaction,
// and an update locks the ticket row first, checks against the locked row,
// saves and records ticket_updated (plus ticket_status_changed when the
// status differs). Refused and failed writes record nothing. Without the
// option the service behaves as before. Every assertion reads committed rows.

import (
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationTicketEventsSQLite(t *testing.T) {
	runAutomationTicketEvents(t, openAutomationSQLite(t))
}

func TestAutomationTicketEventsMySQL(t *testing.T) {
	runAutomationTicketEvents(t, openAutomationMySQL(t, 3))
}

func runAutomationTicketEvents(t *testing.T, db *gorm.DB) {
	env := newTicketAutomationEnv(t, db)

	t.Run("create records ticket_created in the write transaction", func(t *testing.T) {
		assignee := env.support.ID
		ticket := &models.Ticket{Title: ticketCanaryTitle, Description: ticketCanaryDescription,
			CustomerID: env.customer.ID, AssignedToID: &assignee}
		require.NoError(t, env.ticketSvc.Create(ticket))
		require.NotZero(t, ticket.ID)

		stored := env.bareTicket(ticket.ID)
		assert.Equal(t, models.TicketStatusOpen, stored.Status, "the default status is stored")
		events := env.ticketEvents(ticket.ID)
		require.Len(t, events, 1)
		event := events[0]
		assert.Equal(t, models.AutomationEntityTicket, event.EntityType)
		assert.Equal(t, models.AutomationEventTicketCreated, event.EventType)
		assert.Equal(t, string(models.TicketStatusOpen), event.ToValue, "to_value is the stored status")
		assert.Equal(t, "", event.FromValue)
		assert.Equal(t, models.AutomationOriginUser, event.Origin)
		assert.Nil(t, event.ActorID, "single create records no actor")
		assert.Nil(t, event.ProcessedAt)
	})

	t.Run("status change records ticket_updated and ticket_status_changed", func(t *testing.T) {
		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		current, err := env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		current.Status = models.TicketStatusInProgress
		require.NoError(t, env.ticketSvc.Update(current))

		assert.Equal(t, models.TicketStatusInProgress, env.bareTicket(ticket.ID).Status)
		events := env.ticketEvents(ticket.ID)
		require.Equal(t, []string{models.AutomationEventTicketUpdated, models.AutomationEventTicketStatusChanged}, eventTypes(events))
		assert.Equal(t, "", events[0].FromValue)
		assert.Equal(t, "", events[0].ToValue)
		assert.Equal(t, string(models.TicketStatusOpen), events[1].FromValue)
		assert.Equal(t, string(models.TicketStatusInProgress), events[1].ToValue)
		for _, event := range events {
			assert.Equal(t, models.AutomationOriginUser, event.Origin)
			assert.Nil(t, event.ActorID)
		}
		require.NotNil(t, current.AssignedTo, "the response still carries the assignee")
		assert.Equal(t, env.support.ID, current.AssignedTo.ID)
	})

	t.Run("priority, assignee or due date alone records ticket_updated only", func(t *testing.T) {
		ticket := env.createTicket(env.support, models.TicketStatusInProgress)
		current, err := env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		current.Priority = models.TicketPriorityLow
		reassigned := env.support2.ID
		current.AssignedToID = &reassigned
		require.NoError(t, env.ticketSvc.Update(current))

		stored := env.bareTicket(ticket.ID)
		assert.Equal(t, models.TicketPriorityLow, stored.Priority)
		require.NotNil(t, stored.AssignedToID)
		assert.Equal(t, env.support2.ID, *stored.AssignedToID)
		assert.Equal(t, []string{models.AutomationEventTicketUpdated}, eventTypes(env.ticketEvents(ticket.ID)))
	})

	t.Run("checks and from_value use the locked row, not the caller's read", func(t *testing.T) {
		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		stale, err := env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		// Another writer resolves the ticket after the caller's read.
		require.NoError(t, db.Model(&models.Ticket{}).Where("id = ?", ticket.ID).Update("status", models.TicketStatusResolved).Error)

		stale.Status = models.TicketStatusInProgress
		require.NoError(t, env.ticketSvc.Update(stale))
		events := env.ticketEvents(ticket.ID)
		require.Len(t, events, 2)
		assert.Equal(t, string(models.TicketStatusResolved), events[1].FromValue, "from_value comes from the locked row")
		assert.Equal(t, string(models.TicketStatusInProgress), events[1].ToValue)

		closed := env.createTicket(env.support, models.TicketStatusOpen)
		staleClosed, err := env.tickets.GetByID(closed.ID)
		require.NoError(t, err)
		require.NoError(t, db.Model(&models.Ticket{}).Where("id = ?", closed.ID).Update("status", models.TicketStatusClosed).Error)
		staleClosed.Status = models.TicketStatusResolved
		err = env.ticketSvc.Update(staleClosed)
		require.ErrorIs(t, err, apperrors.ErrClosedTicketReopen, "the reopen check reads the locked row")
		assert.Equal(t, models.TicketStatusClosed, env.bareTicket(closed.ID).Status)
		assert.Empty(t, env.ticketEvents(closed.ID))
	})

	t.Run("a refused update writes nothing and records no event", func(t *testing.T) {
		closed := env.createTicket(env.support, models.TicketStatusClosed)
		current, err := env.tickets.GetByID(closed.ID)
		require.NoError(t, err)
		current.Status = models.TicketStatusOpen
		current.Priority = models.TicketPriorityLow
		require.ErrorIs(t, env.ticketSvc.Update(current), apperrors.ErrClosedTicketReopen)
		stored := env.bareTicket(closed.ID)
		assert.Equal(t, models.TicketStatusClosed, stored.Status)
		assert.Equal(t, models.TicketPriorityHigh, stored.Priority)
		assert.Empty(t, env.ticketEvents(closed.ID))

		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		current, err = env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		salesID := env.sales.ID
		current.AssignedToID = &salesID
		current.Status = models.TicketStatusResolved
		require.ErrorIs(t, env.ticketSvc.Update(current), apperrors.ErrInvalidAssigneeRole)
		stored = env.bareTicket(ticket.ID)
		assert.Equal(t, models.TicketStatusOpen, stored.Status)
		require.NotNil(t, stored.AssignedToID)
		assert.Equal(t, env.support.ID, *stored.AssignedToID)
		assert.Empty(t, env.ticketEvents(ticket.ID))

		missing := &models.Ticket{Status: models.TicketStatusOpen}
		missing.ID = 99999999
		require.True(t, errors.Is(env.ticketSvc.Update(missing), gorm.ErrRecordNotFound), "a missing ticket is not found")
	})

	t.Run("an event insert failure rolls the ticket write back", func(t *testing.T) {
		recorded := 0
		faulty := NewTicketService(env.tickets, env.customers, env.users,
			WithTicketAutomationEvents(eventFaultRepo{AutomationRepository: env.repo, allow: 0, recorded: &recorded}, env.txManager))
		before := env.count(&models.Ticket{})
		eventsBefore := env.count(&models.AutomationEvent{})

		created := &models.Ticket{Title: "Fault", Description: "Fault", CustomerID: env.customer.ID}
		require.ErrorIs(t, faulty.Create(created), errEventWriteFault)
		assert.Equal(t, before, env.count(&models.Ticket{}), "the ticket insert rolled back")
		assert.Zero(t, created.ID)

		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		current, err := env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		current.Status = models.TicketStatusResolved
		require.ErrorIs(t, faulty.Update(current), errEventWriteFault)
		assert.Equal(t, models.TicketStatusOpen, env.bareTicket(ticket.ID).Status, "the ticket write rolled back")
		assert.Equal(t, eventsBefore, env.count(&models.AutomationEvent{}))

		// The second event (ticket_status_changed) failing rolls back the
		// first as well.
		recorded = 0
		second := NewTicketService(env.tickets, env.customers, env.users,
			WithTicketAutomationEvents(eventFaultRepo{AutomationRepository: env.repo, allow: 1, recorded: &recorded}, env.txManager))
		current, err = env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		current.Status = models.TicketStatusResolved
		require.ErrorIs(t, second.Update(current), errEventWriteFault)
		assert.Equal(t, models.TicketStatusOpen, env.bareTicket(ticket.ID).Status)
		assert.Empty(t, env.ticketEvents(ticket.ID))
	})

	t.Run("the update locks the ticket before it writes", func(t *testing.T) {
		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		current, err := env.tickets.GetByID(ticket.ID)
		require.NoError(t, err)
		log := captureStatements(t, db)
		current.Status = models.TicketStatusResolved
		require.NoError(t, env.ticketSvc.Update(current))

		read := log.index(ticketReadSQL)
		write := log.index(ticketWriteSQL)
		event := log.index(ticketEventInsert)
		require.GreaterOrEqual(t, read, 0, "the ticket is read inside the update")
		require.Greater(t, write, read, "the read precedes the write")
		require.Greater(t, event, write, "the events follow the write")
		if isMySQL(db) {
			locking := log.index(ticketLockingRead)
			require.GreaterOrEqual(t, locking, 0, "the read is FOR UPDATE on MySQL:\n%s", log.joined())
			assert.Less(t, locking, write)
		}
	})
}

// AC-14: a ticket service built without WithTicketAutomationEvents writes the
// ticket, records no event and returns the same errors as before.
func TestTicketServiceWithoutAutomationUnchanged(t *testing.T) {
	env := newTicketAutomationEnv(t, openAutomationSQLite(t))
	variants := map[string]TicketService{
		"bare": NewTicketService(env.tickets, env.customers, env.users),
		// The comment mail option sets the transaction manager; without the
		// automation repository nothing is recorded either.
		"comment mail only": NewTicketService(env.tickets, env.customers, env.users,
			WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(env.db), env.txManager, nil)),
	}
	for name, svc := range variants {
		svc := svc
		t.Run(name, func(t *testing.T) {
			created := &models.Ticket{Title: "Plain", Description: "Plain", CustomerID: env.customer.ID}
			require.NoError(t, svc.Create(created))
			assert.Equal(t, models.TicketStatusOpen, env.bareTicket(created.ID).Status)

			current, err := env.tickets.GetByID(created.ID)
			require.NoError(t, err)
			current.Status = models.TicketStatusResolved
			require.NoError(t, svc.Update(current))
			assert.Equal(t, models.TicketStatusResolved, env.bareTicket(created.ID).Status)

			current, err = env.tickets.GetByID(created.ID)
			require.NoError(t, err)
			salesID := env.sales.ID
			current.AssignedToID = &salesID
			require.ErrorIs(t, svc.Update(current), apperrors.ErrInvalidAssigneeRole)

			require.ErrorIs(t, svc.Create(&models.Ticket{Title: "No customer", CustomerID: 99999999}), apperrors.ErrCustomerNotFound)
			require.ErrorIs(t, svc.Create(&models.Ticket{Title: "Bad assignee", CustomerID: env.customer.ID, AssignedToID: &salesID}),
				apperrors.ErrInvalidAssigneeRole)

			closed := env.createTicket(env.support, models.TicketStatusClosed)
			current, err = env.tickets.GetByID(closed.ID)
			require.NoError(t, err)
			current.Status = models.TicketStatusOpen
			require.ErrorIs(t, svc.Update(current), apperrors.ErrClosedTicketReopen)

			missing := &models.Ticket{Status: models.TicketStatusOpen}
			missing.ID = 99999999
			require.True(t, errors.Is(svc.Update(missing), gorm.ErrRecordNotFound))

			assert.Empty(t, env.ticketEvents(created.ID))
			assert.Zero(t, env.count(&models.AutomationEvent{}), "no automation event was recorded")
		})
	}
}
