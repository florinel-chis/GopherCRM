package service

import (
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// DryRun simulates the rule against the chosen record and reports what each
// step would do: the outcome, the simulated time and the ids it would touch.
// Waits advance a simulated clock without pausing, and a simulated assign
// feeds the conditions after it. Nothing is written, nothing is sent and no
// run, notification, task or event is created: the record and the users are
// read with plain reads and the report is built in memory.
func (e *AutomationEngine) DryRun(rule *models.AutomationRule, entityType string, entityID uint) (*models.AutomationDryRunReport, error) {
	if rule.EntityType != entityType {
		if rule.ID == 0 {
			return nil, fmt.Errorf("the rule applies to %s records, not %s: %w", rule.EntityType, entityType, apperrors.ErrValidation)
		}
		return nil, fmt.Errorf("rule %d applies to %s records, not %s: %w", rule.ID, rule.EntityType, entityType, apperrors.ErrValidation)
	}
	rec, err := e.loadRecord(entityType, entityID)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%s %d not found: %w", entityType, entityID, apperrors.ErrNotFound)
		}
		return nil, err
	}

	clock := e.now().UTC()
	report := &models.AutomationDryRunReport{
		RuleID:         rule.ID,
		RuleVersion:    rule.Version,
		EntityType:     entityType,
		EntityID:       entityID,
		SimulatedStart: clock,
		Steps:          []models.AutomationDryRunStep{},
	}
	stepCount := len(rule.Steps)
	pos := 1
	for guard := 0; pos >= 1 && pos <= stepCount && guard <= models.AutomationMaxSteps; guard++ {
		step := rule.Steps[pos-1]
		cfg := step.Config
		entry := models.AutomationDryRunStep{
			Position:    pos,
			StepType:    step.StepType,
			Outcome:     models.AutomationOutcomeSucceeded,
			SimulatedAt: clock,
			Details:     map[string]interface{}{},
		}
		next := pos + 1

		switch step.StepType {
		case models.AutomationStepWait:
			clock = clock.Add(time.Duration(cfg.DurationMinutes) * time.Minute)
			entry.Outcome = models.AutomationOutcomeWaiting
			entry.Details["resume_at"] = clock.Format(time.RFC3339)
		case models.AutomationStepCondition:
			ok, err := evaluateCondition(cfg, rec)
			if err != nil {
				return nil, err
			}
			entry.Details["result"] = ok
			switch {
			case ok:
				entry.Details["branch"] = "next"
			case cfg.Else == models.AutomationElseJump:
				entry.Details["branch"] = models.AutomationElseJump
				entry.Details["next_position"] = cfg.ElsePosition
				next = cfg.ElsePosition
			default:
				entry.Details["branch"] = models.AutomationElseStop
				next = stepCount + 1
			}
		case models.AutomationStepCreateTask:
			assignee, available, err := resolveRecipient(e.users, cfg, rec)
			if err != nil {
				return nil, err
			}
			if !available {
				entry.Outcome, entry.ErrorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
				break
			}
			task := buildAutomationTask(cfg, rec, assignee.ID, clock)
			entry.Details["assignee_id"] = assignee.ID
			entry.Details["due_date"] = task.DueDate.Format(time.RFC3339)
			if task.LeadID != nil {
				entry.Details["lead_id"] = *task.LeadID
			}
			if task.CustomerID != nil {
				entry.Details["customer_id"] = *task.CustomerID
			}
		case models.AutomationStepAssignOwner:
			assignCfg := cfg
			assignCfg.Recipient = models.AutomationRecipientUser
			target, available, err := resolveRecipient(e.users, assignCfg, rec)
			if err != nil {
				return nil, err
			}
			if !available {
				entry.Outcome, entry.ErrorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
				break
			}
			entry.Details["from_owner_id"] = rec.ownerID
			entry.Details["to_owner_id"] = target.ID
			// The simulated assign is what a later condition on owner_id and a
			// later "owner" recipient see.
			rec.ownerID = target.ID
		case models.AutomationStepSendEmail, models.AutomationStepNotify:
			recipient, available, err := resolveRecipient(e.users, cfg, rec)
			if err != nil {
				return nil, err
			}
			if !available {
				entry.Outcome, entry.ErrorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
				break
			}
			entry.Details["recipient_id"] = recipient.ID
		default:
			return nil, fmt.Errorf("unknown automation step type %q: %w", step.StepType, apperrors.ErrValidation)
		}

		report.Steps = append(report.Steps, entry)
		pos = next
	}
	report.SimulatedEnd = clock
	return report, nil
}
