package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #428: ListByFilter and CountByFilter share whereUserListFilter. These roots
// seed users of several roles, users sharing a search token and a
// soft-deleted sales user, then check role only, role plus search, role plus
// sort, paging inside the filtered set and the count. They run on a
// file-backed SQLite database with one connection and, when TEST_MYSQL_DSN is
// set, on a disposable MySQL schema; the MySQL root skips without the
// variable and fails when it is unreachable.

func TestUserListRoleFilterRepositorySQLite(t *testing.T) {
	runUserListRoleFilterRepository(t, openUserListFilterDB(t, false))
}

func TestUserListRoleFilterRepositoryMySQL(t *testing.T) {
	runUserListRoleFilterRepository(t, openUserListFilterDB(t, true))
}

func openUserListFilterDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "user_list_filter.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		dsnCfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_user_list_filter_%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.Role{}, &models.User{}))
	return db
}

func userIDs(users []models.User) []uint {
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return ids
}

func runUserListRoleFilterRepository(t *testing.T, db *gorm.DB) {
	seed := func(email, first string, role models.UserRole) uint {
		t.Helper()
		u := models.User{Email: email, Password: "x", FirstName: first, LastName: "Seed", Role: role, IsActive: true}
		require.NoError(t, db.Create(&u).Error)
		return u.ID
	}
	// Insertion order fixes ascending ids; emails are chosen so email order
	// differs from id order inside the sales role.
	admin := seed("a-admin@example.com", "Ada", models.RoleAdmin)
	salesB := seed("b-sales@example.com", "Kiwi", models.RoleSales)
	support1 := seed("c-support@example.com", "Kiwi", models.RoleSupport)
	salesD := seed("d-sales@example.com", "Bob", models.RoleSales)
	customer := seed("e-customer@example.com", "Kiwi", models.RoleCustomer)
	salesA := seed("a-sales@example.com", "Kiwi", models.RoleSales)
	support2 := seed("f-support@example.com", "Cy", models.RoleSupport)
	deleted := seed("z-sales-deleted@example.com", "Kiwi", models.RoleSales)
	require.NoError(t, db.Delete(&models.User{}, deleted).Error)
	_ = admin
	_ = customer

	repo := NewUserRepository(db)

	cases := []struct {
		name      string
		filter    UserListFilter
		sortBy    string
		sortOrder string
		want      []uint
	}{
		{"role only, id ascending", UserListFilter{Role: "sales"}, "", "asc", []uint{salesB, salesD, salesA}},
		{"role plus search", UserListFilter{Role: "sales", Search: "Kiwi"}, "", "asc", []uint{salesB, salesA}},
		{"role plus email desc", UserListFilter{Role: "sales"}, "email", "desc", []uint{salesD, salesB, salesA}},
		{"support only", UserListFilter{Role: "support"}, "", "asc", []uint{support1, support2}},
		{"custom matches nothing", UserListFilter{Role: "custom"}, "", "asc", []uint{}},
		{"search without role", UserListFilter{Search: "Kiwi"}, "", "asc", []uint{salesB, support1, customer, salesA}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users, err := repo.ListByFilter(tc.filter, 0, 100, tc.sortBy, tc.sortOrder)
			require.NoError(t, err)
			assert.Equal(t, tc.want, userIDs(users))
			assert.NotContains(t, userIDs(users), deleted, "a soft-deleted user is never listed")
			count, err := repo.CountByFilter(tc.filter)
			require.NoError(t, err)
			assert.Equal(t, int64(len(tc.want)), count)
		})
	}

	t.Run("limit and offset page within the filtered set", func(t *testing.T) {
		filter := UserListFilter{Role: "sales"}
		page1, err := repo.ListByFilter(filter, 0, 2, "", "asc")
		require.NoError(t, err)
		assert.Equal(t, []uint{salesB, salesD}, userIDs(page1))
		page2, err := repo.ListByFilter(filter, 2, 2, "", "asc")
		require.NoError(t, err)
		assert.Equal(t, []uint{salesA}, userIDs(page2))
		page3, err := repo.ListByFilter(filter, 1, 1, "email", "desc")
		require.NoError(t, err)
		assert.Equal(t, []uint{salesB}, userIDs(page3))
		count, err := repo.CountByFilter(filter)
		require.NoError(t, err)
		assert.Equal(t, int64(3), count, "the count is the full filtered count, not the page size")
	})
}
