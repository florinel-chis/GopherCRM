package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type leadRepository struct {
	db *gorm.DB
}

func NewLeadRepository(db *gorm.DB) LeadRepository {
	return &leadRepository{db: db}
}

func (r *leadRepository) Create(lead *models.Lead) error {
	return r.db.Create(lead).Error
}

func (r *leadRepository) GetByID(id uint) (*models.Lead, error) {
	var lead models.Lead
	err := r.db.Preload("Owner").First(&lead, id).Error
	if err != nil {
		return nil, err
	}
	return &lead, nil
}

func (r *leadRepository) GetByOwnerID(ownerID uint, offset, limit int) ([]models.Lead, error) {
	var leads []models.Lead
	err := r.db.Where("owner_id = ?", ownerID).Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) Update(lead *models.Lead) error {
	return r.db.Save(lead).Error
}

func (r *leadRepository) Delete(id uint) error {
	return r.db.Delete(&models.Lead{}, id).Error
}

func (r *leadRepository) List(offset, limit int) ([]models.Lead, error) {
	var leads []models.Lead
	err := r.db.Preload("Owner").Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Lead{}).Count(&count).Error
	return count, err
}

func (r *leadRepository) ConvertToCustomer(leadID uint, customerID uint) error {
	return r.db.Model(&models.Lead{}).Where("id = ?", leadID).
		Updates(map[string]interface{}{
			"status": models.LeadStatusConverted,
			"customer_id": customerID,
		}).Error
}