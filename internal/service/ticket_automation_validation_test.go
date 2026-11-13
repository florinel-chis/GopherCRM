package service

// AC-3 of #242: validateRule accepts ticket rules on ticket_created,
// ticket_updated, ticket_status_changed and schedule, with ticket statuses in
// from/to and filters.ticket_status; the ticket condition fields, the notify
// and task placeholder allowlists (no title, description or customer field)
// and the support/admin rule of a ticket assign_owner. The draft dry run
// applies the same refusals.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomationTicketRuleValidation(t *testing.T) {
	env := newTicketAutomationEnv(t, openAutomationSQLite(t))
	ticket := env.createTicket(env.support, models.TicketStatusOpen)

	ticketRule := func(triggerType string, trigger models.AutomationTrigger, steps ...models.AutomationStep) *models.AutomationRule {
		return &models.AutomationRule{Name: "Ticket rule", EntityType: models.AutomationEntityTicket, TriggerType: triggerType,
			Trigger: trigger, Steps: steps}
	}
	notify := ticketNotifyStep(models.AutomationRecipientOwner, 0)
	// fields validates a body and returns its refusals by field.
	fields := func(t *testing.T, rule *models.AutomationRule) map[string]RuleFieldError {
		t.Helper()
		err := env.svc.validateRule(rule)
		require.Error(t, err)
		var refusal *RuleValidationError
		require.True(t, errors.As(err, &refusal), "a rule refusal: %v", err)
		out := map[string]RuleFieldError{}
		for _, field := range refusal.Fields {
			out[field.Field] = field
		}
		return out
	}
	accepted := func(t *testing.T, rule *models.AutomationRule) {
		t.Helper()
		require.NoError(t, env.svc.validateRule(rule))
	}
	condition := func(field, operator string, value interface{}) models.AutomationStep {
		raw, _ := json.Marshal(value)
		return models.AutomationStep{StepType: models.AutomationStepCondition, Config: models.AutomationStepConfig{
			Field: field, Operator: operator, Value: raw}}
	}

	t.Run("ticket triggers are accepted and saved", func(t *testing.T) {
		for _, trigger := range []string{models.AutomationEventTicketCreated, models.AutomationEventTicketUpdated, models.AutomationEventTicketStatusChanged} {
			rule := ticketRule(trigger, models.AutomationTrigger{}, notify)
			require.NoError(t, env.automationEnv.svc.CreateRule(rule, env.admin.ID), trigger)
			stored, err := env.automationEnv.svc.GetRule(rule.ID)
			require.NoError(t, err)
			assert.Equal(t, models.AutomationEntityTicket, stored.EntityType)
			assert.Equal(t, trigger, stored.TriggerType)
		}
		accepted(t, ticketRule(models.AutomationEventTicketStatusChanged,
			models.AutomationTrigger{From: string(models.TicketStatusOpen), To: string(models.TicketStatusResolved)}, notify))
		rule := ticketRule(models.AutomationTriggerSchedule,
			models.AutomationTrigger{HourUTC: 8, StaleDays: 3, Filters: &models.AutomationScheduleFilter{TicketStatus: string(models.TicketStatusResolved)}}, notify)
		require.NoError(t, env.automationEnv.svc.CreateRule(rule, env.admin.ID))
		stored, err := env.automationEnv.svc.GetRule(rule.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.Trigger.Filters)
		assert.Equal(t, string(models.TicketStatusResolved), stored.Trigger.Filters.TicketStatus, "filters.ticket_status round-trips")
	})

	t.Run("trigger types of other entities are refused on a ticket rule and the reverse", func(t *testing.T) {
		got := fields(t, ticketRule(models.AutomationEventLeadCreated, models.AutomationTrigger{}, notify))
		assert.Equal(t, RuleErrorInvalidValue, got["trigger_type"].Code)
		got = fields(t, ticketRule(models.AutomationEventDealStageChanged, models.AutomationTrigger{}, notify))
		assert.Equal(t, RuleErrorInvalidValue, got["trigger_type"].Code)
		leadRule := &models.AutomationRule{Name: "Lead", EntityType: models.AutomationEntityLead,
			TriggerType: models.AutomationEventTicketCreated, Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientOwner, 0)}}
		got = fields(t, leadRule)
		assert.Equal(t, RuleErrorInvalidValue, got["trigger_type"].Code)
	})

	t.Run("from and to must be ticket statuses", func(t *testing.T) {
		got := fields(t, ticketRule(models.AutomationEventTicketStatusChanged,
			models.AutomationTrigger{From: string(models.LeadStatusNew), To: string(models.DealStageWon)}, notify))
		assert.Equal(t, RuleErrorInvalidValue, got["trigger.from"].Code)
		assert.Equal(t, RuleErrorInvalidValue, got["trigger.to"].Code)
		got = fields(t, ticketRule(models.AutomationEventTicketUpdated,
			models.AutomationTrigger{To: string(models.TicketStatusResolved)}, notify))
		assert.Equal(t, RuleErrorNotApplicable, got["trigger.to"].Code, "from/to only apply to ticket_status_changed")
	})

	t.Run("schedule filters", func(t *testing.T) {
		got := fields(t, ticketRule(models.AutomationTriggerSchedule, models.AutomationTrigger{Filters: &models.AutomationScheduleFilter{
			TicketStatus: "pending", Status: string(models.LeadStatusNew), Stage: string(models.DealStageWon)}}, notify))
		assert.Equal(t, RuleErrorInvalidValue, got["trigger.filters.ticket_status"].Code)
		assert.Equal(t, RuleErrorInvalidValue, got["trigger.filters.status"].Code, "filters.status is refused on a ticket rule")
		assert.Equal(t, RuleErrorInvalidValue, got["trigger.filters.stage"].Code, "filters.stage is refused on a ticket rule")

		for _, entity := range []string{models.AutomationEntityLead, models.AutomationEntityDeal} {
			step := notifyStep(models.AutomationRecipientOwner, 0)
			if entity == models.AutomationEntityDeal {
				step = dealNotifyStep(models.AutomationRecipientOwner, 0)
			}
			rule := &models.AutomationRule{Name: "Other", EntityType: entity, TriggerType: models.AutomationTriggerSchedule,
				Trigger: models.AutomationTrigger{Filters: &models.AutomationScheduleFilter{TicketStatus: string(models.TicketStatusOpen)}},
				Steps:   []models.AutomationStep{step}}
			got := fields(t, rule)
			assert.Equal(t, RuleErrorInvalidValue, got["trigger.filters.ticket_status"].Code, entity)
		}
	})

	t.Run("condition fields", func(t *testing.T) {
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{},
			condition("status", "eq", "resolved"), condition("priority", "in", []string{"high", "urgent"}),
			condition("owner_id", "eq", env.support.ID), condition("customer_id", "gt", 0), notify))
		for _, field := range []string{"title", "description", "resolution", "stage", "source", "amount_cents", "company", "utm_source", "email"} {
			got := fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, condition(field, "eq", "x"), notify))
			assert.Contains(t, []string{RuleErrorInvalidValue, RuleErrorNotApplicable}, got["steps[0].config.field"].Code, field)
		}
		got := fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, condition("customer_id", "eq", "seven"), notify))
		assert.Equal(t, RuleErrorInvalidValue, got["steps[0].config.value"].Code, "customer_id is a number")
		got = fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, condition("priority", "gt", 2), notify))
		assert.Equal(t, RuleErrorInvalidValue, got["steps[0].config.operator"].Code, "priority is a string")
	})

	t.Run("notify and email placeholders", func(t *testing.T) {
		ok := models.AutomationStep{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
			Title: "{{ticket.id}} {{ticket.number}} {{ticket.status}} {{ticket.priority}}",
			Body:  "{{owner.first_name}} {{owner.last_name}} {{owner.email}}"}}
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, ok, ticketEmailStep(models.AutomationRecipientOwner, 0)))
		for _, placeholder := range []string{"ticket.title", "ticket.description", "ticket.resolution", "customer.email",
			"customer.first_name", "ticket.customer_name", "lead.id", "deal.title"} {
			step := models.AutomationStep{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
				Title: "Ticket {{" + placeholder + "}}"}}
			got := fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, step))
			assert.Equal(t, RuleErrorUnknownPlaceholder, got["steps[0].config.title"].Code, placeholder)
			email := models.AutomationStep{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
				Subject: "S", Body: "Body {{" + placeholder + "}}"}}
			got = fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, email))
			assert.Equal(t, RuleErrorUnknownPlaceholder, got["steps[0].config.body"].Code, placeholder)
		}
	})

	t.Run("task placeholders", func(t *testing.T) {
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, ticketTaskStep(models.AutomationRecipientOwner, 0, 1)))
		for _, placeholder := range []string{"ticket.title", "ticket.description", "owner.email", "owner.first_name", "customer.email"} {
			step := models.AutomationStep{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
				Title: "Task", Description: "About {{" + placeholder + "}}"}}
			got := fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, step))
			assert.Equal(t, RuleErrorUnknownPlaceholder, got["steps[0].config.description"].Code, placeholder)
		}
	})

	t.Run("assign_owner names a support or admin user", func(t *testing.T) {
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, assignStep(env.support2.ID)))
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, assignStep(env.admin.ID)))
		// A sales user may receive a ticket notification by user_id...
		accepted(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, ticketNotifyStep(models.AutomationRecipientUser, env.sales.ID)))

		// ...but is never a ticket's assignee.
		rule := ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{},
			ticketNotifyStep(models.AutomationRecipientUser, env.sales.ID), assignStep(env.sales.ID))
		err := env.svc.validateRule(rule)
		var refusal *RuleValidationError
		require.True(t, errors.As(err, &refusal))
		require.Len(t, refusal.Fields, 1, "only the assign step is refused: %+v", refusal.Fields)
		assert.Equal(t, "steps[1].config.user_id", refusal.Fields[0].Field)
		assert.Equal(t, RuleErrorUnknownReference, refusal.Fields[0].Code)
		assert.True(t, IsReferenceRefusal(refusal), "answered as INVALID_REFERENCE")

		// The lead rule's assign_owner keeps its rules: a sales owner is fine.
		leadRule := &models.AutomationRule{Name: "Lead", EntityType: models.AutomationEntityLead,
			TriggerType: models.AutomationEventLeadCreated, Steps: []models.AutomationStep{assignStep(env.sales.ID)}}
		accepted(t, leadRule)

		inactive := env.createUser("inactive-support", models.RoleSupport, false)
		got := fields(t, ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, assignStep(inactive.ID)))
		assert.Equal(t, RuleErrorUnknownReference, got["steps[0].config.user_id"].Code)
	})

	t.Run("an unknown entity type names the three", func(t *testing.T) {
		rule := &models.AutomationRule{Name: "Company", EntityType: "company", TriggerType: models.AutomationEventTicketCreated,
			Steps: []models.AutomationStep{notify}}
		got := fields(t, rule)
		require.Contains(t, got, "entity_type")
		assert.Equal(t, RuleErrorInvalidValue, got["entity_type"].Code)
		assert.Equal(t, "entity_type must be lead, deal or ticket", got["entity_type"].Message)
		assert.EqualError(t, env.svc.validateRule(rule), "entity_type must be lead, deal or ticket: validation failed")
	})

	t.Run("the draft dry run applies the same refusals", func(t *testing.T) {
		rulesBefore := env.count(&models.AutomationRule{})
		for _, rule := range []*models.AutomationRule{
			ticketRule(models.AutomationEventTicketStatusChanged, models.AutomationTrigger{To: "won"}, notify),
			ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, models.AutomationStep{
				StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: "{{ticket.title}}"}}),
			ticketRule(models.AutomationEventTicketUpdated, models.AutomationTrigger{}, assignStep(env.sales.ID)),
		} {
			want := fields(t, rule)
			_, err := env.automationEnv.svc.DryRunDraft(rule, models.AutomationEntityTicket, ticket.ID)
			var refusal *RuleValidationError
			require.True(t, errors.As(err, &refusal), "the draft is refused: %v", err)
			for _, field := range refusal.Fields {
				assert.Equal(t, want[field.Field].Code, field.Code, field.Field)
			}
			assert.Len(t, refusal.Fields, len(want))
		}
		assert.Equal(t, rulesBefore, env.count(&models.AutomationRule{}))
	})
}
