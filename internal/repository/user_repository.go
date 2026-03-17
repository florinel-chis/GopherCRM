package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(user *models.User) error {
	return r.db.Create(user).Error
}

func (r *userRepository) GetByID(id uint) (*models.User, error) {
	var user models.User
	err := r.db.First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) GetByEmail(email string) (*models.User, error) {
	var user models.User
	err := r.db.Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) Update(user *models.User) error {
	return r.db.Save(user).Error
}

func (r *userRepository) Delete(id uint) error {
	return r.db.Delete(&models.User{}, id).Error
}

func (r *userRepository) List(offset, limit int) ([]models.User, error) {
	var users []models.User
	err := r.db.Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Count(&count).Error
	return count, err
}

func (r *userRepository) UpdateLastLogin(id uint) error {
	now := time.Now()
	return r.db.Model(&models.User{}).Where("id = ?", id).Update("last_login_at", &now).Error
}

func (r *userRepository) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, error) {
	var users []models.User
	query := r.db
	if sortBy != "" {
		query = query.Order(sortBy + " " + sortOrder)
	}
	err := query.Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, error) {
	var users []models.User
	db := r.db
	searchPattern := "%" + query + "%"
	db = db.Where(
		"email LIKE ? OR first_name LIKE ? OR last_name LIKE ?",
		searchPattern, searchPattern, searchPattern,
	)
	if sortBy != "" {
		db = db.Order(sortBy + " " + sortOrder)
	}
	err := db.Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.User{}).Where(
		"email LIKE ? OR first_name LIKE ? OR last_name LIKE ?",
		searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

func (r *userRepository) WithTx(tx *gorm.DB) UserRepository {
	return &userRepository{db: tx}
}