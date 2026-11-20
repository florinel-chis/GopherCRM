package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #524: 20261129120001_admin_only_sso_scim_permissions removes
// settings.sso_manage and settings.scim_manage from every custom role and
// writes one role_updated audit row (auth_method migration, actor NULL) per
// role it changes, with the same content the startup step
// (models.MigrateDatabase) writes. The down half is a no-op: the data step is
// one-way.

const (
	adminOnlyPermissionsVersion         = 20261129120001
	adminOnlyPermissionsPreviousVersion = scimGroupMembersVersion
	adminOnlyPermissionsMigrationName   = "20261129120001_admin_only_sso_scim_permissions"
)

// TestAdminOnlyPermissionsMigrationFiles needs no database: the pair exists
// with its headers, the up half writes role_audit_log and deletes the two
// admin-only keys only, the down half writes nothing, and it is older than
// the newest migration.
func TestAdminOnlyPermissionsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, adminOnlyPermissionsMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, adminOnlyPermissionsMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Admin-only single sign-on and SCIM permissions (#524)"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Keep admin-only single sign-on and SCIM permissions (#524)"), "the down half has its header")
	assert.Contains(t, string(up), "The\n-- application does the same on every start", "the header says the application does the same")

	statementsOf := func(sql string) string {
		var lines []string
		for _, line := range strings.Split(strings.ToLower(sql), "\n") {
			if !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	upStatements, downStatements := statementsOf(string(up)), statementsOf(string(down))

	assert.Equal(t, 1, strings.Count(upStatements, "insert into role_audit_log"), "one audit insert")
	assert.Equal(t, 1, strings.Count(upStatements, "insert "), "no other insert")
	assert.Equal(t, 1, strings.Count(upStatements, "delete "), "one delete")
	assert.Contains(t, upStatements, "delete rp from role_permissions rp")
	assert.Contains(t, upStatements, "where r.system_key is null\n  and rp.permission_key in ('settings.scim_manage', 'settings.sso_manage');",
		"the delete removes the two keys of custom roles only")
	assert.Contains(t, upStatements, "set session group_concat_max_len = 1048576;")
	assert.Contains(t, upStatements, "utc_timestamp(3), null, 'migration', 'role_updated'")
	for _, forbidden := range []string{"update ", "drop ", "alter table", "create table", "truncate"} {
		assert.NotContains(t, upStatements, forbidden)
	}
	// Every permission key the up half names is one of the admin-only keys.
	keyLiteral := regexp.MustCompile(`'([a-z_]+\.[a-z_]+)'`)
	for _, match := range keyLiteral.FindAllStringSubmatch(upStatements, -1) {
		assert.Contains(t, models.AdminOnlyPermissionKeys, match[1])
	}

	for _, forbidden := range []string{"insert ", "delete ", "update ", "drop ", "alter ", "create ", "replace "} {
		assert.NotContains(t, downStatements, forbidden, "the down half writes nothing")
	}
	assert.Contains(t, downStatements, "do 0;", "the down half is a non-empty no-op")

	// Older than the newest migration (the newest-migration assertions moved
	// to TestAPIKeyRevokedAtMigrationFiles, #525).
	assert.Less(t, adminOnlyPermissionsVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Equal(t, adminOnlyPermissionsPreviousVersion+1, adminOnlyPermissionsVersion)
}

type adminOnlyAuditGrant struct {
	Key   string `json:"key"`
	Scope string `json:"scope"`
}

type adminOnlyAuditState struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Permissions []adminOnlyAuditGrant `json:"permissions"`
}

// adminOnlyAuditRow is a role_audit_log row with its states decoded.
type adminOnlyAuditRow struct {
	ActorUserIDNull  bool
	AuthMethod       string
	Action           string
	RoleName         string
	TargetUserIDNull bool
	Before, After    adminOnlyAuditState
}

// adminOnlyFixture seeds three custom roles: A holds every leaf a custom role
// can hold plus both admin-only keys and has a name with a double quote and a
// non-ASCII letter, B holds only settings.sso_manage, C holds neither.
func adminOnlyFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	insertRole := func(name, description string) int64 {
		result, err := db.Exec("INSERT INTO roles (created_at, updated_at, name, name_key, description) VALUES (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), ?, ?, ?)",
			name, models.RoleNameKey(name), description)
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		return id
	}
	grant := func(roleID int64, key, scope string) {
		_, err := db.Exec("INSERT INTO role_permissions (role_id, permission_key, scope) VALUES (?, ?, ?)", roleID, key, scope)
		require.NoError(t, err)
	}
	a := insertRole(`Field "Ops" Ștefan`, `regional "east"`)
	for _, leaf := range permissions.Leaves() {
		switch {
		case leaf.AdminOnly:
			grant(a, leaf.Key, "all")
		case leaf.CustomRoleMayHold() && leaf.AllowsOwn:
			grant(a, leaf.Key, "own")
		case leaf.CustomRoleMayHold():
			grant(a, leaf.Key, "all")
		}
	}
	b := insertRole("SSO only", "")
	grant(b, permissions.SettingsSSOManage, "all")
	c := insertRole("Unaffected", "no admin-only key")
	grant(c, permissions.LeadsView, "own")
	grant(c, permissions.SettingsUpdate, "all")
}

// readAdminOnlyAuditRows returns the audit rows keyed by role name, with the
// states decoded.
func readAdminOnlyAuditRows(t *testing.T, db *sql.DB) map[string]adminOnlyAuditRow {
	t.Helper()
	rows, err := db.Query("SELECT actor_user_id IS NULL, auth_method, action, role_name, target_user_id IS NULL, before_state, after_state FROM role_audit_log ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]adminOnlyAuditRow{}
	for rows.Next() {
		var row adminOnlyAuditRow
		var before, after sql.NullString
		require.NoError(t, rows.Scan(&row.ActorUserIDNull, &row.AuthMethod, &row.Action, &row.RoleName, &row.TargetUserIDNull, &before, &after))
		require.True(t, before.Valid)
		require.True(t, after.Valid)
		require.NoError(t, json.Unmarshal([]byte(before.String), &row.Before))
		require.NoError(t, json.Unmarshal([]byte(after.String), &row.After))
		_, duplicate := out[row.RoleName]
		require.False(t, duplicate, "one audit row per role: %s", row.RoleName)
		out[row.RoleName] = row
	}
	require.NoError(t, rows.Err())
	return out
}

// readAdminOnlyPermissionRows returns every role_permissions row as
// "role name|key|scope", sorted.
func readAdminOnlyPermissionRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT r.name, rp.permission_key, rp.scope FROM role_permissions rp JOIN roles r ON r.id = rp.role_id")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, key, scope string
		require.NoError(t, rows.Scan(&name, &key, &scope))
		out = append(out, name+"|"+key+"|"+scope)
	}
	require.NoError(t, rows.Err())
	sort.Strings(out)
	return out
}

func countAuditRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM role_audit_log").Scan(&count))
	return count
}

func TestAdminOnlyPermissionsMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The reference: what the startup step writes for the same fixture on the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "aop_ref")
	require.NoError(t, reference.m.Migrate(adminOnlyPermissionsPreviousVersion))
	adminOnlyFixture(t, reference.sql)
	runMigrateDatabase(t, reference.dsn)
	want := readAdminOnlyAuditRows(t, reference.sql)
	require.Len(t, want, 2, "the startup step writes one row for A and one for B")
	wantPermissions := readAdminOnlyPermissionRows(t, reference.sql)

	tdb := openMigrationTestDB(t, baseDSN, "aop_mig")
	require.NoError(t, tdb.m.Migrate(adminOnlyPermissionsPreviousVersion))
	adminOnlyFixture(t, tdb.sql)
	before := readAdminOnlyPermissionRows(t, tdb.sql)
	require.NoError(t, tdb.m.Migrate(adminOnlyPermissionsVersion))
	version, dirty, err := tdb.m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(adminOnlyPermissionsVersion), version)

	got := readAdminOnlyAuditRows(t, tdb.sql)
	assert.Equal(t, want, got, "the migration writes the rows the startup step writes")
	b, ok := got["SSO only"]
	require.True(t, ok)
	assert.NotNil(t, b.After.Permissions, "an emptied role's after_state holds [] and not null")
	assert.Empty(t, b.After.Permissions)
	for name, row := range got {
		assert.True(t, row.ActorUserIDNull, name)
		assert.True(t, row.TargetUserIDNull, name)
		assert.Equal(t, "migration", row.AuthMethod, name)
		assert.Equal(t, "role_updated", row.Action, name)
		assert.Equal(t, name, row.Before.Name)
		assert.Equal(t, name, row.After.Name)
	}
	_, hasC := got["Unaffected"]
	assert.False(t, hasC, "no row for a role without the keys")

	after := readAdminOnlyPermissionRows(t, tdb.sql)
	assert.Equal(t, wantPermissions, after, "the migration leaves what the startup step leaves")
	var removed []string
	for _, row := range before {
		if !containsString(after, row) {
			removed = append(removed, row)
		}
	}
	assert.Equal(t, []string{
		`Field "Ops" Ștefan|settings.scim_manage|all`,
		`Field "Ops" Ștefan|settings.sso_manage|all`,
		"SSO only|settings.sso_manage|all",
	}, removed, "exactly the admin-only rows of the custom roles are removed")

	// The application starting on top adds nothing.
	runMigrateDatabase(t, tdb.dsn)
	assert.Equal(t, 2, countAuditRows(t, tdb.sql))
	assert.Equal(t, after, readAdminOnlyPermissionRows(t, tdb.sql))

	// Rollback leaves both tables as they are.
	audit := readAdminOnlyAuditRows(t, tdb.sql)
	require.NoError(t, tdb.m.Steps(-1))
	version, dirty, err = tdb.m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(adminOnlyPermissionsPreviousVersion), version)
	assert.Equal(t, after, readAdminOnlyPermissionRows(t, tdb.sql), "rollback restores no grant")
	assert.Equal(t, audit, readAdminOnlyAuditRows(t, tdb.sql), "rollback keeps the audit rows")

	// Reapplying finds nothing to change.
	require.NoError(t, tdb.m.Migrate(adminOnlyPermissionsVersion))
	assert.Equal(t, 2, countAuditRows(t, tdb.sql))
	assert.Equal(t, after, readAdminOnlyPermissionRows(t, tdb.sql))
}
