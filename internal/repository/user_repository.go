package repository

import (
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type userRepository struct {
	db *gorm.DB
	// lockSeams are the test seams of the erasure protocol (#385), set only by
	// the test option in erasure_hooks.go and copied by WithTx; nil in
	// production.
	lockSeams *erasureSeams
}

// UserRepositoryOption configures NewUserRepository beyond its handle.
// Production wiring passes none.
type UserRepositoryOption func(*userRepository)

func NewUserRepository(db *gorm.DB, opts ...UserRepositoryOption) UserRepository {
	r := &userRepository{db: db}
	for _, opt := range opts {
		opt(r)
	}
	return r
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

// GetByID reads one live user. A user with role_id NULL costs exactly one
// SELECT, against users; a custom-role user also gets CustomRole with its
// permissions (ordered by permission_key) filled (#344).
func (r *userRepository) GetByID(id uint) (*models.User, error) {
	var user models.User
	err := r.db.First(&user, id).Error
	if err != nil {
		return nil, err
	}
	if user.RoleID != nil {
		var role models.Role
		err := r.db.Preload("Permissions", func(db *gorm.DB) *gorm.DB {
			return db.Order("permission_key ASC")
		}).First(&role, *user.RoleID).Error
		if err != nil {
			return nil, fmt.Errorf("load custom role of user %d: %w", user.ID, err)
		}
		user.CustomRole = &role
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

// userUpdatableColumns is the closed set of columns UpdateFields may write.
// Anything else (ids, timestamps, deleted_at) is refused outright.
var userUpdatableColumns = map[string]struct{}{
	"email":                 {},
	"first_name":            {},
	"last_name":             {},
	"role":                  {},
	"is_active":             {},
	"password":              {},
	"failed_login_attempts": {},
	"locked_until":          {},
	"last_login_at":         {},
	// token_version is the access-token generation (#236); logout, the
	// password change and the reset write it under the users-row lock.
	"token_version": {},
	// role_id names a custom role (#344); nil clears it.
	"role_id": {},
}

// UpdateFields writes exactly the named columns of one live user and nothing
// else. It is a conditional UPDATE (`WHERE id = ? AND deleted_at IS NULL`):
// it never inserts, never touches a soft-deleted (erased) row, and reports
// gorm.ErrRecordNotFound when no live row matched.
//
// It replaces a full-struct Save. GORM's Save falls back to an upsert when its
// UPDATE matches no row, so a caller holding a struct read before an erasure
// wrote the erased person's name, email and password hash back and cleared
// deleted_at; writing every column of a stale struct could also put an old
// password hash back over a newer one. Callers name the columns they own.
func (r *userRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	for column := range fields {
		if _, ok := userUpdatableColumns[column]; !ok {
			return fmt.Errorf("user column %q is not updatable", column)
		}
	}
	result := r.db.Model(&models.User{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(fields)
	if result.Error != nil {
		if isDuplicateKeyError(result.Error) {
			return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// MySQL reports changed rows, not matched rows, so an UPDATE that wrote
	// the values the row already held affects zero rows. Tell that apart
	// from a missing or erased row.
	var live int64
	if err := r.db.Model(&models.User{}).Where("id = ? AND deleted_at IS NULL", id).Count(&live).Error; err != nil {
		return err
	}
	if live == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete erases a user under the GDPR right to erasure (Article 17).
//
// It is NOT the same operation as deactivation, and the two must not be
// confused:
//
//   - Deactivation (IsActive = false, via UpdateFields) is the NON-destructive way to
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
// A customer-role user is the login of one or more customer records
// (customers.user_id), and those records — and the leads converted into
// them — are the same person, so they are erased with it in the same unit.
// A staff user (admin, sales, support) is erased alone: the customers that
// point at it, and everything it owns or is assigned to, stay. See
// erasure_cascade.go.
//
// Anonymisation, the credential purge, the soft delete and the linked records
// run as ONE unit — inside a savepoint of the caller's transaction if this
// repository was obtained through WithTx, otherwise in a transaction started
// here. A crash between the steps would otherwise leave a live, anonymised,
// still-listed account behind, or an anonymised account whose API keys still
// authenticate.
//
// Side effect worth knowing: because the address is replaced, the original
// email becomes free for re-registration. The unique index on users.email is
// not scoped to deleted_at, so before this change a deleted address stayed
// locked forever.
//
// See erasure.go for the shared machinery and the full GDPR rationale.
func (r *userRepository) Delete(id uint) error {
	return eraseGraph(r.db, true, erasureRoot{kind: erasureRootUser, id: id}, r.lockSeams, nil)
}

// userErasurePlan lists what counts as personal data on a user, and what goes
// with the account. It is the only copy: every path that erases a user, from
// any entry point of the identity cascade, uses it.
func userErasurePlan() erasurePlan {
	return erasurePlan{
		Model:       &models.User{},
		EmailColumn: "email",
		// The NOT NULL name columns are blanked rather than nulled. The login
		// bookkeeping goes too: a last-login timestamp and a lock-out window are
		// behavioural data about the person, not business records. The role
		// stays: it is not personal, and the cascade needs it to recognise an
		// erased customer login.
		Scrub: map[string]interface{}{
			"password":              unusablePasswordHash,
			"first_name":            "",
			"last_name":             "",
			"is_active":             false,
			"last_login_at":         nil,
			"failed_login_attempts": 0,
			"locked_until":          nil,
		},
		AfterScrub: func(tx *gorm.DB, id uint) error {
			if err := purgeCredentials(tx, id); err != nil {
				return err
			}
			return scrubUserTicketComments(tx, id)
		},
	}
}

func (r *userRepository) List(offset, limit int) ([]models.User, error) {
	var users []models.User
	err := orderByID(r.db, "users").Offset(offset).Limit(limit).Find(&users).Error
	return users, err
}

func (r *userRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Count(&count).Error
	return count, err
}

// UpdateLastLogin stamps the login time of a live user; like UpdateFields it
// never touches an erased row and reports gorm.ErrRecordNotFound for one.
func (r *userRepository) UpdateLastLogin(id uint) error {
	now := time.Now()
	return r.UpdateFields(id, map[string]interface{}{"last_login_at": &now})
}

func (r *userRepository) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, error) {
	var users []models.User
	query := r.db
	query, err := orderPage(query, "users", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = query.Offset(offset).Limit(limit).Find(&users).Error
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
	db, err := orderPage(db, "users", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = db.Offset(offset).Limit(limit).Find(&users).Error
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
	return &userRepository{db: tx, lockSeams: r.lockSeams}
}

var _ UserRoleMaintenance = (*userRepository)(nil)

// LockActiveAdmins implements UserRoleMaintenance. Discovery is a plain
// SELECT (a consistent non-locking read on MySQL/MariaDB); the lock is then
// taken by primary key only (WHERE id IN (...) ORDER BY id, FOR UPDATE on
// MySQL/MariaDB, a plain read on SQLite), Unscoped so the locking statement
// carries no other predicate, and the predicate is re-checked on the locked
// rows. Locking through a role predicate would lock idx_users_role entries
// before the primary key, the reverse of every other users-row lock.
func (r *userRepository) LockActiveAdmins() ([]models.User, error) {
	var ids []uint
	if err := r.db.Raw("SELECT id FROM users WHERE role = ? AND is_active = ? AND deleted_at IS NULL ORDER BY id",
		string(models.RoleAdmin), true).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("discover active admins: %w", err)
	}
	admins := []models.User{}
	if len(ids) == 0 {
		return admins, nil
	}
	var locked []models.User
	if err := r.db.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id").Find(&locked).Error; err != nil {
		return nil, fmt.Errorf("lock active admins: %w", err)
	}
	for _, user := range locked {
		if user.Role == models.RoleAdmin && user.IsActive && !user.DeletedAt.Valid {
			admins = append(admins, user)
		}
	}
	return admins, nil
}

// ClearRoleIDOfErased implements UserRoleMaintenance: one UPDATE that sets
// role_id NULL on the soft-deleted users referencing roleID, so the role can
// be deleted afterwards. updated_at is not touched.
func (r *userRepository) ClearRoleIDOfErased(roleID uint) (int64, error) {
	result := r.db.Exec("UPDATE users SET role_id = NULL WHERE role_id = ? AND deleted_at IS NOT NULL", roleID)
	if result.Error != nil {
		return 0, fmt.Errorf("clear role of erased users: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// LockLiveByIDs implements UserRoleMaintenance: one locking read by primary
// key in id order, Unscoped so the statement carries no other predicate, with
// erased rows dropped afterwards. The rows are what the locking read saw, so
// on MySQL/MariaDB they are the committed state even after a lock wait.
func (r *userRepository) LockLiveByIDs(ids []uint) ([]models.User, error) {
	live := []models.User{}
	if len(ids) == 0 {
		return live, nil
	}
	var locked []models.User
	if err := r.db.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id").Find(&locked).Error; err != nil {
		return nil, fmt.Errorf("lock users: %w", err)
	}
	for _, user := range locked {
		if !user.DeletedAt.Valid {
			live = append(live, user)
		}
	}
	return live, nil
}

// LockLiveRoleHolders implements UserRoleMaintenance: a locking read of the
// live users referencing roleID (FOR UPDATE on MySQL/MariaDB, where it also
// reads the committed rows rather than the snapshot; a plain read on SQLite).
func (r *userRepository) LockLiveRoleHolders(roleID uint) ([]uint, error) {
	ids := []uint{}
	if err := r.db.Model(&models.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("role_id = ?", roleID).Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("lock role holders: %w", err)
	}
	return ids, nil
}

// CountLiveRoleHolders implements UserRoleMaintenance with two grouped
// counts over the live users.
func (r *userRepository) CountLiveRoleHolders() (map[string]int64, map[uint]int64, error) {
	var byRole []struct {
		Role  string
		Total int64
	}
	if err := r.db.Model(&models.User{}).Select("role, COUNT(*) AS total").
		Where("role_id IS NULL").Group("role").Scan(&byRole).Error; err != nil {
		return nil, nil, fmt.Errorf("count users per system role: %w", err)
	}
	var byID []struct {
		RoleID uint
		Total  int64
	}
	if err := r.db.Model(&models.User{}).Select("role_id, COUNT(*) AS total").
		Where("role_id IS NOT NULL").Group("role_id").Scan(&byID).Error; err != nil {
		return nil, nil, fmt.Errorf("count users per custom role: %w", err)
	}
	bySystemRole := make(map[string]int64, len(byRole))
	for _, row := range byRole {
		bySystemRole[row.Role] = row.Total
	}
	byRoleID := make(map[uint]int64, len(byID))
	for _, row := range byID {
		byRoleID[row.RoleID] = row.Total
	}
	return bySystemRole, byRoleID, nil
}

// HasLinkedCustomerRecord implements UserRoleMaintenance with one plain count
// of the customers rows whose user_id is userID, erased rows included: the
// rows the erasure of a customer login follows. It takes no lock.
func (r *userRepository) HasLinkedCustomerRecord(userID uint) (bool, error) {
	var linked int64
	if err := r.db.Unscoped().Model(&models.Customer{}).
		Where("user_id = ?", userID).Count(&linked).Error; err != nil {
		return false, fmt.Errorf("count the customer records of user %d: %w", userID, err)
	}
	return linked > 0, nil
}
