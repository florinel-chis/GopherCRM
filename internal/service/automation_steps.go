package service

import (
	"context"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// stepResult is what one executed step reports back to the run loop.
type stepResult struct {
	terminal     bool
	nextPosition int
}

// lockRecord re-reads the run's record with the transaction-bound locking
// read. A record that is gone (deleted or erased) is nil.
func (e *AutomationEngine) lockRecord(r repository.AutomationRepository, run *models.AutomationRun) (*automationRecord, error) {
	switch run.EntityType {
	case models.AutomationEntityLead:
		lead, err := r.GetLeadForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromLead(lead), nil
	case models.AutomationEntityDeal:
		deal, err := r.GetDealForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromDeal(deal), nil
	case models.AutomationEntityTicket:
		// The ticket row is locked before the run advance: the order every
		// ticket writer (update, bulk status, delete) uses, so they serialise
		// on the ticket row instead of deadlocking (#242).
		ticket, err := r.GetTicketForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromTicket(ticket), nil
	}
	return nil, fmt.Errorf("unknown automation entity type %q", run.EntityType)
}

// loadRecord is the plain read of the dry run and of the API.
func (e *AutomationEngine) loadRecord(entityType string, entityID uint) (*automationRecord, error) {
	switch entityType {
	case models.AutomationEntityLead:
		lead, err := e.repo.GetLead(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromLead(lead), nil
	case models.AutomationEntityDeal:
		deal, err := e.repo.GetDeal(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromDeal(deal), nil
	case models.AutomationEntityTicket:
		ticket, err := e.repo.GetTicket(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromTicket(ticket), nil
	}
	return nil, fmt.Errorf("unknown automation entity type %q: %w", entityType, apperrors.ErrValidation)
}

// resolveAssignTarget is resolveRecipient for an assign_owner step: the
// configured user, live, active and staff, and on a ticket run (#242) also a
// support or admin user, as the ticket API requires of an assignee.
func resolveAssignTarget(users repository.UserRepository, cfg models.AutomationStepConfig, rec *automationRecord) (*models.User, bool, error) {
	assignCfg := cfg
	assignCfg.Recipient = models.AutomationRecipientUser
	target, available, err := resolveRecipient(users, assignCfg, rec)
	if err != nil || !available {
		return nil, false, err
	}
	if rec.entityType == models.AutomationEntityTicket && !isTicketAssignee(target) {
		return nil, false, nil
	}
	return target, true, nil
}

// resolveRecipient returns the staff user a step addresses: the record's
// current owner or the configured user. A user that does not exist, is
// erased or is inactive is unavailable.
func resolveRecipient(users repository.UserRepository, cfg models.AutomationStepConfig, rec *automationRecord) (*models.User, bool, error) {
	userID := cfg.UserID
	if cfg.Recipient != models.AutomationRecipientUser {
		userID = rec.ownerID
	}
	if userID == 0 {
		return nil, false, nil
	}
	user, err := users.GetByID(userID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !user.IsActive || user.Role == models.RoleCustomer {
		return nil, false, nil
	}
	return user, true, nil
}

// loadOwner loads the record's owner for placeholder rendering; a missing
// owner renders blank.
func loadOwner(users repository.UserRepository, rec *automationRecord) *models.User {
	if rec.ownerID == 0 {
		return nil
	}
	owner, err := users.GetByID(rec.ownerID)
	if err != nil {
		return nil
	}
	return owner
}

// stepPlan is the decided outcome of one step inside its transaction: the
// advance to write, the effect to apply after it, and the log row.
type stepPlan struct {
	advance   repository.AutomationRunAdvance
	effect    func() error
	outcome   string
	errorCode string
	details   map[string]interface{}
}

// planStep decides what a single-transaction step does, against the locked
// record. The effect closure runs after the conditional advance.
func (e *AutomationEngine) planStep(ctx context.Context, tx *gorm.DB, r repository.AutomationRepository, run *models.AutomationRun, rule *models.AutomationRule, step models.AutomationStep, rec *automationRecord, now time.Time) (*stepPlan, error) {
	pos := step.Position
	stepCount := len(rule.Steps)
	cfg := step.Config
	users := e.users.WithTx(tx)
	plan := &stepPlan{outcome: models.AutomationOutcomeSucceeded, details: map[string]interface{}{}}

	switch step.StepType {
	case models.AutomationStepWait:
		resumeAt := now.Add(time.Duration(cfg.DurationMinutes) * time.Minute)
		plan.advance = repository.AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: pos + 1, ResumeAt: &resumeAt}
		plan.outcome = models.AutomationOutcomeWaiting
		plan.details["resume_at"] = resumeAt.Format(time.RFC3339)
	case models.AutomationStepCondition:
		if rec.entityType == models.AutomationEntityLead && isAttributionField(cfg.Field) {
			// Read here, inside the step transaction and after the lead's
			// locking read, so the condition sees the submission as it is now.
			attribution, err := e.triggeringAttribution(r, run)
			if err != nil {
				return nil, err
			}
			rec.attribution = attribution
		}
		ok, err := evaluateCondition(cfg, rec)
		if err != nil {
			return nil, err
		}
		plan.details["result"] = ok
		switch {
		case ok:
			plan.details["branch"] = "next"
			plan.advance = nextAdvance(stepCount, pos+1, now)
		case cfg.Else == models.AutomationElseJump:
			plan.details["branch"] = models.AutomationElseJump
			plan.details["next_position"] = cfg.ElsePosition
			plan.advance = nextAdvance(stepCount, cfg.ElsePosition, now)
		default:
			plan.details["branch"] = models.AutomationElseStop
			plan.advance = repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: pos + 1, FinishedAt: &now}
		}
	case models.AutomationStepCreateTask:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		assignee, available, err := resolveRecipient(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["assignee_id"] = assignee.ID
		task := buildAutomationTask(cfg, rec, assignee.ID, now)
		plan.details["due_date"] = task.DueDate.Format(time.RFC3339)
		if task.LeadID != nil {
			plan.details["lead_id"] = *task.LeadID
		}
		if task.CustomerID != nil {
			plan.details["customer_id"] = *task.CustomerID
		}
		if task.DealID != nil {
			plan.details["deal_id"] = *task.DealID
		}
		plan.effect = func() error {
			if err := e.tasks.WithTx(tx).Create(task); err != nil {
				return err
			}
			plan.details["task_id"] = task.ID
			// task.created (#516) in the step transaction; a dry run runs no
			// effect and writes neither the task nor the row.
			return e.webhooks.Bind(tx).Created(ctx, webhook.Task, task.ID)
		}
	case models.AutomationStepAssignOwner:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		target, available, err := resolveAssignTarget(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["from_owner_id"] = rec.ownerID
		plan.details["to_owner_id"] = target.ID
		plan.effect = func() error {
			var err error
			switch rec.entityType {
			case models.AutomationEntityLead:
				if err = r.SetLeadOwner(rec.id, target.ID, now); err != nil {
					return err
				}
				if err = automationLeadUpdated(r, rec.id, rec.lead.Status, rec.lead.Status, models.AutomationOriginAutomation, nil); err != nil {
					return err
				}
				// lead.updated (#408) when the new owner changed the state of
				// the lead read under lock in this step.
				after := *rec.lead
				after.OwnerID = target.ID
				if rec.lead.WebhookState() != after.WebhookState() {
					err = e.webhooks.Bind(tx).Updated(ctx, webhook.Lead, rec.id)
				}
			case models.AutomationEntityDeal:
				if err = r.SetDealOwner(rec.id, target.ID, now); err != nil {
					return err
				}
				if err = automationDealUpdated(r, rec.id, rec.deal.Stage, rec.deal.Stage, models.AutomationOriginAutomation, nil); err != nil {
					return err
				}
				// deal.updated (#515) when the new owner changed the state of
				// the deal read under lock in this step.
				after := *rec.deal
				after.OwnerID = target.ID
				if rec.deal.WebhookState() != after.WebhookState() {
					err = e.webhooks.Bind(tx).Updated(ctx, webhook.Deal, rec.id)
				}
			case models.AutomationEntityTicket:
				// Only assigned_to_id and updated_at are written; the
				// ticket_updated event carries origin automation, which the
				// dispatcher claims without starting a run (#242).
				if err = r.SetTicketAssignee(rec.id, target.ID, now); err != nil {
					return err
				}
				if err = automationTicketUpdated(r, rec.id, rec.ticket.Status, rec.ticket.Status, models.AutomationOriginAutomation, nil); err != nil {
					return err
				}
				// ticket.updated (#408) when the new assignee changed the
				// state of the ticket read under lock in this step.
				after := *rec.ticket
				assignee := target.ID
				after.AssignedToID = &assignee
				if rec.ticket.WebhookState() != after.WebhookState() {
					err = e.webhooks.Bind(tx).Updated(ctx, webhook.Ticket, rec.id)
				}
			}
			return err
		}
	case models.AutomationStepNotify:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		recipient, available, err := resolveRecipient(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["recipient_id"] = recipient.ID
		owner := loadOwner(users, rec)
		runID := run.ID
		notification := &models.Notification{
			UserID:     recipient.ID,
			Title:      truncateRunes(renderPlaceholders(cfg.Title, rec, owner), models.NotificationTitleMaxLength),
			Body:       renderPlaceholders(cfg.Body, rec, owner),
			EntityType: rec.entityType,
			EntityID:   rec.id,
			RunID:      &runID,
		}
		plan.effect = func() error {
			if err := e.notifications.WithTx(tx).Create(notification); err != nil {
				return err
			}
			plan.details["notification_id"] = notification.ID
			return nil
		}
	default:
		return nil, fmt.Errorf("unknown automation step type %q: %w", step.StepType, apperrors.ErrValidation)
	}
	return plan, nil
}

// triggeringAttribution returns the attribution of the form submission named
// by the run's triggering event (#220). It is nil — every attribution field
// reads empty — for a schedule run, an event without a submission, a
// submission that is gone, or one whose lead_id is not the run's lead.
func (e *AutomationEngine) triggeringAttribution(r repository.AutomationRepository, run *models.AutomationRun) (*formAttribution, error) {
	if run.EventID == nil || run.EntityType != models.AutomationEntityLead {
		return nil, nil
	}
	submission, err := r.GetEventFormSubmission(*run.EventID)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if submission == nil || submission.LeadID == nil || *submission.LeadID != run.EntityID {
		return nil, nil
	}
	return attributionFromSubmission(submission), nil
}

// buildAutomationTask renders the task of a create_task step: assigned to
// the resolved user, due due_in_days after execution (UTC), linked to the
// lead of a lead run, to the deal of a deal run plus the deal's customer,
// else its lead, never both of those two, and to the customer of a ticket
// run's ticket (#242). The deal link is a business link the
// rule wrote, not the rule's actor reading the deal, so it is set whoever the
// assignee is and whatever the stage. The text is rendered through the task allowlist only: a task survives the
// erasure of its lead, so no name, address, phone or company is ever copied
// into it.
func buildAutomationTask(cfg models.AutomationStepConfig, rec *automationRecord, assigneeID uint, now time.Time) *models.Task {
	due := now.UTC().Add(time.Duration(cfg.DueInDays) * 24 * time.Hour)
	priority := models.TaskPriority(cfg.Priority)
	if priority == "" {
		priority = models.TaskPriorityMedium
	}
	task := &models.Task{
		Title:        truncateRunes(renderTaskPlaceholders(cfg.Title, rec), models.AutomationStepTextMaxLength),
		Description:  renderTaskPlaceholders(cfg.Description, rec),
		Status:       models.TaskStatusPending,
		Priority:     priority,
		DueDate:      &due,
		AssignedToID: assigneeID,
	}
	switch rec.entityType {
	case models.AutomationEntityLead:
		leadID := rec.id
		task.LeadID = &leadID
	case models.AutomationEntityDeal:
		dealID := rec.id
		task.DealID = &dealID
		if rec.deal.CustomerID != nil {
			customerID := *rec.deal.CustomerID
			task.CustomerID = &customerID
		} else if rec.deal.LeadID != nil {
			leadID := *rec.deal.LeadID
			task.LeadID = &leadID
		}
	case models.AutomationEntityTicket:
		// Tasks have no ticket link (#242): the task is linked to the
		// ticket's customer only.
		if rec.ticket.CustomerID != 0 {
			customerID := rec.ticket.CustomerID
			task.CustomerID = &customerID
		}
	}
	return task
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// executeStep executes the run's next step and reports where the run stands.
func (e *AutomationEngine) executeStep(run *models.AutomationRun, rule *models.AutomationRule, logger *logrus.Entry) (stepResult, error) {
	pos := run.NextPosition
	step := rule.Steps[pos-1]
	logger = logger.WithFields(logrus.Fields{"position": pos, "step_type": step.StepType})
	if step.StepType == models.AutomationStepSendEmail {
		return e.executeEmailStep(run, rule, step, logger)
	}

	var result stepResult
	var outcome, errorCode string
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		rec, err := e.lockRecord(r, run)
		if err != nil {
			return err
		}
		if e.afterRecordRead != nil {
			e.afterRecordRead(run.ID, pos)
		}
		now := e.now().UTC()
		if rec == nil {
			result = stepResult{terminal: true}
			outcome = models.AutomationRunCancelled
			return e.advance(r, run, pos, repository.AutomationRunAdvance{
				Status: models.AutomationRunCancelled, NextPosition: pos, FinishedAt: &now, CancelReason: models.AutomationCancelRecordDeleted,
			}, now)
		}
		plan, err := e.planStep(ctx, tx, r, run, rule, step, rec, now)
		if err != nil {
			return err
		}
		if err := e.advance(r, run, pos, plan.advance, now); err != nil {
			return err
		}
		if plan.effect != nil {
			if err := plan.effect(); err != nil {
				return err
			}
		}
		if e.afterEffect != nil {
			if err := e.afterEffect(run.ID, pos); err != nil {
				return err
			}
		}
		if err := r.CreateRunStep(&models.AutomationRunStep{
			RunID: run.ID, Position: pos, StepType: step.StepType,
			Outcome: plan.outcome, ErrorCode: plan.errorCode, Details: plan.details, ExecutedAt: now,
		}); err != nil {
			return err
		}
		outcome, errorCode = plan.outcome, plan.errorCode
		result = stepResult{terminal: plan.advance.Status != models.AutomationRunRunning, nextPosition: plan.advance.NextPosition}
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	entry := logger.WithField("outcome", outcome)
	if errorCode != "" {
		entry = entry.WithField("error_code", errorCode)
	}
	entry.Info("Automation step executed")
	return result, nil
}
