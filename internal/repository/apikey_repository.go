package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type apiKeyRepository struct {
	db *gorm.DB
}

func NewAPIKeyRepository(db *gorm.DB) APIKeyRepository {
	return &apiKeyRepository{db: db}
}

func (r *apiKeyRepository) Create(apiKey *models.APIKey) error {
	return r.db.Create(apiKey).Error
}

func (r *apiKeyRepository) GetByID(id uint) (*models.APIKey, error) {
	var apiKey models.APIKey
	err := r.db.Preload("User").First(&apiKey, id).Error
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func (r *apiKeyRepository) GetByKeyHash(keyHash string) (*models.APIKey, error) {
	var apiKey models.APIKey
	err := r.db.Preload("User").Where("key_hash = ? AND is_active = ?", keyHash, true).First(&apiKey).Error
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func (r *apiKeyRepository) GetByUserID(userID uint) ([]models.APIKey, error) {
	var apiKeys []models.APIKey
	err := r.db.Where("user_id = ?", userID).Find(&apiKeys).Error
	return apiKeys, err
}

func (r *apiKeyRepository) Update(apiKey *models.APIKey) error {
	return r.db.Save(apiKey).Error
}

func (r *apiKeyRepository) Delete(id uint) error {
	return r.db.Delete(&models.APIKey{}, id).Error
}

func (r *apiKeyRepository) UpdateLastUsed(id uint) error {
	now := time.Now()
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("last_used_at", &now).Error
}