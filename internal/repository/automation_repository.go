package repository

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// automationRepository backs the follow-up automation module. Every
// conditional update here (the event claim, the run claim, the run advance,
// the cancels, the schedule cursor) reports the driver's affected-row count,
// and every one of them changes at least one column value when it matches,
// so MySQL's changed-rows semantics agree with the matched count. The locking
// reads carry `FOR UPDATE` on MySQL; the SQLite dialector drops the clause,
// where the single pooled connection serialises writers anyway.
type automationRepository struct {
	db *gorm.DB
}

func NewAutomationRepository(db *gorm.DB) AutomationRepository {
	return &automationRepository{db: db}
}

func (r *automationRepository) WithTx(tx *gorm.DB) AutomationRepository {
	return &automationRepository{db: tx}
}

// locking is a fresh session carrying `FOR UPDATE`, as the quote repository
// does: Clauses alone returns a statement GORM keeps reusing.
func (r *automationRepository) locking() *gorm.DB {
	return r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
}

// activeRunStatuses are the statuses a cancel reaches.
var activeRunStatuses = []string{models.AutomationRunPending, models.AutomationRunWaiting, models.AutomationRunRunning}

// --- Rules -------------------------------------------------------------------

func (r *automationRepository) CreateRule(rule *models.AutomationRule) error {
	steps := rule.Steps
	rule.Steps = nil
	if err := r.db.Omit("Steps", "CreatedBy").Create(rule).Error; err != nil {
		return err
	}
	for i := range steps {
		steps[i].ID = 0
		steps[i].RuleID = rule.ID
		steps[i].Position = i + 1
	}
	if len(steps) > 0 {
		if err := r.db.Create(&steps).Error; err != nil {
			return err
		}
	}
	rule.Steps = steps
	return nil
}

func (r *automationRepository) withSteps(db *gorm.DB) *gorm.DB {
	return db.Preload("Steps", func(db *gorm.DB) *gorm.DB {
		return db.Order("position ASC").Order("id ASC")
	})
}

func (r *automationRepository) GetRuleByID(id uint) (*models.AutomationRule, error) {
	var rule models.AutomationRule
	if err := r.withSteps(r.db).First(&rule, id).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *automationRepository) GetRuleByIDForUpdate(id uint) (*models.AutomationRule, error) {
	var rule models.AutomationRule
	if err := r.locking().First(&rule, id).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *automationRepository) ruleScope(db *gorm.DB, filter AutomationRuleListFilter) *gorm.DB {
	if filter.EntityType != "" {
		db = db.Where("entity_type = ?", filter.EntityType)
	}
	if filter.Enabled != nil {
		db = db.Where("enabled = ?", *filter.Enabled)
	}
	return db
}

func (r *automationRepository) ListRules(offset, limit int, filter AutomationRuleListFilter) ([]models.AutomationRule, int64, error) {
	column, direction, err := utils.ValidateSort("automation_rules", filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := r.ruleScope(r.db.Model(&models.AutomationRule{}), filter).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rules := []models.AutomationRule{}
	err = r.ruleScope(r.withSteps(r.db), filter).
		Order(column + " " + direction).Order("id " + direction).
		Offset(offset).Limit(limit).
		Find(&rules).Error
	if err != nil {
		return nil, 0, err
	}
	return rules, total, nil
}

// UpdateRule writes the editable columns of the rule, the version included,
// by primary key. The trigger is serialised here because a map update does
// not run the model's BeforeSave hook.
func (r *automationRepository) UpdateRule(rule *models.AutomationRule) error {
	trigger, err := json.Marshal(rule.Trigger)
	if err != nil {
		return err
	}
	rule.TriggerConfig = string(trigger)
	return r.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).Updates(map[string]interface{}{
		"name":           rule.Name,
		"description":    rule.Description,
		"entity_type":    rule.EntityType,
		"trigger_type":   rule.TriggerType,
		"trigger_config": rule.TriggerConfig,
		"enabled":        rule.Enabled,
		"version":        rule.Version,
		"updated_at":     time.Now().UTC(),
	}).Error
}

// ReplaceSteps deletes every step of the rule and inserts the given ones,
// numbered 1..n in the given order.
func (r *automationRepository) ReplaceSteps(ruleID uint, steps []models.AutomationStep) error {
	if err := r.db.Where("rule_id = ?", ruleID).Delete(&models.AutomationStep{}).Error; err != nil {
		return err
	}
	for i := range steps {
		steps[i].ID = 0
		steps[i].RuleID = ruleID
		steps[i].Position = i + 1
	}
	if len(steps) == 0 {
		return nil
	}
	return r.db.Create(&steps).Error
}

func (r *automationRepository) SetRuleEnabled(id uint, enabled bool) error {
	return r.db.Model(&models.AutomationRule{}).Where("id = ?", id).Updates(map[string]interface{}{
		"enabled":    enabled,
		"updated_at": time.Now().UTC(),
	}).Error
}

func (r *automationRepository) DeleteRule(id uint) error {
	result := r.db.Delete(&models.AutomationRule{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *automationRepository) ListMatchingRules(entityType, triggerType string) ([]models.AutomationRule, error) {
	rules := []models.AutomationRule{}
	err := r.withSteps(r.db).
		Where("entity_type = ? AND trigger_type = ? AND enabled = ?", entityType, triggerType, true).
		Order("id ASC").
		Find(&rules).Error
	return rules, err
}

func (r *automationRepository) ListScheduleRules() ([]models.AutomationRule, error) {
	rules := []models.AutomationRule{}
	err := r.withSteps(r.db).
		Where("trigger_type = ? AND enabled = ?", models.AutomationTriggerSchedule, true).
		Order("id ASC").
		Find(&rules).Error
	return rules, err
}

// AdvanceScheduleCursor is the conditional update of a schedule pass: it
// matches only while the rule still holds the (schedule_pass_on,
// schedule_cursor) pair the caller read, so two workers handing out the same
// batch commit exactly one. A pass start changes schedule_pass_on and a
// batch changes the cursor or sets schedule_completed_on, so a matching
// update always changes a column.
func (r *automationRepository) AdvanceScheduleCursor(ruleID uint, expectedPassOn *string, expectedCursor uint, passOn string, cursor uint, completedOn *string, now time.Time) (int64, error) {
	query := r.db.Model(&models.AutomationRule{}).Where("id = ? AND schedule_cursor = ?", ruleID, expectedCursor)
	if expectedPassOn == nil {
		query = query.Where("schedule_pass_on IS NULL")
	} else {
		query = query.Where("schedule_pass_on = ?", *expectedPassOn)
	}
	result := query.Updates(map[string]interface{}{
		"schedule_pass_on":      passOn,
		"schedule_cursor":       cursor,
		"schedule_completed_on": completedOn,
		"updated_at":            now.UTC(),
	})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// --- Events ------------------------------------------------------------------

func (r *automationRepository) RecordEvent(event *models.AutomationEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return r.db.Create(event).Error
}

func (r *automationRepository) ListUnprocessedEvents(limit int) ([]models.AutomationEvent, error) {
	events := []models.AutomationEvent{}
	err := r.db.Where("processed_at IS NULL").Order("id ASC").Limit(limit).Find(&events).Error
	return events, err
}

// ClaimEvent stamps processed_at only while it is still NULL: exactly one
// dispatcher ever claims an event.
func (r *automationRepository) ClaimEvent(id uint, now time.Time) (int64, error) {
	stamp := now.UTC()
	result := r.db.Model(&models.AutomationEvent{}).
		Where("id = ? AND processed_at IS NULL", id).
		Update("processed_at", &stamp)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// --- Runs --------------------------------------------------------------------

// CreateRunIgnoringDuplicate inserts the run with GORM's
// clause.OnConflict{DoNothing: true}, which renders ON CONFLICT DO NOTHING on
// SQLite and ON DUPLICATE KEY UPDATE id=id on MySQL: a duplicate dedupe_key is
// neither an error nor a row. It reports whether a row was inserted.
func (r *automationRepository) CreateRunIgnoringDuplicate(run *models.AutomationRun) (bool, error) {
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Omit("Rule", "Steps").Create(run)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		run.ID = 0
		return false, nil
	}
	return true, nil
}

func (r *automationRepository) withRunSteps(db *gorm.DB) *gorm.DB {
	return db.Preload("Steps", func(db *gorm.DB) *gorm.DB {
		return db.Order("position ASC").Order("id ASC")
	})
}

func (r *automationRepository) GetRunByID(id uint) (*models.AutomationRun, error) {
	var run models.AutomationRun
	if err := r.withRunSteps(r.db).First(&run, id).Error; err != nil {
		return nil, err
	}
	if err := r.attachRuleNames([]*models.AutomationRun{&run}); err != nil {
		return nil, err
	}
	return &run, nil
}

// attachRuleNames fills RuleName and RuleDeleted of the runs with one query
// over the distinct rule ids, soft-deleted rules included. Only the id, the
// name and the deletion stamp are read: the rule's trigger and steps never
// reach a run response. A rule row that no longer exists leaves the name
// empty and RuleDeleted set.
func (r *automationRepository) attachRuleNames(runs []*models.AutomationRun) error {
	if len(runs) == 0 {
		return nil
	}
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(runs))
	for _, run := range runs {
		if !seen[run.RuleID] {
			seen[run.RuleID] = true
			ids = append(ids, run.RuleID)
		}
	}
	var rows []struct {
		ID        uint
		Name      string
		DeletedAt gorm.DeletedAt
	}
	if err := r.db.Unscoped().Model(&models.AutomationRule{}).
		Select("id", "name", "deleted_at").Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return err
	}
	type ruleName struct {
		name    string
		deleted bool
	}
	names := make(map[uint]ruleName, len(rows))
	for _, row := range rows {
		names[row.ID] = ruleName{name: row.Name, deleted: row.DeletedAt.Valid}
	}
	for _, run := range runs {
		found, ok := names[run.RuleID]
		run.RuleName = found.name
		run.RuleDeleted = !ok || found.deleted
	}
	return nil
}

func (r *automationRepository) GetRunByIDForUpdate(id uint) (*models.AutomationRun, error) {
	var run models.AutomationRun
	if err := r.locking().First(&run, id).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// runScope applies the filter and the role scope. The scope is a predicate
// of the query itself — a sales caller sees only the runs whose lead or deal
// it currently owns, decided by two sub-selects over the live rows — so it
// applies before offset and limit and the total counts the same set.
func (r *automationRepository) runScope(db *gorm.DB, filter AutomationRunListFilter, scope AutomationRunScope) *gorm.DB {
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.RuleID != nil {
		db = db.Where("rule_id = ?", *filter.RuleID)
	}
	if filter.EntityType != "" {
		db = db.Where("entity_type = ?", filter.EntityType)
	}
	if filter.EntityID != nil {
		db = db.Where("entity_id = ?", *filter.EntityID)
	}
	if scope.OwnerID != nil {
		ownedLeads := r.db.Model(&models.Lead{}).Select("id").Where("owner_id = ?", *scope.OwnerID)
		ownedDeals := r.db.Model(&models.Deal{}).Select("id").Where("owner_id = ?", *scope.OwnerID)
		db = db.Where("(entity_type = ? AND entity_id IN (?)) OR (entity_type = ? AND entity_id IN (?))",
			models.AutomationEntityLead, ownedLeads, models.AutomationEntityDeal, ownedDeals)
	}
	return db
}

func (r *automationRepository) ListRuns(offset, limit int, filter AutomationRunListFilter, scope AutomationRunScope) ([]models.AutomationRun, int64, error) {
	column, direction, err := utils.ValidateSort("automation_runs", filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := r.runScope(r.db.Model(&models.AutomationRun{}), filter, scope).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	runs := []models.AutomationRun{}
	err = r.runScope(r.db, filter, scope).
		Order(column + " " + direction).Order("id " + direction).
		Offset(offset).Limit(limit).
		Find(&runs).Error
	if err != nil {
		return nil, 0, err
	}
	page := make([]*models.AutomationRun, len(runs))
	for i := range runs {
		page[i] = &runs[i]
	}
	if err := r.attachRuleNames(page); err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

// RunInScope reports whether the run exists inside the scope.
func (r *automationRepository) RunInScope(id uint, scope AutomationRunScope) (bool, error) {
	var count int64
	err := r.runScope(r.db.Model(&models.AutomationRun{}), AutomationRunListFilter{}, scope).
		Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *automationRepository) ListClaimableRuns(now time.Time, limit int) ([]models.AutomationRun, error) {
	runs := []models.AutomationRun{}
	err := r.db.
		Where("status = ? OR (status = ? AND resume_at <= ?)", models.AutomationRunPending, models.AutomationRunWaiting, now.UTC()).
		Order("id ASC").Limit(limit).
		Find(&runs).Error
	return runs, err
}

// ClaimRun moves the run from fromStatus to running with one conditional
// update; a waiting run is claimed only once its resume_at has passed.
func (r *automationRepository) ClaimRun(id uint, fromStatus string, now time.Time) (int64, error) {
	stamp := now.UTC()
	query := r.db.Model(&models.AutomationRun{}).Where("id = ? AND status = ?", id, fromStatus)
	if fromStatus == models.AutomationRunWaiting {
		query = query.Where("resume_at <= ?", stamp)
	}
	result := query.Updates(map[string]interface{}{
		"status":     models.AutomationRunRunning,
		"claimed_at": &stamp,
		"updated_at": stamp,
	})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// AdvanceRun is the conditional advance every step makes: it matches only
// while the run is still running at the position the executor claimed. A
// cancel, a rule change or an erasure committed meanwhile leaves nothing to
// match, so the step's transaction rolls back and the executor stops.
func (r *automationRepository) AdvanceRun(id uint, expectedPosition int, advance AutomationRunAdvance, now time.Time) (int64, error) {
	var resumeAt, finishedAt *time.Time
	if advance.ResumeAt != nil {
		stamp := advance.ResumeAt.UTC()
		resumeAt = &stamp
	}
	if advance.FinishedAt != nil {
		stamp := advance.FinishedAt.UTC()
		finishedAt = &stamp
	}
	result := r.db.Model(&models.AutomationRun{}).
		Where("id = ? AND status = ? AND next_position = ?", id, models.AutomationRunRunning, expectedPosition).
		Updates(map[string]interface{}{
			"status":        advance.Status,
			"next_position": advance.NextPosition,
			"resume_at":     resumeAt,
			"finished_at":   finishedAt,
			"cancel_reason": advance.CancelReason,
			"error_code":    advance.ErrorCode,
			"updated_at":    now.UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// ReleaseRun hands a running run back to pending at the same position after
// a failed step attempt, counting the attempt.
func (r *automationRepository) ReleaseRun(id uint, expectedPosition int, now time.Time) (int64, error) {
	result := r.db.Model(&models.AutomationRun{}).
		Where("id = ? AND status = ? AND next_position = ?", id, models.AutomationRunRunning, expectedPosition).
		Updates(map[string]interface{}{
			"status":     models.AutomationRunPending,
			"attempts":   gorm.Expr("attempts + 1"),
			"claimed_at": nil,
			"updated_at": now.UTC(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *automationRepository) cancel(query *gorm.DB, reason string, now time.Time) (int64, error) {
	stamp := now.UTC()
	result := query.Where("status IN ?", activeRunStatuses).Updates(map[string]interface{}{
		"status":        models.AutomationRunCancelled,
		"cancel_reason": reason,
		"finished_at":   &stamp,
		"updated_at":    stamp,
	})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *automationRepository) CancelRun(id uint, reason string, now time.Time) (int64, error) {
	return r.cancel(r.db.Model(&models.AutomationRun{}).Where("id = ?", id), reason, now)
}

func (r *automationRepository) CancelRunsForRule(ruleID uint, reason string, now time.Time) (int64, error) {
	return r.cancel(r.db.Model(&models.AutomationRun{}).Where("rule_id = ?", ruleID), reason, now)
}

func (r *automationRepository) CancelRunsForEntity(entityType string, entityID uint, reason string, now time.Time) (int64, error) {
	return r.cancel(r.db.Model(&models.AutomationRun{}).Where("entity_type = ? AND entity_id = ?", entityType, entityID), reason, now)
}

func (r *automationRepository) ListRunsByStatus(status string) ([]models.AutomationRun, error) {
	runs := []models.AutomationRun{}
	err := r.db.Where("status = ?", status).Order("id ASC").Find(&runs).Error
	return runs, err
}

func (r *automationRepository) FailRun(id uint, errorCode string, now time.Time) error {
	stamp := now.UTC()
	return r.db.Model(&models.AutomationRun{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":      models.AutomationRunFailed,
		"error_code":  errorCode,
		"finished_at": &stamp,
		"claimed_at":  nil,
		"updated_at":  stamp,
	}).Error
}

func (r *automationRepository) ResetRunToPending(id uint, now time.Time) error {
	return r.db.Model(&models.AutomationRun{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     models.AutomationRunPending,
		"claimed_at": nil,
		"updated_at": now.UTC(),
	}).Error
}

// --- Run steps ---------------------------------------------------------------

func (r *automationRepository) CreateRunStep(step *models.AutomationRunStep) error {
	if step.ExecutedAt.IsZero() {
		step.ExecutedAt = time.Now().UTC()
	}
	return r.db.Create(step).Error
}

func (r *automationRepository) GetRunStep(runID uint, position int) (*models.AutomationRunStep, error) {
	var step models.AutomationRunStep
	if err := r.db.Where("run_id = ? AND position = ?", runID, position).First(&step).Error; err != nil {
		return nil, err
	}
	return &step, nil
}

// LastRunStep returns the highest-positioned step row of the run, or nil
// when the run has none.
func (r *automationRepository) LastRunStep(runID uint) (*models.AutomationRunStep, error) {
	var step models.AutomationRunStep
	err := r.db.Where("run_id = ?", runID).Order("position DESC").Order("id DESC").Take(&step).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &step, nil
}

func (r *automationRepository) UpdateRunStepOutcome(id uint, outcome, errorCode string, details map[string]interface{}) error {
	detail := "{}"
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		detail = string(encoded)
	}
	return r.db.Model(&models.AutomationRunStep{}).Where("id = ?", id).Updates(map[string]interface{}{
		"outcome":    outcome,
		"error_code": errorCode,
		"detail":     detail,
	}).Error
}

func (r *automationRepository) ListRunSteps(runID uint) ([]models.AutomationRunStep, error) {
	steps := []models.AutomationRunStep{}
	err := r.db.Where("run_id = ?", runID).Order("position ASC").Order("id ASC").Find(&steps).Error
	return steps, err
}

// --- Records -----------------------------------------------------------------

// GetLeadForUpdate is the locking read of a step transaction: the live lead
// row with `FOR UPDATE` on MySQL, so a plain read under REPEATABLE READ
// cannot answer from a snapshot older than a concurrent erasure or owner
// change. A soft-deleted (erased) lead is a not-found.
func (r *automationRepository) GetLeadForUpdate(id uint) (*models.Lead, error) {
	var lead models.Lead
	if err := r.locking().First(&lead, id).Error; err != nil {
		return nil, err
	}
	return &lead, nil
}

func (r *automationRepository) GetDealForUpdate(id uint) (*models.Deal, error) {
	var deal models.Deal
	if err := r.locking().First(&deal, id).Error; err != nil {
		return nil, err
	}
	return &deal, nil
}

func (r *automationRepository) GetLead(id uint) (*models.Lead, error) {
	var lead models.Lead
	if err := r.db.First(&lead, id).Error; err != nil {
		return nil, err
	}
	return &lead, nil
}

func (r *automationRepository) GetDeal(id uint) (*models.Deal, error) {
	var deal models.Deal
	if err := r.db.First(&deal, id).Error; err != nil {
		return nil, err
	}
	return &deal, nil
}

func (r *automationRepository) SetLeadOwner(id uint, ownerID uint, now time.Time) error {
	return r.db.Model(&models.Lead{}).Where("id = ?", id).Updates(map[string]interface{}{
		"owner_id":   ownerID,
		"updated_at": now.UTC(),
	}).Error
}

func (r *automationRepository) SetDealOwner(id uint, ownerID uint, now time.Time) error {
	return r.db.Model(&models.Deal{}).Where("id = ?", id).Updates(map[string]interface{}{
		"owner_id":   ownerID,
		"updated_at": now.UTC(),
	}).Error
}

// ListScheduleCandidateIDs returns the ids of the live records a schedule
// batch visits: id order, strictly above the cursor, at most Limit rows,
// narrowed by the status or stage filter and by updated_at < StaleBefore.
func (r *automationRepository) ListScheduleCandidateIDs(query AutomationScheduleQuery) ([]uint, error) {
	var db *gorm.DB
	switch query.EntityType {
	case models.AutomationEntityLead:
		db = r.db.Model(&models.Lead{})
		if query.Status != "" {
			db = db.Where("status = ?", query.Status)
		}
	case models.AutomationEntityDeal:
		db = r.db.Model(&models.Deal{})
		if query.Stage != "" {
			db = db.Where("stage = ?", query.Stage)
		}
	default:
		return nil, errors.New("unknown automation entity type " + query.EntityType)
	}
	if query.StaleBefore != nil {
		db = db.Where("updated_at < ?", query.StaleBefore.UTC())
	}
	ids := []uint{}
	err := db.Where("id > ?", query.AfterID).Order("id ASC").Limit(query.Limit).Pluck("id", &ids).Error
	return ids, err
}
