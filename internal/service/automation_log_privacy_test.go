package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-18: the logs written while dispatching, executing and dry-running a
// rule contain none of the lead's name, email, phone or notes and none of
// the email or notification subject and body text; they carry operation
// names, ids, statuses, step types, outcomes and error codes.
func TestAutomationLogsOmitPersonalData(t *testing.T) {
	output, hook := capturePrivacyLogs(t)
	env := newAutomationEnv(t, openAutomationSQLite(t))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	rule := env.leadRule(models.AutomationEventLeadCreated,
		conditionStep("status", "eq", "new", models.AutomationElseStop, 0),
		taskStep(models.AutomationRecipientOwner, 0, 1),
		assignStep(env.other.ID),
		notifyStep(models.AutomationRecipientOwner, 0),
		emailStep(models.AutomationRecipientOwner, 0),
		notifyStep(models.AutomationRecipientUser, env.other.ID))
	// One send fails, so the failure path logs too.
	env.mailer.reset(assertableMailError{}, nil)

	env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
	env.dispatch()
	env.executeDue()
	_, err := env.svc.DryRun(rule.ID, models.AutomationEntityLead, lead.ID)
	require.NoError(t, err)
	run := env.runsOf(rule.ID)[0]
	require.Equal(t, models.AutomationRunCompleted, run.Status)
	require.NotEmpty(t, hook.AllEntries(), "the flows logged something")

	logs := output.String()
	for _, needle := range []string{
		automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, automationCanaryNotes, lead.Email,
		"EMAIL-SUBJECT-CANARY", "EMAIL-BODY-CANARY", "NOTIFY-TITLE-CANARY", "NOTIFY-BODY-CANARY",
		"Follow up with", env.sales.Email, env.other.Email, "relay rejected", "mailbox unavailable",
	} {
		assert.NotContains(t, logs, needle)
	}
	for _, expected := range []string{
		`"msg":"Automation runs started"`, `"msg":"Automation step executed"`, `"msg":"Automation dry run completed"`,
		`"step_type":"send_email"`, `"outcome":"succeeded"`, `"outcome":"failed"`, `"error_code":"mail_failed"`,
		`"run_id":`, `"rule_id":`, `"position":`,
	} {
		assert.Contains(t, logs, expected)
	}
}

// assertableMailError is a transport error that quotes the recipient, as a
// relay reply does; the engine must not log it.
type assertableMailError struct{}

func (assertableMailError) Error() string {
	return "relay rejected sales@automation.test: 550 mailbox unavailable"
}
