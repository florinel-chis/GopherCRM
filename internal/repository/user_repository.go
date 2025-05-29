package repository

import (
	"time"

	"github.com/florinel-chis/gocrm/internal/models"
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