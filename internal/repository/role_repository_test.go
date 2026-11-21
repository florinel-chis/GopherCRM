package repository

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #344 (#259 AC-9): role persistence behaves the same on SQLite and MySQL.
// The SQLite roots use the production connector (foreign_keys(1)) over a file
// under t.TempDir(); the MySQL roots a disposable schema on TEST_MYSQL_DSN,
// skipped without the variable and failing when it is unreachable. Both are
// built by models.MigrateDatabase, so the schema and the seeded system roles
// are the application's own.

// roleDB is what database.Open gives the application on SQLite: the file
// <name>.db with busy_timeout, WAL and foreign keys, one open and one idle
// connection and GORM's clock in UTC. On MySQL it is a disposable schema. No
// models: models.MigrateDatabase (migrateRoleDB) builds the schema.
var roleDB = testsupport.OnSQLite(
	testsupport.File("", testsupport.BusyTimeout, testsupport.WAL),
	testsupport.MaxOpenConns(1), testsupport.MaxIdleConns(1),
	testsupport.NowUTC(),
)

// migrateRoleDB runs models.MigrateDatabase on db, with the global handle
// pointed at it for the test.
func migrateRoleDB(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	previous := models.DB
	t.Cleanup(func() { models.DB = previous })
	models.DB = db
	require.NoError(t, models.MigrateDatabase())
	return db
}

// roleSQLCapture records the SQL of every statement the handle runs.
type roleSQLCapture struct {
	mu         sync.Mutex
	statements []string
}

func installRoleSQLCapture(t *testing.T, db *gorm.DB, name string) *roleSQLCapture {
	t.Helper()
	c := &roleSQLCapture{}
	record := func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.statements = append(c.statements, tx.Statement.SQL.String())
	}
	callbacks := db.Callback()
	require.NoError(t, callbacks.Query().After("gorm:query").Register("test:"+name+"_query", record))
	require.NoError(t, callbacks.Row().After("gorm:row").Register("test:"+name+"_row", record))
	require.NoError(t, callbacks.Raw().After("gorm:raw").Register("test:"+name+"_raw", record))
	require.NoError(t, callbacks.Create().After("gorm:create").Register("test:"+name+"_create", record))
	require.NoError(t, callbacks.Update().After("gorm:update").Register("test:"+name+"_update", record))
	require.NoError(t, callbacks.Delete().After("gorm:delete").Register("test:"+name+"_delete", record))
	return c
}

func (c *roleSQLCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = nil
}

func (c *roleSQLCapture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.statements...)
}

func TestRoleRepositorySQLite(t *testing.T) {
	runRoleRepository(t, migrateRoleDB(t, testsupport.Open(t, testsupport.SQLite, "role_repository", roleDB)), false)
}

func TestRoleRepositoryMySQL(t *testing.T) {
	runRoleRepository(t, migrateRoleDB(t, testsupport.Open(t, testsupport.MySQL, "role_repository", roleDB)), true)
}

// roleRow is a role as stored, read without the repository.
type roleRow struct {
	ID          uint
	SystemKey   *string
	Name        string
	NameKey     string
	Description string
}

func readRoleRows(t *testing.T, db *gorm.DB) []roleRow {
	t.Helper()
	var rows []roleRow
	require.NoError(t, db.Raw("SELECT id, system_key, name, name_key, description FROM roles ORDER BY id").Scan(&rows).Error)
	return rows
}

type permissionRow struct {
	RoleID        uint
	PermissionKey string
	Scope         string
}

func readPermissionRows(t *testing.T, db *gorm.DB, roleID uint) []permissionRow {
	t.Helper()
	var rows []permissionRow
	require.NoError(t, db.Raw("SELECT role_id, permission_key, scope FROM role_permissions WHERE role_id = ? ORDER BY permission_key", roleID).Scan(&rows).Error)
	return rows
}

func permissionKeys(permissions []models.RolePermission) []string {
	out := []string{}
	for _, permission := range permissions {
		out = append(out, permission.PermissionKey+"="+string(permission.Scope))
	}
	return out
}

func createCustomRoleUser(t *testing.T, db *gorm.DB, email string, roleID uint, erased bool) uint {
	t.Helper()
	id := roleID
	user := models.User{Email: email, Password: "x", FirstName: "Role", LastName: "User", Role: models.RoleCustom, IsActive: true, RoleID: &id}
	require.NoError(t, db.Create(&user).Error)
	if erased {
		require.NoError(t, db.Delete(&models.User{}, user.ID).Error)
	}
	return user.ID
}

func runRoleRepository(t *testing.T, db *gorm.DB, useMySQL bool) {
	repo := NewRoleRepository(db)
	capture := installRoleSQLCapture(t, db, "role_repository")

	newRole := func(t *testing.T, name string) *models.Role {
		t.Helper()
		role := &models.Role{Name: name, Description: "test role"}
		require.NoError(t, repo.Create(role))
		require.NotZero(t, role.ID)
		return role
	}

	t.Run("Create sets name_key and refuses a name taken regardless of case and whitespace", func(t *testing.T) {
		role := newRole(t, "Field Ops")
		assert.Equal(t, "field ops", role.NameKey)
		stored := readRoleRows(t, db)
		var found bool
		for _, row := range stored {
			if row.ID == role.ID {
				found = true
				assert.Nil(t, row.SystemKey, "a custom role has system_key NULL")
				assert.Equal(t, "Field Ops", row.Name)
				assert.Equal(t, "field ops", row.NameKey)
			}
		}
		require.True(t, found)

		for _, name := range []string{" field ops ", "FIELD OPS", "ADMIN", "Customer", " support"} {
			before := readRoleRows(t, db)
			err := repo.Create(&models.Role{Name: name})
			require.Error(t, err, "name %q", name)
			assert.ErrorIs(t, err, apperrors.ErrRoleNameTaken, "name %q", name)
			assert.Equal(t, before, readRoleRows(t, db), "nothing is written for %q", name)
		}

		systemKey := "sales"
		for name, role := range map[string]*models.Role{
			"blank":            {Name: "   "},
			"empty":            {Name: ""},
			"101 runes":        {Name: strings.Repeat("é", 101)},
			"system key":       {Name: "Brand New", SystemKey: &systemKey},
			"long description": {Name: "Long Description", Description: strings.Repeat("d", 501)},
		} {
			before := readRoleRows(t, db)
			capture.reset()
			err := repo.Create(role)
			require.Error(t, err, name)
			assert.ErrorIs(t, err, apperrors.ErrValidation, name)
			assert.Empty(t, capture.all(), "%s: no statement runs", name)
			assert.Equal(t, before, readRoleRows(t, db), "%s: nothing is written", name)
		}

		// 100 runes is the limit, not beyond it.
		hundred := newRole(t, strings.Repeat("ö", 100))
		assert.Equal(t, strings.Repeat("ö", 100), hundred.Name)
	})

	t.Run("Update writes only name, name_key, description and updated_at", func(t *testing.T) {
		role := newRole(t, "Update Me")
		other := newRole(t, "Other Team")

		sneaky := "admin"
		capture.reset()
		require.NoError(t, repo.Update(&models.Role{ID: role.ID, Name: "  Renamed Team ", Description: "new text", SystemKey: &sneaky}))
		var updates []string
		for _, statement := range capture.all() {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "UPDATE") {
				updates = append(updates, strings.ToLower(statement))
			}
		}
		require.Len(t, updates, 1)
		for _, column := range []string{"name", "name_key", "description", "updated_at"} {
			assert.Contains(t, updates[0], column)
		}
		assert.NotContains(t, updates[0], "system_key", "Update never writes system_key")
		assert.NotContains(t, updates[0], "created_at")

		got, err := repo.GetByID(role.ID)
		require.NoError(t, err)
		assert.Nil(t, got.SystemKey)
		assert.Equal(t, "Renamed Team", got.Name)
		assert.Equal(t, "renamed team", got.NameKey)
		assert.Equal(t, "new text", got.Description)

		for _, name := range []string{"OTHER TEAM", " other team ", "Admin", "sales"} {
			before := readRoleRows(t, db)
			err := repo.Update(&models.Role{ID: role.ID, Name: name})
			require.Error(t, err, name)
			assert.ErrorIs(t, err, apperrors.ErrRoleNameTaken, name)
			assert.Equal(t, before, readRoleRows(t, db), "nothing is changed for %q", name)
		}

		// Its own name in another case is not a conflict.
		require.NoError(t, repo.Update(&models.Role{ID: role.ID, Name: "RENAMED TEAM", Description: "new text"}))
		// Writing the same values again is not a missing row (MySQL changed rows).
		require.NoError(t, repo.Update(&models.Role{ID: role.ID, Name: "RENAMED TEAM", Description: "new text"}))

		before := readRoleRows(t, db)
		err = repo.Update(&models.Role{ID: other.ID, Name: " "})
		assert.ErrorIs(t, err, apperrors.ErrValidation)
		err = repo.Update(&models.Role{ID: other.ID, Name: strings.Repeat("x", 101)})
		assert.ErrorIs(t, err, apperrors.ErrValidation)
		assert.Equal(t, before, readRoleRows(t, db))

		err = repo.Update(&models.Role{ID: 99999999, Name: "Nobody"})
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("ReplacePermissions replaces the set atomically", func(t *testing.T) {
		role := newRole(t, "Permission Holder")

		require.NoError(t, repo.ReplacePermissions(role.ID, []models.RolePermission{
			{PermissionKey: "leads.read", Scope: models.PermissionScopeOwn},
			{PermissionKey: "deals.write", Scope: models.PermissionScopeAll},
		}))
		got, err := repo.GetByID(role.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"deals.write=all", "leads.read=own"}, permissionKeys(got.Permissions), "ordered by permission_key, with scopes")

		require.NoError(t, repo.ReplacePermissions(role.ID, []models.RolePermission{{PermissionKey: "tickets.read"}}))
		got, err = repo.GetByID(role.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"tickets.read=all"}, permissionKeys(got.Permissions), "exactly the new set; an empty scope is all")

		// A failure after the delete keeps the previous set.
		err = repo.ReplacePermissions(role.ID, []models.RolePermission{
			{PermissionKey: "a.read", Scope: models.PermissionScopeAll},
			{PermissionKey: "a.read", Scope: models.PermissionScopeOwn},
		})
		require.Error(t, err, "a repeated permission_key hits the primary key")
		assert.Equal(t, []permissionRow{{RoleID: role.ID, PermissionKey: "tickets.read", Scope: "all"}}, readPermissionRows(t, db, role.ID))

		// Invalid input writes nothing.
		for name, permissions := range map[string][]models.RolePermission{
			"blank key":     {{PermissionKey: " "}},
			"unknown scope": {{PermissionKey: "x.read", Scope: "everything"}},
		} {
			err := repo.ReplacePermissions(role.ID, permissions)
			assert.ErrorIs(t, err, apperrors.ErrValidation, name)
			assert.Len(t, readPermissionRows(t, db, role.ID), 1, name)
		}

		// A caller transaction that rolls back keeps the previous set.
		injected := errors.New("caller rolls back")
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTx(tx).ReplacePermissions(role.ID, []models.RolePermission{{PermissionKey: "rolled.back"}}); err != nil {
				return err
			}
			return injected
		})
		require.ErrorIs(t, err, injected)
		assert.Equal(t, []permissionRow{{RoleID: role.ID, PermissionKey: "tickets.read", Scope: "all"}}, readPermissionRows(t, db, role.ID))

		// Through WithTx, a committing caller keeps the new set.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return repo.WithTx(tx).ReplacePermissions(role.ID, []models.RolePermission{{PermissionKey: "committed.read", Scope: models.PermissionScopeOwn}})
		}))
		assert.Equal(t, []permissionRow{{RoleID: role.ID, PermissionKey: "committed.read", Scope: "own"}}, readPermissionRows(t, db, role.ID))

		// An empty slice leaves none.
		require.NoError(t, repo.ReplacePermissions(role.ID, nil))
		got, err = repo.GetByID(role.ID)
		require.NoError(t, err)
		assert.Empty(t, got.Permissions)

		// A missing role is not found and nothing is written.
		var before int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM role_permissions").Scan(&before).Error)
		err = repo.ReplacePermissions(99999999, []models.RolePermission{{PermissionKey: "ghost.read"}})
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		var after int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM role_permissions").Scan(&after).Error)
		assert.Equal(t, before, after)

		_, err = repo.GetByID(99999999)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("List walks a total order on every page in both directions", func(t *testing.T) {
		for i := 0; i < 7; i++ {
			newRole(t, fmt.Sprintf("Tied Role %d", i))
		}
		tied := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		require.NoError(t, db.Exec("UPDATE roles SET created_at = ?, updated_at = ?", tied, tied).Error)

		var total int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM roles").Scan(&total).Error)
		require.Greater(t, total, int64(10))
		var ascending []uint
		require.NoError(t, db.Raw("SELECT id FROM roles ORDER BY id ASC").Scan(&ascending).Error)
		descending := make([]uint, len(ascending))
		for i, id := range ascending {
			descending[len(ascending)-1-i] = id
		}
		var byNameAsc, byNameDesc []uint
		require.NoError(t, db.Raw("SELECT id FROM roles ORDER BY name ASC, id ASC").Scan(&byNameAsc).Error)
		require.NoError(t, db.Raw("SELECT id FROM roles ORDER BY name DESC, id DESC").Scan(&byNameDesc).Error)

		cases := []struct {
			by, order string
			want      []uint
		}{
			{"", "", ascending},
			{"id", "asc", ascending},
			{"id", "desc", descending},
			{"created_at", "asc", ascending},
			{"created_at", "desc", descending},
			{"updated_at", "asc", ascending},
			{"updated_at", "desc", descending},
			{"name", "asc", byNameAsc},
			{"name", "desc", byNameDesc},
		}
		for _, tc := range cases {
			for _, pageSize := range []int{1, 3, 4} {
				var walked []uint
				for offset := 0; offset < int(total)+pageSize; offset += pageSize {
					page, count, err := repo.List(offset, pageSize, tc.by, tc.order)
					require.NoError(t, err)
					assert.Equal(t, total, count, "%s %s: the total", tc.by, tc.order)
					for _, role := range page {
						walked = append(walked, role.ID)
					}
				}
				assert.Equal(t, tc.want, walked, "sort %q %q, page size %d: every role exactly once, in order", tc.by, tc.order, pageSize)
			}
		}

		for _, column := range []string{"description", "system_key", "name_key", "id; DROP TABLE roles"} {
			capture.reset()
			page, count, err := repo.List(0, 10, column, "asc")
			require.Error(t, err, column)
			assert.Nil(t, page)
			assert.Zero(t, count)
			assert.Empty(t, capture.all(), "%q: no query runs", column)
		}
	})

	t.Run("Delete removes the role and its permissions unless a user references it", func(t *testing.T) {
		free := newRole(t, "Free Role")
		require.NoError(t, repo.ReplacePermissions(free.ID, []models.RolePermission{{PermissionKey: "free.read"}, {PermissionKey: "free.write"}}))
		require.NoError(t, repo.Delete(free.ID))
		_, err := repo.GetByID(free.ID)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		assert.Empty(t, readPermissionRows(t, db, free.ID))

		for _, erased := range []bool{false, true} {
			name := fmt.Sprintf("Used Role erased=%t", erased)
			used := newRole(t, name)
			require.NoError(t, repo.ReplacePermissions(used.ID, []models.RolePermission{{PermissionKey: "used.read", Scope: models.PermissionScopeOwn}}))
			createCustomRoleUser(t, db, fmt.Sprintf("used-%t@example.com", erased), used.ID, erased)

			err := repo.Delete(used.ID)
			require.Error(t, err, name)
			assert.ErrorIs(t, err, apperrors.ErrRoleInUse, "%s: got %v", name, err)
			got, err := repo.GetByID(used.ID)
			require.NoError(t, err, "%s: the role remains", name)
			assert.Equal(t, []string{"used.read=own"}, permissionKeys(got.Permissions), "%s: its permissions remain", name)
		}

		before := readRoleRows(t, db)
		assert.ErrorIs(t, repo.Delete(99999999), gorm.ErrRecordNotFound)
		assert.Equal(t, before, readRoleRows(t, db))
	})

	t.Run("system rows are the seeded ones", func(t *testing.T) {
		var keys []string
		require.NoError(t, db.Raw("SELECT system_key FROM roles WHERE system_key IS NOT NULL ORDER BY id").Scan(&keys).Error)
		assert.Equal(t, []string{"admin", "sales", "support", "customer"}, keys)
	})

	if useMySQL {
		t.Run("the refusal is MySQL error 1451", func(t *testing.T) {
			used := newRole(t, "Error Code Role")
			createCustomRoleUser(t, db, "error-code@example.com", used.ID, false)
			err := db.Exec("DELETE FROM roles WHERE id = ?", used.ID).Error
			require.Error(t, err)
			var mysqlErr *mysqldriver.MySQLError
			require.True(t, errors.As(err, &mysqlErr), "a driver error: %v", err)
			assert.Equal(t, uint16(1451), mysqlErr.Number)
			assert.True(t, isForeignKeyRefusal(err))
		})
	}
}
