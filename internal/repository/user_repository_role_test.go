package repository

import (
	"strings"
	"sync"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #344: the user repository edits of #259 S2 — role_id updatable, the custom
// role preload in GetByID, and LockActiveAdmins and ClearRoleIDOfErased behind
// the optional UserRoleMaintenance interface — on SQLite and MySQL.

func TestUserRepositoryRoleColumnsSQLite(t *testing.T) {
	runUserRepositoryRoleColumns(t, migrateRoleDB(t, testsupport.Open(t, testsupport.SQLite, "user_role_columns", roleDB)), false)
}

func TestUserRepositoryRoleColumnsMySQL(t *testing.T) {
	runUserRepositoryRoleColumns(t, migrateRoleDB(t, testsupport.Open(t, testsupport.MySQL, "user_role_columns", roleDB)), true)
}

func storedRoleID(t *testing.T, db *gorm.DB, userID uint) *uint {
	t.Helper()
	var row struct{ RoleID *uint }
	require.NoError(t, db.Raw("SELECT role_id FROM users WHERE id = ?", userID).Scan(&row).Error)
	return row.RoleID
}

func runUserRepositoryRoleColumns(t *testing.T, db *gorm.DB, useMySQL bool) {
	capture := installRoleSQLCapture(t, db, "user_role_columns")
	users := NewUserRepository(db)
	maintenance, ok := users.(UserRoleMaintenance)
	require.True(t, ok, "the production user repository implements UserRoleMaintenance")
	roles := NewRoleRepository(db)

	newUser := func(t *testing.T, email string, role models.UserRole, active bool) *models.User {
		t.Helper()
		user := &models.User{Email: email, Password: "hash", FirstName: "Role", LastName: "Columns", Role: role, IsActive: true}
		require.NoError(t, users.Create(user))
		if !active {
			require.NoError(t, users.UpdateFields(user.ID, map[string]interface{}{"is_active": false}))
		}
		return user
	}
	newRole := func(t *testing.T, name string) *models.Role {
		t.Helper()
		role := &models.Role{Name: name}
		require.NoError(t, roles.Create(role))
		return role
	}

	fieldOps := newRole(t, "Field Ops")
	require.NoError(t, roles.ReplacePermissions(fieldOps.ID, []models.RolePermission{
		{PermissionKey: "leads.read", Scope: models.PermissionScopeOwn},
		{PermissionKey: "deals.read", Scope: models.PermissionScopeAll},
	}))

	t.Run("GetByID of a user with role_id NULL is one SELECT against users", func(t *testing.T) {
		user := newUser(t, "plain@example.com", models.RoleSales, true)
		capture.reset()
		got, err := users.GetByID(user.ID)
		require.NoError(t, err)
		statements := capture.all()
		require.Len(t, statements, 1, "exactly one statement: %v", statements)
		lower := strings.ToLower(statements[0])
		assert.True(t, strings.HasPrefix(strings.TrimSpace(lower), "select"))
		assert.Contains(t, lower, "users")
		assert.NotContains(t, lower, "roles")
		assert.Nil(t, got.RoleID)
		assert.Nil(t, got.CustomRole)
	})

	t.Run("UpdateFields sets and clears role_id; GetByID fills the custom role", func(t *testing.T) {
		user := newUser(t, "custom@example.com", models.RoleSales, true)
		require.NoError(t, users.UpdateFields(user.ID, map[string]interface{}{"role": models.RoleCustom, "role_id": fieldOps.ID}))
		require.NotNil(t, storedRoleID(t, db, user.ID))
		assert.Equal(t, fieldOps.ID, *storedRoleID(t, db, user.ID))

		got, err := users.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, models.UserRole(models.RoleCustom), got.Role)
		require.NotNil(t, got.RoleID)
		require.NotNil(t, got.CustomRole, "the custom role is preloaded")
		assert.Equal(t, fieldOps.ID, got.CustomRole.ID)
		assert.Equal(t, "Field Ops", got.CustomRole.Name)
		assert.Equal(t, []string{"deals.read=all", "leads.read=own"}, permissionKeys(got.CustomRole.Permissions))

		// Through a transaction-bound repository as well.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			inTx, err := users.WithTx(tx).GetByID(user.ID)
			if err != nil {
				return err
			}
			assert.NotNil(t, inTx.CustomRole)
			return nil
		}))

		require.NoError(t, users.UpdateFields(user.ID, map[string]interface{}{"role": models.RoleSales, "role_id": nil}))
		assert.Nil(t, storedRoleID(t, db, user.ID))
		got, err = users.GetByID(user.ID)
		require.NoError(t, err)
		assert.Nil(t, got.CustomRole)

		// A role_id naming no role is refused by the foreign key.
		err = users.UpdateFields(user.ID, map[string]interface{}{"role_id": uint(99999999)})
		require.Error(t, err)
		assert.Nil(t, storedRoleID(t, db, user.ID))
	})

	t.Run("LockActiveAdmins discovers plainly, locks by primary key and re-checks", func(t *testing.T) {
		first := newUser(t, "admin-first@example.com", models.RoleAdmin, true)
		newUser(t, "admin-inactive@example.com", models.RoleAdmin, false)
		erased := newUser(t, "admin-erased@example.com", models.RoleAdmin, true)
		require.NoError(t, db.Delete(&models.User{}, erased.ID).Error)
		newUser(t, "sales-active@example.com", models.RoleSales, true)
		second := newUser(t, "admin-second@example.com", models.RoleAdmin, true)
		third := newUser(t, "admin-third@example.com", models.RoleAdmin, true)

		capture.reset()
		admins, err := maintenance.LockActiveAdmins()
		require.NoError(t, err)
		ids := []uint{}
		for _, admin := range admins {
			ids = append(ids, admin.ID)
		}
		assert.Equal(t, []uint{first.ID, second.ID, third.ID}, ids, "the live, active admins in id order")

		statements := capture.all()
		require.Len(t, statements, 2, "one discovery, one lock: %v", statements)
		discovery, lock := strings.ToUpper(statements[0]), strings.ToUpper(statements[1])
		assert.Contains(t, discovery, "ROLE = ")
		assert.Contains(t, discovery, "ORDER BY ID")
		assert.NotContains(t, discovery, "FOR UPDATE", "the discovery read takes no lock")
		assert.Contains(t, lock, "ID IN (")
		assert.Contains(t, lock, "ORDER BY")
		assert.NotContains(t, lock, "ROLE", "the locking statement never filters on role")
		assert.NotContains(t, lock, "IS_ACTIVE")
		if useMySQL {
			assert.Contains(t, lock, "FOR UPDATE", "MySQL locks the rows")
		}

		// A row that stops matching between the discovery and the lock is
		// dropped by the re-check.
		var armed bool
		var injectErr error
		var mu sync.Mutex
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:deactivate_between_discovery_and_lock", func(tx *gorm.DB) {
			mu.Lock()
			defer mu.Unlock()
			if !armed {
				return
			}
			armed = false
			injectErr = db.Exec("UPDATE users SET is_active = ? WHERE id = ?", false, first.ID).Error
		}))
		mu.Lock()
		armed = true
		mu.Unlock()
		admins, err = maintenance.LockActiveAdmins()
		require.NoError(t, err)
		require.NoError(t, injectErr)
		ids = []uint{}
		for _, admin := range admins {
			ids = append(ids, admin.ID)
			assert.True(t, admin.IsActive)
			assert.Equal(t, models.RoleAdmin, admin.Role)
		}
		assert.Equal(t, []uint{second.ID, third.ID}, ids, "the deactivated admin is not returned")

		// Inside a caller's transaction, through WithTx.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			locked, err := users.WithTx(tx).(UserRoleMaintenance).LockActiveAdmins()
			if err != nil {
				return err
			}
			assert.Len(t, locked, 2)
			return nil
		}))
	})

	t.Run("ClearRoleIDOfErased clears only erased users, then the role can be deleted", func(t *testing.T) {
		shared := newRole(t, "Shared Role")
		live := createCustomRoleUser(t, db, "shared-live@example.com", shared.ID, false)
		erasedOne := createCustomRoleUser(t, db, "shared-erased-1@example.com", shared.ID, true)
		erasedTwo := createCustomRoleUser(t, db, "shared-erased-2@example.com", shared.ID, true)
		other := newRole(t, "Other Role")
		otherErased := createCustomRoleUser(t, db, "other-erased@example.com", other.ID, true)

		assert.ErrorIs(t, roles.Delete(shared.ID), apperrors.ErrRoleInUse)

		cleared, err := maintenance.ClearRoleIDOfErased(shared.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), cleared)
		assert.Nil(t, storedRoleID(t, db, erasedOne))
		assert.Nil(t, storedRoleID(t, db, erasedTwo))
		require.NotNil(t, storedRoleID(t, db, live), "a live user keeps its role")
		require.NotNil(t, storedRoleID(t, db, otherErased), "another role's erased users are untouched")
		assert.Equal(t, other.ID, *storedRoleID(t, db, otherErased))

		again, err := maintenance.ClearRoleIDOfErased(shared.ID)
		require.NoError(t, err)
		assert.Zero(t, again)

		assert.ErrorIs(t, roles.Delete(shared.ID), apperrors.ErrRoleInUse, "the live user still holds it")
		require.NoError(t, users.UpdateFields(live, map[string]interface{}{"role": models.RoleSupport, "role_id": nil}))
		require.NoError(t, roles.Delete(shared.ID))

		// A role referenced only by erased users.
		assert.ErrorIs(t, roles.Delete(other.ID), apperrors.ErrRoleInUse)
		cleared, err = maintenance.ClearRoleIDOfErased(other.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), cleared)
		require.NoError(t, roles.Delete(other.ID), "a role referenced only by erased users can be deleted once cleared")
		_, err = roles.GetByID(other.ID)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("GetByID through a locking handle locks only the users row", func(t *testing.T) {
		userID := createCustomRoleUser(t, db, "locking-custom@example.com", fieldOps.ID, false)

		// The SQLite dialector drops the locking clause from the SQL text, so
		// the statement's clause map is what shows a locking read on both
		// engines. Only statements of this subtest's transaction are kept.
		type queried struct {
			table   string
			locking bool
		}
		var (
			mu        sync.Mutex
			recording bool
			seen      []queried
		)
		const recorder = "test:user_role_columns_locking_clause"
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(recorder, func(tx *gorm.DB) {
			mu.Lock()
			defer mu.Unlock()
			if recording {
				_, locking := tx.Statement.Clauses["FOR"]
				seen = append(seen, queried{table: tx.Statement.Table, locking: locking})
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Query().Remove(recorder) })

		var got *models.User
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			mu.Lock()
			recording = true
			mu.Unlock()
			capture.reset()
			var err error
			got, err = users.WithTx(LockingRead(tx)).GetByID(userID)
			mu.Lock()
			recording = false
			mu.Unlock()
			return err
		}))
		rendered := capture.all()

		require.NotNil(t, got.CustomRole, "the custom role is filled")
		assert.Equal(t, fieldOps.ID, got.CustomRole.ID)
		assert.Equal(t, []string{"deals.read=all", "leads.read=own"}, permissionKeys(got.CustomRole.Permissions))

		mu.Lock()
		statements := append([]queried(nil), seen...)
		mu.Unlock()
		byTable := map[string]queried{}
		for _, q := range statements {
			byTable[q.table] = q
		}
		require.Len(t, statements, 3, "users, roles and role_permissions: %v", statements)
		for _, table := range []string{"users", "roles", "role_permissions"} {
			_, ok := byTable[table]
			require.True(t, ok, "a %s statement: %v", table, statements)
		}
		assert.True(t, byTable["users"].locking, "the users read is a locking read")
		assert.False(t, byTable["roles"].locking, "the roles read takes no lock")
		assert.False(t, byTable["role_permissions"].locking, "the permissions read takes no lock")

		if useMySQL {
			require.Len(t, rendered, 3, "rendered statements: %v", rendered)
			for _, sql := range rendered {
				lower := strings.ToLower(sql)
				switch {
				case strings.Contains(lower, "from `role_permissions`"):
					assert.NotContains(t, lower, "for update", sql)
				case strings.Contains(lower, "from `roles`"):
					assert.NotContains(t, lower, "for update", sql)
				case strings.Contains(lower, "from `users`"):
					assert.Contains(t, lower, "for update", sql)
				default:
					t.Errorf("unexpected statement %q", sql)
				}
			}
		}
	})
}
