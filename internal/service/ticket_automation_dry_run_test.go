package service

// AC-9 of #242: the saved-rule and draft dry runs against a ticket report the
// simulated steps with entity_type ticket, change no row in any table, send no
// mail and create no run, notification, task or event; a missing ticket id is
// not found.

import (
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomationTicketDryRunNoSideEffectsSQLite(t *testing.T) {
	env := newTicketAutomationEnv(t, openAutomationSQLite(t))
	ticket := env.createTicket(env.support, models.TicketStatusResolved)
	unassigned := env.createTicket(nil, models.TicketStatusOpen)
	steps := func() []models.AutomationStep {
		return []models.AutomationStep{
			waitStep(90),
			ticketNotifyStep(models.AutomationRecipientOwner, 0),
			ticketTaskStep(models.AutomationRecipientOwner, 0, 2),
			assignStep(env.support2.ID),
			conditionStep("owner_id", "eq", env.support2.ID, models.AutomationElseStop, 0),
			conditionStep("priority", "eq", string(models.TicketPriorityHigh), models.AutomationElseStop, 0),
			ticketEmailStep(models.AutomationRecipientOwner, 0),
		}
	}
	rule := env.ticketRule(models.AutomationEventTicketStatusChanged, models.AutomationTrigger{To: string(models.TicketStatusResolved)}, steps()...)

	before := snapshotDatabase(t, env.db)
	saved, err := env.svc.DryRun(rule.ID, models.AutomationEntityTicket, ticket.ID)
	require.NoError(t, err)
	draft, err := env.svc.DryRunDraft(&models.AutomationRule{Name: "Draft", EntityType: models.AutomationEntityTicket,
		TriggerType: models.AutomationEventTicketUpdated, Steps: steps()}, models.AutomationEntityTicket, ticket.ID)
	require.NoError(t, err)
	skipped, err := env.svc.DryRun(rule.ID, models.AutomationEntityTicket, unassigned.ID)
	require.NoError(t, err)
	after := snapshotDatabase(t, env.db)

	assert.Equal(t, before, after, "no row of any table changed")
	assert.Empty(t, env.mailer.messages(), "no mail was sent")
	assert.Empty(t, env.allRuns())
	assert.Empty(t, env.notificationsList())
	assert.Empty(t, env.tasksList())
	assert.Empty(t, env.events())

	for name, report := range map[string]*models.AutomationDryRunReport{"saved": saved, "draft": draft} {
		assert.Equal(t, models.AutomationEntityTicket, report.EntityType, name)
		assert.Equal(t, ticket.ID, report.EntityID, name)
		require.Len(t, report.Steps, 7, name)
		assert.Equal(t, models.AutomationOutcomeWaiting, report.Steps[0].Outcome)
		assert.EqualValues(t, env.support.ID, report.Steps[1].Details["recipient_id"], "the owner is the assignee")
		assert.EqualValues(t, env.support.ID, report.Steps[2].Details["assignee_id"])
		assert.EqualValues(t, ticket.CustomerID, report.Steps[2].Details["customer_id"], "the task links the customer")
		assert.NotContains(t, report.Steps[2].Details, "lead_id")
		assert.NotContains(t, report.Steps[2].Details, "deal_id")
		assert.EqualValues(t, env.support.ID, report.Steps[3].Details["from_owner_id"])
		assert.EqualValues(t, env.support2.ID, report.Steps[3].Details["to_owner_id"])
		assert.Equal(t, true, report.Steps[4].Details["result"], "the simulated assign feeds the condition")
		assert.Equal(t, true, report.Steps[5].Details["result"])
		assert.EqualValues(t, env.support2.ID, report.Steps[6].Details["recipient_id"])
	}
	assert.False(t, saved.Draft)
	assert.True(t, draft.Draft)

	require.Len(t, skipped.Steps, 7)
	assert.Equal(t, models.AutomationOutcomeSkipped, skipped.Steps[1].Outcome, "an unassigned ticket has no owner")
	assert.Equal(t, models.AutomationErrorRecipientUnavailable, skipped.Steps[1].ErrorCode)

	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityTicket, 99999999)
	require.ErrorIs(t, err, apperrors.ErrNotFound)
	_, err = env.svc.DryRunDraft(&models.AutomationRule{Name: "Draft", EntityType: models.AutomationEntityTicket,
		TriggerType: models.AutomationEventTicketUpdated, Steps: steps()}, models.AutomationEntityTicket, 99999999)
	require.ErrorIs(t, err, apperrors.ErrNotFound)
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityLead, ticket.ID)
	require.ErrorIs(t, err, apperrors.ErrValidation, "the entity type must match the rule's")

	// A sales target of a ticket assign is skipped at run time, as in the
	// executor, even when a stored rule names one.
	require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", env.support2.ID).Update("role", models.RoleSales).Error)
	demoted, err := env.svc.DryRun(rule.ID, models.AutomationEntityTicket, ticket.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AutomationOutcomeSkipped, demoted.Steps[3].Outcome)
	assert.Equal(t, models.AutomationErrorRecipientUnavailable, demoted.Steps[3].ErrorCode)
}
