package service

import (
	"context"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-14, as amended by #237: the stale sweep (which replaced the boot-time
// Reconcile) hands a running run whose lease went stale back to pending at its
// next_position, and fails a stale run whose last step row is a send_email in
// sending with email_unconfirmed — on both the row and the run — so the mail
// is never sent again. A run with a fresh lease, waiting runs and finished
// runs are left alone. cmd/automation_runtime_test.go pins that main sweeps
// before the worker and the HTTP server start.

func TestAutomationReconcileSQLite(t *testing.T) {
	runAutomationReconcile(t, openAutomationSQLite(t))
}

func TestAutomationReconcileMySQL(t *testing.T) {
	runAutomationReconcile(t, openAutomationMySQL(t, 2))
}

func runAutomationReconcile(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	rule := env.leadRule(models.AutomationEventLeadCreated,
		taskStep(models.AutomationRecipientOwner, 0, 1), emailStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	now := env.clock.Now()
	dead := env.engine.instanceID

	// A run interrupted between two ordinary steps.
	interrupted := env.startRun(rule, lead)
	claimed, err := env.repo.ClaimRun(interrupted.ID, models.AutomationRunPending, dead, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed)
	rows, err := env.repo.AdvanceRun(interrupted.ID, 1, dead, repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: interrupted.ID, Position: 1, StepType: models.AutomationStepCreateTask, Outcome: models.AutomationOutcomeSucceeded, ExecutedAt: now}))

	// A run interrupted after the first email transaction committed.
	unconfirmed := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(unconfirmed.ID, models.AutomationRunPending, dead, now)
	require.NoError(t, err)
	_, err = env.repo.AdvanceRun(unconfirmed.ID, 1, dead, repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.NoError(t, err)
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: unconfirmed.ID, Position: 1, StepType: models.AutomationStepCreateTask, Outcome: models.AutomationOutcomeSucceeded, ExecutedAt: now}))
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: unconfirmed.ID, Position: 2, StepType: models.AutomationStepSendEmail, Outcome: models.AutomationOutcomeSending,
		Details: map[string]interface{}{"recipient_id": env.sales.ID}, ExecutedAt: now}))

	// A legacy running row, claimed before leases existed: no owner, no
	// heartbeat.
	legacy := env.startRun(rule, lead)
	require.NoError(t, db.Model(&models.AutomationRun{}).Where("id = ?", legacy.ID).
		Updates(map[string]interface{}{"status": models.AutomationRunRunning, "claimed_at": now}).Error)

	// A waiting run and a finished one, which the sweep must not touch.
	waiting := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(waiting.ID, models.AutomationRunPending, dead, now)
	require.NoError(t, err)
	resumeAt := now.Add(time.Hour)
	_, err = env.repo.AdvanceRun(waiting.ID, 1, dead, repository.AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: 2, ResumeAt: &resumeAt}, now)
	require.NoError(t, err)
	finished := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(finished.ID, models.AutomationRunPending, dead, now)
	require.NoError(t, err)
	_, err = env.repo.AdvanceRun(finished.ID, 1, dead, repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 4, FinishedAt: &now}, now)
	require.NoError(t, err)

	// The restart, past the stale horizon. Meanwhile another live engine
	// claimed a run whose lease is fresh: it is spared.
	env.clock.Advance(automationRunLeaseStaleAfter + time.Second)
	live := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(live.ID, models.AutomationRunPending, "live-engine-instance", env.clock.Now())
	require.NoError(t, err)
	liveBefore := *env.run(live.ID)

	env.rebuild()
	reset, failed, err := env.engine.SweepStaleRuns(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, reset, "the interrupted and the legacy run")
	assert.Equal(t, 1, failed)

	for _, id := range []uint{interrupted.ID, legacy.ID} {
		back := env.run(id)
		assert.Equal(t, models.AutomationRunPending, back.Status, "run %d", id)
		assert.Nil(t, back.ClaimedAt, "run %d", id)
		assert.Nil(t, back.OwnerInstance, "run %d", id)
		assert.Nil(t, back.HeartbeatAt, "run %d", id)
		assert.Zero(t, back.Attempts, "run %d: the attempts are unchanged", id)
	}
	assert.Equal(t, 2, env.run(interrupted.ID).NextPosition, "at its next position")

	deadRun := env.run(unconfirmed.ID)
	assert.Equal(t, models.AutomationRunFailed, deadRun.Status)
	assert.Equal(t, models.AutomationErrorEmailUnconfirmed, deadRun.ErrorCode)
	require.NotNil(t, deadRun.FinishedAt)
	assert.Nil(t, deadRun.OwnerInstance)
	steps := env.stepsOf(unconfirmed.ID)
	require.Len(t, steps, 2)
	assert.Equal(t, models.AutomationOutcomeFailed, steps[1].Outcome)
	assert.Equal(t, models.AutomationErrorEmailUnconfirmed, steps[1].ErrorCode)
	assert.EqualValues(t, env.sales.ID, steps[1].Details["recipient_id"], "the row keeps its details")

	assert.Equal(t, models.AutomationRunWaiting, env.run(waiting.ID).Status)
	assert.Equal(t, models.AutomationRunCompleted, env.run(finished.ID).Status)
	liveAfter := env.run(live.ID)
	assert.Equal(t, models.AutomationRunRunning, liveAfter.Status, "a fresh lease is spared")
	require.NotNil(t, liveAfter.OwnerInstance)
	assert.Equal(t, "live-engine-instance", *liveAfter.OwnerInstance)
	assert.Equal(t, liveBefore.ClaimedAt.UTC(), liveAfter.ClaimedAt.UTC())

	// The worker resumes the recovered runs from their next position and never
	// touches the failed one: the interrupted run sends its own email, the
	// legacy run starts from position 1 (a task and an email), and the
	// unconfirmed mail is never sent again.
	tasksBefore := env.count(&models.Task{})
	env.executeDue()
	assert.Len(t, env.mailer.messages(), 2, "the unconfirmed mail is never sent again; the recovered runs send their own")
	assert.Equal(t, models.AutomationRunCompleted, env.run(interrupted.ID).Status)
	assert.Equal(t, models.AutomationRunCompleted, env.run(legacy.ID).Status)
	assert.Equal(t, tasksBefore+1, env.count(&models.Task{}), "the step before the interruption is not repeated")
	assert.Len(t, env.stepsOf(interrupted.ID), 3)
	assert.Equal(t, models.AutomationRunFailed, env.run(unconfirmed.ID).Status)
	assert.Len(t, env.stepsOf(unconfirmed.ID), 2)
	assert.Equal(t, models.AutomationRunRunning, env.run(live.ID).Status, "the live run is still its owner's")

	// Sweeping again recovers nothing.
	reset, failed, err = env.engine.SweepStaleRuns(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reset)
	assert.Zero(t, failed)
}
