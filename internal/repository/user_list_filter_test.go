package repository

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #428: ListByFilter and CountByFilter share whereUserListFilter. These roots
// seed users of several roles, users sharing a search token and a
// soft-deleted sales user, then check role only, role plus search, role plus
// sort, paging inside the filtered set and the count. They run on a
// file-backed SQLite database with one connection and, when TEST_MYSQL_DSN is
// set, on a disposable MySQL schema; the MySQL root skips without the
// variable and fails when it is unreachable.

func TestUserListRoleFilterRepositorySQLite(t *testing.T) {
	runUserListRoleFilterRepository(t, testsupport.Open(t, testsupport.SQLite, "user_list_filter", userListFilterDB))
}

func TestUserListRoleFilterRepositoryMySQL(t *testing.T) {
	runUserListRoleFilterRepository(t, testsupport.Open(t, testsupport.MySQL, "user_list_filter", userListFilterDB))
}

// userListFilterDB is a file-backed SQLite database with one connection, or a uniquely
// named disposable schema on the TEST_MYSQL_DSN server.
var userListFilterDB = testsupport.Group(
	testsupport.OnSQLite(testsupport.File("user_list_filter.db", testsupport.BusyTimeout)),
	testsupport.Models(&models.Role{}, &models.User{}),
)

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
