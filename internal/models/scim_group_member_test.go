package models

import (
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #506: scim_group_members on SQLite, created by MigrateDatabase: the unique
// (group_value, user_id) pair, the user_id index and the foreign key to
// users.
func TestSCIMGroupMemberSchemaSQLite(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "scim-groups.db"),
	}))
	require.NoError(t, MigrateDatabase())
	require.True(t, DB.Migrator().HasTable("scim_group_members"))

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('scim_group_members')").Scan(&indexes).Error)
	byName := map[string]int{}
	for _, index := range indexes {
		byName[index.Name] = index.Unique
	}
	assert.Equal(t, map[string]int{"idx_scim_group_members_group_user": 1, "idx_scim_group_members_user_id": 0}, byName)

	user := User{Email: "group-member@example.com", Password: "x", FirstName: "G", LastName: "M", Role: RoleSales, IsActive: true}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, DB.Create(&SCIMGroupMember{GroupValue: "Sales", UserID: user.ID}).Error)
	require.NoError(t, DB.Create(&SCIMGroupMember{GroupValue: "sales", UserID: user.ID}).Error, "a value differing only in case is another group")
	require.Error(t, DB.Create(&SCIMGroupMember{GroupValue: "Sales", UserID: user.ID}).Error, "the pair is unique")
	require.Error(t, DB.Create(&SCIMGroupMember{GroupValue: "Sales", UserID: user.ID + 1000}).Error, "user_id must name a user")
	var count int64
	require.NoError(t, DB.Model(&SCIMGroupMember{}).Where("user_id = ?", user.ID).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}
