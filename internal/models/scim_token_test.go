package models

import (
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #468 on SQLite: MigrateDatabase is the only schema path, so it creates
// scim_tokens with its six columns and the unique token_hash index, and a
// second run changes nothing.
func TestSCIMTokenSchemaSQLite(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "scim.db"),
	}))
	require.NoError(t, MigrateDatabase())

	snapshot := func() (map[string]sqliteColumn, map[string]int) {
		return roleSchemaColumns(t, "scim_tokens"), roleSchemaIndexes(t, "scim_tokens")
	}
	columns, indexes := snapshot()
	assert.Equal(t, []string{"created_at", "id", "last_used_at", "name", "revoked_at", "token_hash"}, roleSchemaColumnNames(columns))
	assert.Equal(t, 1, columns["id"].PK)
	for name, notNull := range map[string]int{"name": 1, "token_hash": 1, "created_at": 1, "last_used_at": 0, "revoked_at": 0} {
		assert.Equal(t, notNull, columns[name].NotNull, "%s NOT NULL", name)
		assert.Nil(t, columns[name].Dflt, "%s has no default", name)
	}
	assert.Equal(t, map[string]int{"idx_scim_tokens_token_hash": 1}, indexes, "one unique index on token_hash")

	var indexed []string
	require.NoError(t, DB.Raw("SELECT name FROM pragma_index_info('idx_scim_tokens_token_hash')").Scan(&indexed).Error)
	assert.Equal(t, []string{"token_hash"}, indexed)

	var foreignKeys int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM pragma_foreign_key_list('scim_tokens')").Scan(&foreignKeys).Error)
	assert.Zero(t, foreignKeys, "no foreign key")

	// The unique index holds.
	require.NoError(t, DB.Exec("INSERT INTO scim_tokens (name, token_hash, created_at) VALUES ('a', 'hmac$x', CURRENT_TIMESTAMP)").Error)
	assert.Error(t, DB.Exec("INSERT INTO scim_tokens (name, token_hash, created_at) VALUES ('b', 'hmac$x', CURRENT_TIMESTAMP)").Error)

	require.NoError(t, MigrateDatabase(), "a second run")
	againColumns, againIndexes := snapshot()
	assert.Equal(t, columns, againColumns, "a second MigrateDatabase changes no column")
	assert.Equal(t, indexes, againIndexes, "a second MigrateDatabase changes no index")
	var count int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM scim_tokens").Scan(&count).Error)
	assert.EqualValues(t, 1, count, "the row is kept")
}
