package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// ErrUserIdentityTaken reports an insert into user_identities that collided
// with the unique (issuer, subject) index: a concurrent first sign-in of the
// same identity committed first.
var ErrUserIdentityTaken = errors.New("the identity is already linked")

type userIdentityRepository struct {
	db *gorm.DB
}

// NewUserIdentityRepository returns the persistence of user_identities (#435).
func NewUserIdentityRepository(db *gorm.DB) UserIdentityRepository {
	return &userIdentityRepository{db: db}
}

// GetByIssuerSubject reads the row whose unique (issuer, subject) pair the
// engine finds equal to the arguments. utf8mb4_bin is a PAD SPACE collation,
// so on MySQL/MariaDB a value differing only in trailing spaces matches too:
// the caller compares the returned row byte for byte.
func (r *userIdentityRepository) GetByIssuerSubject(issuer, subject string) (*models.UserIdentity, error) {
	var identity models.UserIdentity
	if err := r.db.Where("issuer = ? AND subject = ?", issuer, subject).First(&identity).Error; err != nil {
		return nil, err
	}
	return &identity, nil
}

// Create inserts the row. A unique-index violation is ErrUserIdentityTaken;
// the table has no other unique index.
func (r *userIdentityRepository) Create(identity *models.UserIdentity) error {
	if err := r.db.Create(identity).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("insert user identity: %w", ErrUserIdentityTaken)
		}
		return err
	}
	return nil
}

// ExistsForUser reports whether the user has at least one linked identity,
// with a plain read (no locking clause).
func (r *userIdentityRepository) ExistsForUser(userID uint) (bool, error) {
	var count int64
	if err := r.db.Model(&models.UserIdentity{}).Where("user_id = ?", userID).Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *userIdentityRepository) WithTx(tx *gorm.DB) UserIdentityRepository {
	return &userIdentityRepository{db: tx}
}

type oidcLoginAttemptRepository struct {
	db *gorm.DB
}

// NewOIDCLoginAttemptRepository returns the persistence of
// oidc_login_attempts (#435).
func NewOIDCLoginAttemptRepository(db *gorm.DB) OIDCLoginAttemptRepository {
	return &oidcLoginAttemptRepository{db: db}
}

func (r *oidcLoginAttemptRepository) Create(attempt *models.OIDCLoginAttempt) error {
	return r.db.Create(attempt).Error
}

// Claim spends the attempt with one conditional update whose predicates are
// part of the statement, so of two callbacks presenting the same state only
// one stamps the row. Exactly one affected row is success and the row is then
// read back by its hash; zero is gorm.ErrRecordNotFound (unknown, used or
// expired, indistinguishable).
func (r *oidcLoginAttemptRepository) Claim(stateHash string, now time.Time) (*models.OIDCLoginAttempt, error) {
	stamp := now.UTC()
	result := r.db.Model(&models.OIDCLoginAttempt{}).
		Where("state_hash = ? AND used_at IS NULL AND expires_at > ?", stateHash, stamp).
		Update("used_at", &stamp)
	if result.Error != nil {
		return nil, result.Error
	}
	switch result.RowsAffected {
	case 1:
	case 0:
		return nil, gorm.ErrRecordNotFound
	default:
		// The unique index makes this unreachable; it is still refused rather
		// than reported as a single-use claim.
		return nil, fmt.Errorf("oidc login attempt claim affected %d rows", result.RowsAffected)
	}
	var attempt models.OIDCLoginAttempt
	if err := r.db.Where("state_hash = ?", stateHash).First(&attempt).Error; err != nil {
		return nil, err
	}
	return &attempt, nil
}

// PurgeExpired deletes at most limit attempts whose expiry lies before
// before, oldest id first: a plain read of the ids, then a delete by primary
// key, so no range of the expiry is locked.
func (r *oidcLoginAttemptRepository) PurgeExpired(before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var ids []uint
	if err := r.db.Model(&models.OIDCLoginAttempt{}).
		Where("expires_at < ?", before.UTC()).
		Order("id ASC").Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.Where("id IN ? AND expires_at < ?", ids, before.UTC()).Delete(&models.OIDCLoginAttempt{})
	return result.RowsAffected, result.Error
}

func (r *oidcLoginAttemptRepository) WithTx(tx *gorm.DB) OIDCLoginAttemptRepository {
	return &oidcLoginAttemptRepository{db: tx}
}
