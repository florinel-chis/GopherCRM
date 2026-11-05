package service

// A deal run records its deal on the tasks it creates (#240), keeping its
// customer-else-lead link. Real engine, real repositories, committed rows.

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationDealTaskLinkSQLite(t *testing.T) {
	runAutomationDealTaskLink(t, openAutomationSQLite(t))
}

func TestAutomationDealTaskLinkMySQL(t *testing.T) {
	runAutomationDealTaskLink(t, openAutomationMySQL(t, 4))
}

func runAutomationDealTaskLink(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)

	t.Run("a deal run links the deal and its customer, else its lead, else neither", func(t *testing.T) {
		rule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
			dealTaskStep(models.AutomationRecipientUser, env.other.ID, 0))
		customer := env.createCustomer()
		lead := env.createLead(env.sales, models.LeadStatusQualified)
		withCustomer := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", &customer.ID, &lead.ID)
		withLead := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, &lead.ID)
		bare := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, nil)
		won := env.createDeal(env.sales, models.DealStageWon, 100, "EUR", &customer.ID, nil)
		lost := env.createDeal(env.sales, models.DealStageLost, 100, "EUR", nil, &lead.ID)
		deals := []*models.Deal{withCustomer, withLead, bare, won, lost}
		for _, deal := range deals {
			env.recordEvent(models.AutomationEntityDeal, deal.ID, models.AutomationEventDealCreated, "", string(deal.Stage), models.AutomationOriginUser)
		}
		tasksBefore := len(env.tasksList())
		env.dispatch()
		env.executeDue()

		tasks := env.tasksList()[tasksBefore:]
		require.Len(t, tasks, len(deals))
		byDeal := map[uint]models.Task{}
		for _, task := range tasks {
			require.NotNil(t, task.DealID, "every task of a deal run records the deal")
			assert.Equal(t, env.other.ID, task.AssignedToID, "the assignee need not own the deal")
			byDeal[*task.DealID] = task
		}
		require.Len(t, byDeal, len(deals), "one task per deal")

		assert.Equal(t, customer.ID, *byDeal[withCustomer.ID].CustomerID)
		assert.Nil(t, byDeal[withCustomer.ID].LeadID, "a deal with a customer links the customer only")
		assert.Nil(t, byDeal[withLead.ID].CustomerID)
		assert.Equal(t, lead.ID, *byDeal[withLead.ID].LeadID)
		assert.Nil(t, byDeal[bare.ID].CustomerID)
		assert.Nil(t, byDeal[bare.ID].LeadID)
		assert.Equal(t, customer.ID, *byDeal[won.ID].CustomerID, "a closed deal still gets its task")
		assert.Equal(t, lead.ID, *byDeal[lost.ID].LeadID, "a closed deal still gets its task")

		runs := env.runsOf(rule.ID)
		require.Len(t, runs, len(deals))
		for _, run := range runs {
			assert.Equal(t, models.AutomationRunCompleted, run.Status)
			steps := env.stepsOf(run.ID)
			require.Len(t, steps, 1)
			assert.EqualValues(t, run.EntityID, steps[0].Details["deal_id"], "the step details hold the deal")
			assert.EqualValues(t, byDeal[run.EntityID].ID, steps[0].Details["task_id"])
		}
	})

	t.Run("a lead run leaves deal_id empty", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, taskStep(models.AutomationRecipientOwner, 0, 1))
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		tasksBefore := len(env.tasksList())
		env.executeDue()

		tasks := env.tasksList()[tasksBefore:]
		require.Len(t, tasks, 1)
		assert.Nil(t, tasks[0].DealID)
		require.NotNil(t, tasks[0].LeadID)
		assert.Equal(t, lead.ID, *tasks[0].LeadID)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.NotContains(t, steps[0].Details, "deal_id")
	})

	t.Run("a fault after the effect rolls the task back and the retry creates one", func(t *testing.T) {
		rule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealUpdated, models.AutomationTrigger{},
			dealTaskStep(models.AutomationRecipientOwner, 0, 1))
		deal := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, nil)
		env.recordEvent(models.AutomationEntityDeal, deal.ID, models.AutomationEventDealUpdated, "", "", models.AutomationOriginUser)
		env.dispatch()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 1)
		run := runs[0]

		linked := func() int64 {
			var count int64
			require.NoError(t, env.db.Model(&models.Task{}).Where("deal_id = ?", deal.ID).Count(&count).Error)
			return count
		}

		faults := 0
		env.engine.afterEffect = func(runID uint, position int) error {
			if runID == run.ID && position == 1 && faults == 0 {
				faults++
				return errors.New("injected fault after the effect")
			}
			return nil
		}
		env.executeDue()
		env.engine.afterEffect = nil
		assert.Equal(t, 1, faults)
		assert.Zero(t, linked(), "the task was rolled back with the step")
		assert.Empty(t, env.stepsOf(run.ID), "and the log row")
		assert.Equal(t, models.AutomationRunPending, env.run(run.ID).Status, "the run was handed back")

		env.executeDue()
		assert.EqualValues(t, 1, linked(), "exactly one task on the retry")
		assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.EqualValues(t, deal.ID, steps[0].Details["deal_id"])
	})
}

// TestAutomationDryRunReportsDealLink: the report names the deal beside the
// customer or the lead, and nothing is written.
func TestAutomationDryRunReportsDealLink(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	customer := env.createCustomer()
	lead := env.createLead(env.sales, models.LeadStatusQualified)
	withCustomer := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", &customer.ID, &lead.ID)
	withLead := env.createDeal(env.sales, models.DealStageWon, 100, "EUR", nil, &lead.ID)
	bare := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, nil)
	dealRule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
		dealTaskStep(models.AutomationRecipientOwner, 0, 1))
	leadRule := env.leadRule(models.AutomationEventLeadCreated, taskStep(models.AutomationRecipientOwner, 0, 1))

	tasksBefore, runsBefore, stepsBefore := env.count(&models.Task{}), env.count(&models.AutomationRun{}), env.count(&models.AutomationRunStep{})

	details := func(ruleID uint, entityType string, entityID uint) map[string]interface{} {
		report, err := env.svc.DryRun(ruleID, entityType, entityID)
		require.NoError(t, err)
		require.Len(t, report.Steps, 1)
		assert.Equal(t, models.AutomationStepCreateTask, report.Steps[0].StepType)
		return report.Steps[0].Details
	}

	got := details(dealRule.ID, models.AutomationEntityDeal, withCustomer.ID)
	assert.EqualValues(t, withCustomer.ID, got["deal_id"])
	assert.EqualValues(t, customer.ID, got["customer_id"])
	assert.NotContains(t, got, "lead_id")

	got = details(dealRule.ID, models.AutomationEntityDeal, withLead.ID)
	assert.EqualValues(t, withLead.ID, got["deal_id"], "a closed deal is reported too")
	assert.EqualValues(t, lead.ID, got["lead_id"])
	assert.NotContains(t, got, "customer_id")

	got = details(dealRule.ID, models.AutomationEntityDeal, bare.ID)
	assert.EqualValues(t, bare.ID, got["deal_id"])
	assert.NotContains(t, got, "customer_id")
	assert.NotContains(t, got, "lead_id")

	got = details(leadRule.ID, models.AutomationEntityLead, lead.ID)
	assert.EqualValues(t, lead.ID, got["lead_id"])
	assert.NotContains(t, got, "deal_id")

	assert.Equal(t, tasksBefore, env.count(&models.Task{}), "a dry run creates no task")
	assert.Equal(t, runsBefore, env.count(&models.AutomationRun{}), "and no run")
	assert.Equal(t, stepsBefore, env.count(&models.AutomationRunStep{}), "and no step row")
}
