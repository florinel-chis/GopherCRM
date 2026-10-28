package service

import (
	"context"
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-2: every rule update, in one transaction, increments the version,
// replaces the steps and cancels the rule's pending, waiting and running runs
// with rule_changed; disable cancels with rule_disabled; delete soft-deletes
// and cancels with rule_deleted. A running run cancelled this way performs no
// further step. A fault injected during the step replacement leaves the
// version, the steps and the runs unchanged.

func TestAutomationRuleLifecycleSQLite(t *testing.T) {
	runAutomationRuleLifecycle(t, openAutomationSQLite(t))
}

func TestAutomationRuleLifecycleMySQL(t *testing.T) {
	runAutomationRuleLifecycle(t, openAutomationMySQL(t, 2))
}

// faultyAutomationRepo injects a failure into ReplaceSteps after the real
// replacement, so the transaction holds the new steps when it rolls back.
type faultyAutomationRepo struct {
	repository.AutomationRepository
	failReplace *bool
}

func (r *faultyAutomationRepo) ReplaceSteps(ruleID uint, steps []models.AutomationStep) error {
	if err := r.AutomationRepository.ReplaceSteps(ruleID, steps); err != nil {
		return err
	}
	if *r.failReplace {
		return errors.New("injected step replacement fault")
	}
	return nil
}

func (r *faultyAutomationRepo) WithTx(tx *gorm.DB) repository.AutomationRepository {
	return &faultyAutomationRepo{AutomationRepository: r.AutomationRepository.WithTx(tx), failReplace: r.failReplace}
}

func stepTypes(steps []models.AutomationStep) []string {
	types := make([]string, 0, len(steps))
	for _, step := range steps {
		types = append(types, step.StepType)
	}
	return types
}

func runAutomationRuleLifecycle(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	lead := env.createLead(env.sales, models.LeadStatusNew)

	t.Run("update increments the version, replaces the steps and cancels the active runs", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, waitStep(30), notifyStep(models.AutomationRecipientOwner, 0))
		assert.Equal(t, 1, rule.Version)
		assert.Equal(t, []int{1, 2}, []int{rule.Steps[0].Position, rule.Steps[1].Position})

		pending := env.startRun(rule, lead)
		waiting := env.startRun(rule, lead)
		env.executeDue()
		assert.Equal(t, models.AutomationRunWaiting, env.run(waiting.ID).Status)
		// The pending one was executed too and is waiting now; make it pending
		// again and claim a third as running, so every active status is
		// covered by the cancel.
		require.NoError(t, env.repo.ResetRunToPending(pending.ID, env.clock.Now()))
		running := env.startRun(rule, lead)
		claimed, err := env.repo.ClaimRun(running.ID, models.AutomationRunPending, env.clock.Now())
		require.NoError(t, err)
		require.EqualValues(t, 1, claimed)
		finished := env.startRun(rule, lead)
		_, err = env.repo.ClaimRun(finished.ID, models.AutomationRunPending, env.clock.Now())
		require.NoError(t, err)
		rows, err := env.repo.AdvanceRun(finished.ID, 1, repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 3}, env.clock.Now())
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)

		replacement := &models.AutomationRule{Name: "Renamed", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
			Enabled: true, Steps: []models.AutomationStep{taskStep(models.AutomationRecipientOwner, 0, 1)}}
		updated, err := env.svc.UpdateRule(rule.ID, replacement)
		require.NoError(t, err)
		assert.Equal(t, 2, updated.Version)
		assert.Equal(t, "Renamed", updated.Name)
		assert.Equal(t, []string{models.AutomationStepCreateTask}, stepTypes(updated.Steps))
		assert.Equal(t, 1, updated.Steps[0].Position)
		var stepRows int64
		require.NoError(t, db.Model(&models.AutomationStep{}).Where("rule_id = ?", rule.ID).Count(&stepRows).Error)
		assert.EqualValues(t, 1, stepRows, "the old steps are gone")

		for _, id := range []uint{pending.ID, waiting.ID, running.ID} {
			run := env.run(id)
			assert.Equal(t, models.AutomationRunCancelled, run.Status, "run %d", id)
			assert.Equal(t, models.AutomationCancelRuleChanged, run.CancelReason, "run %d", id)
			assert.NotNil(t, run.FinishedAt)
		}
		assert.Equal(t, models.AutomationRunCompleted, env.run(finished.ID).Status, "a finished run is left alone")

		// The executor still holds the running run in memory: its next
		// conditional advance matches zero rows, so no step row, task or
		// notification is written.
		tasksBefore, notificationsBefore := env.count(&models.Task{}), env.count(&models.Notification{})
		stepsBefore := len(env.stepsOf(running.ID))
		snapshot := *running
		snapshot.Status = models.AutomationRunRunning
		// The in-memory snapshot carries the current version, so the executor
		// reaches the step and it is the conditional advance that refuses it.
		snapshot.RuleVersion = updated.Version
		env.engine.executeRun(context.Background(), snapshot)
		assert.Equal(t, tasksBefore, env.count(&models.Task{}))
		assert.Equal(t, notificationsBefore, env.count(&models.Notification{}))
		assert.Len(t, env.stepsOf(running.ID), stepsBefore)
		assert.Equal(t, models.AutomationRunCancelled, env.run(running.ID).Status)
	})

	t.Run("disable and delete cancel the active runs with their own reasons", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, waitStep(30))
		run := env.startRun(rule, lead)
		disabled, err := env.svc.SetEnabled(rule.ID, false)
		require.NoError(t, err)
		assert.False(t, disabled.Enabled)
		assert.Equal(t, models.AutomationRunCancelled, env.run(run.ID).Status)
		assert.Equal(t, models.AutomationCancelRuleDisabled, env.run(run.ID).CancelReason)
		assert.Equal(t, 1, disabled.Version, "a switch does not bump the version")

		enabled, err := env.svc.SetEnabled(rule.ID, true)
		require.NoError(t, err)
		assert.True(t, enabled.Enabled)
		run = env.startRun(rule, lead)
		require.NoError(t, env.svc.DeleteRule(rule.ID))
		_, err = env.svc.GetRule(rule.ID)
		assert.True(t, errors.Is(err, apperrors.ErrAutomationRuleNotFound))
		var row models.AutomationRule
		require.NoError(t, db.Unscoped().First(&row, rule.ID).Error)
		assert.True(t, row.DeletedAt.Valid, "soft-deleted, not dropped")
		assert.Equal(t, models.AutomationRunCancelled, env.run(run.ID).Status)
		assert.Equal(t, models.AutomationCancelRuleDeleted, env.run(run.ID).CancelReason)
		_, err = env.svc.SetEnabled(rule.ID, true)
		assert.True(t, errors.Is(err, apperrors.ErrAutomationRuleNotFound))
	})

	t.Run("a fault during the step replacement leaves version, steps and runs unchanged", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, waitStep(30), notifyStep(models.AutomationRecipientOwner, 0))
		run := env.startRun(rule, lead)
		fail := true
		faulty := &faultyAutomationRepo{AutomationRepository: env.repo, failReplace: &fail}
		svc := NewAutomationService(faulty, env.users, env.engine, env.txManager)
		replacement := &models.AutomationRule{Name: "Broken", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
			Enabled: true, Steps: []models.AutomationStep{taskStep(models.AutomationRecipientOwner, 0, 1)}}
		_, err := svc.UpdateRule(rule.ID, replacement)
		require.Error(t, err)

		stored, err := env.svc.GetRule(rule.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, stored.Version)
		assert.Equal(t, rule.Name, stored.Name)
		assert.Equal(t, []string{models.AutomationStepWait, models.AutomationStepNotify}, stepTypes(stored.Steps))
		assert.Equal(t, models.AutomationRunPending, env.run(run.ID).Status)
		assert.Empty(t, env.run(run.ID).CancelReason)
	})
}
