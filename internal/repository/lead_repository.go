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
	// Store custom created_at if provided (for imports)
	customCreatedAt := lead.CreatedAt

	// Create the lead (GORM will auto-set created_at to now)
	if err := r.db.Create(lead).Error; err != nil {
		return err
	}

	// If a custom created_at was provided, update it with raw SQL
	// (GORM auto-populates created_at, so we override after creation)
	if !customCreatedAt.IsZero() {
		return r.db.Model(lead).UpdateColumn("created_at", customCreatedAt).Error
	}

	return nil
}

func (r *leadRepository) GetByID(id uint) (*models.Lead, error) {
	var lead models.Lead
	err := r.db.First(&lead, id).Error
	if err != nil {
		return nil, err
	}
	return &lead, nil
}

func (r *leadRepository) GetByIDWithPreloads(id uint, preloads ...string) (*models.Lead, error) {
	var lead models.Lead
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.First(&lead, id).Error
	if err != nil {
		return nil, err
	}
	return &lead, nil
}

func (r *leadRepository) GetByExternalID(externalID string) (*models.Lead, error) {
	var lead models.Lead
	err := r.db.Where("external_id = ?", externalID).First(&lead).Error
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

func (r *leadRepository) GetByOwnerIDWithPreloads(ownerID uint, offset, limit int, preloads ...string) ([]models.Lead, error) {
	var leads []models.Lead
	query := r.db.Where("owner_id = ?", ownerID)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, error) {
	var leads []models.Lead
	err := r.db.Where("classification = ?", classification).Offset(offset).Limit(limit).Find(&leads).Error
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
	err := r.db.Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) ListWithPreloads(offset, limit int, preloads ...string) ([]models.Lead, error) {
	var leads []models.Lead
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error) {
	var leads []models.Lead
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if sortBy != "" {
		query = query.Order(sortBy + " " + sortOrder)
	}
	err := query.Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error) {
	var leads []models.Lead
	db := r.db
	for _, preload := range preloads {
		db = db.Preload(preload)
	}
	searchPattern := "%" + query + "%"
	db = db.Where(
		"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
		searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
	)
	if sortBy != "" {
		db = db.Order(sortBy + " " + sortOrder)
	}
	err := db.Offset(offset).Limit(limit).Find(&leads).Error
	return leads, err
}

func (r *leadRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.Lead{}).Where(
		"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
		searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

func (r *leadRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Lead{}).Count(&count).Error
	return count, err
}

func (r *leadRepository) CountByClassification(classification models.LeadClassification) (int64, error) {
	var count int64
	err := r.db.Model(&models.Lead{}).Where("classification = ?", classification).Count(&count).Error
	return count, err
}

func (r *leadRepository) ConvertToCustomer(leadID uint, customerID uint) error {
	return r.db.Model(&models.Lead{}).Where("id = ?", leadID).
		Updates(map[string]interface{}{
			"status": models.LeadStatusConverted,
			"customer_id": customerID,
		}).Error
}

func (r *leadRepository) WithTx(tx *gorm.DB) LeadRepository {
	return &leadRepository{db: tx}
}