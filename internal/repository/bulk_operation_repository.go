package repository

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type bulkOperationRepository struct {
	db *gorm.DB
}

func NewBulkOperationRepository(db *gorm.DB) BulkOperationRepository {
	return &bulkOperationRepository{db: db}
}

// Create writes the history row on the caller's context, so a request that
// has gone away stops waiting for a connection instead of recording an
// operation that will never run.
func (r *bulkOperationRepository) Create(ctx context.Context, operation *models.BulkOperation) error {
	return r.db.WithContext(ctx).Create(operation).Error
}

func (r *bulkOperationRepository) UpdateStatus(id uint, status models.BulkOperationStatus) error {
	return r.db.Model(&models.BulkOperation{}).
		Where("id = ?", id).
		Update("status", status).Error
}
