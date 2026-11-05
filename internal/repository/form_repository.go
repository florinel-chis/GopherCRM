package repository

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// formSortColumns is the sort allowlist for the form list.
//
// It is local to this file for the same reason the AEO allowlists are: the
// forms table is its only consumer, and validateFormSort below reproduces
// utils.ValidateSort's semantics exactly, so the guarantee is unchanged — no
// ORDER BY fragment is ever built from a string that did not come out of an
// allowlist.
var formSortColumns = map[string]bool{
	"id":         true,
	"name":       true,
	"status":     true,
	"created_at": true,
	"updated_at": true,
}

// validateFormSort mirrors utils.ValidateSort: an empty sortBy falls back to
// ("created_at", "desc"), a column outside the allowlist is an error, and a
// sortOrder that is neither "asc" nor "desc" degrades to "desc".
func validateFormSort(sortBy, sortOrder string) (string, string, error) {
	if sortBy == "" {
		return "created_at", "desc", nil
	}
	if !formSortColumns[sortBy] {
		return "", "", fmt.Errorf("invalid sort column %q for forms", sortBy)
	}

	order := strings.ToLower(strings.TrimSpace(sortOrder))
	if order != "asc" && order != "desc" {
		order = "desc"
	}
	return sortBy, order, nil
}

// formOrderClause builds the ORDER BY from an already-validated column and
// direction, with id as a tie-breaker so a page boundary cannot drop or repeat
// a row when several rows share the sort value.
//
// The identifiers are backtick-quoted, which both MySQL 8 and SQLite accept:
// the column has been checked against an allowlist, so this is not about
// injection but about keeping a column name that happens to collide with a
// reserved word from turning into a syntax error on MySQL only.
func formOrderClause(column, order string) string {
	if column == "id" {
		return "`id` " + order
	}
	return "`" + column + "` " + order + ", `id` " + order
}

type formRepository struct {
	db *gorm.DB
}

func NewFormRepository(db *gorm.DB) FormRepository {
	return &formRepository{db: db}
}

func (r *formRepository) WithTx(tx *gorm.DB) FormRepository {
	return &formRepository{db: tx}
}

// ---------------------------------------------------------------------------
// Forms
// ---------------------------------------------------------------------------

func (r *formRepository) Create(form *models.Form) error {
	return r.db.Create(form).Error
}

func (r *formRepository) GetByID(id uint) (*models.Form, error) {
	var form models.Form
	if err := r.db.First(&form, id).Error; err != nil {
		return nil, err
	}
	return &form, nil
}

// GetByPublicID looks a form up by the identifier the outside world knows,
// whatever its status. Filtering unpublished forms out is the service's job:
// the repository cannot tell the admin preview from a public visitor.
func (r *formRepository) GetByPublicID(publicID string) (*models.Form, error) {
	var form models.Form
	if err := r.db.Where("public_id = ?", publicID).First(&form).Error; err != nil {
		return nil, err
	}
	return &form, nil
}

// List returns one page of forms plus the total matching the same filter, so
// the caller can build pagination metadata from a single call.
func (r *formRepository) List(offset, limit int, status string, sortBy, sortOrder string) ([]models.Form, int64, error) {
	column, order, err := validateFormSort(sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}

	filtered := func() *gorm.DB {
		query := r.db.Model(&models.Form{})
		if status != "" {
			query = query.Where("status = ?", status)
		}
		return query
	}

	var total int64
	if err := filtered().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	forms := []models.Form{}
	err = filtered().
		Order(formOrderClause(column, order)).
		Offset(offset).
		Limit(limit).
		Find(&forms).Error
	if err != nil {
		return nil, 0, err
	}
	return forms, total, nil
}

// Update writes the whole row, which keeps the model's BeforeSave hook (and
// with it the JSON column encoding) on the path.
func (r *formRepository) Update(form *models.Form) error {
	return r.db.Save(form).Error
}

// Delete soft-deletes the form. Its submissions are deliberately left alone:
// they are the record of what real people sent, and removing the form they
// came through must not erase them.
func (r *formRepository) Delete(id uint) error {
	result := r.db.Delete(&models.Form{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SubmissionCounts counts the live submissions of every requested form,
// including spam ones: the list column reports what is still stored, and a form
// whose only traffic is spam is exactly what an admin needs to see. Rows the
// retention purge removed no longer count. Forms with no submissions are absent
// from the map rather than present with a zero.
func (r *formRepository) SubmissionCounts(formIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(formIDs))
	if len(formIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		FormID uint
		Total  int64
	}
	err := r.db.Model(&models.FormSubmission{}).
		Select("form_id, COUNT(*) as total").
		Where("form_id IN ?", formIDs).
		Group("form_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		counts[row.FormID] = row.Total
	}
	return counts, nil
}

// ---------------------------------------------------------------------------
// Submissions
// ---------------------------------------------------------------------------

func (r *formRepository) CreateSubmission(sub *models.FormSubmission) error {
	return r.db.Create(sub).Error
}

func (r *formRepository) GetSubmissionByID(id uint) (*models.FormSubmission, error) {
	var submission models.FormSubmission
	if err := r.db.First(&submission, id).Error; err != nil {
		return nil, err
	}
	return &submission, nil
}

// ListSubmissions returns one page of a form's submissions, newest first, plus
// the total matching the same filter.
func (r *formRepository) ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
	filtered := func() *gorm.DB {
		query := r.db.Model(&models.FormSubmission{}).Where("form_id = ?", formID)
		if status != "" {
			query = query.Where("status = ?", status)
		}
		return query
	}

	var total int64
	if err := filtered().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	submissions := []models.FormSubmission{}
	err := filtered().
		Order("`created_at` desc, `id` desc").
		Offset(offset).
		Limit(limit).
		Find(&submissions).Error
	if err != nil {
		return nil, 0, err
	}
	return submissions, total, nil
}

func (r *formRepository) UpdateSubmission(sub *models.FormSubmission) error {
	return r.db.Save(sub).Error
}

// ---------------------------------------------------------------------------
// Confirmation tokens
// ---------------------------------------------------------------------------

func (r *formRepository) CreateConfirmationToken(t *models.FormConfirmationToken) error {
	return r.db.Create(t).Error
}

// GetConfirmationTokenByHash returns only spendable tokens: unused and
// unexpired. Used, expired and unknown tokens all surface as
// gorm.ErrRecordNotFound so a visitor — or a bot walking the confirm endpoint —
// cannot tell the three apart.
func (r *formRepository) GetConfirmationTokenByHash(hash string) (*models.FormConfirmationToken, error) {
	var token models.FormConfirmationToken
	err := r.db.Where("token_hash = ? AND used_at IS NULL AND expires_at > ?",
		hash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// MarkConfirmationTokenUsed claims a token: it stamps used_at only while the
// row is still unused and strictly unexpired, and reports
// gorm.ErrRecordNotFound when nothing was claimed. The two predicates are part
// of the UPDATE itself rather than checked beforehand, so two callers that both
// read the token as spendable cannot both claim it — whichever UPDATE runs
// second finds the row already stamped (or expired meanwhile) and affects no
// row. Exactly one affected row is the only success; the caller runs the claim
// inside the transaction that confirms the submission, so a rollback leaves the
// token usable for a retry.
func (r *formRepository) MarkConfirmationTokenUsed(id uint) error {
	now := time.Now()
	result := r.db.Model(&models.FormConfirmationToken{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", id, now).
		Update("used_at", &now)
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return gorm.ErrRecordNotFound
	default:
		// The primary key makes this unreachable; it is still refused rather
		// than reported as a single-use claim.
		return fmt.Errorf("confirmation token claim affected %d rows", result.RowsAffected)
	}
}

// InvalidatePendingTokens spends every outstanding token belonging to a pending
// submission of this form with this address, so re-submitting the same form
// leaves exactly one working confirmation link — the newest one.
//
// The submission match is expressed as a GORM subquery rather than a join or a
// raw statement: an UPDATE ... JOIN is MySQL-specific syntax that SQLite would
// reject, and the whole test suite runs on SQLite.
func (r *formRepository) InvalidatePendingTokens(formID uint, email string) error {
	pendingSubmissions := r.db.Model(&models.FormSubmission{}).
		Select("id").
		Where("form_id = ? AND email = ? AND status = ?", formID, email, models.FormSubmissionPending)

	now := time.Now()
	return r.db.Model(&models.FormConfirmationToken{}).
		Where("used_at IS NULL").
		Where("submission_id IN (?)", pendingSubmissions).
		Update("used_at", &now).Error
}

// ---------------------------------------------------------------------------
// Retention purge
// ---------------------------------------------------------------------------

// errFormPurgeOutsideTransaction refuses a chunk on the root handle: its locks
// would be released statement by statement and a failed delete could not be
// undone.
var errFormPurgeOutsideTransaction = errors.New("form submission purge must run inside a transaction")

// PurgeSubmissionChunk removes one chunk in the lock order a confirmation uses,
// token first and submission second, and takes both before its first delete.
//
//  1. The candidates and their token ids come from plain reads, so selection
//     holds no lock and cannot block anything on a table it merely scanned.
//  2. The tokens are locked by primary key. This is where the chunk waits for a
//     confirmation that has claimed a token, or for a re-submission that has
//     spent one.
//  3. The submissions are locked by primary key, and the rule is re-checked on
//     what those locking reads return. On MySQL they are current reads, so a
//     row a confirmation just confirmed or a competing sweep just removed is
//     seen as it now is, whatever the transaction's snapshot says.
//  4. Tokens are deleted before their submissions, by primary key. Everything
//     is Unscoped, so soft-deleted rows go too and the removal is permanent.
//
// Locking by primary key alone is deliberate: a predicate on status or
// created_at could lead InnoDB through a secondary index, whose next-key locks
// would block a new submission's insert for as long as the chunk runs.
func (r *formRepository) PurgeSubmissionChunk(q FormSubmissionPurgeQuery) (FormSubmissionPurgeChunk, error) {
	var chunk FormSubmissionPurgeChunk
	if _, inTx := r.db.Statement.ConnPool.(gorm.TxCommitter); !inTx {
		return chunk, errFormPurgeOutsideTransaction
	}
	if q.Limit <= 0 {
		return chunk, fmt.Errorf("form submission purge limit must be positive, got %d", q.Limit)
	}
	if q.Status != models.FormSubmissionSpam && q.Status != models.FormSubmissionPending {
		return chunk, fmt.Errorf("form submission purge does not remove status %q", q.Status)
	}

	candidates, err := r.purgeCandidateIDs(q)
	if err != nil {
		return chunk, err
	}
	chunk.Selected = len(candidates)
	if len(candidates) == 0 {
		return chunk, nil
	}
	chunk.LastID = candidates[len(candidates)-1]

	tokens, err := r.lockPurgeTokens(candidates)
	if err != nil {
		return chunk, err
	}
	locked, err := r.lockPurgeSubmissions(candidates)
	if err != nil {
		return chunk, err
	}

	spendable := make(map[uint]bool)
	for i := range tokens {
		token := &tokens[i]
		if !token.DeletedAt.Valid && token.UsedAt == nil && token.ExpiresAt.After(q.Now) {
			spendable[token.SubmissionID] = true
		}
	}
	doomed := make(map[uint]bool, len(locked))
	submissionIDs := make([]uint, 0, len(locked))
	for i := range locked {
		row := &locked[i]
		if row.Status != q.Status || row.LeadID != nil {
			continue
		}
		if q.Status == models.FormSubmissionPending && spendable[row.ID] {
			continue
		}
		doomed[row.ID] = true
		submissionIDs = append(submissionIDs, row.ID)
	}
	if len(submissionIDs) == 0 {
		return chunk, nil
	}

	tokenIDs := make([]uint, 0, len(tokens))
	for i := range tokens {
		if doomed[tokens[i].SubmissionID] {
			tokenIDs = append(tokenIDs, tokens[i].ID)
		}
	}
	if len(tokenIDs) > 0 {
		result := r.db.Unscoped().Where("id IN ?", tokenIDs).Delete(&models.FormConfirmationToken{})
		if result.Error != nil {
			return chunk, result.Error
		}
		chunk.Tokens = result.RowsAffected
	}

	result := r.db.Unscoped().Where("id IN ?", submissionIDs).Delete(&models.FormSubmission{})
	if result.Error != nil {
		return chunk, result.Error
	}
	chunk.Submissions = result.RowsAffected
	return chunk, nil
}

// purgeCandidateIDs reads the next ids of the walk, oldest id first. Only a
// pending row is protected by a spendable token: a spam row can never be
// confirmed, so a token left on one protects nothing. A soft-deleted token is
// not spendable, because the confirmation's lookup cannot find it.
func (r *formRepository) purgeCandidateIDs(q FormSubmissionPurgeQuery) ([]uint, error) {
	query := r.db.Unscoped().Model(&models.FormSubmission{}).
		Where("status = ? AND lead_id IS NULL AND created_at < ? AND id > ?", q.Status, q.Cutoff.UTC(), q.AfterID)
	if q.Status == models.FormSubmissionPending {
		query = query.Where(`NOT EXISTS (SELECT 1 FROM form_confirmation_tokens spendable
			WHERE spendable.submission_id = form_submissions.id
			AND spendable.deleted_at IS NULL
			AND spendable.used_at IS NULL
			AND spendable.expires_at > ?)`, q.Now)
	}
	var ids []uint
	err := query.Order("id").Limit(q.Limit).Pluck("id", &ids).Error
	return ids, err
}

// lockPurgeTokens locks every token of the candidates by primary key, in id
// order. The ids can be read first without a lock because a token is only ever
// inserted together with its own new submission, so no candidate gains one.
func (r *formRepository) lockPurgeTokens(submissionIDs []uint) ([]models.FormConfirmationToken, error) {
	var ids []uint
	err := r.db.Unscoped().Model(&models.FormConfirmationToken{}).
		Where("submission_id IN ?", submissionIDs).
		Order("id").
		Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return nil, err
	}

	var tokens []models.FormConfirmationToken
	err = r.db.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).
		Order("id").
		Find(&tokens).Error
	return tokens, err
}

// lockPurgeSubmissions locks the candidates by primary key, in id order, and
// reads only what the re-check needs: the visitor's values stay out of memory.
func (r *formRepository) lockPurgeSubmissions(ids []uint) ([]models.FormSubmission, error) {
	var rows []models.FormSubmission
	err := r.db.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status", "lead_id").
		Where("id IN ?", ids).
		Order("id").
		Find(&rows).Error
	return rows, err
}
