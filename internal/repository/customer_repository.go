package repository

import (
	"github.com/florinel-chis/gocrm/internal/models"
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

func (r *customerRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Customer{}).Count(&count).Error
	return count, err
}