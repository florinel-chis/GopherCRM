package service

// AC-5 of #242, on a disposable MySQL schema: a ticket update, a bulk ticket
// status batch and a ticket delete each race an automation step on the same
// ticket, in both orderings. Every writer locks the ticket row first, as the
// step does, so the second party waits on that row (seen in
// information_schema.PROCESSLIST on two consecutive polls) and both complete
// without error 1213. The update's and the batch's events are committed
// exactly once; a delete that wins leaves the run cancelled with
// record_deleted.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ticketWriter is one of the three ticket writers raced against a step.
type ticketWriter struct {
	name string
	// gateTable names the table of the update the writer is parked before
	// in the writer-first ordering, when it already holds the ticket lock.
	gateTable string
	run       func(env *ticketAutomationEnv, ticketID uint) error
	// verify checks the committed outcome; stepFirst reports the ordering.
	verify func(t *testing.T, env *ticketAutomationEnv, ticket *models.Ticket, run models.AutomationRun, stepFirst bool)
}

func TestAutomationTicketLockOrderMySQL(t *testing.T) {
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)

	writers := []ticketWriter{
		{
			name:      "update",
			gateTable: "tickets",
			run: func(env *ticketAutomationEnv, ticketID uint) error {
				var current models.Ticket
				if err := env.db.First(&current, ticketID).Error; err != nil {
					return err
				}
				current.Status = models.TicketStatusResolved
				return env.ticketSvc.Update(&current)
			},
			verify: func(t *testing.T, env *ticketAutomationEnv, ticket *models.Ticket, run models.AutomationRun, _ bool) {
				assert.Equal(t, models.TicketStatusResolved, env.bareTicket(ticket.ID).Status)
				assertWriterEventsOnce(t, env, ticket.ID, models.AutomationOriginUser)
				assert.Equal(t, models.AutomationRunCompleted, run.Status)
			},
		},
		{
			name:      "bulk",
			gateTable: "tickets",
			run: func(env *ticketAutomationEnv, ticketID uint) error {
				bulk := NewBulkStatusService(repository.NewBulkOperationRepository(env.db), repository.NewBulkStatusRepository(env.db),
					env.txManager, quiet, WithBulkAutomationEvents(env.repo))
				_, err := bulk.BulkSetTicketStatus(context.Background(), env.admin.ID, models.RoleAdmin, []uint{ticketID}, models.TicketStatusResolved)
				return err
			},
			verify: func(t *testing.T, env *ticketAutomationEnv, ticket *models.Ticket, run models.AutomationRun, _ bool) {
				assert.Equal(t, models.TicketStatusResolved, env.bareTicket(ticket.ID).Status)
				assertWriterEventsOnce(t, env, ticket.ID, models.AutomationOriginBulk)
				assert.Equal(t, models.AutomationRunCompleted, run.Status)
			},
		},
		{
			// Parked before it cancels the runs: the ticket is locked, no run
			// row yet, so the executor's claim passes and its step waits.
			name:      "delete",
			gateTable: "automation_runs",
			run: func(env *ticketAutomationEnv, ticketID uint) error {
				return repository.NewTicketRepository(env.db).Delete(ticketID)
			},
			verify: func(t *testing.T, env *ticketAutomationEnv, ticket *models.Ticket, run models.AutomationRun, stepFirst bool) {
				assert.True(t, env.bareTicket(ticket.ID).DeletedAt.Valid)
				assert.Empty(t, env.notificationsList(), "the ticket's notifications are purged")
				if stepFirst {
					assert.Equal(t, models.AutomationRunCompleted, run.Status, "the step committed before the delete")
					return
				}
				assert.Equal(t, models.AutomationRunCancelled, run.Status, "the delete won")
				assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
				assert.Empty(t, env.stepsOf(run.ID), "the step applied nothing")
			},
		},
	}

	for _, writer := range writers {
		writer := writer
		t.Run(writer.name+" waits for a step", func(t *testing.T) {
			raceTicketWriterAgainstStep(t, writer, true)
		})
		t.Run("a step waits for "+writer.name, func(t *testing.T) {
			raceTicketWriterAgainstStep(t, writer, false)
		})
	}
}

// assertWriterEventsOnce checks the writer's ticket_updated and
// ticket_status_changed were committed exactly once.
func assertWriterEventsOnce(t *testing.T, env *ticketAutomationEnv, ticketID uint, origin string) {
	t.Helper()
	var updated, changed int
	for _, event := range env.ticketEvents(ticketID) {
		if event.Origin != origin {
			continue
		}
		switch event.EventType {
		case models.AutomationEventTicketUpdated:
			updated++
		case models.AutomationEventTicketStatusChanged:
			changed++
			assert.Equal(t, string(models.TicketStatusOpen), event.FromValue)
			assert.Equal(t, string(models.TicketStatusResolved), event.ToValue)
		}
	}
	assert.Equal(t, 1, updated, "ticket_updated committed once")
	assert.Equal(t, 1, changed, "ticket_status_changed committed once")
}

func raceTicketWriterAgainstStep(t *testing.T, writer ticketWriter, stepFirst bool) {
	db := openAutomationMySQL(t, 6)
	env := newTicketAutomationEnv(t, db)
	rule := env.ticketRule(models.AutomationEventTicketCreated, models.AutomationTrigger{},
		ticketNotifyStep(models.AutomationRecipientOwner, 0))
	ticket := env.createTicket(env.support, models.TicketStatusOpen)
	env.recordEvent(models.AutomationEntityTicket, ticket.ID, models.AutomationEventTicketCreated, "", string(models.TicketStatusOpen), models.AutomationOriginUser)
	require.Equal(t, 1, env.dispatch())

	writerDone := make(chan error, 1)
	stepDone := make(chan error, 1)
	if stepFirst {
		// The step holds the ticket lock; the writer queues behind it.
		var once sync.Once
		env.engine.afterRecordRead = func(uint, int) {
			once.Do(func() {
				go func() { writerDone <- writer.run(env, ticket.ID) }()
				awaitTicketLockWait(t, db)
			})
		}
		// Run on the test goroutine: the seam above probes with t.
		stepDone <- env.engine.ExecuteDue(context.Background())
	} else {
		// The writer holds the ticket lock, parked before its gate statement;
		// the step queues behind it.
		release := make(chan struct{})
		reached := make(chan struct{})
		var armed atomic.Bool
		armed.Store(true)
		gate := func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != writer.gateTable {
				return
			}
			if !armed.CompareAndSwap(true, false) {
				return
			}
			close(reached)
			<-release
		}
		name := "ticket_lock_order_gate_" + writer.name
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, gate))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
		go func() { writerDone <- writer.run(env, ticket.ID) }()
		select {
		case <-reached:
		case <-time.After(automationTestTimeout):
			t.Fatal("the writer never reached its gate")
		}
		go func() { stepDone <- env.engine.ExecuteDue(context.Background()) }()
		awaitTicketLockWait(t, db)
		close(release)
	}

	for name, done := range map[string]chan error{"writer": writerDone, "step": stepDone} {
		select {
		case err := <-done:
			assertNotDeadlock(t, err)
			require.NoError(t, err, name)
		case <-time.After(automationTestTimeout):
			t.Fatalf("the %s never finished", name)
		}
	}
	runs := env.runsOf(rule.ID)
	require.Len(t, runs, 1)
	writer.verify(t, env, ticket, runs[0], stepFirst)
}
