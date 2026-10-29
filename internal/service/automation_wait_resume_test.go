package service

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-8: a wait step leaves the run waiting with resume_at = now + duration
// (UTC) and next_position after the wait; nothing resumes it before
// resume_at; after it, a new service and executor on the same database (a
// restart, with an injected clock) resume it through a conditional claim; a
// condition after the wait sees changes made during the wait; a run whose
// record was deleted or erased, or whose rule changed, is cancelled with
// record_deleted or rule_changed and runs no further step. On MySQL a cancel
// committed from a second connection after the resuming step's first read
// leaves no effect or run-step row.

func TestAutomationWaitResumeSQLite(t *testing.T) {
	runAutomationWaitResume(t, openAutomationSQLite(t))
}

func TestAutomationWaitResumeMySQL(t *testing.T) {
	runAutomationWaitResume(t, openAutomationMySQL(t, 4))
}

func runAutomationWaitResume(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	rule := env.leadRule(models.AutomationEventLeadCreated,
		waitStep(60),
		conditionStep("status", "eq", "contacted", models.AutomationElseStop, 0),
		notifyStep(models.AutomationRecipientOwner, 0))

	t.Run("the wait persists and survives a restart", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		started := env.clock.Now()
		env.executeDue()

		waiting := env.run(run.ID)
		assert.Equal(t, models.AutomationRunWaiting, waiting.Status)
		assert.Equal(t, 2, waiting.NextPosition)
		require.NotNil(t, waiting.ResumeAt)
		assert.WithinDuration(t, started.UTC().Add(60*time.Minute), *waiting.ResumeAt, time.Second)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeWaiting, steps[0].Outcome)

		// Before resume_at nothing claims it, however often the worker ticks.
		env.clock.Advance(59 * time.Minute)
		env.executeDue()
		env.executeDue()
		assert.Equal(t, models.AutomationRunWaiting, env.run(run.ID).Status)
		assert.Len(t, env.stepsOf(run.ID), 1)

		// The record changes during the wait and the process restarts.
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("status", models.LeadStatusContacted).Error)
		env.clock.Advance(2 * time.Minute)
		env.rebuild()
		notificationsBefore := env.count(&models.Notification{})
		env.executeDue()

		resumed := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, resumed.Status)
		assert.Equal(t, 4, resumed.NextPosition)
		steps = env.stepsOf(run.ID)
		require.Len(t, steps, 3)
		assert.Equal(t, true, steps[1].Details["result"], "the condition saw the status set during the wait")
		assert.Equal(t, notificationsBefore+1, env.count(&models.Notification{}))
	})

	t.Run("a deleted record cancels the resumed run with record_deleted", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusContacted)
		run := env.startRun(rule, lead)
		env.executeDue()
		require.Equal(t, models.AutomationRunWaiting, env.run(run.ID).Status)
		// A plain soft delete, as a deal's delete would do: no erasure hook
		// runs, so it is the resumed step that finds the record gone.
		require.NoError(t, env.db.Delete(&models.Lead{}, lead.ID).Error)
		env.clock.Advance(61 * time.Minute)
		notificationsBefore := env.count(&models.Notification{})
		env.executeDue()
		cancelled := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, cancelled.CancelReason)
		assert.Len(t, env.stepsOf(run.ID), 1, "no further step")
		assert.Equal(t, notificationsBefore, env.count(&models.Notification{}))
	})

	t.Run("an erased record cancels the waiting run inside the erasure", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusContacted)
		run := env.startRun(rule, lead)
		env.executeDue()
		require.NoError(t, env.leads.Delete(lead.ID))
		cancelled := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, cancelled.CancelReason)
		env.clock.Advance(61 * time.Minute)
		env.executeDue()
		assert.Len(t, env.stepsOf(run.ID), 1, "a cancelled run is never claimed again")
	})

	t.Run("a rule changed during the wait cancels the run with rule_changed", func(t *testing.T) {
		changing := env.leadRule(models.AutomationEventLeadCreated, waitStep(10), notifyStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusContacted)
		run := env.startRun(changing, lead)
		env.executeDue()
		require.Equal(t, models.AutomationRunWaiting, env.run(run.ID).Status)
		enabled := true
		_, err := env.svc.UpdateRule(changing.ID, &models.AutomationRule{Name: "Changed", EntityType: changing.EntityType, TriggerType: changing.TriggerType,
			Enabled: enabled, Steps: []models.AutomationStep{waitStep(10), taskStep(models.AutomationRecipientOwner, 0, 1)}}, &enabled)
		require.NoError(t, err)
		env.clock.Advance(11 * time.Minute)
		tasksBefore := env.count(&models.Task{})
		env.executeDue()
		cancelled := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
		assert.Equal(t, models.AutomationCancelRuleChanged, cancelled.CancelReason)
		assert.Len(t, env.stepsOf(run.ID), 1)
		assert.Equal(t, tasksBefore, env.count(&models.Task{}))
	})

	if !isMySQL(db) {
		return
	}

	t.Run("a cancel committed after the resuming step's first read leaves nothing", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusContacted)
		run := env.startRun(rule, lead)
		env.executeDue()
		require.Equal(t, models.AutomationRunWaiting, env.run(run.ID).Status)
		env.clock.Advance(61 * time.Minute)
		cancelledFromAnotherConnection := false
		env.engine.afterRecordRead = func(runID uint, position int) {
			if runID == run.ID && position == 2 && !cancelledFromAnotherConnection {
				cancelledFromAnotherConnection = true
				_, err := env.svc.CancelRun(run.ID)
				require.NoError(t, err)
			}
		}
		notificationsBefore := env.count(&models.Notification{})
		env.executeDue()
		env.engine.afterRecordRead = nil
		require.True(t, cancelledFromAnotherConnection)
		cancelled := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
		assert.Equal(t, models.AutomationCancelManual, cancelled.CancelReason)
		assert.Len(t, env.stepsOf(run.ID), 1, "the condition left no row")
		assert.Equal(t, notificationsBefore, env.count(&models.Notification{}))
	})
}
