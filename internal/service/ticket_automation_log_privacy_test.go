package service

// AC-10 of #242: no log line of ticket event capture (create, update, bulk
// status), ticket delete cancellation, ticket dispatch, step execution or dry
// run carries the ticket's title, description or resolution, the customer's
// name or email, or any rendered notification or email text. The lines carry
// operation names, ids, statuses, outcomes and error codes.

import (
	"context"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomationTicketLogsOmitPersonalData(t *testing.T) {
	output, hook := capturePrivacyLogs(t)
	env := newTicketAutomationEnv(t, openAutomationSQLite(t))
	bulk := NewBulkStatusService(repository.NewBulkOperationRepository(env.db), repository.NewBulkStatusRepository(env.db),
		env.txManager, utils.Logger, WithBulkAutomationEvents(env.repo))

	rule := env.ticketRule(models.AutomationEventTicketStatusChanged, models.AutomationTrigger{To: string(models.TicketStatusResolved)},
		ticketNotifyStep(models.AutomationRecipientOwner, 0),
		ticketTaskStep(models.AutomationRecipientOwner, 0, 1),
		ticketEmailStep(models.AutomationRecipientOwner, 0),
		assignStep(env.support2.ID),
		waitStep(60),
		ticketNotifyStep(models.AutomationRecipientOwner, 0))
	// One send fails, so the failure path logs too.
	env.mailer.reset(assertableMailError{}, nil)

	// Capture: a create and an update through the wired service.
	assignee := env.support.ID
	created := &models.Ticket{Title: ticketCanaryTitle, Description: ticketCanaryDescription, Resolution: ticketCanaryResolution,
		Priority: models.TicketPriorityHigh, CustomerID: env.customer.ID, AssignedToID: &assignee}
	require.NoError(t, env.ticketSvc.Create(created))
	current, err := env.tickets.GetByID(created.ID)
	require.NoError(t, err)
	current.Status = models.TicketStatusResolved
	require.NoError(t, env.ticketSvc.Update(current))
	// Capture: a bulk status batch.
	other := env.createTicket(env.support, models.TicketStatusOpen)
	_, err = bulk.BulkSetTicketStatus(context.Background(), env.admin.ID, models.RoleAdmin, []uint{other.ID}, models.TicketStatusResolved)
	require.NoError(t, err)

	// Dispatch and execution up to the wait.
	require.Equal(t, 2, env.dispatch())
	env.executeDue()
	// Dry runs, saved and draft.
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityTicket, other.ID)
	require.NoError(t, err)
	_, err = env.svc.DryRunDraft(&models.AutomationRule{Name: "Draft", EntityType: models.AutomationEntityTicket,
		TriggerType: models.AutomationEventTicketUpdated, Steps: []models.AutomationStep{ticketNotifyStep(models.AutomationRecipientOwner, 0)}},
		models.AutomationEntityTicket, other.ID)
	require.NoError(t, err)
	// Delete cancellation of the waiting runs.
	require.NoError(t, env.ticketSvc.Delete(created.ID))
	require.NoError(t, env.ticketSvc.Delete(other.ID))

	runs := env.runsOf(rule.ID)
	require.Len(t, runs, 2)
	for _, run := range runs {
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
	}
	require.NotEmpty(t, hook.AllEntries(), "the flows logged something")

	logs := output.String()
	for _, needle := range []string{
		ticketCanaryTitle, ticketCanaryDescription, ticketCanaryResolution, ticketCanaryCustomer, "Kowalska-Canary", env.customer.Email,
		"TICKET-NOTIFY-CANARY", "TICKET-NOTIFY-BODY-CANARY", "TICKET-EMAIL-CANARY", "TICKET-EMAIL-BODY-CANARY",
		"Check ticket", env.support.Email, env.support2.Email, "relay rejected", "mailbox unavailable",
	} {
		assert.NotContains(t, logs, needle)
	}
	for _, expected := range []string{
		`"msg":"Automation runs started"`, `"msg":"Automation step executed"`, `"msg":"Automation dry run completed"`,
		`"msg":"Automation draft dry run completed"`, `"entity_type":"ticket"`, `"step_type":"send_email"`,
		`"error_code":"mail_failed"`, `"run_id":`, `"ticket_id":`,
	} {
		assert.Contains(t, logs, expected)
	}
}
