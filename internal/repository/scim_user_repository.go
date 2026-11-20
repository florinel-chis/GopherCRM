package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type scimUserRepository struct {
	db *gorm.DB
}

// NewSCIMUserRepository returns the SCIM Users resource repository (#469,
// #470) over db.
func NewSCIMUserRepository(db *gorm.DB) SCIMUserRepository {
	return &scimUserRepository{db: db}
}

// whereManagedBy narrows a query over users to the rows that carry at least
// one user_identities row whose issuer equals issuer exactly, whatever its
// source. The SCIM Users and Groups resources share it, so both apply one
// definition of a managed account. user_identities.issuer is utf8mb4_bin PAD
// SPACE on MySQL/MariaDB, where issuer = ? alone also matches a stored issuer
// with a trailing space; the LENGTH guard refuses it on every engine (SQLite
// compares BINARY already). An empty issuer matches nothing.
func whereManagedBy(query *gorm.DB, issuer string) *gorm.DB {
	if issuer == "" {
		return query.Where("1 = 0")
	}
	return query.Where("EXISTS (SELECT 1 FROM user_identities ui WHERE ui.user_id = users.id AND ui.issuer = ? AND LENGTH(ui.issuer) = LENGTH(?))",
		issuer, issuer)
}

// managedUsers narrows a users query to the accounts SCIM manages for issuer:
// live rows (GORM's soft-delete scope adds deleted_at IS NULL, so erased
// accounts drop out) whose role is not customer and that carry at least one
// user_identities row whose issuer equals issuer exactly (any source: scim
// or oidc). An empty issuer matches nothing.
func (r *scimUserRepository) managedUsers(issuer string) *gorm.DB {
	return whereManagedBy(r.db.Model(&models.User{}).Where("role <> ?", models.RoleCustomer), issuer)
}

// GetManagedByID returns the account id names when it is managed under
// issuer, or gorm.ErrRecordNotFound.
func (r *scimUserRepository) GetManagedByID(issuer string, id uint) (*models.User, error) {
	var user models.User
	if err := r.managedUsers(issuer).Where("id = ?", id).Take(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ListManaged counts every account managed under issuer and returns the
// window of them at offset, at most limit rows, id ascending. A limit of 0
// reads only the count.
func (r *scimUserRepository) ListManaged(issuer string, offset, limit int) ([]models.User, int64, error) {
	var total int64
	if err := r.managedUsers(issuer).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	users := []models.User{}
	if limit <= 0 || int64(offset) >= total {
		return users, total, nil
	}
	if err := r.managedUsers(issuer).Order("id ASC").Offset(offset).Limit(limit).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// FindManagedByEmailFolded returns every account managed under issuer whose
// email equals email under LOWER on both sides, id ascending, without a
// limit. It is a
// candidate set only: the database collation decides what LOWER(email) =
// LOWER(?) matches (utf8mb4_unicode_ci also matches padded, accented and
// Unicode-fold variants), so the caller judges each returned row.
func (r *scimUserRepository) FindManagedByEmailFolded(issuer, email string) ([]models.User, error) {
	users := []models.User{}
	if err := r.managedUsers(issuer).Where("LOWER(email) = LOWER(?)", email).Order("id ASC").Find(&users).Error; err != nil {
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

// EmailTakenByOther reports whether any user other than id, live or erased,
// holds email under LOWER on both sides. On MySQL/MariaDB the collation also
// equates padded and accented variants, which count as taken.
func (r *scimUserRepository) EmailTakenByOther(email string, id uint) (bool, error) {
	var count int64
	if err := r.db.Unscoped().Model(&models.User{}).Where("LOWER(email) = LOWER(?) AND id <> ?", email, id).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// WithTx returns the repository bound to tx.
func (r *scimUserRepository) WithTx(tx *gorm.DB) SCIMUserRepository {
	return &scimUserRepository{db: tx}
}
