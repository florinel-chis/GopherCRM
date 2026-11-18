package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type roleAuditRepository struct {
	db *gorm.DB
}

// NewRoleAuditRepository returns the persistence of role_audit_log (#348).
func NewRoleAuditRepository(db *gorm.DB) RoleAuditRepository {
	return &roleAuditRepository{db: db}
}

func (r *roleAuditRepository) WithTx(tx *gorm.DB) RoleAuditRepository {
	return &roleAuditRepository{db: tx}
}

// Create appends one audit row, stamped with the current UTC time.
func (r *roleAuditRepository) Create(entry *models.RoleAuditEntry) error {
	entry.CreatedAt = r.db.NowFunc().UTC()
	return r.db.Create(entry).Error
}

// List returns one page of the audit log and the total, both under the same
// filter. Without a sort column the newest row comes first (created_at
// descending, then id descending); with one, the validated column is followed
// by the id in the same direction. A column outside the allowlist is an error
// and no query runs.
func (r *roleAuditRepository) List(filter RoleAuditFilter, offset, limit int, sortBy, sortOrder string) ([]models.RoleAuditEntry, int64, error) {
	if sortBy == "" {
		sortBy, sortOrder = "created_at", "desc"
	}
	filtered := func() *gorm.DB {
		query := r.db.Model(&models.RoleAuditEntry{})
		if filter.RoleID != nil {
			query = query.Where("role_id = ?", *filter.RoleID)
		}
		if filter.TargetUserID != nil {
			query = query.Where("target_user_id = ?", *filter.TargetUserID)
		}
		return query
	}
	ordered, err := orderPage(filtered(), "role_audit_log", sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := filtered().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	entries := []models.RoleAuditEntry{}
	if err := ordered.Offset(offset).Limit(limit).Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}
