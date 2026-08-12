package repository

import (
	"fmt"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
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
// including spam ones: the list column reports what arrived, and a form whose
// only traffic is spam is exactly what an admin needs to see. Forms with no
// submissions are absent from the map rather than present with a zero.
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

func (r *formRepository) MarkConfirmationTokenUsed(id uint) error {
	now := time.Now()
	return r.db.Model(&models.FormConfirmationToken{}).
		Where("id = ?", id).
		Update("used_at", &now).Error
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
