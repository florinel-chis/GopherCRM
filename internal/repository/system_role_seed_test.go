package repository

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #344 (#259 AC-3): every start on SQLite and MySQL leaves exactly the four
// system role rows, keeps custom roles untouched and writes nothing when they
// already exist.

func TestSystemRoleSeedSQLite(t *testing.T) {
	runSystemRoleSeed(t, migrateRoleDB(t, testsupport.Open(t, testsupport.SQLite, "system_role_seed", roleDB)), false)
}

func TestSystemRoleSeedMySQL(t *testing.T) {
	runSystemRoleSeed(t, migrateRoleDB(t, testsupport.Open(t, testsupport.MySQL, "system_role_seed", roleDB)), true)
}

// systemRoleRow is a system row with the columns a rerun must not change.
type systemRoleRow struct {
	ID          uint
	SystemKey   string
	Name        string
	NameKey     string
	Description string
	UpdatedAt   string
}

func readSystemRoleRows(t *testing.T, db *gorm.DB, useMySQL bool) map[string]systemRoleRow {
	t.Helper()
	updatedAt := "CAST(updated_at AS TEXT)"
	if useMySQL {
		updatedAt = "DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f')"
	}
	var rows []systemRoleRow
	require.NoError(t, db.Raw("SELECT id, system_key, name, name_key, description, "+updatedAt+" AS updated_at FROM roles WHERE system_key IS NOT NULL ORDER BY id").Scan(&rows).Error)
	out := map[string]systemRoleRow{}
	for _, row := range rows {
		_, duplicate := out[row.SystemKey]
		require.False(t, duplicate, "one row per system key")
		out[row.SystemKey] = row
	}
	return out
}

func assertSeededSystemRoles(t *testing.T, rows map[string]systemRoleRow) {
	t.Helper()
	require.Len(t, rows, len(models.SystemRoleSeeds), "exactly the four system rows")
	for _, seed := range models.SystemRoleSeeds {
		row, ok := rows[seed.SystemKey]
		require.True(t, ok, "system row %s", seed.SystemKey)
		assert.Equal(t, seed.Name, row.Name, seed.SystemKey)
		assert.Equal(t, models.RoleNameKey(seed.Name), row.NameKey, seed.SystemKey)
		assert.Equal(t, seed.Description, row.Description, seed.SystemKey)
	}
}

// roleWrites returns the captured INSERT and UPDATE statements on roles.
var roleWritePattern = regexp.MustCompile("(?i)^\\s*(insert\\s+(ignore\\s+)?into|update)\\s+`?roles`?[\\s(]")

func roleWrites(statements []string) []string {
	var out []string
	for _, statement := range statements {
		if roleWritePattern.MatchString(statement) {
			out = append(out, statement)
		}
	}
	return out
}

func systemKeyReads(statements []string) int {
	count := 0
	for _, statement := range statements {
		if strings.Contains(strings.ToLower(statement), "select system_key from roles where system_key is not null") {
			count++
		}
	}
	return count
}

func roleRowByID(rows []roleRow, id uint) *roleRow {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func runSystemRoleSeed(t *testing.T, db *gorm.DB, useMySQL bool) {
	capture := installRoleSQLCapture(t, db, "system_role_seed")
	roles := NewRoleRepository(db)

	// The first MigrateDatabase (in migrateRoleDB) seeded the four rows.
	seeded := readSystemRoleRows(t, db, useMySQL)
	assertSeededSystemRoles(t, seeded)

	// A custom role with permissions, which no later run may touch.
	custom := &models.Role{Name: "Field Ops", Description: "custom"}
	require.NoError(t, roles.Create(custom))
	require.NoError(t, roles.ReplacePermissions(custom.ID, []models.RolePermission{{PermissionKey: "leads.read", Scope: models.PermissionScopeOwn}}))
	customBefore := readRoleRows(t, db)
	permissionsBefore := readPermissionRows(t, db, custom.ID)

	t.Run("a second and a third run write nothing", func(t *testing.T) {
		for run := 2; run <= 3; run++ {
			capture.reset()
			require.NoError(t, models.MigrateDatabase(), "run %d", run)
			statements := capture.all()
			assert.Empty(t, roleWrites(statements), "run %d issues no INSERT or UPDATE on roles", run)
			assert.Equal(t, 1, systemKeyReads(statements), "run %d reads the system keys once", run)
			assert.Equal(t, seeded, readSystemRoleRows(t, db, useMySQL), "run %d keeps id, description and updated_at", run)
			assert.Equal(t, customBefore, readRoleRows(t, db), "run %d leaves the custom role alone", run)
			assert.Equal(t, permissionsBefore, readPermissionRows(t, db, custom.ID), "run %d leaves its permissions alone", run)
		}
	})

	t.Run("a deleted system row is re-inserted with its seed name and description", func(t *testing.T) {
		require.NoError(t, db.Exec("DELETE FROM roles WHERE system_key = 'support'").Error)
		// An edited system row is not rewritten.
		require.NoError(t, db.Exec("UPDATE roles SET description = 'edited by hand' WHERE system_key = 'admin'").Error)
		capture.reset()
		require.NoError(t, models.MigrateDatabase())
		writes := roleWrites(capture.all())
		require.Len(t, writes, 1, "one insert, for the missing row only")
		assert.Regexp(t, "(?i)^\\s*insert", writes[0])

		rows := readSystemRoleRows(t, db, useMySQL)
		require.Len(t, rows, 4)
		assert.Equal(t, "Support", rows["support"].Name)
		assert.Equal(t, "support", rows["support"].NameKey)
		assert.Equal(t, "Built-in support access. Cannot be edited or deleted.", rows["support"].Description)
		assert.NotEqual(t, seeded["support"].ID, rows["support"].ID, "a new row")
		assert.Equal(t, "edited by hand", rows["admin"].Description, "an existing system row is never updated")
		for _, key := range []string{"sales", "customer"} {
			assert.Equal(t, seeded[key], rows[key], key)
		}
		require.NoError(t, db.Exec("UPDATE roles SET description = ? WHERE system_key = 'admin'", models.SystemRoleSeeds[0].Description).Error)
		assert.Equal(t, roleRowByID(customBefore, custom.ID), roleRowByID(readRoleRows(t, db), custom.ID), "the custom role is untouched")
		assert.Equal(t, permissionsBefore, readPermissionRows(t, db, custom.ID))
	})

	t.Run("a custom role holding a missing system name_key stops the start", func(t *testing.T) {
		require.NoError(t, db.Exec("DELETE FROM roles WHERE system_key = 'sales'").Error)
		require.NoError(t, db.Exec("UPDATE roles SET name = 'Sales', name_key = 'sales' WHERE id = ?", custom.ID).Error)
		before := readRoleRows(t, db)

		capture.reset()
		err := models.MigrateDatabase()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"sales"`, "the error names the system key")
		assert.NotContains(t, err.Error(), "Sales", "and not the custom role's name")
		assert.NotContains(t, err.Error(), "Field Ops")
		assert.Empty(t, roleWrites(capture.all()), "nothing is written")
		assert.Equal(t, before, readRoleRows(t, db), "roles is unchanged")
		assert.Equal(t, permissionsBefore, readPermissionRows(t, db, custom.ID))

		// Renaming the custom role lets the next start seed the row.
		require.NoError(t, db.Exec("UPDATE roles SET name = 'Field Ops', name_key = 'field ops' WHERE id = ?", custom.ID).Error)
		require.NoError(t, models.MigrateDatabase())
		assertSeededSystemRoles(t, readSystemRoleRows(t, db, useMySQL))
		assert.Equal(t, permissionsBefore, readPermissionRows(t, db, custom.ID))
	})

	if !useMySQL {
		return
	}

	t.Run("two replicas booting at once end with the four rows", func(t *testing.T) {
		require.NoError(t, db.Exec("DELETE FROM roles WHERE system_key IS NOT NULL").Error)
		schema := db.Migrator().CurrentDatabase()
		pools := make([]*gorm.DB, 2)
		for i := range pools {
			pools[i] = testsupport.OpenMySQLSchema(t, schema)
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
		assertSeededSystemRoles(t, readSystemRoleRows(t, db, useMySQL))
		assert.Equal(t, permissionsBefore, readPermissionRows(t, db, custom.ID))
	})
}
