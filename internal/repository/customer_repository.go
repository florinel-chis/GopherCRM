package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type customerRepository struct {
	db *gorm.DB
}

func NewCustomerRepository(db *gorm.DB) CustomerRepository {
	return &customerRepository{db: db}
}

func (r *customerRepository) Create(customer *models.Customer) error {
	return r.db.Create(customer).Error
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

func (r *customerRepository) Update(customer *models.Customer) error {
	return r.db.Save(customer).Error
}

func (r *customerRepository) Delete(id uint) error {
	return r.db.Delete(&models.Customer{}, id).Error
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
		query = query.Order(sortBy + " " + sortOrder)
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
		db = db.Order(sortBy + " " + sortOrder)
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