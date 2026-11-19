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
	// claimAfterRead is a test seam run between Claim's read and its update;
	// nil in production.
	claimAfterRead func()
}

// OIDCLoginAttemptRepositoryOption configures NewOIDCLoginAttemptRepository.
// Production passes none.
type OIDCLoginAttemptRepositoryOption func(*oidcLoginAttemptRepository)

// WithOIDCClaimAfterRead runs fn after Claim has read the row and before it
// updates it; tests only.
func WithOIDCClaimAfterRead(fn func()) OIDCLoginAttemptRepositoryOption {
	return func(r *oidcLoginAttemptRepository) { r.claimAfterRead = fn }
}

// NewOIDCLoginAttemptRepository returns the persistence of
// oidc_login_attempts (#435).
func NewOIDCLoginAttemptRepository(db *gorm.DB, opts ...OIDCLoginAttemptRepositoryOption) OIDCLoginAttemptRepository {
	r := &oidcLoginAttemptRepository{db: db}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *oidcLoginAttemptRepository) Create(attempt *models.OIDCLoginAttempt) error {
	return r.db.Create(attempt).Error
}

// Claim spends the attempt (#453) in statements that each commit on their
// own, with no transaction and no locking read:
//
//   - a plain read of the row by its state hash; no row, or a used one, is
//     gorm.ErrRecordNotFound;
//   - an expired row has its nonce and PKCE verifier blanked by a conditional
//     update by id and is gorm.ErrRecordNotFound all the same;
//   - otherwise one conditional update by id, whose predicates
//     (used_at IS NULL AND expires_at > now) are part of the statement,
//     stamps used_at and blanks the nonce and verifier, so of two callbacks
//     presenting the same state only one spends the row. One affected row is
//     success and the row from the read is returned with UsedAt set, still
//     holding the verifier and nonce the caller needs; zero is
//     gorm.ErrRecordNotFound.
//
// Unknown, used and expired attempts stay indistinguishable to the caller.
func (r *oidcLoginAttemptRepository) Claim(stateHash string, now time.Time) (*models.OIDCLoginAttempt, error) {
	stamp := now.UTC()
	var attempt models.OIDCLoginAttempt
	if err := r.db.Where("state_hash = ?", stateHash).First(&attempt).Error; err != nil {
		return nil, err
	}
	if attempt.UsedAt != nil {
		return nil, gorm.ErrRecordNotFound
	}
	if r.claimAfterRead != nil {
		r.claimAfterRead()
	}
	if !attempt.ExpiresAt.After(stamp) {
		err := r.db.Model(&models.OIDCLoginAttempt{}).
			Where("id = ? AND used_at IS NULL AND expires_at <= ?", attempt.ID, stamp).
			Updates(map[string]interface{}{"nonce": "", "code_verifier": ""}).Error
		if err != nil {
			// A failed clear still answers like an unknown state: a 500 or
			// 503 here would tell the caller that an expired row exists.
			// The row holds nothing usable once expired, and the stale bulk
			// operation sweep prunes it within a day.
			return nil, errors.Join(gorm.ErrRecordNotFound, fmt.Errorf("clear expired oidc login attempt: %w", err))
		}
		return nil, gorm.ErrRecordNotFound
	}
	result := r.db.Model(&models.OIDCLoginAttempt{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", attempt.ID, stamp).
		Updates(map[string]interface{}{"used_at": &stamp, "nonce": "", "code_verifier": ""})
	if result.Error != nil {
		return nil, result.Error
	}
	switch result.RowsAffected {
	case 1:
	case 0:
		return nil, gorm.ErrRecordNotFound
	default:
		// The primary key makes this unreachable; it is still refused rather
		// than reported as a single-use claim.
		return nil, fmt.Errorf("oidc login attempt claim affected %d rows", result.RowsAffected)
	}
	attempt.UsedAt = &stamp
	return &attempt, nil
}

// PruneCreatedBefore deletes at most limit attempts created before before,
// oldest id first: a plain read of the ids, then a delete by primary key that
// repeats the predicate, so no range of created_at is locked.
func (r *oidcLoginAttemptRepository) PruneCreatedBefore(before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var ids []uint
	if err := r.db.Model(&models.OIDCLoginAttempt{}).
		Where("created_at < ?", before.UTC()).
		Order("id ASC").Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.Where("id IN ? AND created_at < ?", ids, before.UTC()).Delete(&models.OIDCLoginAttempt{})
	return result.RowsAffected, result.Error
}

func (r *oidcLoginAttemptRepository) WithTx(tx *gorm.DB) OIDCLoginAttemptRepository {
	return &oidcLoginAttemptRepository{db: tx, claimAfterRead: r.claimAfterRead}
}
