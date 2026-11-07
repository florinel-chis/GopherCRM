package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #236: users.token_version is the per-account session generation. It is
// server-maintained and never part of the user's JSON.
func TestUserTokenVersionJSONHidden(t *testing.T) {
	user := User{BaseModel: BaseModel{ID: 9}, Email: "versioned@example.com", Role: RoleSales, IsActive: true, TokenVersion: 4242}

	for name, value := range map[string]interface{}{
		"value":   user,
		"pointer": &user,
		"slice":   []User{user},
	} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err, name)
		assert.NotContains(t, string(encoded), "token_version", "%s: the key never appears", name)
		assert.NotContains(t, string(encoded), "TokenVersion", "%s: nor the Go field name", name)
		assert.NotContains(t, string(encoded), "4242", "%s: nor the value", name)
	}

	// A client cannot set it through a decoded body either.
	var decoded User
	require.NoError(t, json.Unmarshal([]byte(`{"email":"x@example.com","token_version":7,"TokenVersion":7}`), &decoded))
	assert.Zero(t, decoded.TokenVersion)

	// The schema contract: uint, NOT NULL, default 0, json "-".
	field, ok := reflect.TypeOf(User{}).FieldByName("TokenVersion")
	require.True(t, ok)
	assert.Equal(t, reflect.Uint, field.Type.Kind())
	assert.Contains(t, field.Tag.Get("gorm"), "not null")
	assert.Contains(t, field.Tag.Get("gorm"), "default:0")
	assert.Equal(t, "-", field.Tag.Get("json"))
}

// TestUserTokenVersionSQLiteUpgrade is the SQLite half of #236: SQLite installs
// never run the SQL migration, so MigrateDatabase on a file written before
// this change adds users.token_version NOT NULL DEFAULT 0, every existing
// account reads 0, and a second run changes nothing.
func TestUserTokenVersionSQLiteUpgrade(t *testing.T) {
	openV120Fixture(t)
	require.False(t, DB.Migrator().HasColumn("users", "token_version"), "the fixture predates token_version")

	var before int64
	require.NoError(t, DB.Table("users").Count(&before).Error)
	require.Positive(t, before, "the fixture holds existing accounts")

	require.NoError(t, MigrateDatabase())
	assertUpgradedV120(t)

	require.True(t, DB.Migrator().HasColumn("users", "token_version"))
	var notNull int
	require.NoError(t, DB.Raw("SELECT \"notnull\" FROM pragma_table_info('users') WHERE name = 'token_version'").Scan(&notNull).Error)
	assert.Equal(t, 1, notNull, "users.token_version is NOT NULL")
	var dflt string
	require.NoError(t, DB.Raw("SELECT dflt_value FROM pragma_table_info('users') WHERE name = 'token_version'").Scan(&dflt).Error)
	assert.Equal(t, "0", strings.Trim(dflt, "'\""), "users.token_version defaults to 0")

	var zero int64
	require.NoError(t, DB.Table("users").Where("token_version = 0").Count(&zero).Error)
	assert.Equal(t, before, zero, "every existing account reads 0")

	var users []User
	require.NoError(t, DB.Unscoped().Order("id").Find(&users).Error)
	require.Len(t, users, int(before))
	for _, user := range users {
		assert.Zero(t, user.TokenVersion, "user %d", user.ID)
	}

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")

	// The column takes writes and reads them back.
	require.NoError(t, DB.Exec("UPDATE users SET token_version = 3 WHERE id = ?", users[0].ID).Error)
	var reread User
	require.NoError(t, DB.Unscoped().First(&reread, users[0].ID).Error)
	assert.Equal(t, uint(3), reread.TokenVersion)
}
