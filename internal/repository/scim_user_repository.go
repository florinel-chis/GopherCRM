package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type scimUserRepository struct {
	db *gorm.DB
}

// NewSCIMUserRepository returns the read side of the SCIM Users resource
// (#469) over db.
func NewSCIMUserRepository(db *gorm.DB) SCIMUserRepository {
	return &scimUserRepository{db: db}
}

// visible narrows to the accounts SCIM serves: live rows (GORM's soft-delete
// scope adds deleted_at IS NULL, so erased accounts drop out) whose role is
// not customer.
func (r *scimUserRepository) visible() *gorm.DB {
	return r.db.Model(&models.User{}).Where("role <> ?", models.RoleCustomer)
}

// GetVisibleByID returns the visible user id, or gorm.ErrRecordNotFound.
func (r *scimUserRepository) GetVisibleByID(id uint) (*models.User, error) {
	var user models.User
	if err := r.visible().Where("id = ?", id).Take(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ListVisible counts every visible user and returns the window of them at
// offset, at most limit rows, id ascending. A limit of 0 reads only the count.
func (r *scimUserRepository) ListVisible(offset, limit int) ([]models.User, int64, error) {
	var total int64
	if err := r.visible().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	users := []models.User{}
	if limit <= 0 || int64(offset) >= total {
		return users, total, nil
	}
	if err := r.visible().Order("id ASC").Offset(offset).Limit(limit).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// FindVisibleByEmailFolded returns every visible user whose email equals
// email under LOWER on both sides, id ascending, without a limit. It is a
// candidate set only: the database collation decides what LOWER(email) =
// LOWER(?) matches (utf8mb4_unicode_ci also matches padded, accented and
// Unicode-fold variants), so the caller judges each returned row.
func (r *scimUserRepository) FindVisibleByEmailFolded(email string) ([]models.User, error) {
	users := []models.User{}
	if err := r.visible().Where("LOWER(email) = LOWER(?)", email).Order("id ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// IdentitiesForUsers returns the user_identities rows of userIDs under issuer
// in one query, id ascending. The comparison is the column's collation
// (utf8mb4_bin PAD SPACE on MySQL/MariaDB), so the caller compares the
// returned issuer byte for byte.
func (r *scimUserRepository) IdentitiesForUsers(issuer string, userIDs []uint) ([]models.UserIdentity, error) {
	identities := []models.UserIdentity{}
	if len(userIDs) == 0 {
		return identities, nil
	}
	if err := r.db.Where("issuer = ? AND user_id IN ?", issuer, userIDs).Order("id ASC").Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}
