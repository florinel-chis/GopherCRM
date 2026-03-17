package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// RefreshTokenRepository interface moved to interfaces.go to avoid duplication

type refreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) RefreshTokenRepository {
	return &refreshTokenRepository{
		db: db,
	}
}

func (r *refreshTokenRepository) Create(token *models.RefreshToken) error {
	return r.db.Create(token).Error
}

func (r *refreshTokenRepository) GetByTokenHash(tokenHash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	err := r.db.Where("token_hash = ? AND is_revoked = ? AND expires_at > ?", 
		tokenHash, false, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *refreshTokenRepository) GetByUserID(userID uint) ([]models.RefreshToken, error) {
	var tokens []models.RefreshToken
	err := r.db.Where("user_id = ? AND is_revoked = ? AND expires_at > ?", 
		userID, false, time.Now()).Find(&tokens).Error
	return tokens, err
}

func (r *refreshTokenRepository) RevokeByTokenHash(tokenHash string) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("token_hash = ?", tokenHash).
		Update("is_revoked", true).Error
}

func (r *refreshTokenRepository) RevokeAllByUserID(userID uint) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, false).
		Update("is_revoked", true).Error
}

func (r *refreshTokenRepository) DeleteExpired() error {
	return r.db.Where("expires_at < ? OR is_revoked = ?", time.Now(), true).
		Delete(&models.RefreshToken{}).Error
}

func (r *refreshTokenRepository) DeleteByTokenHash(tokenHash string) error {
	return r.db.Where("token_hash = ?", tokenHash).Delete(&models.RefreshToken{}).Error
}

// Update method names to match interface
func (r *refreshTokenRepository) RevokeAllForUser(userID uint) error {
	return r.RevokeAllByUserID(userID)
}

func (r *refreshTokenRepository) WithTx(tx *gorm.DB) RefreshTokenRepository {
	return &refreshTokenRepository{db: tx}
}