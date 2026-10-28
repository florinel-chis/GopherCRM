package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-7: a condition step evaluates allowlisted fields with allowlisted
// operators against a fresh read of the record; true continues, false either
// completes the run (else=stop) or continues at else_position (else=jump); a
// condition on amount_cents is true only when the deal's currency equals the
// condition's; unknown fields, operators or value types are refused when the
// rule is validated.
func TestAutomationConditionBranching(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))

	t.Run("evaluation matrix", func(t *testing.T) {
		lead := &models.Lead{Status: models.LeadStatusContacted, Source: "Webinar", Company: "", OwnerID: 7}
		lead.ID = 3
		rec := recordFromLead(lead)
		deal := &models.Deal{Stage: models.DealStageProposal, AmountCents: 5000, Currency: "USD", Probability: 40, OwnerID: 7}
		deal.ID = 4
		dealRec := recordFromDeal(deal)
		for _, tc := range []struct {
			name string
			rec  *automationRecord
			step models.AutomationStep
			want bool
		}{
			{"status eq", rec, conditionStep("status", "eq", "contacted", "", 0), true},
			{"status neq", rec, conditionStep("status", "neq", "contacted", "", 0), false},
			{"status in", rec, conditionStep("status", "in", []string{"new", "contacted"}, "", 0), true},
			{"status not_in", rec, conditionStep("status", "not_in", []string{"new", "contacted"}, "", 0), false},
			{"source contains is case-insensitive", rec, conditionStep("source", "contains", "webinar", "", 0), true},
			{"company is_empty", rec, conditionStep("company", "is_empty", nil, "", 0), true},
			{"company is_not_empty", rec, conditionStep("company", "is_not_empty", nil, "", 0), false},
			{"owner_id eq", rec, conditionStep("owner_id", "eq", 7, "", 0), true},
			{"owner_id gt", rec, conditionStep("owner_id", "gt", 7, "", 0), false},
			{"probability gte", dealRec, conditionStep("probability", "gte", 40, "", 0), true},
			{"stage eq", dealRec, conditionStep("stage", "eq", "proposal", "", 0), true},
			{"amount in the deal's currency", dealRec, moneyCondition("gt", 1000, "USD"), true},
			{"amount in another currency is never true", dealRec, moneyCondition("gt", 1000, "EUR"), false},
			{"amount lt", dealRec, moneyCondition("lt", 1000, "USD"), false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.NoError(t, validateCondition(tc.rec.entityType, tc.step.Config), "the matrix only uses valid conditions")
				got, err := evaluateCondition(tc.step.Config, tc.rec)
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("validation refuses unknown fields, operators and value types", func(t *testing.T) {
		for name, step := range map[string]models.AutomationStep{
			"unknown field":                 conditionStep("email", "eq", "x", "", 0),
			"deal field on a lead rule":     conditionStep("stage", "eq", "won", "", 0),
			"unknown operator":              conditionStep("status", "matches", "new", "", 0),
			"number operator on a string":   conditionStep("status", "gt", "new", "", 0),
			"string value for a number":     conditionStep("owner_id", "eq", "7", "", 0),
			"number value for a string":     conditionStep("status", "eq", 5, "", 0),
			"in without an array":           conditionStep("status", "in", "new", "", 0),
			"is_empty with a value":         conditionStep("company", "is_empty", "x", "", 0),
			"currency on a non-money field": {StepType: models.AutomationStepCondition, Config: models.AutomationStepConfig{Field: "status", Operator: "eq", Value: json.RawMessage(`"new"`), Currency: "EUR"}},
			"jump backwards":                conditionStep("status", "eq", "new", models.AutomationElseJump, 1),
			"jump out of range":             conditionStep("status", "eq", "new", models.AutomationElseJump, 9),
			"unknown else":                  conditionStep("status", "eq", "new", "retry", 0),
		} {
			t.Run(name, func(t *testing.T) {
				rule := &models.AutomationRule{Name: "Bad", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
					Steps: []models.AutomationStep{step, notifyStep(models.AutomationRecipientOwner, 0)}}
				err := env.svc.CreateRule(rule, env.admin.ID)
				require.Error(t, err)
				assert.True(t, errors.Is(err, apperrors.ErrValidation), "got %v", err)
			})
		}
		money := &models.AutomationRule{Name: "Money", EntityType: models.AutomationEntityDeal, TriggerType: models.AutomationEventDealCreated,
			Steps: []models.AutomationStep{{StepType: models.AutomationStepCondition, Config: models.AutomationStepConfig{Field: "amount_cents", Operator: "gt", Value: json.RawMessage(`100`)}}}}
		err := env.svc.CreateRule(money, env.admin.ID)
		assert.True(t, errors.Is(err, apperrors.ErrValidation), "amount_cents needs a currency, got %v", err)
		assert.Zero(t, env.count(&models.AutomationRule{}), "no refused rule wrote a row")
		assert.Zero(t, env.count(&models.AutomationStep{}))
	})

	t.Run("true continues, false stops", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated,
			conditionStep("status", "eq", "new", models.AutomationElseStop, 0), notifyStep(models.AutomationRecipientOwner, 0))
		fresh := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, fresh)
		env.executeDue()
		stored := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, stored.Status)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 2)
		assert.Equal(t, true, steps[0].Details["result"])
		assert.Equal(t, "next", steps[0].Details["branch"])
		assert.Len(t, env.notificationsList(), 1)

		contacted := env.createLead(env.sales, models.LeadStatusContacted)
		run = env.startRun(rule, contacted)
		env.executeDue()
		stored = env.run(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, stored.Status)
		steps = env.stepsOf(run.ID)
		require.Len(t, steps, 1, "the run stopped at the condition")
		assert.Equal(t, false, steps[0].Details["result"])
		assert.Equal(t, models.AutomationElseStop, steps[0].Details["branch"])
		assert.Len(t, env.notificationsList(), 1, "no notification for the stopped run")
	})

	t.Run("false jumps forward and the read is fresh", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated,
			conditionStep("status", "eq", "new", models.AutomationElseJump, 3),
			taskStep(models.AutomationRecipientOwner, 0, 1),
			notifyStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		// The record changed between the dispatch and the execution: the
		// condition evaluates what is stored now, not what the event saw.
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("status", models.LeadStatusUnqualified).Error)
		tasksBefore, notificationsBefore := env.count(&models.Task{}), env.count(&models.Notification{})
		env.executeDue()
		stored := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, stored.Status)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 2)
		assert.Equal(t, 1, steps[0].Position)
		assert.Equal(t, models.AutomationElseJump, steps[0].Details["branch"])
		assert.EqualValues(t, 3, steps[0].Details["next_position"])
		assert.Equal(t, 3, steps[1].Position)
		assert.Equal(t, models.AutomationStepNotify, steps[1].StepType)
		assert.Equal(t, tasksBefore, env.count(&models.Task{}), "the skipped-over task step never ran")
		assert.Equal(t, notificationsBefore+1, env.count(&models.Notification{}))
	})

	t.Run("a money condition follows the deal's currency", func(t *testing.T) {
		rule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
			moneyCondition("gte", 100000, "EUR"), dealNotifyStep(models.AutomationRecipientOwner, 0))
		usd := env.createDeal(env.sales, models.DealStageProposal, 500000, "USD", nil, nil)
		eur := env.createDeal(env.sales, models.DealStageProposal, 500000, "EUR", nil, nil)
		for _, deal := range []*models.Deal{usd, eur} {
			env.recordEvent(models.AutomationEntityDeal, deal.ID, models.AutomationEventDealCreated, "", string(deal.Stage), models.AutomationOriginUser)
		}
		env.dispatch()
		notificationsBefore := env.count(&models.Notification{})
		env.executeDue()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 2)
		for _, run := range runs {
			steps := env.stepsOf(run.ID)
			require.NotEmpty(t, steps)
			if run.EntityID == usd.ID {
				assert.Equal(t, false, steps[0].Details["result"], "USD amount against an EUR condition is false")
			} else {
				assert.Equal(t, true, steps[0].Details["result"])
			}
		}
		assert.Equal(t, notificationsBefore+1, env.count(&models.Notification{}))
	})
}

func moneyCondition(operator string, amount int64, currency string) models.AutomationStep {
	step := conditionStep("amount_cents", operator, amount, "", 0)
	step.Config.Currency = currency
	return step
}

// A task created by a rule survives the erasure of the lead it links to, so
// its title and description may carry only the non-personal placeholders:
// a personal one is refused when the rule is validated, and the renderer
// blanks it even for a rule stored before the allowlist was narrowed. The
// email and notification text keeps the wider allowlist: a mail is not
// stored and a notification is deleted with the lead and with its recipient.
func TestAutomationTaskPlaceholdersExcludePersonalData(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	lead := env.createLead(env.sales, models.LeadStatusNew)

	t.Run("the renderer substitutes ids, status and source and blanks the rest", func(t *testing.T) {
		text := "{{lead.id}}|{{lead.status}}|{{lead.source}}|{{lead.first_name}}|{{lead.last_name}}|{{lead.email}}|{{lead.phone}}|{{lead.company}}|{{owner.first_name}}|{{owner.email}}"
		rendered := renderTaskPlaceholders(text, recordFromLead(lead))
		assert.Equal(t, fmt.Sprintf("%d|new|trade_show|||||||", lead.ID), rendered)
		for _, personal := range []string{automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, lead.Email, "Canary Works", env.sales.Email} {
			assert.NotContains(t, rendered, personal)
		}
		assert.Equal(t, []string{"lead.first_name", "lead.last_name", "lead.email", "lead.phone", "lead.company", "owner.first_name", "owner.email"},
			unknownTaskPlaceholders(models.AutomationEntityLead, text))
		assert.Empty(t, unknownPlaceholders(models.AutomationEntityLead, text), "the email and notification allowlist knows every one of them")
	})

	t.Run("validation refuses a personal placeholder in a task and accepts it in an email or a notification", func(t *testing.T) {
		rulesBefore, stepsBefore := env.count(&models.AutomationRule{}), env.count(&models.AutomationStep{})
		task := func(title, description string) models.AutomationStep {
			return models.AutomationStep{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
				Recipient: models.AutomationRecipientOwner, Title: title, Description: description, DueInDays: 1,
			}}
		}
		for name, step := range map[string]models.AutomationStep{
			"first name in the title":         task("Call {{lead.first_name}}", ""),
			"email in the description":        task("Call", "Write to {{lead.email}}"),
			"phone in the description":        task("Call", "Dial {{lead.phone}}"),
			"company in the title":            task("Visit {{lead.company}}", ""),
			"owner address in the title":      task("Owned by {{owner.email}}", ""),
			"owner name in the description":   task("Call", "Owner {{owner.first_name}} {{owner.last_name}}"),
			"unknown placeholder in a task":   task("Call {{lead.password}}", ""),
			"deal placeholder in a lead task": task("Call about {{deal.title}}", ""),
		} {
			t.Run(name, func(t *testing.T) {
				rule := &models.AutomationRule{Name: "Bad task", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated, Steps: []models.AutomationStep{step}}
				err := env.svc.CreateRule(rule, env.admin.ID)
				require.Error(t, err)
				assert.True(t, errors.Is(err, apperrors.ErrValidation), "got %v", err)
			})
		}
		assert.Equal(t, rulesBefore, env.count(&models.AutomationRule{}), "no refused rule wrote a row")
		assert.Equal(t, stepsBefore, env.count(&models.AutomationStep{}))

		accepted := &models.AutomationRule{Name: "Good", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated, Steps: []models.AutomationStep{
			task("Follow up with lead {{lead.id}}", "Status {{lead.status}}, source {{lead.source}}"),
			emailStep(models.AutomationRecipientOwner, 0),
			notifyStep(models.AutomationRecipientOwner, 0),
		}}
		require.NoError(t, env.svc.CreateRule(accepted, env.admin.ID), "ids, status and source in a task; the name, email and company in the mail and the notification")

		dealTask := &models.AutomationRule{Name: "Deal task", EntityType: models.AutomationEntityDeal, TriggerType: models.AutomationEventDealCreated, Steps: []models.AutomationStep{
			dealTaskStep(models.AutomationRecipientOwner, 0, 1),
		}}
		require.NoError(t, env.svc.CreateRule(dealTask, env.admin.ID), "every deal placeholder is allowed in a task: deals hold no personal data")
	})
}
