package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type authAuditRepository struct {
	db *gorm.DB
}

// NewAuthAuditRepository returns the auth_audit_log repository over db, the
// root handle.
func NewAuthAuditRepository(db *gorm.DB) AuthAuditRepository {
	return &authAuditRepository{db: db}
}

// Create inserts one row. The caller sets created_at, in UTC.
func (r *authAuditRepository) Create(entry *models.AuthAuditLog) error {
	return r.db.Create(entry).Error
}

// PruneCreatedBefore deletes at most limit rows created before before, oldest
// id first: a plain read of the ids, then a delete by primary key that repeats
// the predicate, so no range of created_at is locked.
func (r *authAuditRepository) PruneCreatedBefore(before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var ids []uint
	if err := r.db.Model(&models.AuthAuditLog{}).
		Where("created_at < ?", before.UTC()).
		Order("id ASC").Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.Where("id IN ? AND created_at < ?", ids, before.UTC()).Delete(&models.AuthAuditLog{})
	return result.RowsAffected, result.Error
}

// AuthAuditFilter narrows List (#455). An empty string or a nil pointer is no
// filter. From is inclusive and To exclusive; both are compared in UTC.
type AuthAuditFilter struct {
	Method  string
	Outcome string
	Reason  string
	UserID  *uint
	From    *time.Time
	To      *time.Time
}

// List returns one page of the rows matching filter, ordered created_at
// descending then id descending (a total order), and the number of matching
// rows. The count and the page share one WHERE; every predicate is bound.
func (r *authAuditRepository) List(filter AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error) {
	query := r.db.Model(&models.AuthAuditLog{})
	if filter.Method != "" {
		query = query.Where("method = ?", filter.Method)
	}
	if filter.Outcome != "" {
		query = query.Where("outcome = ?", filter.Outcome)
	}
	if filter.Reason != "" {
		query = query.Where("reason = ?", filter.Reason)
	}
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", filter.From.UTC())
	}
	if filter.To != nil {
		query = query.Where("created_at < ?", filter.To.UTC())
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []models.AuthAuditLog{}
	if err := query.Session(&gorm.Session{}).
		Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
