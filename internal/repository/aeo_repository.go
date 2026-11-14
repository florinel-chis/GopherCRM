package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sort allowlists for the AEO entities.
//
// These are deliberately local to this file rather than folded into
// utils.AllowedSortColumns: that map is shared state nobody owns, and the AEO
// tables are the only consumers of these two column sets. validateAEOSort below
// reproduces utils.ValidateSort's semantics exactly, so the guarantee is the
// same one the rest of the repository layer relies on — no ORDER BY fragment is
// ever built from a string that did not come out of an allowlist.
var (
	aeoPromptSortColumns = map[string]bool{
		"id":         true,
		"text":       true,
		"is_active":  true,
		"created_at": true,
		"updated_at": true,
	}

	aeoRunSortColumns = map[string]bool{
		"id":           true,
		"status":       true,
		"trigger":      true,
		"started_at":   true,
		"completed_at": true,
		"created_at":   true,
	}
)

// validateAEOSort mirrors utils.ValidateSort: an empty sortBy falls back to
// ("created_at", "desc"), a column outside the allowlist is an error, and a
// sortOrder that is neither "asc" nor "desc" degrades to "desc".
func validateAEOSort(allowed map[string]bool, sortBy, sortOrder string) (string, string, error) {
	if sortBy == "" {
		return "created_at", "desc", nil
	}

	if !allowed[sortBy] {
		return "", "", fmt.Errorf("invalid sort column %q for aeo", sortBy)
	}

	order := strings.ToLower(strings.TrimSpace(sortOrder))
	if order != "asc" && order != "desc" {
		order = "desc"
	}

	return sortBy, order, nil
}

// aeoOrderClause builds the ORDER BY from an already-validated column and
// direction, appending id as a tie-breaker so a page boundary cannot drop or
// repeat a row when several rows share the sort value — prompts created in one
// batch share a created_at down to the microsecond.
//
// Column names are backtick-quoted because aeo_runs.trigger collides with the
// MySQL reserved word TRIGGER: unquoted, "ORDER BY trigger desc" is a syntax
// error (MySQL 8 / MariaDB error 1064) while SQLite happily accepts it, so the
// test suite would stay green and production would 500. Backticks are valid
// identifier quoting on both engines. The column has already been checked
// against an allowlist, so nothing here can be injected.
func aeoOrderClause(column, order string) string {
	if column == "id" {
		return "`id` " + order
	}
	return "`" + column + "` " + order + ", `id` " + order
}

type aeoRepository struct {
	db *gorm.DB
}

func NewAEORepository(db *gorm.DB) AEORepository {
	return &aeoRepository{db: db}
}

func (r *aeoRepository) WithTx(tx *gorm.DB) AEORepository {
	return &aeoRepository{db: tx}
}

// WithContext binds every statement of the returned repository to ctx. GORM
// hands the context to database/sql, so a cancelled ctx aborts a statement
// waiting for a pooled connection or for the database lock: on MySQL the driver
// kills the in-flight query, on SQLite the driver interrupts it at the next
// statement boundary. A nil ctx keeps the receiver's existing session.
func (r *aeoRepository) WithContext(ctx context.Context) AEORepository {
	if ctx == nil {
		return r
	}
	return &aeoRepository{db: r.db.WithContext(ctx)}
}

// ---------------------------------------------------------------------------
// Profile
// ---------------------------------------------------------------------------

// GetProfile returns the single brand profile. The service pins its ID to 1, so
// "the first row by primary key" and "the profile" are the same thing; absence
// surfaces as gorm.ErrRecordNotFound, which apperrors.IsNotFound classifies.
func (r *aeoRepository) GetProfile() (*models.AEOProfile, error) {
	var profile models.AEOProfile
	if err := r.db.First(&profile).Error; err != nil {
		return nil, err
	}
	return &profile, nil
}

// UpsertProfile writes editable fields atomically while preserving created_at.
// Each HTTP request supplies a fresh model, so Save would overwrite the existing
// creation time with zero, which strict MySQL rejects. Create keeps the JSON
// serialization hook on the path; the conflict clause also writes empty values.
func (r *aeoRepository) UpsertProfile(profile *models.AEOProfile) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		now := tx.NowFunc()
		if profile.CreatedAt.IsZero() {
			profile.CreatedAt = now
		}
		profile.UpdatedAt = now
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"updated_at", "brand_name", "description", "brand_aliases", "owned_domains", "competitors",
			}),
		}).Create(profile).Error; err != nil {
			return err
		}
		// The insert candidate has new timestamps even on a conflict. Reload
		// under the same transaction so the returned model matches the row.
		return tx.First(profile, profile.ID).Error
	})
}

// ---------------------------------------------------------------------------
// Prompts
// ---------------------------------------------------------------------------

func (r *aeoRepository) CreatePrompt(prompt *models.AEOPrompt) error {
	return r.db.Create(prompt).Error
}

func (r *aeoRepository) GetPromptByID(id uint) (*models.AEOPrompt, error) {
	var prompt models.AEOPrompt
	if err := r.db.First(&prompt, id).Error; err != nil {
		return nil, err
	}
	return &prompt, nil
}

func (r *aeoRepository) UpdatePrompt(prompt *models.AEOPrompt) error {
	return r.db.Save(prompt).Error
}

// DeletePrompt soft-deletes the prompt. A delete that matched nothing is
// reported as gorm.ErrRecordNotFound rather than silently succeeding, so the
// service can turn it into a 404 instead of a 204 for a row that never existed.
func (r *aeoRepository) DeletePrompt(id uint) error {
	result := r.db.Delete(&models.AEOPrompt{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *aeoRepository) ListPrompts(activeOnly bool, offset, limit int, sortBy, sortOrder string) ([]models.AEOPrompt, error) {
	column, order, err := validateAEOSort(aeoPromptSortColumns, sortBy, sortOrder)
	if err != nil {
		return nil, err
	}

	prompts := []models.AEOPrompt{}
	query := r.db.Model(&models.AEOPrompt{})
	if activeOnly {
		query = query.Where("is_active = ?", true)
	}

	err = query.Order(aeoOrderClause(column, order)).
		Offset(offset).Limit(limit).Find(&prompts).Error
	return prompts, err
}

func (r *aeoRepository) CountPrompts(activeOnly bool) (int64, error) {
	query := r.db.Model(&models.AEOPrompt{})
	if activeOnly {
		query = query.Where("is_active = ?", true)
	}

	var count int64
	err := query.Count(&count).Error
	return count, err
}

// ListActivePrompts returns every active prompt, oldest first. This is the run
// engine's input, so it is deliberately unpaginated — the service caps the
// active set at 100.
func (r *aeoRepository) ListActivePrompts() ([]models.AEOPrompt, error) {
	prompts := []models.AEOPrompt{}
	err := r.db.Where("is_active = ?", true).Order("id ASC").Find(&prompts).Error
	return prompts, err
}

// ExistsByTextInsensitive backs the service's duplicate-text pre-check over live
// rows only. excludeID is the row an update is allowed to collide with; pass 0
// for a create.
//
// LOWER() on both sides is what makes MySQL (case-insensitive collation) and
// SQLite (case-sensitive) agree — the same trap the labels feature hit.
func (r *aeoRepository) ExistsByTextInsensitive(text string, excludeID uint) (bool, error) {
	query := r.db.Model(&models.AEOPrompt{}).Where("LOWER(text) = LOWER(?)", text)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func (r *aeoRepository) CreateRun(run *models.AEORun) error {
	return r.db.Create(run).Error
}

func (r *aeoRepository) GetRunByID(id uint) (*models.AEORun, error) {
	var run models.AEORun
	if err := r.db.First(&run, id).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *aeoRepository) ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, error) {
	column, order, err := validateAEOSort(aeoRunSortColumns, sortBy, sortOrder)
	if err != nil {
		return nil, err
	}

	runs := []models.AEORun{}
	err = r.db.Model(&models.AEORun{}).
		Order(aeoOrderClause(column, order)).
		Offset(offset).Limit(limit).Find(&runs).Error
	return runs, err
}

func (r *aeoRepository) CountRuns() (int64, error) {
	var count int64
	err := r.db.Model(&models.AEORun{}).Count(&count).Error
	return count, err
}

// GetLatestRun returns the most recently started run, or (nil, nil) when no run
// has ever happened. "No runs yet" is an ordinary state of a fresh install, not
// an error the dashboard should have to classify.
func (r *aeoRepository) GetLatestRun() (*models.AEORun, error) {
	var run models.AEORun
	err := r.db.Order("started_at DESC").Order("id DESC").First(&run).Error
	if err != nil {
		if isAEONotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// CountRunsByStatus counts the runs in one status. Admission no longer depends
// on it (ClaimRun is atomic); it remains a plain read for reports and tests.
func (r *aeoRepository) CountRunsByStatus(status string) (int64, error) {
	var count int64
	err := r.db.Model(&models.AEORun{}).Where("status = ?", status).Count(&count).Error
	return count, err
}

// aeoRunningLock is the value of aeo_runs.running_lock while a run is running.
// The unique index idx_aeo_runs_running_lock admits one such row; NULL (every
// terminal row) never collides.
const aeoRunningLock uint8 = 1

// ClaimRun admits a run across every process sharing the database (#229). It
// inserts the row as running, owned by run.OwnerInstance, with heartbeat_at =
// started_at and running_lock = 1, in one INSERT: the unique running_lock
// index makes the admission atomic, so there is no count-then-insert window.
//
// A duplicate key inserts nothing and is classified: when run.ScheduleSlot is
// set and a row with that slot exists (soft-deleted rows included, since they
// still hold the index) it is apperrors.ErrAEOScheduleSlotTaken, otherwise
// apperrors.ErrRunInProgress. Any other failure is returned as is.
func (r *aeoRepository) ClaimRun(run *models.AEORun) error {
	if run == nil || run.OwnerInstance == nil || strings.TrimSpace(*run.OwnerInstance) == "" {
		return errors.New("aeo: a run claim needs an owner instance")
	}
	startedAt := run.StartedAt.UTC()
	heartbeatAt := startedAt
	lock := aeoRunningLock

	run.StartedAt = startedAt
	run.Status = models.AEORunStatusRunning
	run.CompletedAt = nil
	run.HeartbeatAt = &heartbeatAt
	run.RunningLock = &lock

	err := r.db.Create(run).Error
	if err == nil {
		return nil
	}
	run.ID = 0
	run.HeartbeatAt = nil
	run.RunningLock = nil
	if !isDuplicateKeyError(err) {
		return err
	}
	if run.ScheduleSlot != nil {
		var taken int64
		if countErr := r.db.Model(&models.AEORun{}).Unscoped().
			Where("schedule_slot = ?", *run.ScheduleSlot).
			Count(&taken).Error; countErr != nil {
			return countErr
		}
		if taken > 0 {
			return apperrors.ErrAEOScheduleSlotTaken
		}
	}
	return apperrors.ErrRunInProgress
}

// FinishRun writes the terminal state of a run its owner finalizes: status,
// completed_at, total_queries, failed_queries, updated_at and running_lock =
// NULL, in one UPDATE … WHERE id = ? AND status = 'running' AND
// owner_instance = ?. It never inserts and never touches a row another
// instance owns or has already failed: such a write matches nothing and
// returns apperrors.ErrAEORunNotOwned with the row unchanged. The status
// always changes on a match, so MySQL's changed-rows count is the matched
// count.
func (r *aeoRepository) FinishRun(run *models.AEORun) error {
	if run == nil || run.ID == 0 || run.OwnerInstance == nil {
		return apperrors.ErrAEORunNotOwned
	}
	if run.Status == "" || run.Status == models.AEORunStatusRunning {
		return fmt.Errorf("aeo: run %d cannot be finished with status %q", run.ID, run.Status)
	}
	now := time.Now().UTC()
	completedAt := now
	if run.CompletedAt != nil {
		completedAt = run.CompletedAt.UTC()
	}

	result := r.db.Model(&models.AEORun{}).
		Where("id = ? AND status = ? AND owner_instance = ?", run.ID, models.AEORunStatusRunning, *run.OwnerInstance).
		Updates(map[string]interface{}{
			"status":         run.Status,
			"completed_at":   completedAt,
			"total_queries":  run.TotalQueries,
			"failed_queries": run.FailedQueries,
			"updated_at":     now,
			"running_lock":   nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperrors.ErrAEORunNotOwned
	}
	run.CompletedAt = &completedAt
	run.UpdatedAt = now
	run.RunningLock = nil
	return nil
}

// RenewRunHeartbeat stamps heartbeat_at and updated_at with at on a run that is
// still running under owner, and touches nothing else. A row that is no longer
// running or belongs to another instance returns apperrors.ErrAEORunNotOwned.
//
// MySQL reports changed rows, so an UPDATE that writes the timestamp the row
// already holds affects zero rows; a zero count is therefore re-checked with a
// read of the same predicate before ownership is reported lost.
func (r *aeoRepository) RenewRunHeartbeat(id uint, owner string, at time.Time) error {
	at = at.UTC()
	result := r.db.Model(&models.AEORun{}).
		Where("id = ? AND status = ? AND owner_instance = ?", id, models.AEORunStatusRunning, owner).
		Updates(map[string]interface{}{
			"heartbeat_at": at,
			"updated_at":   at,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var owned int64
	if err := r.db.Model(&models.AEORun{}).
		Where("id = ? AND status = ? AND owner_instance = ?", id, models.AEORunStatusRunning, owner).
		Count(&owned).Error; err != nil {
		return err
	}
	if owned > 0 {
		return nil
	}
	return apperrors.ErrAEORunNotOwned
}

// FailAbandonedRuns fails, in one conditional UPDATE, every running row that
// started before startedBefore, or whose heartbeat_at is NULL (a row written
// before ownership existed) or older than heartbeatBefore. It sets status
// failed, completed_at, updated_at and running_lock = NULL and returns the row
// count. A running row with a fresh heartbeat and every terminal row are left
// alone, so any replica may call it at boot and before every start without
// failing another replica's live run. Concurrent sweeps change each row once:
// the status predicate stops matching after the first.
func (r *aeoRepository) FailAbandonedRuns(startedBefore, heartbeatBefore time.Time) (int64, error) {
	now := time.Now().UTC()
	result := r.db.Model(&models.AEORun{}).
		Where("status = ?", models.AEORunStatusRunning).
		Where("(started_at < ? OR heartbeat_at IS NULL OR heartbeat_at < ?)", startedBefore.UTC(), heartbeatBefore.UTC()).
		Updates(map[string]interface{}{
			"status":       models.AEORunStatusFailed,
			"completed_at": now,
			"updated_at":   now,
			"running_lock": nil,
		})
	return result.RowsAffected, result.Error
}

// ---------------------------------------------------------------------------
// Answers and citations
// ---------------------------------------------------------------------------

// CreateAnswerWithCitations writes the answer and its citations in one
// transaction: a citation row whose answer never landed is unreachable garbage,
// and an answer whose citations were lost silently understates the citation
// report. Nested inside an outer transaction this becomes a SAVEPOINT, so it is
// safe to call from a repository already bound to a tx via WithTx.
func (r *aeoRepository) CreateAnswerWithCitations(answer *models.AEOAnswer, citations []models.AEOCitation) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Omit the association: the citations are written explicitly below with
		// the answer id filled in, and letting GORM cascade the (empty) slice on
		// the model as well would either duplicate them or clear them.
		if err := tx.Omit("Citations").Create(answer).Error; err != nil {
			return err
		}

		if len(citations) == 0 {
			return nil
		}

		for i := range citations {
			citations[i].AnswerID = answer.ID
		}
		if err := tx.Create(&citations).Error; err != nil {
			return err
		}

		answer.Citations = citations
		return nil
	})
}

// ListAnswersByPrompt returns one prompt's answers newest first, each with its
// citations preloaded. A nil runID means every run.
func (r *aeoRepository) ListAnswersByPrompt(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, error) {
	answers := []models.AEOAnswer{}
	query := r.db.Preload("Citations").Where("prompt_id = ?", promptID)
	if runID != nil {
		query = query.Where("run_id = ?", *runID)
	}

	err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&answers).Error
	return answers, err
}

func (r *aeoRepository) CountAnswersByPrompt(promptID uint, runID *uint) (int64, error) {
	query := r.db.Model(&models.AEOAnswer{}).Where("prompt_id = ?", promptID)
	if runID != nil {
		query = query.Where("run_id = ?", *runID)
	}

	var count int64
	err := query.Count(&count).Error
	return count, err
}

// ---------------------------------------------------------------------------
// Metrics
//
// DUAL-DATABASE CONSTRAINT. Every statement below has to mean the same thing on
// MySQL 8 (production) and on in-memory SQLite (the whole test suite), so:
//
//   - no DATE(), DATE_FORMAT(), strftime(), EXTRACT() or any other date
//     function. Per-day bucketing happens in Go from ListAnswerFacts, the same
//     way LeadRepository.ConversionTimestampsSince does it;
//   - no JSON functions. competitor_mentions is a JSON document in a TEXT
//     column, so it is decoded and aggregated in Go;
//   - no aggregate over a time column. SQLite reports no declared type for an
//     expression like MAX(created_at), so the driver hands it back as a string
//     and the scan into time.Time is format-dependent. Where a "latest"
//     timestamp is needed, MAX(id) picks the row and a second keyed read
//     fetches its created_at from a real column. That is sound because
//     created_at is stamped at insert time and nothing ever backfills an
//     answer, so id order and created_at order are the same order;
//   - COALESCE around every SUM, because SUM over an empty group is NULL;
//   - joins are written against the tables directly, so GORM's soft-delete
//     scope (which only covers the model being queried) is spelled out by hand
//     for every joined table.
//
// The range convention is [from, to): `from` inclusive, `to` exclusive.
// ---------------------------------------------------------------------------

// aeoAnswerFactRow is the projection ListAnswerFacts reads. models.AEOAnswerFact
// cannot be scanned into directly: its CompetitorMentions is a decoded map with
// no column behind it, and Errored is derived rather than stored.
type aeoAnswerFactRow struct {
	AnswerID           uint      `gorm:"column:answer_id"`
	PromptID           uint      `gorm:"column:prompt_id"`
	Provider           string    `gorm:"column:provider"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	BrandMentioned     bool      `gorm:"column:brand_mentioned"`
	ErrorText          string    `gorm:"column:error_text"`
	CompetitorMentions string    `gorm:"column:competitor_mentions"`
}

// ListAnswerFacts returns one fact per answer in the range, oldest first,
// including the failed ones — the caller needs the failure count and decides
// itself which rows belong in a rate denominator.
func (r *aeoRepository) ListAnswerFacts(from, to time.Time) ([]models.AEOAnswerFact, error) {
	rows := []aeoAnswerFactRow{}
	err := r.db.Model(&models.AEOAnswer{}).
		Select("id AS answer_id, prompt_id, provider, created_at, brand_mentioned, "+
			"COALESCE(error, '') AS error_text, "+
			"COALESCE(competitor_mentions, '') AS competitor_mentions").
		Where("created_at >= ? AND created_at < ?", from, to).
		Order("created_at ASC").Order("id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	facts := make([]models.AEOAnswerFact, 0, len(rows))
	for _, row := range rows {
		facts = append(facts, models.AEOAnswerFact{
			AnswerID:           row.AnswerID,
			PromptID:           row.PromptID,
			Provider:           row.Provider,
			CreatedAt:          row.CreatedAt,
			BrandMentioned:     row.BrandMentioned,
			Errored:            strings.TrimSpace(row.ErrorText) != "",
			CompetitorMentions: models.DecodeCompetitorMentions(row.CompetitorMentions),
		})
	}
	return facts, nil
}

type aeoPromptVisibilityRow struct {
	PromptID     uint  `gorm:"column:prompt_id"`
	Answers      int64 `gorm:"column:answers"`
	Mentions     int64 `gorm:"column:mentions"`
	LastAnswerID uint  `gorm:"column:last_answer_id"`
}

type aeoAnswerTimeRow struct {
	ID        uint      `gorm:"column:id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// PromptVisibility aggregates the answer counts of the given prompts over the
// range, keyed by prompt id.
//
// Answers and Mentions count non-error rows only, so Mentions/Answers is the
// visibility ratio directly. LastRunAt deliberately does NOT apply that filter:
// it answers "when was this prompt last run", and a run that failed still ran.
// An empty promptIDs returns an empty map — the caller asks for the prompts it
// is about to render, the same convention as LabelRepository.FindByIDs.
func (r *aeoRepository) PromptVisibility(from, to time.Time, promptIDs []uint) (map[uint]models.AEOPromptVisibility, error) {
	result := map[uint]models.AEOPromptVisibility{}
	if len(promptIDs) == 0 {
		return result, nil
	}

	rows := []aeoPromptVisibilityRow{}
	err := r.db.Model(&models.AEOAnswer{}).
		Select("prompt_id, "+
			"COALESCE(SUM(CASE WHEN (error IS NULL OR error = '') THEN 1 ELSE 0 END), 0) AS answers, "+
			"COALESCE(SUM(CASE WHEN (error IS NULL OR error = '') AND brand_mentioned THEN 1 ELSE 0 END), 0) AS mentions, "+
			"COALESCE(MAX(id), 0) AS last_answer_id").
		Where("prompt_id IN ?", promptIDs).
		Where("created_at >= ? AND created_at < ?", from, to).
		Group("prompt_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return result, nil
	}

	lastIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.LastAnswerID != 0 {
			lastIDs = append(lastIDs, row.LastAnswerID)
		}
	}

	timestamps := map[uint]time.Time{}
	if len(lastIDs) > 0 {
		timeRows := []aeoAnswerTimeRow{}
		if err := r.db.Model(&models.AEOAnswer{}).
			Select("id, created_at").
			Where("id IN ?", lastIDs).
			Scan(&timeRows).Error; err != nil {
			return nil, err
		}
		for _, row := range timeRows {
			timestamps[row.ID] = row.CreatedAt
		}
	}

	for _, row := range rows {
		entry := models.AEOPromptVisibility{
			PromptID: row.PromptID,
			Answers:  row.Answers,
			Mentions: row.Mentions,
		}
		if ts, ok := timestamps[row.LastAnswerID]; ok {
			at := ts
			entry.LastRunAt = &at
		}
		result[row.PromptID] = entry
	}
	return result, nil
}

// CitationDomainStats aggregates the citations of non-error answers in the range
// by domain, carrying the ownership attribution the extraction stage stamped on
// each row plus how many of those citations sat in an answer that mentioned the
// brand. Ordered by citation count descending so the caller can take a top-N
// slice without re-sorting.
func (r *aeoRepository) CitationDomainStats(from, to time.Time) ([]models.AEOCitationAggRow, error) {
	rows := []models.AEOCitationAggRow{}
	err := r.db.Table("aeo_citations AS c").
		Select("c.domain AS domain, c.is_owned AS is_owned, "+
			"COALESCE(c.competitor_name, '') AS competitor_name, "+
			"COUNT(*) AS citations, "+
			"COALESCE(SUM(CASE WHEN a.brand_mentioned THEN 1 ELSE 0 END), 0) AS with_brand_mention").
		Joins("JOIN aeo_answers AS a ON a.id = c.answer_id AND a.deleted_at IS NULL").
		Where("c.deleted_at IS NULL").
		Where("a.created_at >= ? AND a.created_at < ?", from, to).
		Where("(a.error IS NULL OR a.error = '')").
		Group("c.domain, c.is_owned, COALESCE(c.competitor_name, '')").
		Order("citations DESC").
		Order("c.domain ASC").
		Scan(&rows).Error
	return rows, err
}

type aeoRangeCountRow struct {
	Total            int64 `gorm:"column:total"`
	WithBrandMention int64 `gorm:"column:with_brand_mention"`
}

// CountAnswersInRange returns the number of answers in the range and how many of
// them mentioned the brand. BOTH counts exclude failed answers, so `total` is
// directly usable as a rate denominator; a caller that needs the failure count
// takes it from ListAnswerFacts, which returns the error rows too.
func (r *aeoRepository) CountAnswersInRange(from, to time.Time) (int64, int64, error) {
	var row aeoRangeCountRow
	err := r.db.Model(&models.AEOAnswer{}).
		Select("COUNT(*) AS total, "+
			"COALESCE(SUM(CASE WHEN brand_mentioned THEN 1 ELSE 0 END), 0) AS with_brand_mention").
		Where("created_at >= ? AND created_at < ?", from, to).
		Where("(error IS NULL OR error = '')").
		Scan(&row).Error
	if err != nil {
		return 0, 0, err
	}
	return row.Total, row.WithBrandMention, nil
}

// CountAnswersWithCitations counts the non-error answers in the range that carry
// at least one citation. COUNT(DISTINCT …) over the join keeps an answer with
// five citations worth one, which is what the "answers with citations" rate
// means.
func (r *aeoRepository) CountAnswersWithCitations(from, to time.Time) (int64, error) {
	var count int64
	err := r.db.Table("aeo_answers AS a").
		Select("COUNT(DISTINCT a.id)").
		Joins("JOIN aeo_citations AS c ON c.answer_id = a.id AND c.deleted_at IS NULL").
		Where("a.deleted_at IS NULL").
		Where("a.created_at >= ? AND a.created_at < ?", from, to).
		Where("(a.error IS NULL OR a.error = '')").
		Scan(&count).Error
	return count, err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isAEONotFound keeps the gorm import local to the repository layer, where it
// belongs. apperrors.IsNotFound would also work, but this is the raw driver
// signal from a First() call and stays deliberately narrow.
func isAEONotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
