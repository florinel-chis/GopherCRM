package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// Codes of a RuleFieldError.
const (
	RuleErrorRequired           = "required"
	RuleErrorTooLong            = "too_long"
	RuleErrorLineBreak          = "line_break"
	RuleErrorInvalidValue       = "invalid_value"
	RuleErrorOutOfRange         = "out_of_range"
	RuleErrorNotApplicable      = "not_applicable"
	RuleErrorUnknownPlaceholder = "unknown_placeholder"
	RuleErrorTooMany            = "too_many"
	RuleErrorUnknownReference   = "unknown_reference"
)

// RuleFieldError is one refused field of an automation rule body. Field is a
// path into the request body: name, description, entity_type, trigger_type,
// trigger.from, trigger.to, trigger.hour_utc, trigger.stale_days,
// trigger.filters, trigger.filters.status, trigger.filters.stage, steps,
// steps[i].step_type or steps[i].config.<name>, where i is the zero-based
// index of the step in the request's steps array.
//
// @Description One refused field of an automation rule body.
type RuleFieldError struct {
	// Path of the refused field in the request body: name, description, entity_type, trigger_type, trigger.from, trigger.to, trigger.hour_utc, trigger.stale_days, trigger.filters, trigger.filters.status, trigger.filters.stage, steps, steps[i].step_type or steps[i].config.<name>, with i the zero-based index in the steps array.
	Field string `json:"field" example:"steps[0].config.title"`
	// Why it was refused: required (missing or blank), too_long, line_break (a title or subject with a line break), invalid_value (not an allowed value), out_of_range (a number outside its bounds, or waits adding up to more than 90 days), not_applicable (set where it does not apply), unknown_placeholder, too_many (more than 20 steps) or unknown_reference (an unknown, deactivated or customer user).
	Code string `json:"code" enums:"required,too_long,line_break,invalid_value,out_of_range,not_applicable,unknown_placeholder,too_many,unknown_reference" example:"required"`
	// Human-readable explanation; it may quote the refused value.
	Message string `json:"message" example:"title is required"`
}

// RuleValidationError carries every refusal of one rule body, in the order
// the checks run. Error() and Unwrap() are those of the first refusal as the
// single-error validator reported it before field errors existed, so the
// message and the errors.Is classification (apperrors.ErrValidation for a
// shape refusal, apperrors.ErrAssigneeNotFound for an unknown, inactive or
// non-staff user) are unchanged for a client that reads only the message.
type RuleValidationError struct {
	Fields []RuleFieldError
	legacy []error
}

func (e *RuleValidationError) Error() string { return e.legacy[0].Error() }

func (e *RuleValidationError) Unwrap() error { return e.legacy[0] }

// LogMessage names the refused paths only: the messages can quote rule text.
func (e *RuleValidationError) LogMessage() string {
	paths := make([]string, 0, len(e.Fields))
	for _, field := range e.Fields {
		paths = append(paths, field.Field)
	}
	return fmt.Sprintf("automation rule rejected (%d fields: %s)", len(e.Fields), strings.Join(paths, ", "))
}

// ruleChecker collects the refusals of one rule. A field records only its
// first refusal, which mirrors the order the single-error validator checked
// in, so the first entry is the refusal it would have returned.
type ruleChecker struct {
	err    RuleValidationError
	failed map[string]bool
}

func newRuleChecker() *ruleChecker {
	return &ruleChecker{failed: map[string]bool{}}
}

func (c *ruleChecker) add(field, code, message string, legacy error) {
	if c.failed[field] {
		return
	}
	c.failed[field] = true
	c.err.Fields = append(c.err.Fields, RuleFieldError{Field: field, Code: code, Message: message})
	c.err.legacy = append(c.err.legacy, legacy)
}

// invalid records a shape refusal. position > 0 prefixes the legacy text
// with the 1-based step number, as the single-error validator did.
func (c *ruleChecker) invalid(field, code string, position int, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	text := message
	if position > 0 {
		text = fmt.Sprintf("step %d: %s", position, message)
	}
	c.add(field, code, message, fmt.Errorf("%s: %w", text, apperrors.ErrValidation))
}

func (c *ruleChecker) result() error {
	if len(c.err.Fields) == 0 {
		return nil
	}
	return &c.err
}

// automationTriggerTypes lists the trigger types each entity type accepts.
var automationTriggerTypes = map[string]map[string]bool{
	models.AutomationEntityLead: {
		models.AutomationEventLeadCreated: true, models.AutomationEventLeadUpdated: true,
		models.AutomationEventLeadStatusChanged: true, models.AutomationTriggerSchedule: true,
	},
	models.AutomationEntityDeal: {
		models.AutomationEventDealCreated: true, models.AutomationEventDealUpdated: true,
		models.AutomationEventDealStageChanged: true, models.AutomationTriggerSchedule: true,
	},
}

// validateRule normalises and checks a rule and its steps. It writes
// nothing: every refusal here precedes any transaction, so an invalid rule
// leaves no rule or step row. A refusal is a *RuleValidationError listing
// every refused field (see its doc for the message and the classification);
// any other error is a failed user lookup.
//
// Checks run in a fixed order. An unknown entity_type stops there; an
// unknown trigger_type skips the trigger checks; no steps or more than 20
// skips the step checks; within a step every field is checked independently.
// The staff-user lookups run last, one per distinct user, for the steps that
// reference a user (assign_owner, or a recipient "user"), and a refused user
// is reported on every step naming it.
func (s *automationService) validateRule(rule *models.AutomationRule) error {
	c := newRuleChecker()
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		c.invalid("name", RuleErrorRequired, 0, "name is required")
	} else if utf8.RuneCountInString(rule.Name) > models.AutomationRuleNameMaxLength {
		c.invalid("name", RuleErrorTooLong, 0, "name is longer than %d characters", models.AutomationRuleNameMaxLength)
	}
	if len(rule.Description) > models.AutomationRuleDescMaxBytes {
		c.invalid("description", RuleErrorTooLong, 0, "description is longer than %d bytes", models.AutomationRuleDescMaxBytes)
	}
	triggers, ok := automationTriggerTypes[rule.EntityType]
	if !ok {
		c.invalid("entity_type", RuleErrorInvalidValue, 0, "entity_type must be lead or deal")
		return c.result()
	}
	if !triggers[rule.TriggerType] {
		c.invalid("trigger_type", RuleErrorInvalidValue, 0, "trigger_type %q is not valid for %s rules", rule.TriggerType, rule.EntityType)
	} else {
		validateTrigger(c, rule.EntityType, rule.TriggerType, &rule.Trigger)
	}

	switch {
	case len(rule.Steps) == 0:
		c.invalid("steps", RuleErrorRequired, 0, "at least one step is required")
		return c.result()
	case len(rule.Steps) > models.AutomationMaxSteps:
		c.invalid("steps", RuleErrorTooMany, 0, "a rule has at most %d steps", models.AutomationMaxSteps)
		return c.result()
	}
	var totalWait int64
	for i := range rule.Steps {
		step := &rule.Steps[i]
		step.Position = i + 1
		validateStep(c, rule.EntityType, step, i, len(rule.Steps))
		if step.StepType == models.AutomationStepWait && step.Config.DurationMinutes > 0 {
			totalWait += step.Config.DurationMinutes
		}
	}
	if totalWait > models.AutomationMaxWaitMinutes {
		c.invalid("steps", RuleErrorOutOfRange, 0, "the waits of a rule add up to more than 90 days")
	}

	// Users in the order the steps first name them, each looked up once.
	var users []uint
	stepsOf := map[uint][]int{}
	for i, step := range rule.Steps {
		if step.StepType != models.AutomationStepAssignOwner && step.Config.Recipient != models.AutomationRecipientUser {
			continue
		}
		id := step.Config.UserID
		if id == 0 {
			continue // already refused as a missing user_id
		}
		if _, seen := stepsOf[id]; !seen {
			users = append(users, id)
		}
		stepsOf[id] = append(stepsOf[id], i)
	}
	for _, id := range users {
		legacy, message, err := s.staffUserProblem(id)
		if err != nil {
			return err
		}
		if legacy == nil {
			continue
		}
		for _, index := range stepsOf[id] {
			c.add(fmt.Sprintf("steps[%d].config.user_id", index), RuleErrorUnknownReference, message, legacy)
		}
	}
	return c.result()
}

// staffUserProblem requires the id to name a live, active staff account. It
// returns the refusal (unwrapping to apperrors.ErrAssigneeNotFound) and its
// field message, or a lookup failure as err.
func (s *automationService) staffUserProblem(userID uint) (legacy error, message string, err error) {
	user, err := s.users.GetByID(userID)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("unknown user_id %d: %w", userID, apperrors.ErrAssigneeNotFound), "the user does not exist", nil
		}
		return nil, "", err
	}
	if !user.IsActive {
		return fmt.Errorf("user %d is inactive: %w", userID, apperrors.ErrAssigneeNotFound), "the user is deactivated", nil
	}
	if user.Role == models.RoleCustomer {
		return fmt.Errorf("user %d is not a staff user: %w", userID, apperrors.ErrAssigneeNotFound), "the user is not a staff user", nil
	}
	return nil, "", nil
}

func validateTrigger(c *ruleChecker, entityType, triggerType string, trigger *models.AutomationTrigger) {
	changed := triggerType == models.AutomationEventLeadStatusChanged || triggerType == models.AutomationEventDealStageChanged
	values := []struct{ name, value string }{{"from", trigger.From}, {"to", trigger.To}}
	for _, v := range values {
		if v.value == "" {
			continue
		}
		if !changed {
			c.invalid("trigger."+v.name, RuleErrorNotApplicable, 0, "from and to only apply to a status or stage change trigger")
		} else if !validAutomationValue(entityType, v.value) {
			c.invalid("trigger."+v.name, RuleErrorInvalidValue, 0, "trigger %s %q is not a valid %s value", v.name, v.value, entityType)
		}
	}
	if triggerType != models.AutomationTriggerSchedule {
		const notSchedule = "hour_utc, filters and stale_days only apply to a schedule trigger"
		if trigger.HourUTC != 0 {
			c.invalid("trigger.hour_utc", RuleErrorNotApplicable, 0, notSchedule)
		}
		if trigger.Filters != nil {
			c.invalid("trigger.filters", RuleErrorNotApplicable, 0, notSchedule)
		}
		if trigger.StaleDays != 0 {
			c.invalid("trigger.stale_days", RuleErrorNotApplicable, 0, notSchedule)
		}
		return
	}
	if trigger.HourUTC < 0 || trigger.HourUTC > 23 {
		c.invalid("trigger.hour_utc", RuleErrorOutOfRange, 0, "hour_utc must be between 0 and 23")
	}
	if trigger.StaleDays < 0 {
		c.invalid("trigger.stale_days", RuleErrorOutOfRange, 0, "stale_days must not be negative")
	}
	if trigger.Filters != nil {
		if status := trigger.Filters.Status; status != "" {
			if entityType != models.AutomationEntityLead || !isValidLeadStatus(models.LeadStatus(status)) {
				c.invalid("trigger.filters.status", RuleErrorInvalidValue, 0, "filters.status %q is not a lead status", status)
			}
		}
		if stage := trigger.Filters.Stage; stage != "" {
			if entityType != models.AutomationEntityDeal || !models.DealStage(stage).IsValid() {
				c.invalid("trigger.filters.stage", RuleErrorInvalidValue, 0, "filters.stage %q is not a deal stage", stage)
			}
		}
	}
}

// validAutomationValue reports whether value is a lead status (lead rules)
// or a deal stage (deal rules).
func validAutomationValue(entityType, value string) bool {
	if entityType == models.AutomationEntityLead {
		return isValidLeadStatus(models.LeadStatus(value))
	}
	return models.DealStage(value).IsValid()
}

// validateStep checks one step, index being its zero-based place in the
// request. Defaults are filled in place: an empty recipient is "owner", an
// empty else is "stop", and assign_owner always names its user.
func validateStep(c *ruleChecker, entityType string, step *models.AutomationStep, index, stepCount int) {
	cfg := &step.Config
	pos := index + 1
	path := func(name string) string { return fmt.Sprintf("steps[%d].config.%s", index, name) }
	invalid := func(name, code, format string, args ...interface{}) {
		c.invalid(path(name), code, pos, format, args...)
	}
	required := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			invalid(name, RuleErrorRequired, "%s is required", name)
		}
	}
	textLimit := func(name, value string) {
		if utf8.RuneCountInString(value) > models.AutomationStepTextMaxLength {
			invalid(name, RuleErrorTooLong, "%s is longer than %d characters", name, models.AutomationStepTextMaxLength)
		} else if strings.ContainsAny(value, "\r\n") {
			invalid(name, RuleErrorLineBreak, "%s must not contain a line break", name)
		}
	}
	bodyLimit := func(name, value string) {
		if len(value) > models.AutomationStepBodyMaxBytes {
			invalid(name, RuleErrorTooLong, "%s is longer than %d bytes", name, models.AutomationStepBodyMaxBytes)
		}
	}
	placeholders := func(name, value string) {
		if unknown := unknownPlaceholders(entityType, value); len(unknown) > 0 {
			invalid(name, RuleErrorUnknownPlaceholder, "%s uses an unknown placeholder %q", name, unknown[0])
		}
	}
	// A task outlives the erasure of the lead it links to, so its text is
	// checked against the narrower task allowlist (no name, address, phone or
	// company); a personal placeholder is an unknown placeholder here.
	taskPlaceholders := func(name, value string) {
		if unknown := unknownTaskPlaceholders(entityType, value); len(unknown) > 0 {
			invalid(name, RuleErrorUnknownPlaceholder, "%s uses a placeholder not allowed in a task %q", name, unknown[0])
		}
	}
	recipient := func() {
		switch cfg.Recipient {
		case "":
			cfg.Recipient = models.AutomationRecipientOwner
		case models.AutomationRecipientOwner:
		case models.AutomationRecipientUser:
			if cfg.UserID == 0 {
				invalid("user_id", RuleErrorRequired, "user_id is required for recipient \"user\"")
			}
		default:
			invalid("recipient", RuleErrorInvalidValue, "recipient must be owner or user")
		}
		if cfg.Recipient == models.AutomationRecipientOwner && cfg.UserID != 0 {
			invalid("user_id", RuleErrorNotApplicable, "user_id only applies to recipient \"user\"")
		}
	}

	switch step.StepType {
	case models.AutomationStepWait:
		if cfg.DurationMinutes <= 0 {
			invalid("duration_minutes", RuleErrorOutOfRange, "duration_minutes must be positive")
		} else if cfg.DurationMinutes > models.AutomationMaxWaitMinutes {
			invalid("duration_minutes", RuleErrorOutOfRange, "a wait is at most 90 days")
		}
	case models.AutomationStepCondition:
		for _, problem := range validateCondition(entityType, *cfg) {
			invalid(problem.field, problem.code, "%s", problem.message)
		}
		switch cfg.Else {
		case "":
			cfg.Else = models.AutomationElseStop
			fallthrough
		case models.AutomationElseStop:
			if cfg.ElsePosition != 0 {
				invalid("else_position", RuleErrorNotApplicable, "else_position only applies to else \"jump\"")
			}
		case models.AutomationElseJump:
			if cfg.ElsePosition <= pos || cfg.ElsePosition > stepCount {
				invalid("else_position", RuleErrorOutOfRange, "else_position must be after the step and at most %d", stepCount)
			}
		default:
			invalid("else", RuleErrorInvalidValue, "else must be stop or jump")
		}
	case models.AutomationStepCreateTask:
		required("title", cfg.Title)
		textLimit("title", cfg.Title)
		bodyLimit("description", cfg.Description)
		switch models.TaskPriority(cfg.Priority) {
		case "", models.TaskPriorityLow, models.TaskPriorityMedium, models.TaskPriorityHigh:
		default:
			invalid("priority", RuleErrorInvalidValue, "priority must be low, medium or high")
		}
		if cfg.DueInDays < 0 || cfg.DueInDays > 365 {
			invalid("due_in_days", RuleErrorOutOfRange, "due_in_days must be between 0 and 365")
		}
		recipient()
		taskPlaceholders("title", cfg.Title)
		taskPlaceholders("description", cfg.Description)
	case models.AutomationStepAssignOwner:
		if cfg.UserID == 0 {
			invalid("user_id", RuleErrorRequired, "user_id is required")
		}
		cfg.Recipient = models.AutomationRecipientUser
	case models.AutomationStepSendEmail:
		required("subject", cfg.Subject)
		textLimit("subject", cfg.Subject)
		required("body", cfg.Body)
		bodyLimit("body", cfg.Body)
		recipient()
		placeholders("subject", cfg.Subject)
		placeholders("body", cfg.Body)
	case models.AutomationStepNotify:
		required("title", cfg.Title)
		textLimit("title", cfg.Title)
		bodyLimit("body", cfg.Body)
		recipient()
		placeholders("title", cfg.Title)
		placeholders("body", cfg.Body)
	default:
		c.invalid(fmt.Sprintf("steps[%d].step_type", index), RuleErrorInvalidValue, pos, "unknown step type %q", step.StepType)
	}
}

// IsReferenceRefusal reports whether the refusal is answered as
// INVALID_REFERENCE: its first entry is an unknown, inactive or non-staff user.
func IsReferenceRefusal(err *RuleValidationError) bool {
	return errors.Is(err.legacy[0], apperrors.ErrAssigneeNotFound)
}
