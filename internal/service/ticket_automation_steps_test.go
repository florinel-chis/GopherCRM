package service

// AC-4 of #242: an enabled ticket_status_changed rule is dispatched from a
// committed ticket event and executed. Every non-email step locks the ticket;
// the owner is the assignee; notify writes a ticket notification; create_task
// links the ticket's customer only; assign_owner writes assigned_to_id through
// SetTicketAssignee and records ticket_updated with origin automation, which
// starts no run. Unavailable recipients are skipped; a ticket deleted before a
// step cancels the run with record_deleted, also a run the dispatcher creates
// after the delete from an event recorded before it.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationTicketStepExecutionSQLite(t *testing.T) {
	runAutomationTicketStepExecution(t, func(t *testing.T) *gorm.DB { return openAutomationSQLite(t) })
}

func TestAutomationTicketStepExecutionMySQL(t *testing.T) {
	runAutomationTicketStepExecution(t, func(t *testing.T) *gorm.DB { return openAutomationMySQL(t, 4) })
	t.Run("delete committed while a step holds the ticket", func(t *testing.T) {
		runTicketDeleteDuringStepMySQL(t, openAutomationMySQL(t, 4))
	})
}

// resolveThroughService moves the ticket to resolved through the wired ticket
// service, which records ticket_updated and ticket_status_changed.
func (e *ticketAutomationEnv) resolveThroughService(ticketID uint) {
	e.t.Helper()
	current, err := e.tickets.GetByID(ticketID)
	require.NoError(e.t, err)
	current.Status = models.TicketStatusResolved
	require.NoError(e.t, e.ticketSvc.Update(current))
}

func runAutomationTicketStepExecution(t *testing.T, open func(t *testing.T) *gorm.DB) {
	resolvedTrigger := models.AutomationTrigger{To: string(models.TicketStatusResolved)}

	t.Run("notify, task and assign on a resolved ticket", func(t *testing.T) {
		env := newTicketAutomationEnv(t, open(t))
		rule := env.ticketRule(models.AutomationEventTicketStatusChanged, resolvedTrigger,
			ticketNotifyStep(models.AutomationRecipientOwner, 0),
			ticketTaskStep(models.AutomationRecipientOwner, 0, 2),
			assignStep(env.support2.ID))
		ticket := env.createTicket(env.support, models.TicketStatusInProgress)

		env.resolveThroughService(ticket.ID)
		require.Equal(t, 1, env.dispatch(), "the status change starts one run")
		env.executeDue()

		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 1)
		run := runs[0]
		assert.Equal(t, models.AutomationEntityTicket, run.EntityType)
		assert.Equal(t, ticket.ID, run.EntityID)
		assert.Equal(t, models.AutomationRunCompleted, run.Status)

		notifications := env.notificationsList()
		require.Len(t, notifications, 1)
		n := notifications[0]
		assert.Equal(t, env.support.ID, n.UserID, "the owner is the assignee")
		assert.Equal(t, models.AutomationEntityTicket, n.EntityType)
		assert.Equal(t, ticket.ID, n.EntityID)
		assert.Contains(t, n.Title, models.TicketNumber(ticket.ID))
		assert.Contains(t, n.Title, "resolved")
		for _, canary := range []string{ticketCanaryTitle, ticketCanaryDescription, ticketCanaryResolution, ticketCanaryCustomer} {
			assert.NotContains(t, n.Title+n.Body, canary)
		}

		tasks := env.tasksList()
		require.Len(t, tasks, 1)
		task := tasks[0]
		assert.Equal(t, env.support.ID, task.AssignedToID)
		require.NotNil(t, task.CustomerID)
		assert.Equal(t, ticket.CustomerID, *task.CustomerID, "the task is linked to the ticket's customer")
		assert.Nil(t, task.LeadID)
		assert.Nil(t, task.DealID)
		assert.Equal(t, "Check ticket "+models.TicketNumber(ticket.ID), task.Title)

		stored := env.bareTicket(ticket.ID)
		require.NotNil(t, stored.AssignedToID)
		assert.Equal(t, env.support2.ID, *stored.AssignedToID, "assign_owner wrote assigned_to_id")
		assert.Equal(t, models.TicketStatusResolved, stored.Status)
		assert.Equal(t, ticketCanaryTitle, stored.Title, "nothing else of the ticket was written")

		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 3)
		assert.EqualValues(t, ticket.CustomerID, steps[1].Details["customer_id"])
		assert.EqualValues(t, env.support.ID, steps[2].Details["from_owner_id"])
		assert.EqualValues(t, env.support2.ID, steps[2].Details["to_owner_id"])

		// The assign's event carries origin automation; a ticket_updated rule
		// enabled now still starts no run from it: the loop guard.
		events := env.ticketEvents(ticket.ID)
		last := events[len(events)-1]
		assert.Equal(t, models.AutomationEventTicketUpdated, last.EventType)
		assert.Equal(t, models.AutomationOriginAutomation, last.Origin)
		assert.Nil(t, last.ActorID)
		loop := env.ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, ticketNotifyStep(models.AutomationRecipientOwner, 0))
		assert.Zero(t, env.dispatch(), "the automation-origin event starts nothing")
		assert.Empty(t, env.runsOf(loop.ID))
		var processed models.AutomationEvent
		require.NoError(t, env.db.First(&processed, last.ID).Error)
		assert.NotNil(t, processed.ProcessedAt, "the event was claimed")
	})

	t.Run("from and to narrow the status change", func(t *testing.T) {
		env := newTicketAutomationEnv(t, open(t))
		rule := env.ticketRule(models.AutomationEventTicketStatusChanged,
			models.AutomationTrigger{From: string(models.TicketStatusOpen), To: string(models.TicketStatusResolved)},
			ticketNotifyStep(models.AutomationRecipientOwner, 0))
		fromOpen := env.createTicket(env.support, models.TicketStatusOpen)
		fromProgress := env.createTicket(env.support, models.TicketStatusInProgress)
		env.resolveThroughService(fromOpen.ID)
		env.resolveThroughService(fromProgress.ID)
		require.Equal(t, 1, env.dispatch())
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 1)
		assert.Equal(t, fromOpen.ID, runs[0].EntityID)
	})

	t.Run("unassigned ticket and unavailable assign target are skipped", func(t *testing.T) {
		env := newTicketAutomationEnv(t, open(t))
		leaving := env.createUser("leaving-support", models.RoleSupport, true)
		moving := env.createUser("moving-support", models.RoleSupport, true)
		rule := env.ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{},
			ticketNotifyStep(models.AutomationRecipientOwner, 0),
			assignStep(leaving.ID),
			assignStep(moving.ID))
		// The targets change after the rule was validated.
		require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", leaving.ID).Update("is_active", false).Error)
		require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", moving.ID).Update("role", models.RoleSales).Error)
		ticket := env.createTicket(nil, models.TicketStatusOpen)
		env.recordEvent(models.AutomationEntityTicket, ticket.ID, models.AutomationEventTicketUpdated, "", "", models.AutomationOriginUser)
		require.Equal(t, 1, env.dispatch())
		env.executeDue()

		run := env.runsOf(rule.ID)[0]
		assert.Equal(t, models.AutomationRunCompleted, run.Status)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 3)
		for _, step := range steps {
			assert.Equal(t, models.AutomationOutcomeSkipped, step.Outcome, "position %d", step.Position)
			assert.Equal(t, models.AutomationErrorRecipientUnavailable, step.ErrorCode)
		}
		assert.Nil(t, env.bareTicket(ticket.ID).AssignedToID, "no assignee was written")
		assert.Empty(t, env.notificationsList())

		// An erased target is unavailable too.
		erased := env.createUser("erased-support", models.RoleSupport, true)
		second := env.ticketRule(models.AutomationEventTicketCreated, models.AutomationTrigger{}, assignStep(erased.ID))
		require.NoError(t, env.db.Delete(&models.User{}, erased.ID).Error)
		other := env.createTicket(env.support, models.TicketStatusOpen)
		env.recordEvent(models.AutomationEntityTicket, other.ID, models.AutomationEventTicketCreated, "", string(models.TicketStatusOpen), models.AutomationOriginUser)
		require.Equal(t, 1, env.dispatch())
		env.executeDue()
		secondRun := env.runsOf(second.ID)[0]
		assert.Equal(t, models.AutomationOutcomeSkipped, env.stepsOf(secondRun.ID)[0].Outcome)
		assert.Equal(t, env.support.ID, *env.bareTicket(other.ID).AssignedToID)
	})

	t.Run("a ticket deleted before a step cancels the run", func(t *testing.T) {
		env := newTicketAutomationEnv(t, open(t))
		rule := env.ticketRule(models.AutomationEventTicketStatusChanged, resolvedTrigger,
			ticketNotifyStep(models.AutomationRecipientOwner, 0), ticketTaskStep(models.AutomationRecipientOwner, 0, 1))

		// Soft-deleted underneath a pending run (not through the repository).
		gone := env.createTicket(env.support, models.TicketStatusOpen)
		env.resolveThroughService(gone.ID)
		require.Equal(t, 1, env.dispatch())
		require.NoError(t, env.db.Delete(&models.Ticket{}, gone.ID).Error)
		env.executeDue()
		run := env.runsOf(rule.ID)[0]
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
		assert.NotNil(t, run.FinishedAt)

		// Deleted through the repository after the event was recorded but
		// before it was dispatched: the run starts after the delete committed
		// and is cancelled at its first step with no effect.
		late := env.createTicket(env.support, models.TicketStatusOpen)
		env.resolveThroughService(late.ID)
		require.NoError(t, env.tickets.Delete(late.ID))
		require.Equal(t, 1, env.dispatch(), "the event recorded before the delete still starts a run")
		env.executeDue()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 2)
		lateRun := runs[1]
		assert.Equal(t, late.ID, lateRun.EntityID)
		assert.Equal(t, models.AutomationRunCancelled, lateRun.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, lateRun.CancelReason)
		assert.Equal(t, 1, lateRun.NextPosition, "cancelled at its first step")
		assert.Empty(t, env.stepsOf(lateRun.ID))
		assert.Empty(t, env.notificationsList(), "no effect was applied")
		assert.Empty(t, env.tasksList())
	})

	t.Run("a ticket deleted while a run waits cancels it eagerly", func(t *testing.T) {
		env := newTicketAutomationEnv(t, open(t))
		rule := env.ticketRule(models.AutomationEventTicketStatusChanged, resolvedTrigger,
			ticketNotifyStep(models.AutomationRecipientOwner, 0), waitStep(60), ticketNotifyStep(models.AutomationRecipientOwner, 0))
		ticket := env.createTicket(env.support, models.TicketStatusOpen)
		env.resolveThroughService(ticket.ID)
		require.Equal(t, 1, env.dispatch())
		env.executeDue()
		run := env.runsOf(rule.ID)[0]
		require.Equal(t, models.AutomationRunWaiting, run.Status)
		require.Len(t, env.notificationsList(), 1)

		require.NoError(t, env.ticketSvc.Delete(ticket.ID))
		run = env.runsOf(rule.ID)[0]
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
		assert.Empty(t, env.notificationsList(), "the ticket's notifications are purged")
		env.clock.Advance(2 * time.Hour)
		env.executeDue()
		assert.Equal(t, models.AutomationRunCancelled, env.runsOf(rule.ID)[0].Status)
	})
}

// runTicketDeleteDuringStepMySQL pauses a step after its locking read of the
// ticket and starts a delete from a second connection: the delete is seen
// blocked on the ticket row (PROCESSLIST, two polls) until the step commits,
// then commits itself and purges the notification the step wrote. Neither
// side deadlocks. (The delete-first ordering, which cancels the run with
// record_deleted, is TestAutomationTicketLockOrderMySQL.)
func runTicketDeleteDuringStepMySQL(t *testing.T, db *gorm.DB) {
	env := newTicketAutomationEnv(t, db)
	rule := env.ticketRule(models.AutomationEventTicketStatusChanged, models.AutomationTrigger{},
		ticketNotifyStep(models.AutomationRecipientOwner, 0))
	ticket := env.createTicket(env.support, models.TicketStatusOpen)
	env.resolveThroughService(ticket.ID)
	require.Equal(t, 1, env.dispatch())

	var once sync.Once
	deleteDone := make(chan error, 1)
	env.engine.afterRecordRead = func(runID uint, position int) {
		once.Do(func() {
			go func() { deleteDone <- repository.NewTicketRepository(db).Delete(ticket.ID) }()
			awaitTicketLockWait(t, db)
		})
	}
	env.executeDue()
	select {
	case err := <-deleteDone:
		require.NoError(t, err)
		assertNotDeadlock(t, err)
	case <-time.After(automationTestTimeout):
		t.Fatal("the delete never finished")
	}
	run := env.runsOf(rule.ID)[0]
	assert.Equal(t, models.AutomationRunCompleted, run.Status, "the step that held the lock committed first")
	assert.Len(t, env.stepsOf(run.ID), 1)
	assert.Empty(t, env.notificationsList(), "the delete, committed after it, purged the ticket's notifications")
	assert.True(t, env.bareTicket(ticket.ID).DeletedAt.Valid, "the ticket is soft-deleted")
}

// awaitTicketLockWait returns once a locking read of a ticket is seen
// executing on the same connection on two consecutive PROCESSLIST polls.
func awaitTicketLockWait(t *testing.T, db *gorm.DB) {
	t.Helper()
	probe := &lockWaitProbe{target: ticketLockingRead}
	ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
	defer cancel()
	for ctx.Err() == nil {
		var rows []executingStatement
		err := db.WithContext(ctx).Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO IS NOT NULL`).Scan(&rows).Error
		if err != nil {
			rows = nil
		}
		if probe.observe(rows) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no locking read of a ticket waited on a lock")
}
