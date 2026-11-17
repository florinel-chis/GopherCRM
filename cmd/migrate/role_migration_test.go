package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #344 (#259 S2): 20261119120000_add_roles_and_permissions creates roles,
// role_permissions and role_audit_log, seeds the four system roles, and adds
// users.role_id with idx_users_role, idx_users_role_id and
// fk_users_custom_role (ON DELETE RESTRICT), under the names AutoMigrate gives
// them and behind INFORMATION_SCHEMA probes. Its down half turns every live
// custom-role user into an inactive support user, then drops all of it.

const (
	rolesVersion         = 20261119120000
	rolesPreviousVersion = quoteQuantityVersion
	rolesMigrationName   = "20261119120000_add_roles_and_permissions"
)

var roleMigrationTables = []string{"roles", "role_permissions", "role_audit_log"}

// sqlLiteral renders a Go string as a single-quoted SQL literal.
func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// TestRoleMigrationFiles needs no database: the pair exists, the up creates
// the three tables with their keys and seeds #259's four rows verbatim, the
// down demotes custom-role users before it drops anything, and a newer
// migration follows it (the newest-migration assertion now lives in
// TestWebhookMigrationFiles, #345).
func TestRoleMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, rolesMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, rolesMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: RBAC roles and permissions"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove RBAC roles and permissions"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, table := range roleMigrationTables {
		assert.Contains(t, upSQL, "create table if not exists "+table+" (", "the up creates %s only when missing", table)
		assert.Contains(t, downSQL, "drop table if exists "+table+";", "the down drops %s", table)
	}
	for _, fragment := range []string{
		"unique key idx_roles_system_key (system_key)",
		"unique key idx_roles_name_key (name_key)",
		"primary key (role_id, permission_key)",
		"constraint fk_roles_permissions foreign key (role_id) references roles (id)",
		"key idx_role_audit_log_created_at (created_at)",
		"key idx_role_audit_log_role_id (role_id)",
		"key idx_role_audit_log_target_user_id (target_user_id)",
		"'alter table users add column role_id bigint unsigned null'",
		"'create index idx_users_role on users (role)'",
		"'create index idx_users_role_id on users (role_id)'",
		"'alter table users add constraint fk_users_custom_role foreign key (role_id) references roles (id) on delete restrict'",
	} {
		assert.Contains(t, upSQL, fragment)
	}
	assert.Less(t, strings.Index(upSQL, "'create index idx_users_role_id"), strings.Index(upSQL, "'alter table users add constraint fk_users_custom_role"),
		"the index exists before the foreign key, so MySQL adds no index of its own")
	assert.Equal(t, 4, strings.Count(upSQL, "execute stmt;"), "four guarded ALTERs")
	for _, line := range strings.Split(upSQL, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "alter table") || strings.Contains(line, "create index") {
			assert.True(t, strings.HasPrefix(line, "set @preparedstatement = (select if((select count(*) from information_schema."), "unguarded: %q", line)
		}
	}

	// The seed: exactly #259's values, as models.SystemRoleSeeds holds them.
	assert.Equal(t, 1, strings.Count(upSQL, "insert ignore into roles (created_at, updated_at, system_key, name, name_key, description) values"))
	for _, seed := range models.SystemRoleSeeds {
		row := strings.Join([]string{sqlLiteral(seed.SystemKey), sqlLiteral(seed.Name), sqlLiteral(models.RoleNameKey(seed.Name)), sqlLiteral(seed.Description)}, ", ")
		assert.Contains(t, string(up), "(UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), "+row+")", "seed row %s", seed.SystemKey)
	}
	assert.Contains(t, string(up), "'A customer''s own login. Cannot be edited, deleted or cloned into portal access.'")

	// The down: the demotion first, then every drop, each guarded.
	demotion := "update users set role = 'support', is_active = 0 where role = 'custom' and deleted_at is null;"
	assert.Equal(t, 1, strings.Count(downSQL, demotion))
	for _, drop := range []string{
		"'alter table users drop foreign key fk_users_custom_role'",
		"'drop index idx_users_role_id on users'",
		"'alter table users drop column role_id'",
		"'drop index idx_users_role on users'",
		"drop table if exists role_permissions;",
		"drop table if exists role_audit_log;",
		"drop table if exists roles;",
	} {
		require.Contains(t, downSQL, drop)
		assert.Less(t, strings.Index(downSQL, demotion), strings.Index(downSQL, drop), "the demotion precedes %s", drop)
	}
	assert.Less(t, strings.Index(downSQL, "drop foreign key fk_users_custom_role"), strings.Index(downSQL, "'drop index idx_users_role_id"))
	assert.Less(t, strings.Index(downSQL, "drop table if exists role_permissions"), strings.Index(downSQL, "drop table if exists roles;"))
	assert.Equal(t, 4, strings.Count(downSQL, "execute stmt;"))

	// The model declares the same objects.
	tag, ok := reflectField(models.User{}, "RoleID")
	require.True(t, ok)
	assert.Contains(t, tag, "index:idx_users_role_id")
	tag, ok = reflectField(models.User{}, "CustomRole")
	require.True(t, ok)
	assert.Contains(t, tag, "constraint:OnDelete:RESTRICT")
	tag, ok = reflectField(models.User{}, "Role")
	require.True(t, ok)
	assert.Contains(t, tag, "index:idx_users_role")

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestWebhookMigrationFiles (#345).
	version, err := strconv.Atoi(strings.SplitN(rolesMigrationName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, rolesVersion, version)
	assert.Less(t, rolesVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Greater(t, rolesVersion, rolesPreviousVersion)
}

// roleObjects is every schema object the migration owns.
type roleObjects struct {
	Tables       map[string]tableSchema
	UserColumns  []string
	UserIndexes  []string
	UserFKs      []string
	DeleteRules  map[string]string
	TablesListed []string
}

func snapshotRoleObjects(t *testing.T, tdb *migrationTestDB) roleObjects {
	t.Helper()
	objects := roleObjects{Tables: map[string]tableSchema{}, UserIndexes: []string{}, UserFKs: []string{}, DeleteRules: map[string]string{}}
	for _, table := range roleMigrationTables {
		objects.Tables[table] = snapshotTable(t, tdb, table)
	}
	users := snapshotTable(t, tdb, "users")
	objects.UserColumns = ownedColumns(users, []string{"role_id"})
	for _, line := range users.Indexes {
		if strings.Contains(line, "(role)") || strings.Contains(line, "(role_id)") || strings.Contains(line, "role_id,") || strings.Contains(line, ",role_id") {
			objects.UserIndexes = append(objects.UserIndexes, line)
		}
	}
	for _, line := range users.ForeignKeys {
		if strings.Contains(line, " role_id ") {
			objects.UserFKs = append(objects.UserFKs, line)
		}
	}
	rows, err := tdb.sql.Query(`SELECT CONSTRAINT_NAME, DELETE_RULE FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA = ? AND CONSTRAINT_NAME IN ('fk_roles_permissions', 'fk_users_custom_role')`, tdb.name)
	require.NoError(t, err)
	for rows.Next() {
		var name, rule string
		require.NoError(t, rows.Scan(&name, &rule))
		objects.DeleteRules[name] = rule
	}
	require.NoError(t, rows.Err())
	rows.Close()
	for _, table := range listTables(t, tdb.sql, tdb.name) {
		for _, owned := range roleMigrationTables {
			if table == owned {
				objects.TablesListed = append(objects.TablesListed, table)
			}
		}
	}
	return objects
}

// assertNoRoleObjects is the state after the full down.
func assertNoRoleObjects(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	objects := snapshotRoleObjects(t, tdb)
	assert.Empty(t, objects.TablesListed, "the three tables are dropped")
	assert.Empty(t, objects.UserColumns, "users.role_id is dropped")
	assert.Empty(t, objects.UserIndexes, "idx_users_role and idx_users_role_id are dropped")
	assert.Empty(t, objects.UserFKs, "fk_users_custom_role is dropped")
	assert.Empty(t, objects.DeleteRules)
}

// assertSystemRoleRows checks that roles holds exactly the four seed rows.
func assertSystemRoleRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	var total int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM roles").Scan(&total))
	assert.Equal(t, len(models.SystemRoleSeeds), total, "roles holds exactly four rows")
	rows, err := tdb.sql.Query("SELECT system_key, name, name_key, description FROM roles WHERE system_key IS NOT NULL")
	require.NoError(t, err)
	defer rows.Close()
	got := map[string][3]string{}
	for rows.Next() {
		var key, name, nameKey, description string
		require.NoError(t, rows.Scan(&key, &name, &nameKey, &description))
		got[key] = [3]string{name, nameKey, description}
	}
	require.NoError(t, rows.Err())
	want := map[string][3]string{}
	for _, seed := range models.SystemRoleSeeds {
		want[seed.SystemKey] = [3]string{seed.Name, models.RoleNameKey(seed.Name), seed.Description}
	}
	assert.Equal(t, want, got)
}

func TestMigrationRolesSchema(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: what models.MigrateDatabase builds on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "roles_ref")
	require.NoError(t, reference.m.Migrate(rolesPreviousVersion))
	assertNoRoleObjects(t, reference)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotRoleObjects(t, reference)
	assert.Equal(t, roleMigrationTables, sortedLike(want.TablesListed, roleMigrationTables))
	for _, table := range roleMigrationTables {
		require.NotEmpty(t, want.Tables[table].Columns, "MigrateDatabase created %s", table)
	}
	assert.Contains(t, want.Tables["roles"].Indexes, "idx_roles_system_key (system_key) non_unique=0")
	assert.Contains(t, want.Tables["roles"].Indexes, "idx_roles_name_key (name_key) non_unique=0")
	for _, index := range []string{"idx_role_audit_log_created_at (created_at) non_unique=1", "idx_role_audit_log_role_id (role_id) non_unique=1", "idx_role_audit_log_target_user_id (target_user_id) non_unique=1"} {
		assert.Contains(t, want.Tables["role_audit_log"].Indexes, index)
	}
	assert.Equal(t, []string{"fk_roles_permissions role_id -> roles.id"}, want.Tables["role_permissions"].ForeignKeys)
	assert.Empty(t, want.Tables["role_audit_log"].ForeignKeys, "the audit log has no foreign key")
	assert.Equal(t, []string{"idx_users_role (role) non_unique=1", "idx_users_role_id (role_id) non_unique=1"}, want.UserIndexes,
		"exactly the two named indexes; no implicit foreign-key index")
	assert.Equal(t, []string{"fk_users_custom_role role_id -> roles.id"}, want.UserFKs)
	require.Len(t, want.UserColumns, 1)
	assert.Contains(t, want.UserColumns[0], "bigint")
	assert.Contains(t, want.UserColumns[0], "unsigned")
	assert.Contains(t, want.UserColumns[0], "nullable=YES")
	assert.Equal(t, "RESTRICT", want.DeleteRules["fk_users_custom_role"])
	assert.Contains(t, want.DeleteRules, "fk_roles_permissions")
	assertSystemRoleRows(t, reference)

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "roles_mig")
			require.NoError(t, tdb.m.Migrate(rolesPreviousVersion))
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotRoleObjects(t, tdb))
			} else {
				assertNoRoleObjects(t, tdb)
			}

			require.NoError(t, tdb.m.Migrate(rolesVersion), "the up applies (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(rolesVersion), version)
			assert.Equal(t, want, snapshotRoleObjects(t, tdb), "the SQL migration and MigrateDatabase produce the same objects")
			assertSystemRoleRows(t, tdb)
			require.ErrorIs(t, tdb.m.Migrate(rolesVersion), migrate.ErrNoChange)

			// The application's start on top changes nothing it owns.
			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotRoleObjects(t, tdb))
			assertSystemRoleRows(t, tdb)

			// The down removes everything; the up applies again.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(rolesPreviousVersion), version)
			assertNoRoleObjects(t, tdb)

			require.NoError(t, tdb.m.Migrate(rolesVersion))
			assert.Equal(t, want, snapshotRoleObjects(t, tdb))
			assertSystemRoleRows(t, tdb)

			// The down is guarded: with the foreign key and an index already
			// gone it still succeeds.
			_, err = tdb.sql.Exec("ALTER TABLE users DROP FOREIGN KEY fk_users_custom_role")
			require.NoError(t, err)
			_, err = tdb.sql.Exec("DROP INDEX idx_users_role ON users")
			require.NoError(t, err)
			require.NoError(t, tdb.m.Steps(-1), "every drop of the down is guarded")
			assertNoRoleObjects(t, tdb)
		})
	}
}

// sortedLike returns the elements of got in the order of reference.
func sortedLike(got, reference []string) []string {
	present := map[string]bool{}
	for _, value := range got {
		present[value] = true
	}
	out := []string{}
	for _, value := range reference {
		if present[value] {
			out = append(out, value)
		}
	}
	return out
}

// userRoleState is what the down may or may not change on a user.
type userRoleState struct {
	Role         string
	IsActive     bool
	TokenVersion int64
	UpdatedAt    string
}

func readUserRoleStates(t *testing.T, tdb *migrationTestDB) map[string]userRoleState {
	t.Helper()
	rows, err := tdb.sql.Query("SELECT email, role, is_active, token_version, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f') FROM users")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]userRoleState{}
	for rows.Next() {
		var email string
		var state userRoleState
		require.NoError(t, rows.Scan(&email, &state.Role, &state.IsActive, &state.TokenVersion, &state.UpdatedAt))
		out[email] = state
	}
	require.NoError(t, rows.Err())
	return out
}

func TestMigrationRolesDownDemotesCustomUsers(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}

	tdb := openMigrationTestDB(t, baseDSN, "roles_down")
	require.NoError(t, tdb.m.Migrate(rolesVersion))

	result, err := tdb.sql.Exec("INSERT INTO roles (created_at, updated_at, name, name_key, description) VALUES (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'Field Ops', 'field ops', '')")
	require.NoError(t, err)
	customRoleID, err := result.LastInsertId()
	require.NoError(t, err)
	_, err = tdb.sql.Exec("INSERT INTO role_permissions (role_id, permission_key, scope) VALUES (?, 'leads.read', 'own')", customRoleID)
	require.NoError(t, err)

	const insertUser = "INSERT INTO users (email, password, first_name, last_name, role, is_active, token_version, role_id, created_at, updated_at, deleted_at) " +
		"VALUES (?, 'hash', 'Role', 'User', ?, ?, ?, ?, '2026-11-01 10:00:00.123', '2026-11-02 11:00:00.456', ?)"
	users := []struct {
		email        string
		role         string
		active       bool
		tokenVersion int
		roleID       interface{}
		deletedAt    interface{}
	}{
		{"custom-live@example.com", "custom", true, 3, customRoleID, nil},
		{"custom-live-inactive@example.com", "custom", false, 0, customRoleID, nil},
		{"custom-erased@example.com", "custom", false, 5, customRoleID, "2026-11-03 12:00:00.000"},
		{"admin@example.com", "admin", true, 1, nil, nil},
		{"sales@example.com", "sales", true, 0, nil, nil},
		{"support-inactive@example.com", "support", false, 2, nil, nil},
		{"customer@example.com", "customer", true, 0, nil, nil},
	}
	for _, user := range users {
		_, err := tdb.sql.Exec(insertUser, user.email, user.role, user.active, user.tokenVersion, user.roleID, user.deletedAt)
		require.NoError(t, err, user.email)
	}
	before := readUserRoleStates(t, tdb)

	require.NoError(t, tdb.m.Steps(-1), "a custom role assigned to users does not make the down fail")
	version, dirty, err := tdb.m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(rolesPreviousVersion), version)

	after := readUserRoleStates(t, tdb)
	require.Len(t, after, len(users), "no user is removed")
	for _, email := range []string{"custom-live@example.com", "custom-live-inactive@example.com"} {
		assert.Equal(t, "support", after[email].Role, "%s becomes support", email)
		assert.False(t, after[email].IsActive, "%s becomes inactive", email)
		assert.Equal(t, before[email].TokenVersion, after[email].TokenVersion, email)
	}
	for _, email := range []string{"custom-erased@example.com", "admin@example.com", "sales@example.com", "support-inactive@example.com", "customer@example.com"} {
		assert.Equal(t, before[email], after[email], "%s is unchanged (role, is_active, token_version, updated_at)", email)
	}
	assert.Equal(t, "custom", after["custom-erased@example.com"].Role, "an erased custom-role user is left alone")

	assertNoRoleObjects(t, tdb)
}
