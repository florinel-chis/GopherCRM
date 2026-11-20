package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type scimTokenRepository struct {
	db *gorm.DB
}

// NewSCIMTokenRepository returns the scim_tokens repository over db (#468).
func NewSCIMTokenRepository(db *gorm.DB) SCIMTokenRepository {
	return &scimTokenRepository{db: db}
}

// Create inserts one token row. The caller sets created_at, in UTC.
func (r *scimTokenRepository) Create(token *models.SCIMToken) error {
	return r.db.Create(token).Error
}

// List returns every token, id descending (newest first).
func (r *scimTokenRepository) List() ([]models.SCIMToken, error) {
	var tokens []models.SCIMToken
	if err := r.db.Order("id DESC").Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

// FindByHash returns the token whose token_hash equals hash, revoked or not,
// or gorm.ErrRecordNotFound.
func (r *scimTokenRepository) FindByHash(hash string) (*models.SCIMToken, error) {
	var token models.SCIMToken
	if err := r.db.Where("token_hash = ?", hash).Take(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// Revoke stamps revoked_at with one conditional UPDATE (revoked_at IS NULL in
// the statement), so a second revoke leaves the first stamp, and then reads
// the row back. An unknown id is gorm.ErrRecordNotFound.
func (r *scimTokenRepository) Revoke(id uint, at time.Time) (*models.SCIMToken, error) {
	if err := r.db.Model(&models.SCIMToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at).Error; err != nil {
		return nil, err
	}
	var token models.SCIMToken
	if err := r.db.Where("id = ?", id).Take(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// TouchLastUsed sets last_used_at of the token id.
func (r *scimTokenRepository) TouchLastUsed(id uint, at time.Time) error {
	return r.db.Model(&models.SCIMToken{}).Where("id = ?", id).Update("last_used_at", at).Error
}
