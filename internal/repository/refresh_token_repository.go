package repository

import (
	"errors"
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

// GetRevokedByTokenHash returns the token with that hash only while it is
// revoked, whatever its expiry; a live or unknown token is a not-found. The
// rotation uses it after GetByTokenHash missed, to tell a token that was
// already exchanged for a successor from one that never existed.
func (r *refreshTokenRepository) GetRevokedByTokenHash(tokenHash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	err := r.db.Where("token_hash = ? AND is_revoked = ?", tokenHash, true).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// HasLaterInFamily reports whether another row of the token's user and
// family has a greater id. Only a rotation adds a row to an existing family
// and a chain is linear, so a revoked token with a later row is one that was
// already rotated. A token without a family is never part of one and returns
// false without a query.
func (r *refreshTokenRepository) HasLaterInFamily(token *models.RefreshToken) (bool, error) {
	if token == nil || token.FamilyID == "" {
		return false, nil
	}
	var count int64
	err := r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND family_id = ? AND id > ?", token.UserID, token.FamilyID, token.ID).
		Limit(1).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// LiveUserIDsByProviderSession returns the distinct owners of the live
// tokens that signed in under the provider session hash refers to, in
// ascending order. An empty hash is refused without a statement: it would
// match every password session.
func (r *refreshTokenRepository) LiveUserIDsByProviderSession(hash string) ([]uint, error) {
	if hash == "" {
		return nil, errors.New("refresh token provider session hash is empty")
	}
	var ids []uint
	err := r.db.Model(&models.RefreshToken{}).
		Where("provider_session_hash = ? AND is_revoked = ? AND expires_at > ?", hash, false, time.Now()).
		Distinct().Order("user_id").Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// RevokeFamily revokes every live token of the user's family. An empty
// family id is refused without a statement: it would match every legacy row
// that carries no family.
func (r *refreshTokenRepository) RevokeFamily(userID uint, familyID string) error {
	if familyID == "" {
		return errors.New("refresh token family id is empty")
	}
	return r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND family_id = ? AND is_revoked = ?", userID, familyID, false).
		Update("is_revoked", true).Error
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
