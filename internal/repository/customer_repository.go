package repository

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type customerRepository struct {
	db *gorm.DB
}

func NewCustomerRepository(db *gorm.DB) CustomerRepository {
	return &customerRepository{db: db}
}

func (r *customerRepository) Create(customer *models.Customer) error {
	if err := r.db.Create(customer).Error; err != nil {
		// Defense in depth: the unique index on customers.email is NOT scoped to
		// deleted_at, so a soft-deleted row still occupies the address. Any
		// duplicate-key violation here must surface as the ErrDuplicateEmail
		// sentinel rather than as a raw driver string, which would leak the
		// index name and driver error code to the caller.
		if isDuplicateKeyError(err) {
			return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
}

func (r *customerRepository) GetByID(id uint) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Preload("User").First(&customer, id).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) GetByEmail(email string) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Where("email = ?", email).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

// GetByEmailUnscoped looks up a customer by email including soft-deleted rows.
// Use it for duplicate-email pre-checks: the database unique index does not
// ignore soft-deleted rows, so an ordinary (scoped) lookup would report "free"
// for an address that the database will still reject on insert.
func (r *customerRepository) GetByEmailUnscoped(email string) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Unscoped().Where("email = ?", email).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) GetByUserID(userID uint) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Where("user_id = ?", userID).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) Update(customer *models.Customer) error {
	if err := r.db.Save(customer).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
}

// Delete erases a customer under the GDPR right to erasure (Article 17).
//
// As with users, this is NOT deactivation: it is irreversible. Every
// personal-data field on the customer — names, email, phone, company, position,
// the full postal address and the free-text notes — is overwritten in place,
// and only then is the row soft-deleted, in the SAME transaction, so a crash
// cannot leave a live anonymised record behind.
//
// The row survives (soft-deleted) because tickets reference customers by
// foreign key; erasure is achieved by making the retained row non-personal, not
// by destroying business history.
//
// UserID is deliberately left alone. It is a foreign key describing which
// account owned this record, not personal data in itself, and the linked user
// account has its own independent erasure path.
//
// The free-text Notes column is the highest-risk field here: it is the one
// place where unstructured personal data accumulates, so it is cleared
// outright rather than trimmed.
//
// As with users, the transaction is the caller's when this repository was
// obtained through WithTx and a fresh one otherwise. See erasure.go for the
// shared machinery and the full GDPR rationale.
func (r *customerRepository) Delete(id uint) error {
	return eraseRecord(r.db, id, erasurePlan{
		Model:       &models.Customer{},
		EmailColumn: "email",
		Scrub: map[string]interface{}{
			"first_name":  "",
			"last_name":   "",
			"phone":       "",
			"company":     "",
			"position":    "",
			"address":     "",
			"city":        "",
			"state":       "",
			"country":     "",
			"postal_code": "",
			"notes":       "",
		},
	})
}

func (r *customerRepository) List(offset, limit int) ([]models.Customer, error) {
	var customers []models.Customer
	err := r.db.Preload("User").Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) GetByIDWithPreloads(id uint, preloads ...string) (*models.Customer, error) {
	var customer models.Customer
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.First(&customer, id).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) ListWithPreloads(offset, limit int, preloads ...string) ([]models.Customer, error) {
	var customers []models.Customer
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error) {
	var customers []models.Customer
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("customers", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			query = query.Order(orderClause)
		}
	}
	err := query.Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error) {
	var customers []models.Customer
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
		orderClause, err := utils.SafeOrderClause("customers", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			db = db.Order(orderClause)
		}
	}
	err := db.Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.Customer{}).Where(
		"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
		searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

func (r *customerRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Customer{}).Count(&count).Error
	return count, err
}

func (r *customerRepository) WithTx(tx *gorm.DB) CustomerRepository {
	return &customerRepository{db: tx}
}