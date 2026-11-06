package repository

import (
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Form mail delivery records (#216). Every state change is one conditional
// UPDATE by primary key whose WHERE clause carries the state it moves from, so
// two callers that both read a row as claimable cannot both claim it: the
// second UPDATE matches nothing. Each one changes the status column, which is
// why MySQL's changed-rows count equals the matched count here.

func (r *formRepository) CreateDelivery(d *models.FormMailDelivery) error {
	return r.db.Create(d).Error
}

func (r *formRepository) GetDeliveryByID(id uint) (*models.FormMailDelivery, error) {
	var delivery models.FormMailDelivery
	if err := r.db.First(&delivery, id).Error; err != nil {
		return nil, err
	}
	return &delivery, nil
}

func (r *formRepository) ClaimPendingDelivery(id uint, now time.Time) error {
	result := r.db.Model(&models.FormMailDelivery{}).
		Where("id = ? AND status = ?", id, models.FormMailStatusPending).
		Updates(claimAssignments(now))
	return exactlyOneDeliveryRow(result, "claim")
}

func (r *formRepository) ClaimDeliveryForRetry(id uint, now, stuckBefore time.Time) error {
	query := r.db.Model(&models.FormMailDelivery{}).Where("id = ?", id)
	result := retryableDeliveryPredicate(query, stuckBefore).Updates(claimAssignments(now))
	return exactlyOneDeliveryRow(result, "retry claim")
}

func (r *formRepository) FinishDelivery(id uint, outcome FormMailDeliveryOutcome) error {
	switch outcome.Status {
	case models.FormMailStatusSent, models.FormMailStatusFailed, models.FormMailStatusLogOnly, models.FormMailStatusSkipped:
	default:
		return fmt.Errorf("form mail delivery cannot be finished as %q", outcome.Status)
	}
	result := r.db.Model(&models.FormMailDelivery{}).
		Where("id = ? AND status = ?", id, models.FormMailStatusSending).
		Updates(map[string]interface{}{
			"status":     outcome.Status,
			"error_code": outcome.ErrorCode,
			"sent_at":    outcome.SentAt,
			"updated_at": outcome.Now.UTC(),
		})
	return exactlyOneDeliveryRow(result, "stamp")
}

func (r *formRepository) ListAttentionDeliveries(q FormMailDeliveryQuery) ([]models.FormMailDelivery, int64, error) {
	filtered := func() *gorm.DB {
		query := r.db.Model(&models.FormMailDelivery{})
		if q.FormID != 0 {
			query = query.Where("form_id = ?", q.FormID)
		}
		if q.Kind != "" {
			query = query.Where("kind = ?", q.Kind)
		}
		if q.Status != "" {
			query = query.Where("status = ?", q.Status)
		} else {
			query = retryableDeliveryPredicate(query, q.StuckBefore)
		}
		return query
	}

	var total int64
	if err := filtered().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	rows := []models.FormMailDelivery{}
	err := filtered().
		Order("id desc").
		Offset(q.Offset).
		Limit(q.Limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *formRepository) ListDeliveriesForSubmissions(submissionIDs []uint) ([]models.FormMailDelivery, error) {
	rows := []models.FormMailDelivery{}
	if len(submissionIDs) == 0 {
		return rows, nil
	}
	err := r.db.Where("submission_id IN ?", submissionIDs).Order("id").Find(&rows).Error
	return rows, err
}

func (r *formRepository) LockSubmissionByID(id uint) (*models.FormSubmission, error) {
	var submission models.FormSubmission
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).
		First(&submission).Error
	if err != nil {
		return nil, err
	}
	return &submission, nil
}

func (r *formRepository) GetFormIncludingDeleted(id uint) (*models.Form, error) {
	var form models.Form
	if err := r.db.Unscoped().First(&form, id).Error; err != nil {
		return nil, err
	}
	return &form, nil
}

func (r *formRepository) HasNewerSubmission(formID uint, email string, afterID uint) (bool, error) {
	var ids []uint
	err := r.db.Model(&models.FormSubmission{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("form_id = ? AND email = ? AND id > ? AND status <> ?", formID, email, afterID, models.FormSubmissionSpam).
		Order("id").
		Limit(1).
		Pluck("id", &ids).Error
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

func (r *formRepository) ListConfirmationTokenIDs(submissionID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.FormConfirmationToken{}).
		Where("submission_id = ?", submissionID).
		Order("id").
		Pluck("id", &ids).Error
	return ids, err
}

func (r *formRepository) ReArmConfirmationToken(id uint, tokenHash string, expiresAt time.Time) error {
	result := r.db.Model(&models.FormConfirmationToken{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"token_hash": tokenHash,
			"expires_at": expiresAt,
			"used_at":    nil,
		})
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return gorm.ErrRecordNotFound
	default:
		return fmt.Errorf("confirmation token re-arm affected %d rows", result.RowsAffected)
	}
}

// claimAssignments is what both claims write: the row moves to sending, the
// attempt is counted and the clock is recorded. updated_at is written from
// the caller's clock in UTC, the zone the stuck threshold is compared in;
// SQLite compares timestamps as text, so both sides must share it.
func claimAssignments(now time.Time) map[string]interface{} {
	now = now.UTC()
	return map[string]interface{}{
		"status":          models.FormMailStatusSending,
		"attempts":        gorm.Expr("attempts + ?", 1),
		"last_attempt_at": now,
		"updated_at":      now,
	}
}

// retryableDeliveryPredicate selects failed and log_only rows, and pending or
// sending rows last updated before stuckBefore.
func retryableDeliveryPredicate(query *gorm.DB, stuckBefore time.Time) *gorm.DB {
	return query.Where("(status IN ? OR (status IN ? AND updated_at < ?))",
		[]string{models.FormMailStatusFailed, models.FormMailStatusLogOnly},
		[]string{models.FormMailStatusPending, models.FormMailStatusSending},
		stuckBefore.UTC())
}

func exactlyOneDeliveryRow(result *gorm.DB, what string) error {
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return gorm.ErrRecordNotFound
	default:
		return fmt.Errorf("form mail delivery %s affected %d rows", what, result.RowsAffected)
	}
}
