package models

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #344 on SQLite: MigrateDatabase is the only schema path, so it builds the
// three role tables, users.role_id with its indexes and fk_users_custom_role
// (ON DELETE RESTRICT), and seeds the four system roles.

func openRoleSchemaDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "roles.db"),
	}))
	require.NoError(t, MigrateDatabase())
}

func roleSchemaColumns(t *testing.T, table string) map[string]sqliteColumn {
	t.Helper()
	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table).Scan(&columns).Error)
	out := map[string]sqliteColumn{}
	for _, column := range columns {
		out[column.Name] = column
	}
	return out
}

func roleSchemaColumnNames(columns map[string]sqliteColumn) []string {
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// roleSchemaIndexes returns name -> unique of every index that is not the
// primary key's.
func roleSchemaIndexes(t *testing.T, table string) map[string]int {
	t.Helper()
	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list(?)", table).Scan(&indexes).Error)
	out := map[string]int{}
	for _, index := range indexes {
		if index.Origin == "pk" {
			continue
		}
		out[index.Name] = index.Unique
	}
	return out
}

func roleSchemaDefault(column sqliteColumn) string {
	if column.Dflt == nil {
		return "<none>"
	}
	return strings.Trim(*column.Dflt, `'"`)
}

func assertRoleSchemaSQLite(t *testing.T) {
	t.Helper()

	// roles
	roles := roleSchemaColumns(t, "roles")
	assert.Equal(t, []string{"created_at", "description", "id", "name", "name_key", "system_key", "updated_at"}, roleSchemaColumnNames(roles),
		"roles has no deleted_at: a role is hard-deleted")
	assert.Equal(t, 1, roles["id"].PK)
	for _, name := range []string{"name", "name_key", "description"} {
		assert.Equal(t, 1, roles[name].NotNull, "roles.%s is NOT NULL", name)
	}
	assert.Equal(t, 0, roles["system_key"].NotNull, "roles.system_key is nullable")
	assert.Equal(t, "varchar(20)", strings.ToLower(roles["system_key"].Type))
	assert.Equal(t, "varchar(100)", strings.ToLower(roles["name"].Type))
	assert.Equal(t, "varchar(100)", strings.ToLower(roles["name_key"].Type))
	assert.Equal(t, "varchar(500)", strings.ToLower(roles["description"].Type))
	assert.Equal(t, "", roleSchemaDefault(roles["description"]), "description defaults to ''")
	assert.Equal(t, map[string]int{"idx_roles_name_key": 1, "idx_roles_system_key": 1}, roleSchemaIndexes(t, "roles"))
	assert.Equal(t, []string{"name_key"}, customerExternalIDIndexColumns(t, "idx_roles_name_key"))
	assert.Equal(t, []string{"system_key"}, customerExternalIDIndexColumns(t, "idx_roles_system_key"))

	// role_permissions
	permissions := roleSchemaColumns(t, "role_permissions")
	assert.Equal(t, []string{"permission_key", "role_id", "scope"}, roleSchemaColumnNames(permissions))
	assert.Equal(t, 1, permissions["role_id"].PK, "role_id is the first primary key column")
	assert.Equal(t, 2, permissions["permission_key"].PK, "permission_key is the second primary key column")
	assert.Equal(t, "varchar(100)", strings.ToLower(permissions["permission_key"].Type))
	assert.Equal(t, 1, permissions["scope"].NotNull)
	assert.Equal(t, "varchar(10)", strings.ToLower(permissions["scope"].Type))
	assert.Equal(t, "all", roleSchemaDefault(permissions["scope"]))
	assert.Empty(t, roleSchemaIndexes(t, "role_permissions"), "no index beyond the primary key")
	assert.Contains(t, tableSQL(t, "role_permissions"),
		"CONSTRAINT `fk_roles_permissions` FOREIGN KEY (`role_id`) REFERENCES `roles`(`id`)")

	// role_audit_log
	audit := roleSchemaColumns(t, "role_audit_log")
	assert.Equal(t, []string{"action", "actor_user_id", "after_state", "auth_method", "before_state", "created_at", "id",
		"role_id", "role_name", "target_user_id"}, roleSchemaColumnNames(audit))
	for _, name := range []string{"created_at", "auth_method", "action", "role_name"} {
		assert.Equal(t, 1, audit[name].NotNull, "role_audit_log.%s is NOT NULL", name)
	}
	for _, name := range []string{"actor_user_id", "role_id", "target_user_id", "before_state", "after_state"} {
		assert.Equal(t, 0, audit[name].NotNull, "role_audit_log.%s is nullable", name)
	}
	assert.Equal(t, "", roleSchemaDefault(audit["auth_method"]))
	assert.Equal(t, "", roleSchemaDefault(audit["role_name"]))
	assert.Equal(t, "text", strings.ToLower(audit["before_state"].Type))
	assert.Equal(t, map[string]int{
		"idx_role_audit_log_created_at":     0,
		"idx_role_audit_log_role_id":        0,
		"idx_role_audit_log_target_user_id": 0,
	}, roleSchemaIndexes(t, "role_audit_log"))
	var auditFKs int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM pragma_foreign_key_list('role_audit_log')").Scan(&auditFKs).Error)
	assert.Zero(t, auditFKs, "the audit log carries no foreign key")

	// users
	users := roleSchemaColumns(t, "users")
	require.Contains(t, users, "role_id")
	assert.Equal(t, 0, users["role_id"].NotNull, "users.role_id is nullable")
	userIndexes := roleSchemaIndexes(t, "users")
	assert.Equal(t, 0, userIndexes["idx_users_role"])
	assert.Contains(t, userIndexes, "idx_users_role")
	assert.Contains(t, userIndexes, "idx_users_role_id")
	assert.Equal(t, []string{"role"}, customerExternalIDIndexColumns(t, "idx_users_role"))
	assert.Equal(t, []string{"role_id"}, customerExternalIDIndexColumns(t, "idx_users_role_id"))
	assert.Equal(t, 1, userIndexes["idx_users_email"], "idx_users_email stays unique")
	assert.Contains(t, userIndexes, "idx_users_deleted_at")
	assert.Contains(t, tableSQL(t, "users"),
		"CONSTRAINT `fk_users_custom_role` FOREIGN KEY (`role_id`) REFERENCES `roles`(`id`) ON DELETE RESTRICT")

	// The four system rows.
	assertSystemRoleRowsSQLite(t)
}

func assertSystemRoleRowsSQLite(t *testing.T) {
	t.Helper()
	var rows []struct {
		SystemKey   string
		Name        string
		NameKey     string
		Description string
	}
	require.NoError(t, DB.Raw("SELECT system_key, name, name_key, description FROM roles WHERE system_key IS NOT NULL ORDER BY id").Scan(&rows).Error)
	require.Len(t, rows, len(SystemRoleSeeds))
	for i, seed := range SystemRoleSeeds {
		assert.Equal(t, seed.SystemKey, rows[i].SystemKey)
		assert.Equal(t, seed.Name, rows[i].Name)
		assert.Equal(t, RoleNameKey(seed.Name), rows[i].NameKey)
		assert.Equal(t, seed.Description, rows[i].Description)
	}
}

func TestRoleSchemaSQLite(t *testing.T) {
	openRoleSchemaDB(t)
	assertRoleSchemaSQLite(t)

	// #259's text, verbatim.
	assert.Equal(t, []SystemRoleSeed{
		{SystemKey: "admin", Name: "Admin", Description: "Full access. Cannot be edited or deleted."},
		{SystemKey: "sales", Name: "Sales", Description: "Built-in sales access. Cannot be edited or deleted."},
		{SystemKey: "support", Name: "Support", Description: "Built-in support access. Cannot be edited or deleted."},
		{SystemKey: "customer", Name: "Customer", Description: "A customer's own login. Cannot be edited, deleted or cloned into portal access."},
	}, SystemRoleSeeds)

	// A second run leaves the schema alone.
	before := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, before, schemaSnapshot(t))

	// Enforcement: a dangling role_id is refused, a real one accepted, and the
	// referenced role cannot be deleted.
	err := DB.Exec("INSERT INTO users (email, password, first_name, last_name, role, role_id) VALUES ('dangling@example.com', 'x', 'D', 'R', 'custom', 999999)").Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")
	require.NoError(t, DB.Exec("INSERT INTO roles (created_at, updated_at, name, name_key, description) VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'Field Ops', 'field ops', '')").Error)
	var roleID uint
	require.NoError(t, DB.Raw("SELECT id FROM roles WHERE name_key = 'field ops'").Scan(&roleID).Error)
	require.NoError(t, DB.Exec("INSERT INTO users (email, password, first_name, last_name, role, role_id) VALUES ('custom@example.com', 'x', 'C', 'R', 'custom', ?)", roleID).Error)
	err = DB.Exec("DELETE FROM roles WHERE id = ?", roleID).Error
	require.Error(t, err, "ON DELETE RESTRICT refuses a referenced role")
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")
}

// TestRoleSQLiteUpgradeFromV120 upgrades the v1.2.0 file: adding
// fk_users_custom_role rebuilds users, which every other table references.
func TestRoleSQLiteUpgradeFromV120(t *testing.T) {
	openV120Fixture(t)
	require.False(t, DB.Migrator().HasColumn("users", "role_id"), "the fixture predates role_id")
	require.False(t, DB.Migrator().HasTable("roles"))

	type userRow struct {
		ID    uint
		Email string
		Role  string
	}
	var before []userRow
	require.NoError(t, DB.Raw("SELECT id, email, role FROM users ORDER BY id").Scan(&before).Error)
	require.NotEmpty(t, before)

	require.NoError(t, MigrateDatabase())
	assertUpgradedV120(t)
	assertRoleSchemaSQLite(t)

	var after []userRow
	require.NoError(t, DB.Raw("SELECT id, email, role FROM users ORDER BY id").Scan(&after).Error)
	assert.Equal(t, before, after, "every user is kept")
	var withRole int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM users WHERE role_id IS NOT NULL").Scan(&withRole).Error)
	assert.Zero(t, withRole, "every existing user has role_id NULL")

	assert.Contains(t, indexSQL(t, "users", "idx_users_email"), "UNIQUE", "idx_users_email survives the rebuild, unique")
	assert.NotEmpty(t, indexSQL(t, "users", "idx_users_deleted_at"), "idx_users_deleted_at survives the rebuild")

	var foreignKeys int
	require.NoError(t, DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	assert.Equal(t, 1, foreignKeys)
	err := DB.Exec("INSERT INTO users (email, password, first_name, last_name, role, role_id) VALUES ('dangling@example.com', 'x', 'D', 'R', 'custom', 999999)").Error
	require.Error(t, err, "a role_id naming no role is refused")
	assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")
}
