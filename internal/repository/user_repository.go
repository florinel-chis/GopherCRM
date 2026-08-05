package repository

import (
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(user *models.User) error {
	if err := r.db.Create(user).Error; err != nil {
		// Defense in depth: the unique index on users.email is NOT scoped to
		// deleted_at, so a soft-deleted row still occupies the address. Any
		// duplicate-key violation here must surface as the ErrDuplicateEmail
		// sentinel rather than as a raw driver string, which would leak the
		// index name and driver error code to the caller.
		if isDuplicateKeyError(err) {
			return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
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

// GetByEmailUnscoped looks up a user by email including soft-deleted rows.
// Use it for duplicate-email pre-checks: the database unique index does not
// ignore soft-deleted rows, so an ordinary (scoped) lookup would report "free"
// for an address that the database will still reject on insert.
func (r *userRepository) GetByEmailUnscoped(email string) (*models.User, error) {
	var user models.User
	err := r.db.Unscoped().Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) Update(user *models.User) error {
	if err := r.db.Save(user).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
}

// Delete erases a user under the GDPR right to erasure (Article 17).
//
// It is NOT the same operation as deactivation, and the two must not be
// confused:
//
//   - Deactivation (IsActive = false, via Update) is the NON-destructive way to
//     suspend an account. It keeps every field intact and is fully reversible.
//     Use it for suspensions, offboarding, or anything that may be undone.
//   - Delete is IRREVERSIBLE. Every personal-data field is overwritten in place
//     before the row is soft-deleted. Nothing here can be undone.
//
// The row itself is kept (soft-deleted) rather than dropped: tickets, tasks,
// leads and audit history reference users by foreign key, and destroying
// business records is neither required by Article 17 nor desirable. Erasure is
// satisfied by making the retained row non-personal — anonymisation in place.
//
// Anonymisation, the credential purge and the soft delete run in a SINGLE
// transaction — the caller's, if this repository was obtained through WithTx,
// otherwise one started here. A crash between the steps would otherwise leave a
// live, anonymised, still-listed account behind, or an anonymised account whose
// API keys still authenticate.
//
// Side effect worth knowing: because the address is replaced, the original
// email becomes free for re-registration. The unique index on users.email is
// not scoped to deleted_at, so before this change a deleted address stayed
// locked forever.
//
// See erasure.go for the shared machinery and the full GDPR rationale.
func (r *userRepository) Delete(id uint) error {
	return eraseRecord(r.db, id, erasurePlan{
		Model:       &models.User{},
		EmailColumn: "email",
		// The NOT NULL name columns are blanked rather than nulled. The login
		// bookkeeping goes too: a last-login timestamp and a lock-out window are
		// behavioural data about the person, not business records.
		Scrub: map[string]interface{}{
			"password":              unusablePasswordHash,
			"first_name":            "",
			"last_name":             "",
			"is_active":             false,
			"last_login_at":         nil,
			"failed_login_attempts": 0,
			"locked_until":          nil,
		},
		AfterScrub: purgeCredentials,
	})
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

func (r *userRepository) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, error) {
	var users []models.User
	query := r.db
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("users", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			query = query.Order(orderClause)
		}
	}
	err := query.Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, error) {
	var users []models.User
	db := r.db
	searchPattern := "%" + query + "%"
	db = db.Where(
		"email LIKE ? OR first_name LIKE ? OR last_name LIKE ?",
		searchPattern, searchPattern, searchPattern,
	)
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("users", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			db = db.Order(orderClause)
		}
	}
	err := db.Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.User{}).Where(
		"email LIKE ? OR first_name LIKE ? OR last_name LIKE ?",
		searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

func (r *userRepository) WithTx(tx *gorm.DB) UserRepository {
	return &userRepository{db: tx}
}