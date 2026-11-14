package service

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-7 (#237): the heartbeat lifecycle of one executed run. One goroutine per
// claimed run renews heartbeat_at every heartbeatInterval (shortened per
// engine instance here) with the engine's clock, and is joined before
// executeRun returns; a renewal refused because the run is no longer owned
// stops the executor before the next step, with no further write; a renewal
// that fails for a database reason is logged and retried; and once the
// runtime's Stop returns nil no renewal is written and no goroutine remains.
func TestAutomationRunLeaseHeartbeatLifecycle(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	newEngine := func(rec *leaseRecorder) *AutomationEngine {
		e := NewAutomationEngine(&leaseRecordingRepo{AutomationRepository: env.repo, rec: rec}, env.notifications, env.tasks, env.users, env.mailer, env.txManager)
		e.SetClock(env.clock.Now)
		e.heartbeatInterval = 20 * time.Millisecond
		return e
	}
	rule := env.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	runRow := func(id uint) models.AutomationRun {
		var run models.AutomationRun
		require.NoError(t, env.db.First(&run, id).Error)
		return run
	}
	parkAtSecondStep := func(t *testing.T, e *AutomationEngine, runID uint) *leaseGate {
		gate := newLeaseGate(t)
		e.beforeStep = func(id uint, position int) {
			if id == runID && position == 2 {
				gate.park()
			}
		}
		return gate
	}

	t.Run("renews at the interval with the engine clock and is joined before executeRun returns", func(t *testing.T) {
		rec := newLeaseRecorder()
		e := newEngine(rec)
		run := env.startRun(rule, lead)
		baseline := runtime.NumGoroutine()
		gate := parkAtSecondStep(t, e, run.ID)
		done := goAsync(func() error { return e.ExecuteDue(context.Background()) })
		gate.await(t)

		env.clock.Advance(time.Minute)
		renewedAt := env.clock.Now().UTC()
		waitUntil(t, "the heartbeat writes the advanced clock", func() bool {
			row := runRow(run.ID)
			return rec.renewCount() >= 3 && row.HeartbeatAt != nil && row.HeartbeatAt.UTC().Equal(renewedAt)
		})
		held := runRow(run.ID)
		require.NotNil(t, held.OwnerInstance)
		assert.Equal(t, e.instanceID, *held.OwnerInstance)
		assert.Equal(t, models.AutomationRunRunning, held.Status)
		for _, err := range rec.renewErrors() {
			assert.NoError(t, err, "every renewal while the run executes succeeds")
		}

		gate.open()
		require.NoError(t, waitDone(t, done))
		finished := runRow(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, finished.Status)
		assert.Nil(t, finished.OwnerInstance)
		assert.Nil(t, finished.HeartbeatAt)
		renewals := rec.renewCount()
		time.Sleep(10 * e.heartbeatInterval)
		assert.Equal(t, renewals, rec.renewCount(), "no renewal after executeRun returned")
		awaitGoroutinesAtMost(t, baseline)
	})

	t.Run("a renewal refused after a cancel stops the executor before the next step", func(t *testing.T) {
		rec := newLeaseRecorder()
		e := newEngine(rec)
		run := env.startRun(rule, lead)
		var secondStepReads atomic.Int32
		e.afterRecordRead = func(id uint, position int) {
			if id == run.ID && position == 2 {
				secondStepReads.Add(1)
			}
		}
		gate := parkAtSecondStep(t, e, run.ID)
		done := goAsync(func() error { return e.ExecuteDue(context.Background()) })
		gate.await(t)

		rows, err := env.repo.CancelRun(run.ID, models.AutomationCancelManual, env.clock.Now())
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		select {
		case <-rec.notOwned:
		case <-time.After(automationTestTimeout):
			t.Fatal("the heartbeat never saw the cancel")
		}
		rec.mark()
		notificationsBefore := env.count(&models.Notification{})

		gate.open()
		require.NoError(t, waitDone(t, done))
		assert.Zero(t, secondStepReads.Load(), "no further step started")
		assert.Empty(t, rec.writesSinceMark(), "nothing more was written for the run")
		assert.Equal(t, notificationsBefore, env.count(&models.Notification{}))
		assert.Len(t, env.stepsOf(run.ID), 1)
		cancelled := runRow(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
		assert.Equal(t, models.AutomationCancelManual, cancelled.CancelReason)
		assert.Nil(t, cancelled.OwnerInstance)
		refused := false
		for _, err := range rec.renewErrors() {
			if errors.Is(err, repository.ErrAutomationRunNotOwned) {
				refused = true
			}
		}
		assert.True(t, refused)
	})

	t.Run("a renewal that fails for a database reason is retried at the next interval", func(t *testing.T) {
		rec := newLeaseRecorder()
		rec.failRenewals(2)
		e := newEngine(rec)
		run := env.startRun(rule, lead)
		gate := parkAtSecondStep(t, e, run.ID)
		done := goAsync(func() error { return e.ExecuteDue(context.Background()) })
		gate.await(t)
		waitUntil(t, "renewals succeed again after the outage", func() bool { return rec.renewCount() >= 4 })
		gate.open()
		require.NoError(t, waitDone(t, done))

		errs := rec.renewErrors()
		require.GreaterOrEqual(t, len(errs), 4)
		assert.ErrorIs(t, errs[0], errInjectedRenewal)
		assert.ErrorIs(t, errs[1], errInjectedRenewal)
		assert.NoError(t, errs[2], "the next interval renews again")
		assert.Equal(t, models.AutomationRunCompleted, runRow(run.ID).Status, "the outage did not stop the run")
		assert.Len(t, env.stepsOf(run.ID), 2)
	})

	t.Run("once Stop returns nil no renewal is written and no goroutine remains", func(t *testing.T) {
		rec := newLeaseRecorder()
		e := newEngine(rec)
		run := env.startRun(rule, lead)
		baseline := runtime.NumGoroutine()
		gate := parkAtSecondStep(t, e, run.ID)
		worker := NewAutomationRuntime(e, time.Hour)
		worker.Start(context.Background())
		gate.await(t)
		waitUntil(t, "the heartbeat renews while the worker runs", func() bool { return rec.renewCount() >= 2 })

		stopped := goAsync(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
			defer cancel()
			return worker.Stop(ctx)
		})
		select {
		case err := <-stopped:
			t.Fatalf("Stop returned while the executor was still running: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		gate.open()
		require.NoError(t, waitDone(t, stopped))
		renewals := rec.renewCount()
		time.Sleep(10 * e.heartbeatInterval)
		assert.Equal(t, renewals, rec.renewCount(), "no renewal after Stop returned")
		awaitGoroutinesAtMost(t, baseline)

		released := runRow(run.ID)
		assert.Equal(t, models.AutomationRunPending, released.Status, "the stopping executor handed the run back")
		assert.Equal(t, 2, released.NextPosition)
		assert.Nil(t, released.OwnerInstance)
		assert.Nil(t, released.HeartbeatAt)
	})
}
