package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type bulkOperationRepository struct {
	db *gorm.DB
}

func NewBulkOperationRepository(db *gorm.DB) BulkOperationRepository {
	return &bulkOperationRepository{db: db}
}

func (r *bulkOperationRepository) Create(operation *models.BulkOperation) error {
	return r.db.Create(operation).Error
}

func (r *bulkOperationRepository) UpdateStatus(id uint, status models.BulkOperationStatus) error {
	return r.db.Model(&models.BulkOperation{}).
		Where("id = ?", id).
		Update("status", status).Error
}
