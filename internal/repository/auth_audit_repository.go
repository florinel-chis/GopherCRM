package repository

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// RecordAggregated adds one outcome to the row of entry.AggregateKey (#466):
// one INSERT ... ON CONFLICT(aggregate_key) DO UPDATE SET attempts = attempts
// + 1 (ON DUPLICATE KEY UPDATE on MySQL and MariaDB), so the first outcome of
// the bucket writes the row and every later one only counts, with no
// read-then-write. It then reads the row's id by its key, because MySQL's
// LastInsertId after a duplicate-key update is not that id. On the root
// handle, outside any transaction, like Create.
func (r *authAuditRepository) RecordAggregated(entry *models.AuthAuditLog) (uint, error) {
	if entry.AggregateKey == nil || *entry.AggregateKey == "" {
		return 0, errors.New("aggregated audit row without an aggregate key")
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "aggregate_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"attempts": gorm.Expr("attempts + 1")}),
	}).Create(entry).Error; err != nil {
		return 0, err
	}
	var ids []uint
	if err := r.db.Model(&models.AuthAuditLog{}).
		Where("aggregate_key = ?", *entry.AggregateKey).
		Limit(1).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("aggregated audit row not found after its write")
	}
	return ids[0], nil
}

// PruneCreatedBefore deletes at most limit rows created before before, oldest
// id first: a plain read of the ids, then a delete by primary key that repeats
// the predicate, so no range of created_at is locked. Both statements run on
// ctx (#485).
func (r *authAuditRepository) PruneCreatedBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	db := r.db.WithContext(ctx)
	var ids []uint
	if err := db.Model(&models.AuthAuditLog{}).
		Where("created_at < ?", before.UTC()).
		Order("id ASC").Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := db.Where("id IN ? AND created_at < ?", ids, before.UTC()).Delete(&models.AuthAuditLog{})
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
