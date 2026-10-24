package repository

import (
	"fmt"
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

// ClaimForRotation spends a refresh token for rotation: it revokes the row
// only while it is still not revoked and strictly unexpired, and reports
// gorm.ErrRecordNotFound when nothing was claimed. Both predicates are part
// of the UPDATE itself rather than checked beforehand, so two callers that
// both read the token as live cannot both claim it — whichever UPDATE runs
// second finds the row already revoked (or expired meanwhile) and affects no
// row. Exactly one affected row is the only success. The service runs the
// claim inside the transaction that inserts the successor, so a rollback hands
// the presented token back instead of stranding the session.
//
// RevokeByTokenHash stays unconditional: logout and invalidation must succeed
// on an already revoked or expired token, and must not race with rotation
// for the same single-use semantics.
func (r *refreshTokenRepository) ClaimForRotation(id uint) error {
	result := r.db.Model(&models.RefreshToken{}).
		Where("id = ? AND is_revoked = ? AND expires_at > ?", id, false, time.Now()).
		Update("is_revoked", true)
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
		return fmt.Errorf("refresh token claim affected %d rows", result.RowsAffected)
	}
}

// RevokeByTokenHash revokes the token unconditionally and idempotently: a
// token that is already revoked, expired or unknown is not an error. It is
// the logout and invalidation primitive; rotation uses ClaimForRotation.
func (r *refreshTokenRepository) RevokeByTokenHash(tokenHash string) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("token_hash = ?", tokenHash).
		Update("is_revoked", true).Error
}

func (r *refreshTokenRepository) DeleteExpired() error {
	return r.db.Where("expires_at < ? OR is_revoked = ?", time.Now(), true).
		Delete(&models.RefreshToken{}).Error
}

func (r *refreshTokenRepository) RevokeAllForUser(userID uint) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, false).
		Update("is_revoked", true).Error
}

func (r *refreshTokenRepository) WithTx(tx *gorm.DB) RefreshTokenRepository {
	return &refreshTokenRepository{db: tx}
}
