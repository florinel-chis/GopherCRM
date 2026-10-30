package service

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// DryRunDraft simulates an unsaved rule body against one record. The body
// goes through validateRule — the normalisation and every check of a create
// or an update, the staff-user lookups included — and is then handed to the
// engine as it is: no rule row, step row, run, event, task, notification or
// email is created, no transaction is started and no row is locked.
func (s *automationService) DryRunDraft(rule *models.AutomationRule, entityType string, entityID uint) (*models.AutomationDryRunReport, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"entity_type": entityType, "entity_id": entityID,
	}), "AutomationService", "DryRunDraft")
	if err := s.validateRule(rule); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Automation draft rejected")
		return nil, err
	}
	rule.ID, rule.Version = 0, 0
	report, err := s.engine.DryRun(rule, entityType, entityID)
	if err != nil {
		if !errors.Is(err, apperrors.ErrValidation) && !isNotFound(err) {
			utils.LogServiceResponse(logger, err)
		}
		return nil, err
	}
	report.Draft = true
	logger.WithField("steps", len(report.Steps)).Info("Automation draft dry run completed")
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
