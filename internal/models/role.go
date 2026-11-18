package models

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RBAC persistence (#259 S2, #344). The permission catalogue and its
// enforcement read these tables since #347; the role API, the governed user
// writes and the audit writer since #348 (internal/service/role_service.go).

// RoleNameMaxRunes is the length limit of roles.name, counted in runes.
const RoleNameMaxRunes = 100

// PermissionScope says which records a permission reaches: all of them, or
// the caller's own.
type PermissionScope string

const (
	PermissionScopeAll PermissionScope = "all"
	PermissionScopeOwn PermissionScope = "own"
)

// RoleAuditAction is the kind of change a role_audit_log row records.
type RoleAuditAction string

const (
	RoleAuditRoleCreated     RoleAuditAction = "role_created"
	RoleAuditRoleUpdated     RoleAuditAction = "role_updated"
	RoleAuditRoleDeleted     RoleAuditAction = "role_deleted"
	RoleAuditUserRoleChanged RoleAuditAction = "user_role_changed"
)

// Role is a named set of permissions. The four system rows (system_key admin,
// sales, support, customer) are seeded by seedSystemRoles and by the SQL
// migration 20261119120000_add_roles_and_permissions; every other row is a
// custom role with system_key NULL. A role is hard-deleted: there is no
// deleted_at, and fk_users_custom_role (ON DELETE RESTRICT) refuses deleting
// one that a user still references.
//
// The name must not carry personal text: in later slices it reaches run
// logs, audit snapshots and every staff screen.
type Role struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// SystemKey is admin, sales, support or customer on the four seeded rows
	// and NULL on custom roles. Only the seed writes it.
	SystemKey *string `gorm:"type:varchar(20);uniqueIndex:idx_roles_system_key" json:"system_key"`
	// Name is at most RoleNameMaxRunes runes and not blank after trimming.
	Name string `gorm:"type:varchar(100);not null" json:"name"`
	// NameKey is RoleNameKey(Name); its unique index makes names unique
	// regardless of letter case and surrounding whitespace.
	NameKey     string `gorm:"type:varchar(100);not null;uniqueIndex:idx_roles_name_key" json:"-"`
	Description string `gorm:"type:varchar(500);not null;default:''" json:"description"`

	Permissions []RolePermission `gorm:"foreignKey:RoleID" json:"permissions,omitempty"`
}

// RolePermission grants one permission key to a role with a scope. The keys
// are the leaves of the catalogue in internal/permissions.
type RolePermission struct {
	RoleID        uint            `gorm:"primaryKey;autoIncrement:false" json:"-"`
	PermissionKey string          `gorm:"primaryKey;type:varchar(100)" json:"permission_key"`
	Scope         PermissionScope `gorm:"type:varchar(10);not null;default:'all'" json:"scope"`
}

// RoleAuditEntry is one append-only row of role_audit_log, written by the
// role service and the governed user writes in the transaction of the change
// it records. By contract a row holds ids, role names, permission keys and
// scopes only, never personal data: the writers keep names, emails and other
// personal text out of BeforeState and AfterState, which is why the erasure
// does not touch the table. No column carries a foreign key, so an audit row
// never blocks a role or user delete.
type RoleAuditEntry struct {
	ID           uint            `gorm:"primaryKey"`
	CreatedAt    time.Time       `gorm:"not null;index:idx_role_audit_log_created_at"`
	ActorUserID  *uint           `gorm:"column:actor_user_id"`
	AuthMethod   string          `gorm:"type:varchar(20);not null;default:''"`
	Action       RoleAuditAction `gorm:"type:varchar(40);not null"`
	RoleID       *uint           `gorm:"index:idx_role_audit_log_role_id"`
	RoleName     string          `gorm:"type:varchar(100);not null;default:''"`
	TargetUserID *uint           `gorm:"index:idx_role_audit_log_target_user_id"`
	BeforeState  *string         `gorm:"type:text"`
	AfterState   *string         `gorm:"type:text"`
}

// TableName keeps the audit table's singular "log" name.
func (RoleAuditEntry) TableName() string {
	return "role_audit_log"
}

// RoleNameKey is the uniqueness key of a role name: trimmed of surrounding
// whitespace and lower-cased.
func RoleNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// SystemRoleSeed is one built-in role written by seedSystemRoles and by the
// SQL up migration.
type SystemRoleSeed struct {
	SystemKey   string
	Name        string
	Description string
}

// SystemRoleSeeds are the four built-in roles with #259's names and
// descriptions. migrations/20261119120000_add_roles_and_permissions.up.sql
// inserts the same literal values; the name_key of each is RoleNameKey(Name).
var SystemRoleSeeds = []SystemRoleSeed{
	{SystemKey: string(RoleAdmin), Name: "Admin", Description: "Full access. Cannot be edited or deleted."},
	{SystemKey: string(RoleSales), Name: "Sales", Description: "Built-in sales access. Cannot be edited or deleted."},
	{SystemKey: string(RoleSupport), Name: "Support", Description: "Built-in support access. Cannot be edited or deleted."},
	{SystemKey: string(RoleCustomer), Name: "Customer", Description: "A customer's own login. Cannot be edited, deleted or cloned into portal access."},
}

// seedSystemRoles writes the system roles that are missing, on every start
// and every driver. An up-to-date database costs one plain SELECT and no
// write; an existing system row is never updated, so its name and
// description are not rewritten. A missing row is inserted with ON CONFLICT
// DO NOTHING, so a replica booting at the same time is not an error.
//
// When a custom role already holds the name_key of a missing system row the
// seed cannot insert it: it returns an error naming the system key only,
// before writing anything, and startup stops. The operator renames that
// custom role and restarts. No log line is written.
func seedSystemRoles(tx *gorm.DB) error {
	present, err := presentSystemRoleKeys(tx)
	if err != nil {
		return err
	}
	var missing []SystemRoleSeed
	for _, seed := range SystemRoleSeeds {
		if !present[seed.SystemKey] {
			missing = append(missing, seed)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// Refuse before any write when a custom role holds a missing row's name.
	nameKeys := make([]string, 0, len(missing))
	for _, seed := range missing {
		nameKeys = append(nameKeys, RoleNameKey(seed.Name))
	}
	var held []string
	if err := tx.Raw("SELECT name_key FROM roles WHERE system_key IS NULL AND name_key IN ?", nameKeys).Scan(&held).Error; err != nil {
		return fmt.Errorf("seed system roles: failed to read custom role names: %w", err)
	}
	if len(held) > 0 {
		heldSet := make(map[string]bool, len(held))
		for _, key := range held {
			heldSet[RoleNameKey(key)] = true
		}
		for _, seed := range missing {
			if heldSet[RoleNameKey(seed.Name)] {
				return systemRoleHeldError(seed.SystemKey)
			}
		}
	}

	now := tx.NowFunc()
	for _, seed := range missing {
		key := seed.SystemKey
		role := Role{
			CreatedAt:   now,
			UpdatedAt:   now,
			SystemKey:   &key,
			Name:        seed.Name,
			NameKey:     RoleNameKey(seed.Name),
			Description: seed.Description,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&role).Error; err != nil {
			// A replica seeding at the same time can make this statement the
			// victim of a lock conflict on the unique indexes (MySQL error
			// 1213). When the row is there afterwards, the other replica
			// wrote it and the start goes on; otherwise the error stands.
			written, readErr := presentSystemRoleKeys(tx)
			if readErr != nil || !written[seed.SystemKey] {
				return fmt.Errorf("seed system roles: failed to insert %q: %w", seed.SystemKey, err)
			}
		}
	}

	present, err = presentSystemRoleKeys(tx)
	if err != nil {
		return err
	}
	for _, seed := range missing {
		if !present[seed.SystemKey] {
			return systemRoleHeldError(seed.SystemKey)
		}
	}
	return nil
}

func systemRoleHeldError(key string) error {
	return fmt.Errorf("seed system roles: roles.name_key %q is held by a custom role; rename that role and restart", key)
}

// presentSystemRoleKeys is the seed's one plain, non-locking read.
func presentSystemRoleKeys(tx *gorm.DB) (map[string]bool, error) {
	var keys []string
	if err := tx.Raw("SELECT system_key FROM roles WHERE system_key IS NOT NULL").Scan(&keys).Error; err != nil {
		return nil, fmt.Errorf("seed system roles: failed to read system roles: %w", err)
	}
	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		present[key] = true
	}
	return present, nil
}
