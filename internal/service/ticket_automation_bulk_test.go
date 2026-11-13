package service

// AC-2 of #242: POST /tickets/bulk/status reads the batch's tickets with a
// locking read in id order inside the batch transaction and records there
// ticket_updated per ticket and ticket_status_changed (from the locked
// status, origin bulk, actor the caller) for each ticket whose status
// differs. A refused batch records no event for any ticket.

import (
	"context"
	"regexp"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationTicketBulkEventsSQLite(t *testing.T) {
	runAutomationTicketBulkEvents(t, openAutomationSQLite(t))
}

func TestAutomationTicketBulkEventsMySQL(t *testing.T) {
	runAutomationTicketBulkEvents(t, openAutomationMySQL(t, 3))
}

func runAutomationTicketBulkEvents(t *testing.T, db *gorm.DB) {
	env := newTicketAutomationEnv(t, db)
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)
	bulk := NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		env.txManager, quiet, WithBulkAutomationEvents(env.repo))
	ctx := context.Background()

	t.Run("each ticket gets its events, origin bulk, actor the caller", func(t *testing.T) {
		open := env.createTicket(env.support, models.TicketStatusOpen)
		resolved := env.createTicket(env.support, models.TicketStatusResolved)
		inProgress := env.createTicket(env.support, models.TicketStatusInProgress)

		_, err := bulk.BulkSetTicketStatus(ctx, env.support.ID, models.RoleSupport,
			[]uint{inProgress.ID, resolved.ID, open.ID}, models.TicketStatusResolved)
		require.NoError(t, err)

		for _, ticket := range []*models.Ticket{open, resolved, inProgress} {
			assert.Equal(t, models.TicketStatusResolved, env.bareTicket(ticket.ID).Status)
		}
		for _, tc := range []struct {
			ticket *models.Ticket
			from   models.TicketStatus
		}{{open, models.TicketStatusOpen}, {inProgress, models.TicketStatusInProgress}} {
			events := env.ticketEvents(tc.ticket.ID)
			require.Equal(t, []string{models.AutomationEventTicketUpdated, models.AutomationEventTicketStatusChanged}, eventTypes(events))
			assert.Equal(t, string(tc.from), events[1].FromValue)
			assert.Equal(t, string(models.TicketStatusResolved), events[1].ToValue)
			for _, event := range events {
				assert.Equal(t, models.AutomationEntityTicket, event.EntityType)
				assert.Equal(t, models.AutomationOriginBulk, event.Origin)
				require.NotNil(t, event.ActorID)
				assert.Equal(t, env.support.ID, *event.ActorID)
			}
		}
		unchanged := env.ticketEvents(resolved.ID)
		require.Equal(t, []string{models.AutomationEventTicketUpdated}, eventTypes(unchanged), "an unchanged status records ticket_updated alone")
		assert.Equal(t, models.AutomationOriginBulk, unchanged[0].Origin)
	})

	t.Run("refused batches record no event for any ticket", func(t *testing.T) {
		mine := env.createTicket(env.support, models.TicketStatusOpen)
		other := env.createTicket(env.support2, models.TicketStatusOpen)
		unassigned := env.createTicket(nil, models.TicketStatusOpen)
		closed := env.createTicket(env.support, models.TicketStatusClosed)
		eventsBefore := env.count(&models.AutomationEvent{})

		_, err := bulk.BulkSetTicketStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{mine.ID, 99999999}, models.TicketStatusResolved)
		require.ErrorIs(t, err, apperrors.ErrNotFound, "missing id")
		_, err = bulk.BulkSetTicketStatus(ctx, env.support.ID, models.RoleSupport, []uint{mine.ID, unassigned.ID}, models.TicketStatusResolved)
		require.ErrorIs(t, err, apperrors.ErrForbidden, "unassigned support ticket")
		_, err = bulk.BulkSetTicketStatus(ctx, env.support.ID, models.RoleSupport, []uint{mine.ID, other.ID}, models.TicketStatusResolved)
		require.ErrorIs(t, err, apperrors.ErrForbidden, "another support user's ticket")
		_, err = bulk.BulkSetTicketStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{mine.ID, closed.ID}, models.TicketStatusOpen)
		require.ErrorIs(t, err, apperrors.ErrClosedTicketReopen, "closed reopen")

		assert.Equal(t, eventsBefore, env.count(&models.AutomationEvent{}), "no refused batch recorded an event")
		for _, ticket := range []*models.Ticket{mine, other, unassigned} {
			assert.Equal(t, models.TicketStatusOpen, env.bareTicket(ticket.ID).Status)
		}
		assert.Equal(t, models.TicketStatusClosed, env.bareTicket(closed.ID).Status)
	})

	t.Run("the events carry the status the batch read, in id order, under a lock", func(t *testing.T) {
		first := env.createTicket(env.support, models.TicketStatusOpen)
		second := env.createTicket(env.support, models.TicketStatusOpen)
		// Another writer moved one ticket on; the batch reads it as it is now.
		require.NoError(t, db.Model(&models.Ticket{}).Where("id = ?", second.ID).Update("status", models.TicketStatusInProgress).Error)

		log := captureStatements(t, db)
		_, err := bulk.BulkSetTicketStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{second.ID, first.ID}, models.TicketStatusResolved)
		require.NoError(t, err)

		events := env.ticketEvents(second.ID)
		require.Len(t, events, 2)
		assert.Equal(t, string(models.TicketStatusInProgress), events[1].FromValue)
		require.NotNil(t, events[0].ActorID)
		assert.Equal(t, env.admin.ID, *events[0].ActorID)

		ordered := regexp.MustCompile("(?is)^SELECT .*FROM [`\"]?tickets[`\"]? WHERE id IN .*ORDER BY id ASC")
		read := log.index(ordered)
		require.GreaterOrEqual(t, read, 0, "the batch reads the tickets in id order:\n%s", log.joined())
		write := log.index(ticketWriteSQL)
		assert.Greater(t, write, read, "the read precedes the status write")
		if isMySQL(db) {
			assert.GreaterOrEqual(t, log.index(regexp.MustCompile("(?is)^SELECT .*FROM `tickets` WHERE id IN .*ORDER BY id ASC.*FOR UPDATE")), 0,
				"the batch read is FOR UPDATE on MySQL:\n%s", log.joined())
		}
		// The admin batch's first event belongs to the lower id.
		all := env.events()
		var firstEvent *models.AutomationEvent
		for i := range all {
			if all[i].EntityID == first.ID || all[i].EntityID == second.ID {
				if all[i].EntityType == models.AutomationEntityTicket && all[i].Origin == models.AutomationOriginBulk {
					firstEvent = &all[i]
					break
				}
			}
		}
		require.NotNil(t, firstEvent)
		assert.Equal(t, first.ID, firstEvent.EntityID, "the events follow the id order of the locking read")
	})
}
