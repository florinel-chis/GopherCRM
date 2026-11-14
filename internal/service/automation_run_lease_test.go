package service

// Regression roots for issue #237: lease-based run claims, so several
// automation workers can share one database. Each root drives two engines
// (two replicas) with separate pools over one SQLite file or one disposable
// MySQL schema; the shared injected clock is what makes a lease stale.
//
// SQLite note: engine A parked inside a step transaction holds a read
// snapshot (WAL), not a lock, so engine B's writes commit while A is parked;
// A's next write then fails on its stale snapshot and its transaction is
// retried or abandoned, and either way its fenced write matches nothing. On
// MySQL, A's locking read of the lead makes B's step wait for A instead.

import (
	"context"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// --- AC-4: takeover of a stale non-email run --------------------------------

func TestAutomationRunLeaseTakeoverSQLite(t *testing.T) {
	dbA, dbB := openLeasePairSQLite(t)
	runAutomationRunLeaseTakeover(t, dbA, dbB)
}

func TestAutomationRunLeaseTakeoverMySQL(t *testing.T) {
	dbA, dbB := openLeasePairMySQL(t, 4)
	runAutomationRunLeaseTakeover(t, dbA, dbB)
}

func runAutomationRunLeaseTakeover(t *testing.T, dbA, dbB *gorm.DB) {
	p := newLeasePair(t, dbA, dbB)
	for _, tc := range []struct {
		name   string
		step   models.AutomationStep
		effect func() int64
	}{
		{"create_task", taskStep(models.AutomationRecipientOwner, 0, 1), func() int64 { return p.count(&models.Task{}) }},
		{"notify", notifyStep(models.AutomationRecipientOwner, 0), func() int64 { return p.count(&models.Notification{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := p.leadRule(models.AutomationEventLeadCreated, tc.step)
			lead := p.createLead(p.sales, models.LeadStatusNew)
			run := p.startRun(rule, lead)
			effectsBefore := tc.effect()

			// Engine A claims the run and is parked inside its step
			// transaction, after the read of the record. Its heartbeat
			// never fires.
			gate := newLeaseGate(t)
			p.a.afterRecordRead = func(runID uint, _ int) {
				if runID == run.ID {
					gate.park()
				}
			}
			aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
			gate.await(t)
			claimed := p.runRow(run.ID)
			require.Equal(t, models.AutomationRunRunning, claimed.Status)
			require.NotNil(t, claimed.OwnerInstance)
			require.Equal(t, p.a.instanceID, *claimed.OwnerInstance, "A holds the claim")
			p.recA.mark()

			// Past the stale horizon, engine B's tick sweeps the run back to
			// pending, claims it under B's id and executes the step.
			p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
			bDone := goAsync(func() error { return p.b.Tick(context.Background()) })
			if isMySQL(dbA) {
				// B's step waits for the lead row A holds.
				awaitRunOwner(t, dbA, run.ID, p.b.instanceID)
			} else {
				require.NoError(t, waitDone(t, bDone))
			}

			gate.open()
			require.NoError(t, waitDone(t, aDone))
			if isMySQL(dbA) {
				require.NoError(t, waitDone(t, bDone))
			}
			p.a.afterRecordRead = nil

			assert.Contains(t, p.recB.allWrites(), "RecoverStaleRun", "B swept the run")
			assert.Contains(t, p.recB.allWrites(), "ClaimRun", "B claimed it")
			assert.Empty(t, p.recA.writesSinceMark(), "A's late advance matched nothing and A wrote nothing")

			done := p.runRow(run.ID)
			assert.Equal(t, models.AutomationRunCompleted, done.Status)
			assert.Nil(t, done.OwnerInstance)
			assert.Nil(t, done.HeartbeatAt)
			assert.Zero(t, done.Attempts, "the takeover counts no attempt")
			assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 1), "the step row exists exactly once")
			assert.Equal(t, effectsBefore+1, tc.effect(), "the effect exists exactly once")
			steps := p.stepsOf(run.ID)
			require.Len(t, steps, 1)
			assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
		})
	}
}

// --- AC-5: email stays at most once across workers -------------------------

func TestAutomationRunLeaseEmailAtMostOnceSQLite(t *testing.T) {
	dbA, dbB := openLeasePairSQLite(t)
	runAutomationRunLeaseEmailAtMostOnce(t, dbA, dbB)
}

func TestAutomationRunLeaseEmailAtMostOnceMySQL(t *testing.T) {
	dbA, dbB := openLeasePairMySQL(t, 4)
	runAutomationRunLeaseEmailAtMostOnce(t, dbA, dbB)
}

func runAutomationRunLeaseEmailAtMostOnce(t *testing.T, dbA, dbB *gorm.DB) {
	p := newLeasePair(t, dbA, dbB)
	rule := p.leadRule(models.AutomationEventLeadCreated, emailStep(models.AutomationRecipientOwner, 0))
	lead := p.createLead(p.sales, models.LeadStatusNew)
	sends := func() (int, int) { return len(p.mailer.messages()), len(p.mailB.messages()) }
	reset := func() {
		p.mailer.reset(nil, nil)
		p.mailB.reset(nil, nil)
	}

	// neverAgain runs both engines past another stale horizon and checks
	// that nobody sends that position again.
	neverAgain := func(t *testing.T, run *models.AutomationRun, sentA, sentB int) {
		t.Helper()
		p.clock.Advance(automationRunLeaseStaleAfter + time.Minute)
		require.NoError(t, p.a.Tick(context.Background()))
		require.NoError(t, p.b.Tick(context.Background()))
		a, b := sends()
		assert.Equal(t, sentA, a, "A sends that position no more")
		assert.Equal(t, sentB, b, "B sends that position no more")
		assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 1))
	}

	t.Run("(1) a first transaction gone stale is refused and sends nothing", func(t *testing.T) {
		reset()
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.afterRecordRead = func(runID uint, _ int) {
			if runID == run.ID {
				gate.park()
			}
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		p.recA.mark()

		p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
		bDone := goAsync(func() error { return p.b.Tick(context.Background()) })
		if isMySQL(dbA) {
			awaitRunOwner(t, dbA, run.ID, p.b.instanceID)
		} else {
			require.NoError(t, waitDone(t, bDone))
		}
		gate.open()
		require.NoError(t, waitDone(t, aDone))
		if isMySQL(dbA) {
			require.NoError(t, waitDone(t, bDone))
		}
		p.a.afterRecordRead = nil

		a, b := sends()
		assert.Equal(t, 0, a, "A never sends: its first transaction refused the run")
		assert.Equal(t, 1, b, "B ran the step normally")
		assert.NotContains(t, p.recA.writesSinceMark(), "CreateRunStep", "A inserted no step row")
		assert.Empty(t, p.recA.writesSinceMark(), "A wrote nothing")
		steps := p.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status)
		neverAgain(t, run, 0, 1)
	})

	t.Run("(2) parked before the send: the sweep fails it and the pre-send check stops A", func(t *testing.T) {
		reset()
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.beforeSend = func(runID uint, _ int) {
			if runID == run.ID {
				gate.park()
			}
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		require.Equal(t, models.AutomationOutcomeSending, p.stepsOf(run.ID)[0].Outcome, "A committed the sending row")
		p.recA.mark()

		p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
		require.NoError(t, p.b.Tick(context.Background()))
		failed := p.runRow(run.ID)
		assert.Equal(t, models.AutomationRunFailed, failed.Status, "B's sweep failed the run")
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, failed.ErrorCode)

		gate.open()
		require.NoError(t, waitDone(t, aDone))
		p.a.beforeSend = nil

		a, b := sends()
		assert.Equal(t, 0, a, "the pre-send check stopped A")
		assert.Equal(t, 0, b, "B never sends an unconfirmed email")
		assert.Empty(t, p.recA.writesSinceMark(), "A skipped the stamp and wrote nothing")
		steps := p.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeFailed, steps[0].Outcome)
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, steps[0].ErrorCode)
		stored := p.runRow(run.ID)
		assert.Equal(t, models.AutomationRunFailed, stored.Status)
		assert.Nil(t, stored.OwnerInstance)
		neverAgain(t, run, 0, 0)
		assert.Equal(t, models.AutomationOutcomeFailed, p.stepsOf(run.ID)[0].Outcome)
	})

	t.Run("(3) parked after the send: the late stamp and advance match nothing", func(t *testing.T) {
		reset()
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.afterSend = func(runID uint, _ int) error {
			if runID == run.ID {
				gate.park()
			}
			return nil
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		p.recA.mark()

		p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
		require.NoError(t, p.b.Tick(context.Background()))
		assert.Equal(t, models.AutomationRunFailed, p.runRow(run.ID).Status)

		gate.open()
		require.NoError(t, waitDone(t, aDone))
		p.a.afterSend = nil

		a, b := sends()
		assert.Equal(t, 1, a, "A's send went out before the sweep")
		assert.Equal(t, 0, b)
		assert.Empty(t, p.recA.writesSinceMark(), "FinishSendingStep and the fenced AdvanceRun matched nothing")
		steps := p.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeFailed, steps[0].Outcome, "the row stays email_unconfirmed")
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, steps[0].ErrorCode)
		stored := p.runRow(run.ID)
		assert.Equal(t, models.AutomationRunFailed, stored.Status, "the run stays failed")
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, stored.ErrorCode)
		neverAgain(t, run, 1, 0)
	})
}

// --- AC-6: a live run is spared ---------------------------------------------

func TestAutomationRunLeaseLiveRunSparedSQLite(t *testing.T) {
	dbA, dbB := openLeasePairSQLite(t)
	runAutomationRunLeaseLiveRunSpared(t, dbA, dbB)
}

func TestAutomationRunLeaseLiveRunSparedMySQL(t *testing.T) {
	dbA, dbB := openLeasePairMySQL(t, 4)
	runAutomationRunLeaseLiveRunSpared(t, dbA, dbB)
}

func runAutomationRunLeaseLiveRunSpared(t *testing.T, dbA, dbB *gorm.DB) {
	p := newLeasePair(t, dbA, dbB)
	lead := p.createLead(p.sales, models.LeadStatusNew)

	// spared boots a fresh engine B (its sweep), ticks it, and checks that
	// the run A holds is exactly as it was.
	spared := func(t *testing.T, runID uint) {
		t.Helper()
		before := p.runRow(runID)
		p.clock.Advance(automationRunLeaseStaleAfter - 30*time.Second)
		bootB := newLeaseEngine(dbB, newLeaseRecorder(), p.mailB, p.clock)
		recovered, failed, err := bootB.SweepStaleRuns(context.Background())
		require.NoError(t, err)
		assert.Zero(t, recovered)
		assert.Zero(t, failed)
		require.NoError(t, bootB.Tick(context.Background()))
		after := p.runRow(runID)
		assert.Equal(t, before.Status, after.Status)
		require.NotNil(t, after.OwnerInstance)
		assert.Equal(t, p.a.instanceID, *after.OwnerInstance, "the owner is unchanged")
		assert.Equal(t, before.NextPosition, after.NextPosition)
		assert.Equal(t, before.Attempts, after.Attempts)
		require.NotNil(t, after.ClaimedAt)
		assert.True(t, before.ClaimedAt.Equal(*after.ClaimedAt), "claimed_at is unchanged")
		require.NotNil(t, after.HeartbeatAt)
		assert.True(t, before.HeartbeatAt.Equal(*after.HeartbeatAt))
	}

	t.Run("a run between two steps", func(t *testing.T) {
		rule := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.beforeStep = func(runID uint, position int) {
			if runID == run.ID && position == 2 {
				gate.park()
			}
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		spared(t, run.ID)
		gate.open()
		require.NoError(t, waitDone(t, aDone))
		p.a.beforeStep = nil
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status, "A completes the run normally")
		assert.Len(t, p.stepsOf(run.ID), 2)
		assert.EqualValues(t, 2, p.notificationsOfRun(run.ID))
	})

	t.Run("a run whose email row is in sending", func(t *testing.T) {
		p.mailer.reset(nil, nil)
		p.mailB.reset(nil, nil)
		rule := p.leadRule(models.AutomationEventLeadCreated, emailStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.beforeSend = func(runID uint, _ int) {
			if runID == run.ID {
				gate.park()
			}
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		spared(t, run.ID)
		steps := p.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeSending, steps[0].Outcome, "the sending row stays sending")
		gate.open()
		require.NoError(t, waitDone(t, aDone))
		p.a.beforeSend = nil
		assert.Len(t, p.mailer.messages(), 1, "A sends once")
		assert.Empty(t, p.mailB.messages())
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status)
		steps = p.stepsOf(run.ID)
		require.Len(t, steps, 2)
		assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
	})

	t.Run("a run claimed late in a long batch carries a fresh lease", func(t *testing.T) {
		// Two runs in one ExecuteDue batch. The first run's execution takes
		// longer than the stale horizon (the injected clock is advanced
		// inside it); the second run, claimed afterwards, must be stamped
		// with the time of its own claim, not the time the batch started,
		// so another engine's sweep spares it.
		ruleFirst := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0))
		first := p.startRun(ruleFirst, lead)
		ruleSecond := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		second := p.startRun(ruleSecond, lead)
		require.Less(t, first.ID, second.ID)
		batchStart := p.clock.Now()

		gate := newLeaseGate(t)
		p.a.beforeStep = func(runID uint, position int) {
			switch {
			case runID == first.ID && position == 1:
				p.clock.Advance(automationRunLeaseStaleAfter + time.Minute)
			case runID == second.ID && position == 2:
				gate.park()
			}
		}
		aDone := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(first.ID).Status, "the first run of the batch finished")

		claimed := p.runRow(second.ID)
		require.Equal(t, models.AutomationRunRunning, claimed.Status)
		require.NotNil(t, claimed.ClaimedAt)
		require.NotNil(t, claimed.HeartbeatAt)
		claimTime := batchStart.Add(automationRunLeaseStaleAfter + time.Minute)
		assert.True(t, claimed.HeartbeatAt.Equal(*claimed.ClaimedAt), "heartbeat_at equals the claim time")
		assert.WithinDuration(t, claimTime, *claimed.HeartbeatAt, time.Millisecond,
			"the second claim is stamped with the clock at its own claim, not at the batch start")

		spared(t, second.ID)
		gate.open()
		require.NoError(t, waitDone(t, aDone))
		p.a.beforeStep = nil
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(second.ID).Status, "A completes the second run normally")
		assert.EqualValues(t, 2, p.notificationsOfRun(second.ID))
	})
}
