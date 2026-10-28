package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-10: a dry run returns a per-step report — outcome, simulated time and
// details such as the resolved user ids, the task due date and link, the
// owner from/to and the branch taken; waits advance the simulated clock
// without pausing and earlier simulated assigns feed later conditions; the
// dry run changes no row count or column value in any table, makes zero
// Mailer.Send calls and creates no run, notification, task or event.

func TestAutomationDryRunNoSideEffectsSQLite(t *testing.T) {
	runAutomationDryRunNoSideEffects(t, openAutomationSQLite(t))
}

func TestAutomationDryRunNoSideEffectsMySQL(t *testing.T) {
	runAutomationDryRunNoSideEffects(t, openAutomationMySQL(t, 2))
}

// snapshotDatabase reads every row of every table as the engine stores it,
// so a before/after comparison catches any changed count or column value.
func snapshotDatabase(t *testing.T, db *gorm.DB) map[string][]map[string]interface{} {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	snapshot := map[string][]map[string]interface{}{}
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Order("1").Find(&rows).Error, table)
		for _, row := range rows {
			for column, value := range row {
				if b, ok := value.([]byte); ok {
					row[column] = string(b)
				}
			}
		}
		snapshot[table] = rows
	}
	return snapshot
}

func runAutomationDryRunNoSideEffects(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	customer := env.createCustomer()
	lead := env.createLead(env.sales, models.LeadStatusNew)
	deal := env.createDeal(env.sales, models.DealStageProposal, 250000, "EUR", &customer.ID, nil)
	rule := env.leadRule(models.AutomationEventLeadCreated,
		waitStep(120),
		assignStep(env.other.ID),
		conditionStep("owner_id", "eq", env.other.ID, models.AutomationElseStop, 0),
		taskStep(models.AutomationRecipientOwner, 0, 3),
		emailStep(models.AutomationRecipientUser, env.sales.ID),
		notifyStep(models.AutomationRecipientOwner, 0))
	dealRule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
		conditionStep("stage", "eq", "won", models.AutomationElseJump, 3),
		dealNotifyStep(models.AutomationRecipientOwner, 0),
		dealTaskStep(models.AutomationRecipientOwner, 0, 1))
	// Something to compare against: an unprocessed event, a run and a
	// notification already in the tables.
	env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
	env.dispatch()
	env.executeDue()
	require.Equal(t, models.AutomationRunWaiting, env.runsOf(rule.ID)[0].Status, "the real run is parked at its wait")
	env.mailer.reset(nil, nil)

	before := snapshotDatabase(t, db)
	start := env.clock.Now().UTC()

	report, err := env.svc.DryRun(rule.ID, models.AutomationEntityLead, lead.ID)
	require.NoError(t, err)
	assert.Equal(t, rule.ID, report.RuleID)
	assert.Equal(t, rule.Version, report.RuleVersion)
	assert.Equal(t, lead.ID, report.EntityID)
	assert.WithinDuration(t, start, report.SimulatedStart, time.Second)
	assert.WithinDuration(t, start.Add(120*time.Minute), report.SimulatedEnd, time.Second)
	require.Len(t, report.Steps, 6)

	wait := report.Steps[0]
	assert.Equal(t, models.AutomationStepWait, wait.StepType)
	assert.Equal(t, models.AutomationOutcomeWaiting, wait.Outcome)
	assert.WithinDuration(t, start, wait.SimulatedAt, time.Second)
	assert.Equal(t, start.Add(120*time.Minute).Format(time.RFC3339), wait.Details["resume_at"])

	assign := report.Steps[1]
	assert.WithinDuration(t, start.Add(120*time.Minute), assign.SimulatedAt, time.Second, "the wait advanced the simulated clock")
	assert.Equal(t, models.AutomationOutcomeSucceeded, assign.Outcome)
	assert.EqualValues(t, env.sales.ID, assign.Details["from_owner_id"])
	assert.EqualValues(t, env.other.ID, assign.Details["to_owner_id"])

	condition := report.Steps[2]
	assert.Equal(t, true, condition.Details["result"], "the simulated assign fed the condition")
	assert.Equal(t, "next", condition.Details["branch"])

	task := report.Steps[3]
	assert.EqualValues(t, env.other.ID, task.Details["assignee_id"], "the owner after the simulated assign")
	assert.Equal(t, start.Add(120*time.Minute).Add(3*24*time.Hour).Format(time.RFC3339), task.Details["due_date"])
	assert.EqualValues(t, lead.ID, task.Details["lead_id"])
	_, hasCustomer := task.Details["customer_id"]
	assert.False(t, hasCustomer)

	email := report.Steps[4]
	assert.EqualValues(t, env.sales.ID, email.Details["recipient_id"])
	notify := report.Steps[5]
	assert.EqualValues(t, env.other.ID, notify.Details["recipient_id"])

	dealReport, err := env.svc.DryRun(dealRule.ID, models.AutomationEntityDeal, deal.ID)
	require.NoError(t, err)
	require.Len(t, dealReport.Steps, 2, "the false condition jumped over the notify")
	assert.Equal(t, models.AutomationElseJump, dealReport.Steps[0].Details["branch"])
	assert.EqualValues(t, 3, dealReport.Steps[0].Details["next_position"])
	assert.Equal(t, 3, dealReport.Steps[1].Position)
	assert.EqualValues(t, customer.ID, dealReport.Steps[1].Details["customer_id"])

	// Refusals write nothing either.
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityDeal, deal.ID)
	assert.True(t, errors.Is(err, apperrors.ErrValidation), "a record of the wrong entity type, got %v", err)
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityLead, 999999)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound), "a missing record, got %v", err)
	_, err = env.svc.DryRun(999999, models.AutomationEntityLead, lead.ID)
	assert.True(t, errors.Is(err, apperrors.ErrAutomationRuleNotFound), "a missing rule, got %v", err)

	after := snapshotDatabase(t, db)
	assert.Equal(t, before, after, "the dry run changed a row count or a column value")
	assert.Empty(t, env.mailer.messages(), "zero Mailer.Send calls")
	assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID, "the simulated assign changed nothing")
}
