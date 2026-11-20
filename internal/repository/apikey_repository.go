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
	err := r.db.First(&apiKey, id).Error
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func (r *apiKeyRepository) GetByKeyHash(keyHash string) (*models.APIKey, error) {
	var apiKey models.APIKey
	err := r.db.Preload("User").Where("key_hash = ?", keyHash).First(&apiKey).Error
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

// Revoke switches off the key id of userID and stamps revoked_at in one
// conditional statement. A key already revoked (revoked_at set) is left as it
// is, so the first revocation time is kept; the count of changed rows is
// returned (0 for a revoked, missing or foreign key).
func (r *apiKeyRepository) Revoke(id, userID uint, at time.Time) (int64, error) {
	result := r.db.Model(&models.APIKey{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Updates(map[string]interface{}{"is_active": false, "revoked_at": at})
	return result.RowsAffected, result.Error
}

// RevokeAllForUser switches off every live (not soft-deleted) key of userID
// whose revoked_at is NULL and stamps revoked_at with at, in one statement.
// A key revoked before keeps its first revocation time, and an inactive key
// without revoked_at is stamped. It returns the count of changed rows.
func (r *apiKeyRepository) RevokeAllForUser(userID uint, at time.Time) (int64, error) {
	result := r.db.Model(&models.APIKey{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]interface{}{"is_active": false, "revoked_at": at})
	return result.RowsAffected, result.Error
}

// Rename writes only name (and updated_at) of the key id of userID and
// returns the count of changed rows. With liveOnly the statement also
// requires is_active true and revoked_at NULL, so a key revoked in the
// meantime is left unchanged and the count is 0.
func (r *apiKeyRepository) Rename(id, userID uint, name string, liveOnly bool) (int64, error) {
	query := r.db.Model(&models.APIKey{}).Where("id = ? AND user_id = ?", id, userID)
	if liveOnly {
		query = query.Where("is_active = ? AND revoked_at IS NULL", true)
	}
	result := query.Update("name", name)
	return result.RowsAffected, result.Error
}

func (r *apiKeyRepository) UpdateLastUsed(id uint) error {
	now := time.Now()
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("last_used_at", &now).Error
}

func (r *apiKeyRepository) WithTx(tx *gorm.DB) APIKeyRepository {
	return &apiKeyRepository{db: tx}
}
