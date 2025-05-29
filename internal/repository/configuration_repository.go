package repository

import (
	"github.com/florinel-chis/gocrm/internal/models"
	"gorm.io/gorm"
)

type ConfigurationRepository interface {
	GetByKey(key string) (*models.Configuration, error)
	GetByCategory(category models.ConfigurationCategory) ([]models.Configuration, error)
	GetAll() ([]models.Configuration, error)
	Create(config *models.Configuration) error
	Update(config *models.Configuration) error
	Delete(key string) error
	BulkUpsert(configs []models.Configuration) error
	InitializeDefaults() error
}

type configurationRepository struct {
	db *gorm.DB
}

func NewConfigurationRepository(db *gorm.DB) ConfigurationRepository {
	return &configurationRepository{db: db}
}

func (r *configurationRepository) GetByKey(key string) (*models.Configuration, error) {
	var config models.Configuration
	err := r.db.Where("config_key = ?", key).First(&config).Error
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *configurationRepository) GetByCategory(category models.ConfigurationCategory) ([]models.Configuration, error) {
	var configs []models.Configuration
	err := r.db.Where("category = ?", category).Order("config_key").Find(&configs).Error
	return configs, err
}

func (r *configurationRepository) GetAll() ([]models.Configuration, error) {
	var configs []models.Configuration
	err := r.db.Order("category, config_key").Find(&configs).Error
	return configs, err
}

func (r *configurationRepository) Create(config *models.Configuration) error {
	return r.db.Create(config).Error
}

func (r *configurationRepository) Update(config *models.Configuration) error {
	return r.db.Save(config).Error
}

func (r *configurationRepository) Delete(key string) error {
	return r.db.Where("config_key = ? AND is_system = false", key).Delete(&models.Configuration{}).Error
}

func (r *configurationRepository) BulkUpsert(configs []models.Configuration) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, config := range configs {
			var existing models.Configuration
			err := tx.Where("config_key = ?", config.Key).First(&existing).Error
			
			if err == gorm.ErrRecordNotFound {
				// Create new configuration
				if err := tx.Create(&config).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				// Update existing configuration if not read-only
				if !existing.IsReadOnly {
					config.ID = existing.ID
					config.CreatedAt = existing.CreatedAt
					if err := tx.Save(&config).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (r *configurationRepository) InitializeDefaults() error {
	defaults := models.DefaultConfigurations()
	return r.BulkUpsert(defaults)
}