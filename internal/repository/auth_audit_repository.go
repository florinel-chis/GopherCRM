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
