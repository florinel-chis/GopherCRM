package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AdminOnlyPermissionKeys are the catalogue leaves only the admin system role
// holds (internal/permissions marks them AdminOnly; a test there keeps the two
// lists equal). The permissions package depends on models, so the list lives
// here for the startup data step below. Sorted.
var AdminOnlyPermissionKeys = []string{"settings.scim_manage", "settings.sso_manage"}

// RoleAuditAuthMethodMigration is the auth_method of a role_audit_log row
// written by a startup or SQL migration data step; actor_user_id is NULL.
const RoleAuditAuthMethodMigration = "migration"

// adminOnlyStripGrant and adminOnlyStripState have the JSON shape of the role
// service's roleAuditState. Unlike the role service, which records a role's
// resolved permissions, this step records the stored rows as they are: they
// can include an unknown or system-only key left from older data. The SQL
// migration 20261129120001_admin_only_sso_scim_permissions writes the same.
type adminOnlyStripGrant struct {
	Key   string          `json:"key"`
	Scope PermissionScope `json:"scope"`
}

type adminOnlyStripState struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Permissions []adminOnlyStripGrant `json:"permissions"`
}

// stripAdminOnlyRolePermissions removes the admin-only keys from every custom
// role, on every start and every driver, and writes one role_updated audit row
// (auth_method migration, actor NULL) per role it changed. An up-to-date
// database costs one plain SELECT and no write. No log line is written: the
// role name stays in the audit row.
//
// Each role is changed in its own transaction, opened on tx (never on the
// global handle: SQLite's migration runs on one pinned connection). The step
// locks only the roles row, so it cannot invert the role API's users-then-roles
// lock order.
func stripAdminOnlyRolePermissions(tx *gorm.DB) error {
	var ids []uint
	if err := tx.Raw(`SELECT DISTINCT roles.id FROM roles
		JOIN role_permissions ON role_permissions.role_id = roles.id
		WHERE roles.system_key IS NULL AND role_permissions.permission_key IN ?
		ORDER BY roles.id`, AdminOnlyPermissionKeys).Scan(&ids).Error; err != nil {
		return fmt.Errorf("strip admin-only permissions: failed to find roles: %w", err)
	}
	for _, id := range ids {
		if err := stripAdminOnlyPermissionsOfRole(tx, id); err != nil {
			return err
		}
	}
	return nil
}

// stripAdminOnlyPermissionsOfRole is the per-role transaction of
// stripAdminOnlyRolePermissions. When another run (a replica booting at the
// same time, or the SQL migration) already removed the keys, the DELETE
// affects no row and nothing is written.
func stripAdminOnlyPermissionsOfRole(tx *gorm.DB, roleID uint) error {
	err := tx.Transaction(func(t *gorm.DB) error {
		var role Role
		locked := t.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
		if err := locked.Where("id = ? AND system_key IS NULL", roleID).Take(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // deleted in the meantime
			}
			return err
		}
		var rows []RolePermission
		if err := t.Where("role_id = ?", roleID).Find(&rows).Error; err != nil {
			return err
		}
		result := t.Exec("DELETE FROM role_permissions WHERE role_id = ? AND permission_key IN ?", roleID, AdminOnlyPermissionKeys)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		sort.Slice(rows, func(i, j int) bool { return rows[i].PermissionKey < rows[j].PermissionKey })
		adminOnly := make(map[string]bool, len(AdminOnlyPermissionKeys))
		for _, key := range AdminOnlyPermissionKeys {
			adminOnly[key] = true
		}
		before := adminOnlyStripState{Name: role.Name, Description: role.Description, Permissions: make([]adminOnlyStripGrant, 0, len(rows))}
		after := adminOnlyStripState{Name: role.Name, Description: role.Description, Permissions: make([]adminOnlyStripGrant, 0, len(rows))}
		for _, row := range rows {
			grant := adminOnlyStripGrant{Key: row.PermissionKey, Scope: row.Scope}
			before.Permissions = append(before.Permissions, grant)
			if !adminOnly[row.PermissionKey] {
				after.Permissions = append(after.Permissions, grant)
			}
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(after)
		if err != nil {
			return err
		}
		beforeState, afterState := string(beforeJSON), string(afterJSON)
		id := role.ID
		entry := RoleAuditEntry{
			CreatedAt:   t.NowFunc(),
			AuthMethod:  RoleAuditAuthMethodMigration,
			Action:      RoleAuditRoleUpdated,
			RoleID:      &id,
			RoleName:    role.Name,
			BeforeState: &beforeState,
			AfterState:  &afterState,
		}
		return t.Create(&entry).Error
	})
	if err != nil {
		return fmt.Errorf("strip admin-only permissions: role %d: %w", roleID, err)
	}
	return nil
}
