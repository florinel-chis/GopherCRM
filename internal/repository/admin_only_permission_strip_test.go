package repository

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #524: every start removes settings.sso_manage and settings.scim_manage from
// every custom role and writes one role_updated audit row (auth_method
// migration, actor NULL) per role it changed; roles without the keys and the
// system roles are left alone, and a rerun writes nothing.

func TestAdminOnlyPermissionStripSQLite(t *testing.T) {
	runAdminOnlyPermissionStrip(t, openRoleDB(t, "admin_only_strip", false), false)
}

func TestAdminOnlyPermissionStripMySQL(t *testing.T) {
	runAdminOnlyPermissionStrip(t, openRoleDB(t, "admin_only_strip", true), true)
}

type stripAuditGrant struct {
	Key   string `json:"key"`
	Scope string `json:"scope"`
}

type stripAuditState struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Permissions []stripAuditGrant `json:"permissions"`
}

type stripAuditRow struct {
	ID           uint
	ActorUserID  *uint
	AuthMethod   string
	Action       string
	RoleID       *uint
	RoleName     string
	TargetUserID *uint
	BeforeState  *string
	AfterState   *string
	CreatedAt    time.Time
}

func readStripAuditRows(t *testing.T, db *gorm.DB) []stripAuditRow {
	t.Helper()
	var rows []stripAuditRow
	require.NoError(t, db.Raw("SELECT id, actor_user_id, auth_method, action, role_id, role_name, target_user_id, before_state, after_state, created_at FROM role_audit_log ORDER BY id").Scan(&rows).Error)
	return rows
}

func decodeStripState(t *testing.T, raw *string) stripAuditState {
	t.Helper()
	require.NotNil(t, raw)
	var state stripAuditState
	require.NoError(t, json.Unmarshal([]byte(*raw), &state))
	return state
}

// stripFixtureGrants is role A's permission list: every leaf a custom role can
// hold (at own where the leaf allows it, so both scopes appear) plus the two
// admin-only keys at all.
func stripFixtureGrants() []stripAuditGrant {
	var out []stripAuditGrant
	for _, leaf := range permissions.Leaves() {
		switch {
		case leaf.AdminOnly:
			out = append(out, stripAuditGrant{Key: leaf.Key, Scope: "all"})
		case leaf.CustomRoleMayHold() && leaf.AllowsOwn:
			out = append(out, stripAuditGrant{Key: leaf.Key, Scope: "own"})
		case leaf.CustomRoleMayHold():
			out = append(out, stripAuditGrant{Key: leaf.Key, Scope: "all"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func withoutAdminOnly(grants []stripAuditGrant) []stripAuditGrant {
	out := []stripAuditGrant{}
	for _, grant := range grants {
		if grant.Key != permissions.SettingsSSOManage && grant.Key != permissions.SettingsSCIMManage {
			out = append(out, grant)
		}
	}
	return out
}

func insertStripGrants(t *testing.T, db *gorm.DB, roleID uint, grants []stripAuditGrant) {
	t.Helper()
	for _, grant := range grants {
		require.NoError(t, db.Exec("INSERT INTO role_permissions (role_id, permission_key, scope) VALUES (?, ?, ?)", roleID, grant.Key, grant.Scope).Error)
	}
}

func runAdminOnlyPermissionStrip(t *testing.T, db *gorm.DB, useMySQL bool) {
	// The handle's clock runs three hours east of UTC, so an audit row stamped
	// in local time instead of UTC is told apart on every host and driver.
	previousNow := db.Config.NowFunc
	t.Cleanup(func() { db.Config.NowFunc = previousNow })
	db.Config.NowFunc = func() time.Time { return time.Now().In(time.FixedZone("UTC+3", 3*3600)) }
	roles := NewRoleRepository(db)
	// The custom roles are created after the first start, as the role API did
	// before the keys became admin-only; their rows are written straight into
	// role_permissions.
	roleA := &models.Role{Name: `Field "Ops" Ștefan`, Description: `regional "east"`}
	roleB := &models.Role{Name: "SSO only", Description: ""}
	roleC := &models.Role{Name: "Unaffected", Description: "no admin-only key"}
	for _, role := range []*models.Role{roleA, roleB, roleC} {
		require.NoError(t, roles.Create(role))
	}
	grantsA := stripFixtureGrants()
	grantsB := []stripAuditGrant{{Key: permissions.SettingsSSOManage, Scope: "all"}}
	grantsC := []stripAuditGrant{{Key: permissions.LeadsView, Scope: "own"}, {Key: permissions.SettingsUpdate, Scope: "all"}}
	seed := func() {
		insertStripGrants(t, db, roleA.ID, grantsA)
		insertStripGrants(t, db, roleB.ID, grantsB)
		insertStripGrants(t, db, roleC.ID, grantsC)
	}
	seed()
	require.Contains(t, grantsA, stripAuditGrant{Key: permissions.SettingsSCIMManage, Scope: "all"})

	var systemRoleIDs []uint
	require.NoError(t, db.Raw("SELECT id FROM roles WHERE system_key IS NOT NULL ORDER BY id").Scan(&systemRoleIDs).Error)
	require.Len(t, systemRoleIDs, 4)
	systemBefore := map[uint][]permissionRow{}
	for _, id := range systemRoleIDs {
		systemBefore[id] = readPermissionRows(t, db, id)
	}
	rolesBefore := readRoleRows(t, db)
	permissionsC := readPermissionRows(t, db, roleC.ID)
	require.Empty(t, readStripAuditRows(t, db))

	grantRows := func(roleID uint, grants []stripAuditGrant) []permissionRow {
		out := []permissionRow{}
		for _, grant := range grants {
			out = append(out, permissionRow{RoleID: roleID, PermissionKey: grant.Key, Scope: grant.Scope})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].PermissionKey < out[j].PermissionKey })
		return out
	}
	assertStripped := func(t *testing.T) {
		t.Helper()
		assert.Equal(t, grantRows(roleA.ID, withoutAdminOnly(grantsA)), readPermissionRows(t, db, roleA.ID), "role A loses exactly the two keys")
		assert.Empty(t, readPermissionRows(t, db, roleB.ID), "role B loses its one key")
		assert.Equal(t, permissionsC, readPermissionRows(t, db, roleC.ID), "role C is unchanged")
		for _, id := range systemRoleIDs {
			assert.Equal(t, systemBefore[id], readPermissionRows(t, db, id), "system role %d is unchanged", id)
		}
		assert.Equal(t, rolesBefore, readRoleRows(t, db), "no roles row changes")
	}
	assertAuditRows := func(t *testing.T, rows []stripAuditRow) {
		t.Helper()
		require.Len(t, rows, 2, "one row for A and one for B, none for C")
		want := map[uint]struct {
			role  *models.Role
			grant []stripAuditGrant
		}{roleA.ID: {roleA, grantsA}, roleB.ID: {roleB, grantsB}}
		for _, row := range rows {
			require.NotNil(t, row.RoleID)
			expected, ok := want[*row.RoleID]
			require.True(t, ok, "an audit row for role %d", *row.RoleID)
			delete(want, *row.RoleID)
			assert.Equal(t, "role_updated", row.Action)
			assert.Nil(t, row.ActorUserID)
			assert.Equal(t, "migration", row.AuthMethod)
			assert.Equal(t, expected.role.Name, row.RoleName)
			assert.Nil(t, row.TargetUserID)
			assert.WithinDuration(t, time.Now().UTC(), row.CreatedAt, time.Minute, "created_at is stamped in UTC")
			_, offset := row.CreatedAt.Zone()
			assert.Zero(t, offset, "created_at is stored in UTC, as roleAuditRepository.Create stores it")
			assert.Equal(t, stripAuditState{Name: expected.role.Name, Description: expected.role.Description, Permissions: expected.grant},
				decodeStripState(t, row.BeforeState), "before_state of role %d", *row.RoleID)
			assert.Equal(t, stripAuditState{Name: expected.role.Name, Description: expected.role.Description, Permissions: withoutAdminOnly(expected.grant)},
				decodeStripState(t, row.AfterState), "after_state of role %d", *row.RoleID)
		}
	}

	require.NoError(t, models.MigrateDatabase())
	assertStripped(t)
	firstRows := readStripAuditRows(t, db)
	assertAuditRows(t, firstRows)

	t.Run("a second run writes nothing", func(t *testing.T) {
		require.NoError(t, models.MigrateDatabase())
		assertStripped(t)
		assert.Equal(t, firstRows, readStripAuditRows(t, db))
	})

	if !useMySQL {
		return
	}

	t.Run("two replicas booting at once write one row per role", func(t *testing.T) {
		require.NoError(t, db.Exec("DELETE FROM role_audit_log").Error)
		require.NoError(t, db.Exec("DELETE FROM role_permissions WHERE role_id IN ?", []uint{roleA.ID, roleB.ID, roleC.ID}).Error)
		seed()
		mysqlDialector, ok := db.Dialector.(*gormmysql.Dialector)
		require.True(t, ok)
		pools := make([]*gorm.DB, 2)
		for i := range pools {
			pool, err := gorm.Open(gormmysql.Open(mysqlDialector.DSN), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			sqlDB, err := pool.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			pools[i] = pool
		}

		start := make(chan struct{})
		errs := make([]error, len(pools))
		var wg sync.WaitGroup
		for i, pool := range pools {
			wg.Add(1)
			go func(i int, pool *gorm.DB) {
				defer wg.Done()
				<-start
				errs[i] = models.MigrateDatabaseWith(pool)
			}(i, pool)
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Minute):
			t.Fatal("the concurrent starts did not finish")
		}
		for i, err := range errs {
			assert.NoError(t, err, "replica %d", i)
		}
		assertStripped(t)
		assertAuditRows(t, readStripAuditRows(t, db))
	})
}
