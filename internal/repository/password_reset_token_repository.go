package repository

import (
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type passwordResetTokenRepository struct {
	db *gorm.DB
}

func NewPasswordResetTokenRepository(db *gorm.DB) PasswordResetTokenRepository {
	return &passwordResetTokenRepository{db: db}
}

func (r *passwordResetTokenRepository) Create(token *models.PasswordResetToken) error {
	return r.db.Create(token).Error
}

// GetByTokenHash returns only spendable tokens: unused and unexpired. Used or
// expired tokens surface as gorm.ErrRecordNotFound so callers cannot tell the
// difference — which is exactly the disclosure posture the endpoints need.
func (r *passwordResetTokenRepository) GetByTokenHash(tokenHash string) (*models.PasswordResetToken, error) {
	var token models.PasswordResetToken
	err := r.db.Where("token_hash = ? AND used_at IS NULL AND expires_at > ?",
		tokenHash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// MarkUsed claims a token: it stamps used_at only while the row is still
// unused and strictly unexpired, and reports gorm.ErrRecordNotFound when
// nothing was claimed. Both predicates are part of the UPDATE itself rather
// than checked beforehand, so two callers that both read the token as
// spendable cannot both claim it — whichever UPDATE runs second finds the row
// already stamped (or expired meanwhile) and affects no row. Exactly one
// affected row is the only success. The service runs the claim inside the
// transaction that changes the password and revokes the sessions, so a
// rollback hands the token back for a retry instead of burning the link.
func (r *passwordResetTokenRepository) MarkUsed(id uint) error {
	now := time.Now()
	result := r.db.Model(&models.PasswordResetToken{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", id, now).
		Update("used_at", &now)
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return gorm.ErrRecordNotFound
	default:
		// The primary key makes this unreachable; it is still refused rather
		// than reported as a single-use claim.
		return fmt.Errorf("password reset token claim affected %d rows", result.RowsAffected)
	}
}

func (r *passwordResetTokenRepository) InvalidateAllForUser(userID uint) error {
	now := time.Now()
	return r.db.Model(&models.PasswordResetToken{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Update("used_at", &now).Error
}

// DeleteExpired hard-deletes spent and expired tokens. The rows hold nothing
// but a hash and a user id, so there is no reason to keep soft-deleted copies.
func (r *passwordResetTokenRepository) DeleteExpired() error {
	return r.db.Unscoped().Where("expires_at < ? OR used_at IS NOT NULL", time.Now()).
		Delete(&models.PasswordResetToken{}).Error
}

func (r *passwordResetTokenRepository) WithTx(tx *gorm.DB) PasswordResetTokenRepository {
	return &passwordResetTokenRepository{db: tx}
}
