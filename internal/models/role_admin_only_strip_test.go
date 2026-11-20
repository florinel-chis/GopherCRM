package models

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #524: the per-role step of stripAdminOnlyRolePermissions writes its audit
// row only when its own DELETE removed a key. When discovery saw a role whose
// keys another run (a second replica, or the SQL migration) removed before the
// role's lock was taken, the step writes nothing.
func TestStripAdminOnlyRolePermissionsSkipsStrippedRole(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin_only_strip.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Role{}, &RolePermission{}, &RoleAuditEntry{}))

	role := Role{Name: "Field Ops", NameKey: "field ops"}
	require.NoError(t, db.Create(&role).Error)
	require.NoError(t, db.Create(&[]RolePermission{
		{RoleID: role.ID, PermissionKey: "settings.sso_manage", Scope: PermissionScopeAll},
		{RoleID: role.ID, PermissionKey: "leads.view", Scope: PermissionScopeOwn},
	}).Error)

	auditRows := func() int64 {
		var count int64
		require.NoError(t, db.Model(&RoleAuditEntry{}).Count(&count).Error)
		return count
	}

	// The first run strips the key and writes one row.
	require.NoError(t, stripAdminOnlyPermissionsOfRole(db, role.ID))
	assert.Equal(t, int64(1), auditRows())

	// The same role again, as an overlapping run would reach it after the
	// first committed: no DELETE affects a row, so no audit row is written.
	require.NoError(t, stripAdminOnlyPermissionsOfRole(db, role.ID))
	assert.Equal(t, int64(1), auditRows())
	var kept []RolePermission
	require.NoError(t, db.Where("role_id = ?", role.ID).Find(&kept).Error)
	assert.Equal(t, []RolePermission{{RoleID: role.ID, PermissionKey: "leads.view", Scope: PermissionScopeOwn}}, kept)

	// A system role is never touched, even when it holds a key.
	admin := "admin"
	system := Role{Name: "Admin", NameKey: "admin", SystemKey: &admin}
	require.NoError(t, db.Create(&system).Error)
	require.NoError(t, db.Create(&RolePermission{RoleID: system.ID, PermissionKey: "settings.sso_manage", Scope: PermissionScopeAll}).Error)
	require.NoError(t, stripAdminOnlyRolePermissions(db))
	require.NoError(t, stripAdminOnlyPermissionsOfRole(db, system.ID))
	assert.Equal(t, int64(1), auditRows())
	var held int64
	require.NoError(t, db.Model(&RolePermission{}).Where("role_id = ?", system.ID).Count(&held).Error)
	assert.Equal(t, int64(1), held)
}
