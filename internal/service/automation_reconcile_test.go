package service

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-14: boot-time reconciliation hands a run left in running back to
// pending at its next_position, and fails a run whose last step row is a
// send_email in sending with email_unconfirmed — on both the row and the run
// — so the mail is never sent again. Waiting and finished runs are left
// alone. cmd/automation_runtime_test.go pins that main calls it before the
// worker and the HTTP server start.

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

	// A run interrupted between two ordinary steps.
	interrupted := env.startRun(rule, lead)
	claimed, err := env.repo.ClaimRun(interrupted.ID, models.AutomationRunPending, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed)
	rows, err := env.repo.AdvanceRun(interrupted.ID, 1, repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: interrupted.ID, Position: 1, StepType: models.AutomationStepCreateTask, Outcome: models.AutomationOutcomeSucceeded, ExecutedAt: now}))

	// A run interrupted after the first email transaction committed.
	unconfirmed := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(unconfirmed.ID, models.AutomationRunPending, now)
	require.NoError(t, err)
	_, err = env.repo.AdvanceRun(unconfirmed.ID, 1, repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.NoError(t, err)
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: unconfirmed.ID, Position: 1, StepType: models.AutomationStepCreateTask, Outcome: models.AutomationOutcomeSucceeded, ExecutedAt: now}))
	require.NoError(t, env.repo.CreateRunStep(&models.AutomationRunStep{RunID: unconfirmed.ID, Position: 2, StepType: models.AutomationStepSendEmail, Outcome: models.AutomationOutcomeSending,
		Details: map[string]interface{}{"recipient_id": env.sales.ID}, ExecutedAt: now}))

	// A waiting run and a finished one, which reconciliation must not touch.
	waiting := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(waiting.ID, models.AutomationRunPending, now)
	require.NoError(t, err)
	resumeAt := now.Add(time.Hour)
	_, err = env.repo.AdvanceRun(waiting.ID, 1, repository.AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: 2, ResumeAt: &resumeAt}, now)
	require.NoError(t, err)
	finished := env.startRun(rule, lead)
	_, err = env.repo.ClaimRun(finished.ID, models.AutomationRunPending, now)
	require.NoError(t, err)
	_, err = env.repo.AdvanceRun(finished.ID, 1, repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 4, FinishedAt: &now}, now)
	require.NoError(t, err)

	// The restart.
	env.rebuild()
	reset, failed, err := env.engine.Reconcile()
	require.NoError(t, err)
	assert.Equal(t, 1, reset)
	assert.Equal(t, 1, failed)

	back := env.run(interrupted.ID)
	assert.Equal(t, models.AutomationRunPending, back.Status)
	assert.Equal(t, 2, back.NextPosition, "at its next position")
	assert.Nil(t, back.ClaimedAt)

	dead := env.run(unconfirmed.ID)
	assert.Equal(t, models.AutomationRunFailed, dead.Status)
	assert.Equal(t, models.AutomationErrorEmailUnconfirmed, dead.ErrorCode)
	require.NotNil(t, dead.FinishedAt)
	steps := env.stepsOf(unconfirmed.ID)
	require.Len(t, steps, 2)
	assert.Equal(t, models.AutomationOutcomeFailed, steps[1].Outcome)
	assert.Equal(t, models.AutomationErrorEmailUnconfirmed, steps[1].ErrorCode)
	assert.EqualValues(t, env.sales.ID, steps[1].Details["recipient_id"], "the row keeps its details")

	assert.Equal(t, models.AutomationRunWaiting, env.run(waiting.ID).Status)
	assert.Equal(t, models.AutomationRunCompleted, env.run(finished.ID).Status)

	// The worker resumes the reset run from its next position and never
	// touches the failed one: exactly one mail goes out, for the reset run's
	// own email step, and no task is created a second time.
	tasksBefore := env.count(&models.Task{})
	env.executeDue()
	assert.Len(t, env.mailer.messages(), 1, "the unconfirmed mail is never sent again; the reset run sends its own")
	assert.Equal(t, models.AutomationRunCompleted, env.run(interrupted.ID).Status)
	assert.Equal(t, tasksBefore, env.count(&models.Task{}), "the step before the interruption is not repeated")
	assert.Len(t, env.stepsOf(interrupted.ID), 3)
	assert.Equal(t, models.AutomationRunFailed, env.run(unconfirmed.ID).Status)
	assert.Len(t, env.stepsOf(unconfirmed.ID), 2)

	// Reconciling an idle database is a no-op.
	reset, failed, err = env.engine.Reconcile()
	require.NoError(t, err)
	assert.Zero(t, reset)
	assert.Zero(t, failed)
}
