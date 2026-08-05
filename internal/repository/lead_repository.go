package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
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

// Delete erases a lead under the GDPR right to erasure (Article 17).
//
// A lead is personal data in exactly the same way a user or a customer is — it
// holds a name, an email address, a phone number, an employer, a job title and
// free-text notes about a real person who never asked to be in the CRM at all.
// It used to be the one entity whose "delete" was a bare soft delete, which
// hid the row from queries while leaving every one of those fields intact in
// the table, and left the address locked in for good.
//
// So the row is anonymised in place and only then soft-deleted, atomically, the
// same way users and customers are; see erasure.go for the full rationale and
// leadErasurePlan for the authoritative list of what is scrubbed.
//
// If the lead was converted, the customer it became is erased too: the
// conversion copied the person's data into that customer, so erasing only the
// lead would leave the copy behind. See erasure_cascade.go.
//
// The transaction is the caller's when this repository was obtained through
// WithTx, and a fresh one otherwise.
func (r *leadRepository) Delete(id uint) error {
	return eraseLeadWithConversionLink(r.db, id)
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
		orderClause, err := utils.SafeOrderClause("leads", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			query = query.Order(orderClause)
		}
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
		orderClause, err := utils.SafeOrderClause("leads", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			db = db.Order(orderClause)
		}
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

func (r *leadRepository) CountByOwnerID(ownerID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Lead{}).Where("owner_id = ?", ownerID).Count(&count).Error
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