package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// automationService drives the rule API, the run history API and the dry
// run. Rules are admin-only (the routes guard that); the run history follows
// the lead and deal visibility of the caller.
type automationService struct {
	repo      repository.AutomationRepository
	users     repository.UserRepository
	engine    *AutomationEngine
	txManager *utils.TransactionManager
	now       func() time.Time
}

// NewAutomationService wires the rule and run services over the repository
// and the engine (which performs the dry run).
func NewAutomationService(repo repository.AutomationRepository, users repository.UserRepository, engine *AutomationEngine, txManager *utils.TransactionManager) AutomationService {
	return &automationService{repo: repo, users: users, engine: engine, txManager: txManager, now: time.Now}
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

func invalidRule(format string, args ...interface{}) error {
	return fmt.Errorf(format+": %w", append(args, apperrors.ErrValidation)...)
}

// validateRule normalises and checks a rule and its steps. It writes
// nothing: every refusal here precedes the transaction, so an invalid rule
// leaves no rule or step row. Plain shape failures unwrap to
// apperrors.ErrValidation (400 INVALID_INPUT); a step naming a user that does
// not exist, is inactive or is not staff unwraps to apperrors.ErrAssigneeNotFound
// (400 INVALID_REFERENCE).
func (s *automationService) validateRule(rule *models.AutomationRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		return invalidRule("name is required")
	}
	if utf8.RuneCountInString(rule.Name) > models.AutomationRuleNameMaxLength {
		return invalidRule("name is longer than %d characters", models.AutomationRuleNameMaxLength)
	}
	if len(rule.Description) > models.AutomationRuleDescMaxBytes {
		return invalidRule("description is longer than %d bytes", models.AutomationRuleDescMaxBytes)
	}
	triggers, ok := automationTriggerTypes[rule.EntityType]
	if !ok {
		return invalidRule("entity_type must be lead or deal")
	}
	if !triggers[rule.TriggerType] {
		return invalidRule("trigger_type %q is not valid for %s rules", rule.TriggerType, rule.EntityType)
	}
	if err := validateTrigger(rule.EntityType, rule.TriggerType, &rule.Trigger); err != nil {
		return err
	}

	if len(rule.Steps) == 0 {
		return invalidRule("at least one step is required")
	}
	if len(rule.Steps) > models.AutomationMaxSteps {
		return invalidRule("a rule has at most %d steps", models.AutomationMaxSteps)
	}
	var totalWait int64
	referenced := map[uint]bool{}
	for i := range rule.Steps {
		step := &rule.Steps[i]
		step.Position = i + 1
		if err := validateStep(rule.EntityType, step, len(rule.Steps)); err != nil {
			return err
		}
		if step.StepType == models.AutomationStepWait {
			totalWait += step.Config.DurationMinutes
		}
		if step.StepType == models.AutomationStepAssignOwner || step.Config.Recipient == models.AutomationRecipientUser {
			referenced[step.Config.UserID] = true
		}
	}
	if totalWait > models.AutomationMaxWaitMinutes {
		return invalidRule("the waits of a rule add up to more than 90 days")
	}
	for userID := range referenced {
		if err := s.checkStaffUser(userID); err != nil {
			return err
		}
	}
	return nil
}

// checkStaffUser requires the id to name a live, active staff account.
func (s *automationService) checkStaffUser(userID uint) error {
	user, err := s.users.GetByID(userID)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("unknown user_id %d: %w", userID, apperrors.ErrAssigneeNotFound)
		}
		return err
	}
	if !user.IsActive {
		return fmt.Errorf("user %d is inactive: %w", userID, apperrors.ErrAssigneeNotFound)
	}
	if user.Role == models.RoleCustomer {
		return fmt.Errorf("user %d is not a staff user: %w", userID, apperrors.ErrAssigneeNotFound)
	}
	return nil
}

func validateTrigger(entityType, triggerType string, trigger *models.AutomationTrigger) error {
	changed := triggerType == models.AutomationEventLeadStatusChanged || triggerType == models.AutomationEventDealStageChanged
	if !changed && (trigger.From != "" || trigger.To != "") {
		return invalidRule("from and to only apply to a status or stage change trigger")
	}
	if changed {
		for name, value := range map[string]string{"from": trigger.From, "to": trigger.To} {
			if value == "" {
				continue
			}
			if !validAutomationValue(entityType, value) {
				return invalidRule("trigger %s %q is not a valid %s value", name, value, entityType)
			}
		}
	}
	if triggerType != models.AutomationTriggerSchedule {
		if trigger.HourUTC != 0 || trigger.StaleDays != 0 || trigger.Filters != nil {
			return invalidRule("hour_utc, filters and stale_days only apply to a schedule trigger")
		}
		return nil
	}
	if trigger.HourUTC < 0 || trigger.HourUTC > 23 {
		return invalidRule("hour_utc must be between 0 and 23")
	}
	if trigger.StaleDays < 0 {
		return invalidRule("stale_days must not be negative")
	}
	if trigger.Filters != nil {
		if trigger.Filters.Status != "" {
			if entityType != models.AutomationEntityLead || !isValidLeadStatus(models.LeadStatus(trigger.Filters.Status)) {
				return invalidRule("filters.status %q is not a lead status", trigger.Filters.Status)
			}
		}
		if trigger.Filters.Stage != "" {
			if entityType != models.AutomationEntityDeal || !models.DealStage(trigger.Filters.Stage).IsValid() {
				return invalidRule("filters.stage %q is not a deal stage", trigger.Filters.Stage)
			}
		}
	}
	return nil
}

// validAutomationValue reports whether value is a lead status (lead rules)
// or a deal stage (deal rules).
func validAutomationValue(entityType, value string) bool {
	if entityType == models.AutomationEntityLead {
		return isValidLeadStatus(models.LeadStatus(value))
	}
	return models.DealStage(value).IsValid()
}

func validateStep(entityType string, step *models.AutomationStep, stepCount int) error {
	cfg := &step.Config
	pos := step.Position
	textLimit := func(name, value string) error {
		if utf8.RuneCountInString(value) > models.AutomationStepTextMaxLength {
			return invalidRule("step %d: %s is longer than %d characters", pos, name, models.AutomationStepTextMaxLength)
		}
		if strings.ContainsAny(value, "\r\n") {
			return invalidRule("step %d: %s must not contain a line break", pos, name)
		}
		return nil
	}
	bodyLimit := func(name, value string) error {
		if len(value) > models.AutomationStepBodyMaxBytes {
			return invalidRule("step %d: %s is longer than %d bytes", pos, name, models.AutomationStepBodyMaxBytes)
		}
		return nil
	}
	placeholders := func(name, value string) error {
		if unknown := unknownPlaceholders(entityType, value); len(unknown) > 0 {
			return invalidRule("step %d: %s uses an unknown placeholder %q", pos, name, unknown[0])
		}
		return nil
	}
	// A task outlives the erasure of the lead it links to, so its text is
	// checked against the narrower task allowlist (no name, address, phone or
	// company); a personal placeholder is an unknown placeholder here.
	taskPlaceholders := func(name, value string) error {
		if unknown := unknownTaskPlaceholders(entityType, value); len(unknown) > 0 {
			return invalidRule("step %d: %s uses a placeholder not allowed in a task %q", pos, name, unknown[0])
		}
		return nil
	}
	recipient := func() error {
		switch cfg.Recipient {
		case "":
			cfg.Recipient = models.AutomationRecipientOwner
		case models.AutomationRecipientOwner:
		case models.AutomationRecipientUser:
			if cfg.UserID == 0 {
				return invalidRule("step %d: user_id is required for recipient \"user\"", pos)
			}
		default:
			return invalidRule("step %d: recipient must be owner or user", pos)
		}
		if cfg.Recipient == models.AutomationRecipientOwner && cfg.UserID != 0 {
			return invalidRule("step %d: user_id only applies to recipient \"user\"", pos)
		}
		return nil
	}

	switch step.StepType {
	case models.AutomationStepWait:
		if cfg.DurationMinutes <= 0 {
			return invalidRule("step %d: duration_minutes must be positive", pos)
		}
		if cfg.DurationMinutes > models.AutomationMaxWaitMinutes {
			return invalidRule("step %d: a wait is at most 90 days", pos)
		}
	case models.AutomationStepCondition:
		if err := validateCondition(entityType, *cfg); err != nil {
			return invalidRule("step %d: %s", pos, err.Error())
		}
		switch cfg.Else {
		case "":
			cfg.Else = models.AutomationElseStop
			fallthrough
		case models.AutomationElseStop:
			if cfg.ElsePosition != 0 {
				return invalidRule("step %d: else_position only applies to else \"jump\"", pos)
			}
		case models.AutomationElseJump:
			if cfg.ElsePosition <= pos || cfg.ElsePosition > stepCount {
				return invalidRule("step %d: else_position must be after the step and at most %d", pos, stepCount)
			}
		default:
			return invalidRule("step %d: else must be stop or jump", pos)
		}
	case models.AutomationStepCreateTask:
		if strings.TrimSpace(cfg.Title) == "" {
			return invalidRule("step %d: title is required", pos)
		}
		if err := textLimit("title", cfg.Title); err != nil {
			return err
		}
		if err := bodyLimit("description", cfg.Description); err != nil {
			return err
		}
		switch models.TaskPriority(cfg.Priority) {
		case "", models.TaskPriorityLow, models.TaskPriorityMedium, models.TaskPriorityHigh:
		default:
			return invalidRule("step %d: priority must be low, medium or high", pos)
		}
		if cfg.DueInDays < 0 || cfg.DueInDays > 365 {
			return invalidRule("step %d: due_in_days must be between 0 and 365", pos)
		}
		if err := recipient(); err != nil {
			return err
		}
		if err := taskPlaceholders("title", cfg.Title); err != nil {
			return err
		}
		if err := taskPlaceholders("description", cfg.Description); err != nil {
			return err
		}
	case models.AutomationStepAssignOwner:
		if cfg.UserID == 0 {
			return invalidRule("step %d: user_id is required", pos)
		}
		cfg.Recipient = models.AutomationRecipientUser
	case models.AutomationStepSendEmail:
		if strings.TrimSpace(cfg.Subject) == "" {
			return invalidRule("step %d: subject is required", pos)
		}
		if err := textLimit("subject", cfg.Subject); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Body) == "" {
			return invalidRule("step %d: body is required", pos)
		}
		if err := bodyLimit("body", cfg.Body); err != nil {
			return err
		}
		if err := recipient(); err != nil {
			return err
		}
		if err := placeholders("subject", cfg.Subject); err != nil {
			return err
		}
		if err := placeholders("body", cfg.Body); err != nil {
			return err
		}
	case models.AutomationStepNotify:
		if strings.TrimSpace(cfg.Title) == "" {
			return invalidRule("step %d: title is required", pos)
		}
		if err := textLimit("title", cfg.Title); err != nil {
			return err
		}
		if err := bodyLimit("body", cfg.Body); err != nil {
			return err
		}
		if err := recipient(); err != nil {
			return err
		}
		if err := placeholders("title", cfg.Title); err != nil {
			return err
		}
		if err := placeholders("body", cfg.Body); err != nil {
			return err
		}
	default:
		return invalidRule("step %d: unknown step type %q", pos, step.StepType)
	}
	return nil
}

// --- Rules -------------------------------------------------------------------

func (s *automationService) CreateRule(rule *models.AutomationRule, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "automation_rule"), "AutomationService", "CreateRule")
	if err := s.validateRule(rule); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Automation rule rejected")
		return err
	}
	rule.ID = 0
	rule.Version = 1
	rule.SchedulePassOn, rule.ScheduleCursor, rule.ScheduleCompletedOn = nil, 0, nil
	if actorID != 0 {
		actor := actorID
		rule.CreatedByID = &actor
	}
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		return s.repo.WithTx(tx).CreateRule(rule)
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	logger.WithField("rule_id", rule.ID).Info("Automation rule created")
	return nil
}

func (s *automationService) GetRule(id uint) (*models.AutomationRule, error) {
	rule, err := s.repo.GetRuleByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("automation rule %d not found: %w", id, apperrors.ErrAutomationRuleNotFound)
		}
		return nil, err
	}
	return rule, nil
}

func (s *automationService) ListRules(offset, limit int, filter repository.AutomationRuleListFilter) ([]models.AutomationRule, int64, error) {
	rules, total, err := s.repo.ListRules(offset, limit, filter)
	if err != nil {
		return nil, 0, err
	}
	if rules == nil {
		rules = []models.AutomationRule{}
	}
	return rules, total, nil
}

// UpdateRule replaces the rule wholesale in one transaction: the row is
// locked, the version incremented, the steps replaced and every pending,
// waiting or running run of the rule cancelled with rule_changed. A failure
// anywhere leaves the version, the steps and the runs as they were. enabled
// nil keeps the switch as read under the row lock, so a disable committed
// after the caller read the rule is not undone.
func (s *automationService) UpdateRule(id uint, rule *models.AutomationRule, enabled *bool) (*models.AutomationRule, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("rule_id", id), "AutomationService", "UpdateRule")
	if err := s.validateRule(rule); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Automation rule rejected")
		return nil, err
	}
	err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := s.repo.WithTx(tx)
		stored, err := r.GetRuleByIDForUpdate(id)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("automation rule %d not found: %w", id, apperrors.ErrAutomationRuleNotFound)
			}
			return err
		}
		rule.ID = stored.ID
		rule.Version = stored.Version + 1
		rule.CreatedByID = stored.CreatedByID
		if enabled == nil {
			rule.Enabled = stored.Enabled
		} else {
			rule.Enabled = *enabled
		}
		if err := r.UpdateRule(rule); err != nil {
			return err
		}
		if err := r.ReplaceSteps(stored.ID, rule.Steps); err != nil {
			return err
		}
		_, err = r.CancelRunsForRule(stored.ID, models.AutomationCancelRuleChanged, s.now())
		return err
	}, 3)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	logger.WithField("version", rule.Version).Info("Automation rule updated")
	return s.GetRule(id)
}

// SetEnabled flips the switch. Disabling cancels the rule's pending, waiting
// and running runs with rule_disabled in the same transaction.
func (s *automationService) SetEnabled(id uint, enabled bool) (*models.AutomationRule, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{"rule_id": id, "enabled": enabled}), "AutomationService", "SetEnabled")
	err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := s.repo.WithTx(tx)
		if _, err := r.GetRuleByIDForUpdate(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("automation rule %d not found: %w", id, apperrors.ErrAutomationRuleNotFound)
			}
			return err
		}
		if err := r.SetRuleEnabled(id, enabled); err != nil {
			return err
		}
		if enabled {
			return nil
		}
		_, err := r.CancelRunsForRule(id, models.AutomationCancelRuleDisabled, s.now())
		return err
	}, 3)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	logger.Info("Automation rule switched")
	return s.GetRule(id)
}

// DeleteRule soft-deletes the rule and cancels its active runs with
// rule_deleted, in one transaction.
func (s *automationService) DeleteRule(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("rule_id", id), "AutomationService", "DeleteRule")
	err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := s.repo.WithTx(tx)
		if _, err := r.GetRuleByIDForUpdate(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("automation rule %d not found: %w", id, apperrors.ErrAutomationRuleNotFound)
			}
			return err
		}
		if _, err := r.CancelRunsForRule(id, models.AutomationCancelRuleDeleted, s.now()); err != nil {
			return err
		}
		return r.DeleteRule(id)
	}, 3)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	logger.Info("Automation rule deleted")
	return nil
}

// DryRun simulates the rule against one record and reports each step's
// would-be outcome. It writes, sends and notifies nothing.
func (s *automationService) DryRun(ruleID uint, entityType string, entityID uint) (*models.AutomationDryRunReport, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"rule_id": ruleID, "entity_type": entityType, "entity_id": entityID,
	}), "AutomationService", "DryRun")
	rule, err := s.GetRule(ruleID)
	if err != nil {
		return nil, err
	}
	report, err := s.engine.DryRun(rule, entityType, entityID)
	if err != nil {
		if !errors.Is(err, apperrors.ErrValidation) && !isNotFound(err) {
			utils.LogServiceResponse(logger, err)
		}
		return nil, err
	}
	logger.WithField("steps", len(report.Steps)).Info("Automation dry run completed")
	return report, nil
}

// --- Runs --------------------------------------------------------------------

// runScopeFor is the role scope of the run history: an admin sees every run,
// a sales user the runs on the leads and deals it owns, and support and
// customer callers are refused before any query.
func runScopeFor(viewer LeadViewer) (repository.AutomationRunScope, error) {
	switch viewer.Role {
	case models.RoleAdmin:
		return repository.AutomationRunScope{}, nil
	case models.RoleSales:
		if viewer.UserID == 0 {
			return repository.AutomationRunScope{}, fmt.Errorf("sales viewer without an id: %w", apperrors.ErrForbidden)
		}
		owner := viewer.UserID
		return repository.AutomationRunScope{OwnerID: &owner}, nil
	default:
		return repository.AutomationRunScope{}, fmt.Errorf("role %q has no access to automation runs: %w", viewer.Role, apperrors.ErrForbidden)
	}
}

func (s *automationService) ListRuns(viewer LeadViewer, offset, limit int, filter repository.AutomationRunListFilter) ([]models.AutomationRun, int64, error) {
	scope, err := runScopeFor(viewer)
	if err != nil {
		return nil, 0, err
	}
	runs, total, err := s.repo.ListRuns(offset, limit, filter, scope)
	if err != nil {
		return nil, 0, err
	}
	if runs == nil {
		runs = []models.AutomationRun{}
	}
	return runs, total, nil
}

func (s *automationService) GetRun(viewer LeadViewer, id uint) (*models.AutomationRun, error) {
	scope, err := runScopeFor(viewer)
	if err != nil {
		return nil, err
	}
	run, err := s.repo.GetRunByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("automation run %d not found: %w", id, apperrors.ErrAutomationRunNotFound)
		}
		return nil, err
	}
	if scope.OwnerID != nil {
		visible, err := s.repo.RunInScope(id, scope)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, fmt.Errorf("automation run %d not found: %w", id, apperrors.ErrAutomationRunNotFound)
		}
	}
	return run, nil
}

// CancelRun cancels a pending, waiting or running run with cancel_reason
// manual; a finished run is apperrors.ErrAutomationRunFinished.
func (s *automationService) CancelRun(id uint) (*models.AutomationRun, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("run_id", id), "AutomationService", "CancelRun")
	run, err := s.repo.GetRunByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("automation run %d not found: %w", id, apperrors.ErrAutomationRunNotFound)
		}
		return nil, err
	}
	if run.IsFinished() {
		return nil, fmt.Errorf("automation run %d is %s: %w", id, run.Status, apperrors.ErrAutomationRunFinished)
	}
	rows, err := s.repo.CancelRun(id, models.AutomationCancelManual, s.now())
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if rows == 0 {
		return nil, fmt.Errorf("automation run %d finished meanwhile: %w", id, apperrors.ErrAutomationRunFinished)
	}
	logger.Info("Automation run cancelled")
	return s.repo.GetRunByID(id)
}
