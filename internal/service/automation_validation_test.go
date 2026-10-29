package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G2: a refused rule body lists every refused field — path, code, message —
// in the order the checks run, while Error() and errors.Is stay those of the
// first refusal as the single-error validator reported it.

type fieldCode struct{ field, code string }

func fieldCodes(t *testing.T, err error) []fieldCode {
	t.Helper()
	var ruleErr *RuleValidationError
	require.Truef(t, errors.As(err, &ruleErr), "a *RuleValidationError, got %v", err)
	out := make([]fieldCode, 0, len(ruleErr.Fields))
	for _, field := range ruleErr.Fields {
		assert.NotEmptyf(t, field.Message, "%s carries a message", field.Field)
		assert.NotContainsf(t, field.Message, "validation failed", "%s message carries no sentinel", field.Field)
		out = append(out, fieldCode{field.Field, field.Code})
	}
	return out
}

func rawJSON(value interface{}) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func TestAutomationRuleValidationCollectsAllFields(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	rule := &models.AutomationRule{
		Name: "Broken", EntityType: models.AutomationEntityDeal, TriggerType: models.AutomationEventDealStageChanged,
		Trigger: models.AutomationTrigger{From: "bogus", To: "nowhere"},
		Steps: []models.AutomationStep{
			{StepType: models.AutomationStepWait},
			// One condition, three independent refusals: a non-numeric value,
			// no currency on amount_cents, and a backward jump.
			{StepType: models.AutomationStepCondition, Config: models.AutomationStepConfig{
				Field: "amount_cents", Operator: "gt", Value: rawJSON("abc"), Else: models.AutomationElseJump, ElsePosition: 1,
			}},
			{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
				Priority: "urgent", DueInDays: 400, Recipient: "boss",
			}},
			// Body and recipient refusals come before the placeholder refusal.
			{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
				Subject: "Hello {{lead.first_name}}", Recipient: "nobody",
			}},
		},
	}
	err := env.svc.CreateRule(rule, env.admin.ID)
	assert.Equal(t, []fieldCode{
		{"trigger.from", RuleErrorInvalidValue},
		{"trigger.to", RuleErrorInvalidValue},
		{"steps[0].config.duration_minutes", RuleErrorOutOfRange},
		{"steps[1].config.value", RuleErrorInvalidValue},
		{"steps[1].config.currency", RuleErrorRequired},
		{"steps[1].config.else_position", RuleErrorOutOfRange},
		{"steps[2].config.title", RuleErrorRequired},
		{"steps[2].config.priority", RuleErrorInvalidValue},
		{"steps[2].config.due_in_days", RuleErrorOutOfRange},
		{"steps[2].config.recipient", RuleErrorInvalidValue},
		{"steps[3].config.body", RuleErrorRequired},
		{"steps[3].config.recipient", RuleErrorInvalidValue},
		{"steps[3].config.subject", RuleErrorUnknownPlaceholder},
	}, fieldCodes(t, err))
	assert.Equal(t, `trigger from "bogus" is not a valid deal value: validation failed`, err.Error(),
		"from is checked before to, so the first message is stable")
	assert.True(t, errors.Is(err, apperrors.ErrValidation))
	assert.False(t, errors.Is(err, apperrors.ErrAssigneeNotFound))
	assert.Zero(t, env.count(&models.AutomationRule{}), "nothing written")
	assert.Zero(t, env.count(&models.AutomationStep{}))
}

func TestAutomationRuleValidationLegacyMessage(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	inactive := env.createUser("inactive", models.RoleSales, false)
	customer := env.createUser("customer", models.RoleCustomer, true)
	notify := func(title string) models.AutomationStep {
		return models.AutomationStep{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: title}}
	}
	many := make([]models.AutomationStep, models.AutomationMaxSteps+1)
	for i := range many {
		many[i] = notify("Hi")
	}
	for _, tc := range []struct {
		name      string
		rule      models.AutomationRule
		message   string
		reference bool
		fields    []fieldCode
	}{
		{"empty name", models.AutomationRule{Name: "  ", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{notify("Hi")}},
			"name is required: validation failed", false, []fieldCode{{"name", RuleErrorRequired}}},
		{"too many steps", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: many},
			"a rule has at most 20 steps: validation failed", false, []fieldCode{{"steps", RuleErrorTooMany}}},
		{"backward jump", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{
			conditionStep("status", "eq", "new", models.AutomationElseJump, 1), notify("Hi")}},
			"step 1: else_position must be after the step and at most 2: validation failed", false, []fieldCode{{"steps[0].config.else_position", RuleErrorOutOfRange}}},
		{"unknown placeholder", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{notify("Hi {{deal.title}}")}},
			`step 1: title uses an unknown placeholder "deal.title": validation failed`, false, []fieldCode{{"steps[0].config.title", RuleErrorUnknownPlaceholder}}},
		{"unknown user", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{assignStep(999999)}},
			"unknown user_id 999999: assignee not found", true, []fieldCode{{"steps[0].config.user_id", RuleErrorUnknownReference}}},
		{"inactive user", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientUser, inactive.ID)}},
			"user " + uintText(inactive.ID) + " is inactive: assignee not found", true, []fieldCode{{"steps[0].config.user_id", RuleErrorUnknownReference}}},
		{"customer user", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{assignStep(customer.ID)}},
			"user " + uintText(customer.ID) + " is not a staff user: assignee not found", true, []fieldCode{{"steps[0].config.user_id", RuleErrorUnknownReference}}},
		// A shape refusal and a reference refusal together: the shape refusal
		// comes first, as it did when the lookups ran only after every shape
		// check had passed, and decides the message and the classification.
		{"shape and reference", models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{
			assignStep(inactive.ID), notify("")}},
			"step 2: title is required: validation failed", false,
			[]fieldCode{{"steps[1].config.title", RuleErrorRequired}, {"steps[0].config.user_id", RuleErrorUnknownReference}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := tc.rule
			err := env.svc.CreateRule(&rule, env.admin.ID)
			require.Error(t, err)
			assert.Equal(t, tc.message, err.Error())
			assert.Equal(t, tc.fields, fieldCodes(t, err))
			assert.Equal(t, tc.reference, errors.Is(err, apperrors.ErrAssigneeNotFound))
			assert.Equal(t, !tc.reference, errors.Is(err, apperrors.ErrValidation))
			var ruleErr *RuleValidationError
			require.True(t, errors.As(err, &ruleErr))
			assert.Equal(t, tc.reference, IsReferenceRefusal(ruleErr))
		})
	}
}

// countingUserRepo counts the staff-user lookups of the validator.
type countingUserRepo struct {
	repository.UserRepository
	lookups atomic.Int64
}

func (r *countingUserRepo) GetByID(id uint) (*models.User, error) {
	r.lookups.Add(1)
	return r.UserRepository.GetByID(id)
}

func TestAutomationRuleValidationReferencePaths(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	inactive := env.createUser("inactive", models.RoleSales, false)
	users := &countingUserRepo{UserRepository: env.users}
	env.svc.users = users

	t.Run("a refused user is reported on every step naming it, after one lookup", func(t *testing.T) {
		rule := &models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: []models.AutomationStep{
			notifyStep(models.AutomationRecipientUser, inactive.ID),
			notifyStep(models.AutomationRecipientOwner, 0),
			assignStep(inactive.ID),
			emailStep(models.AutomationRecipientUser, 999999),
		}}
		users.lookups.Store(0)
		err := env.svc.CreateRule(rule, env.admin.ID)
		assert.Equal(t, []fieldCode{
			{"steps[0].config.user_id", RuleErrorUnknownReference},
			{"steps[2].config.user_id", RuleErrorUnknownReference},
			{"steps[3].config.user_id", RuleErrorUnknownReference},
		}, fieldCodes(t, err))
		assert.Equal(t, "user "+uintText(inactive.ID)+" is inactive: assignee not found", err.Error(),
			"users are looked up in the order the steps first name them")
		assert.EqualValues(t, 2, users.lookups.Load(), "one lookup per distinct user")
	})

	t.Run("twenty steps naming one user cost one lookup", func(t *testing.T) {
		steps := make([]models.AutomationStep, models.AutomationMaxSteps)
		for i := range steps {
			steps[i] = notifyStep(models.AutomationRecipientUser, env.other.ID)
		}
		users.lookups.Store(0)
		require.NoError(t, env.svc.CreateRule(&models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created", Steps: steps}, env.admin.ID))
		assert.EqualValues(t, 1, users.lookups.Load())
	})

	t.Run("a user_id the step type ignores is still accepted and not looked up", func(t *testing.T) {
		wait := waitStep(30)
		wait.Config.UserID = 999999
		users.lookups.Store(0)
		require.NoError(t, env.svc.CreateRule(&models.AutomationRule{Name: "R", EntityType: "lead", TriggerType: "lead_created",
			Steps: []models.AutomationStep{wait, notifyStep(models.AutomationRecipientOwner, 0)}}, env.admin.ID))
		assert.Zero(t, users.lookups.Load())
	})
}

func TestAutomationRuleValidationStopsOnEntityType(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	err := env.svc.CreateRule(&models.AutomationRule{
		Name: strings.Repeat("n", models.AutomationRuleNameMaxLength+1), EntityType: "contact", TriggerType: "contact_created",
		Trigger: models.AutomationTrigger{HourUTC: 30},
	}, env.admin.ID)
	assert.Equal(t, []fieldCode{{"name", RuleErrorTooLong}, {"entity_type", RuleErrorInvalidValue}}, fieldCodes(t, err),
		"nothing after an unknown entity type is checked")
	assert.Equal(t, "name is longer than 200 characters: validation failed", err.Error())

	err = env.svc.CreateRule(&models.AutomationRule{
		Name: "R", EntityType: "lead", TriggerType: "lead_created", Trigger: models.AutomationTrigger{HourUTC: 4, StaleDays: 2},
	}, env.admin.ID)
	assert.Equal(t, []fieldCode{
		{"trigger.hour_utc", RuleErrorNotApplicable}, {"trigger.stale_days", RuleErrorNotApplicable}, {"steps", RuleErrorRequired},
	}, fieldCodes(t, err))
}

func uintText(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
