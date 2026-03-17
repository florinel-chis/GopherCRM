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

func (r *bulkOperationRepository) WithTx(tx *gorm.DB) BulkOperationRepository {
	return &bulkOperationRepository{db: tx}
}

func (r *bulkOperationRepository) Create(operation *models.BulkOperation) error {
	return r.db.Create(operation).Error
}

func (r *bulkOperationRepository) GetByID(id uint) (*models.BulkOperation, error) {
	var operation models.BulkOperation
	err := r.db.First(&operation, id).Error
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

func (r *bulkOperationRepository) GetByIDWithItems(id uint) (*models.BulkOperation, error) {
	var operation models.BulkOperation
	err := r.db.Preload("Items").First(&operation, id).Error
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

func (r *bulkOperationRepository) GetByUserID(userID uint, offset, limit int) ([]models.BulkOperation, error) {
	var operations []models.BulkOperation
	err := r.db.Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&operations).Error
	return operations, err
}

func (r *bulkOperationRepository) Update(operation *models.BulkOperation) error {
	return r.db.Save(operation).Error
}

func (r *bulkOperationRepository) UpdateStatus(id uint, status models.BulkOperationStatus) error {
	return r.db.Model(&models.BulkOperation{}).
		Where("id = ?", id).
		Update("status", status).Error
}

func (r *bulkOperationRepository) Delete(id uint) error {
	return r.db.Delete(&models.BulkOperation{}, id).Error
}

func (r *bulkOperationRepository) List(offset, limit int) ([]models.BulkOperation, error) {
	var operations []models.BulkOperation
	err := r.db.Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&operations).Error
	return operations, err
}

func (r *bulkOperationRepository) CreateItem(item *models.BulkOperationItem) error {
	return r.db.Create(item).Error
}

func (r *bulkOperationRepository) UpdateItem(item *models.BulkOperationItem) error {
	return r.db.Save(item).Error
}

func (r *bulkOperationRepository) GetItemsByOperationID(operationID uint) ([]models.BulkOperationItem, error) {
	var items []models.BulkOperationItem
	err := r.db.Where("bulk_operation_id = ?", operationID).
		Order("created_at ASC").
		Find(&items).Error
	return items, err
}