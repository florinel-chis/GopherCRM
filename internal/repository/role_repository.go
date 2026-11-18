package repository

import (
	"fmt"
	"strings"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// roleDescriptionMaxRunes is the width of roles.description.
const roleDescriptionMaxRunes = 500

type roleRepository struct {
	db *gorm.DB
}

// NewRoleRepository returns the role persistence of #344. The role service
// and the governed user writes (#348) are its callers.
func NewRoleRepository(db *gorm.DB) RoleRepository {
	return &roleRepository{db: db}
}

func (r *roleRepository) WithTx(tx *gorm.DB) RoleRepository {
	return &roleRepository{db: tx}
}

// normalizeRoleFields validates a role's name and description and returns the
// trimmed name. A blank name, a name over models.RoleNameMaxRunes runes or a
// description over roleDescriptionMaxRunes runes is apperrors.ErrValidation.
func normalizeRoleFields(name, description string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("role name is required: %w", apperrors.ErrValidation)
	}
	if utf8.RuneCountInString(trimmed) > models.RoleNameMaxRunes {
		return "", fmt.Errorf("role name is longer than %d characters: %w", models.RoleNameMaxRunes, apperrors.ErrValidation)
	}
	if utf8.RuneCountInString(description) > roleDescriptionMaxRunes {
		return "", fmt.Errorf("role description is longer than %d characters: %w", roleDescriptionMaxRunes, apperrors.ErrValidation)
	}
	return trimmed, nil
}

// Create inserts a custom role row (its Permissions are not written; use
// ReplacePermissions). It refuses a non-nil SystemKey with ErrValidation, so
// idx_roles_name_key is the only unique index this insert can hit and a
// duplicate key is ErrRoleNameTaken. Name is stored trimmed and NameKey is set
// to models.RoleNameKey(Name).
func (r *roleRepository) Create(role *models.Role) error {
	if role.SystemKey != nil {
		return fmt.Errorf("a system role cannot be created through the repository: %w", apperrors.ErrValidation)
	}
	name, err := normalizeRoleFields(role.Name, role.Description)
	if err != nil {
		return err
	}
	role.Name = name
	role.NameKey = models.RoleNameKey(name)
	if err := r.db.Omit(clause.Associations).Create(role).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("create role: %w", apperrors.ErrRoleNameTaken)
		}
		return err
	}
	return nil
}

// GetByID reads one role with its permissions, ordered by permission_key.
// A missing role is gorm.ErrRecordNotFound.
func (r *roleRepository) GetByID(id uint) (*models.Role, error) {
	var role models.Role
	err := r.db.Preload("Permissions", func(db *gorm.DB) *gorm.DB {
		return db.Order("permission_key ASC")
	}).First(&role, id).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// Update writes the name, name_key, description and updated_at of one role,
// and nothing else: system_key is never written. A name taken by another role
// (case and surrounding whitespace ignored) is ErrRoleNameTaken; a missing
// role is gorm.ErrRecordNotFound. Refusing writes to system rows is the
// service's job (owner decision 2), not the repository's.
func (r *roleRepository) Update(role *models.Role) error {
	name, err := normalizeRoleFields(role.Name, role.Description)
	if err != nil {
		return err
	}
	now := r.db.NowFunc()
	result := r.db.Model(&models.Role{}).Where("id = ?", role.ID).Updates(map[string]interface{}{
		"name":        name,
		"name_key":    models.RoleNameKey(name),
		"description": role.Description,
		"updated_at":  now,
	})
	if result.Error != nil {
		if isDuplicateKeyError(result.Error) {
			return fmt.Errorf("update role %d: %w", role.ID, apperrors.ErrRoleNameTaken)
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		// MySQL reports changed rows: tell an unchanged row from a missing one.
		var count int64
		if err := r.db.Model(&models.Role{}).Where("id = ?", role.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	role.Name = name
	role.NameKey = models.RoleNameKey(name)
	role.UpdatedAt = now
	return nil
}

// lockRole reads the role row by primary key with a locking read (FOR UPDATE
// on MySQL/MariaDB, a plain read on SQLite, whose dialector drops the
// clause). A missing role is gorm.ErrRecordNotFound.
func lockRole(tx *gorm.DB, id uint) error {
	var role models.Role
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", id).First(&role).Error
}

// ReplacePermissions replaces every role_permissions row of the role with
// permissions, in one transaction (a savepoint when the repository was
// obtained through WithTx): lock the role, delete its rows, insert the new
// set. A failure after the delete, such as a repeated permission_key hitting
// the primary key, rolls the delete back and keeps the previous set. An empty
// slice leaves the role without permissions. An empty scope is stored as
// all; a blank key or another scope is ErrValidation, before anything is
// written.
func (r *roleRepository) ReplacePermissions(roleID uint, permissions []models.RolePermission) error {
	rows := make([]models.RolePermission, 0, len(permissions))
	for _, permission := range permissions {
		if strings.TrimSpace(permission.PermissionKey) == "" {
			return fmt.Errorf("permission key is required: %w", apperrors.ErrValidation)
		}
		scope := permission.Scope
		if scope == "" {
			scope = models.PermissionScopeAll
		}
		if scope != models.PermissionScopeAll && scope != models.PermissionScopeOwn {
			return fmt.Errorf("permission scope must be all or own: %w", apperrors.ErrValidation)
		}
		rows = append(rows, models.RolePermission{RoleID: roleID, PermissionKey: permission.PermissionKey, Scope: scope})
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := lockRole(tx, roleID); err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", roleID).Delete(&models.RolePermission{}).Error; err != nil {
			return fmt.Errorf("replace permissions of role %d: %w", roleID, err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("replace permissions of role %d: %w", roleID, err)
		}
		return nil
	})
}

// Delete removes the role and its permissions in one transaction (a savepoint
// under WithTx): lock the role, delete its permissions, delete the role. When
// a user, live or erased, still references it through users.role_id, the
// foreign key refuses the delete (MySQL 1451, SQLite FOREIGN KEY constraint
// failed); that comes back as ErrRoleInUse and the role and its permissions
// remain. A missing role is gorm.ErrRecordNotFound.
func (r *roleRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := lockRole(tx, id); err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", id).Delete(&models.RolePermission{}).Error; err != nil {
			return fmt.Errorf("delete permissions of role %d: %w", id, err)
		}
		if err := tx.Where("id = ?", id).Delete(&models.Role{}).Error; err != nil {
			if isForeignKeyRefusal(err) {
				return fmt.Errorf("delete role %d: %w", id, apperrors.ErrRoleInUse)
			}
			return fmt.Errorf("delete role %d: %w", id, err)
		}
		return nil
	})
}

// List returns one page of roles, each with its permissions ordered by
// permission_key, and the total. Rows are ordered by the
// validated column (utils.AllowedSortColumns["roles"]) and then by roles.id in
// the same direction, or by id ascending without a sort column, so every page
// walks one total order. A column outside the allowlist is an error and no
// query runs. The rows and the total share one predicate (every role).
func (r *roleRepository) List(offset, limit int, sortBy, sortOrder string) ([]models.Role, int64, error) {
	ordered, err := orderPage(r.db.Model(&models.Role{}), "roles", sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := r.db.Model(&models.Role{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var roles []models.Role
	err = ordered.Preload("Permissions", func(db *gorm.DB) *gorm.DB {
		return db.Order("permission_key ASC")
	}).Offset(offset).Limit(limit).Find(&roles).Error
	if err != nil {
		return nil, 0, err
	}
	return roles, total, nil
}

// LockByIDs locks the role rows by primary key in ascending id order, then
// reads their permissions with a locking read as well. On MySQL/MariaDB a
// locking read returns the latest committed rows whatever the transaction's
// snapshot, which a plain read after a lock wait would not: a role widened
// while this transaction waited is read as widened. SQLite drops the clause.
// Ids without a row are simply absent from the result.
func (r *roleRepository) LockByIDs(ids []uint) ([]models.Role, error) {
	roles := []models.Role{}
	if len(ids) == 0 {
		return roles, nil
	}
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("lock roles: %w", err)
	}
	if len(roles) == 0 {
		return roles, nil
	}
	found := make([]uint, 0, len(roles))
	for _, role := range roles {
		found = append(found, role.ID)
	}
	var permissions []models.RolePermission
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("role_id IN ?", found).Order("role_id, permission_key").Find(&permissions).Error; err != nil {
		return nil, fmt.Errorf("read permissions of locked roles: %w", err)
	}
	for i := range roles {
		roles[i].Permissions = []models.RolePermission{}
		for _, permission := range permissions {
			if permission.RoleID == roles[i].ID {
				roles[i].Permissions = append(roles[i].Permissions, permission)
			}
		}
	}
	return roles, nil
}
