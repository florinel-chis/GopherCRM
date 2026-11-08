package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #236: 20261109120000_add_user_token_version adds users.token_version as
// bigint unsigned NOT NULL DEFAULT 0, the column AutoMigrate gives the uint
// field on MySQL, only when it is missing; rollback drops it only when it is
// present. Existing accounts are kept and read 0.

const (
	userTokenVersionVersion         = 20261109120000
	userTokenVersionPreviousVersion = 20261108120000
	userTokenVersionName            = "20261109120000_add_user_token_version"
)

var userTokenVersionColumns = []string{"token_version"}

// TestUserTokenVersionMigrationFiles needs no database: the pair exists, adds
// and drops exactly the guarded column, and adds no index, key or backfill.
func TestUserTokenVersionMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, userTokenVersionName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, userTokenVersionName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "'alter table users add column token_version bigint unsigned not null default 0'")
	assert.Contains(t, upSQL, "table_name = 'users' and column_name = 'token_version'", "the add is guarded")
	assert.Contains(t, downSQL, "'alter table users drop column token_version'")
	assert.Contains(t, downSQL, "table_name = 'users' and column_name = 'token_version'", "the drop is guarded")
	assert.NotContains(t, upSQL, "create index", "no new index")
	assert.NotContains(t, upSQL, "foreign key", "no new foreign key")
	assert.NotContains(t, upSQL, "create table", "no new table")
	assert.NotContains(t, upSQL, "\nupdate ", "existing rows are not backfilled")
	assert.NotContains(t, downSQL, "drop table", "rollback keeps the rows")
	// The newest-migration assertion moved to TestBulkOperationStatusIndexMigrationFiles
	// when 20261110120000_add_bulk_operation_status_index (#196) followed this pair.
}

func snapshotUserTokenVersionColumns(t *testing.T, tdb *migrationTestDB) []string {
	t.Helper()
	return ownedColumns(snapshotTable(t, tdb, "users"), userTokenVersionColumns)
}

// seedTokenVersionUsers writes two existing accounts the upgrade must keep.
func seedTokenVersionUsers(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at) " +
			"VALUES ('kept-one@example.com', 'hash-one', 'Kept', 'One', 'sales', 1, '2026-11-01 10:00:00.123', '2026-11-02 11:00:00.456')",
		"INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at) " +
			"VALUES ('kept-two@example.com', 'hash-two', 'Kept', 'Two', 'customer', 0, '2026-11-01 10:00:00.123', '2026-11-02 11:00:00.456')",
	} {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

func assertTokenVersionUsersKept(t *testing.T, tdb *migrationTestDB, column bool) {
	t.Helper()
	var count int
	require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM users").Scan(&count))
	assert.Equal(t, 2, count, "the existing accounts are kept")
	var email, password, updatedAt string
	require.NoError(t, tdb.sql.QueryRow(`SELECT email, password, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f')
		FROM users WHERE email = 'kept-one@example.com'`).Scan(&email, &password, &updatedAt))
	assert.Equal(t, "hash-one", password)
	assert.Equal(t, "2026-11-02 11:00:00.456000", updatedAt, "updated_at is untouched")
	if column {
		var zero int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM users WHERE token_version = 0").Scan(&zero))
		assert.Equal(t, 2, zero, "existing accounts read 0")
	}
}

func TestUserTokenVersionMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the column as AutoMigrate adds it on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "token_version_ref")
	require.NoError(t, reference.m.Migrate(userTokenVersionPreviousVersion))
	assert.Empty(t, snapshotUserTokenVersionColumns(t, reference), "the previous head has no token_version")
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(&models.User{}))
	want := snapshotUserTokenVersionColumns(t, reference)
	require.Len(t, want, 1)
	assert.Contains(t, want[0], "bigint")
	assert.Contains(t, want[0], "unsigned")
	assert.Contains(t, want[0], "nullable=NO")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "token_version_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotUserTokenVersionColumns(t, tdb), "a fresh SQL schema carries what AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "token_version_mig")
			require.NoError(t, tdb.m.Migrate(userTokenVersionPreviousVersion))
			seedTokenVersionUsers(t, tdb)
			usersBeforeUp := snapshotTable(t, tdb, "users")
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.User{}))
				assert.Equal(t, want, snapshotUserTokenVersionColumns(t, tdb))
			}

			require.NoError(t, tdb.m.Migrate(userTokenVersionVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userTokenVersionVersion), version)
			assert.Equal(t, want, snapshotUserTokenVersionColumns(t, tdb), "the SQL migration and AutoMigrate must produce the same column")
			if !autoMigrateFirst {
				assert.Equal(t, usersBeforeUp, withoutColumns(snapshotTable(t, tdb, "users"), userTokenVersionColumns),
					"the migration adds the column and touches nothing else")
			}
			assertTokenVersionUsersKept(t, tdb, true)

			// The application's startup on top leaves the column alone and the
			// migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotUserTokenVersionColumns(t, tdb), "MigrateDatabase must not alter the column")
			require.ErrorIs(t, tdb.m.Migrate(userTokenVersionVersion), migrate.ErrNoChange)

			// Rollback removes exactly the column and keeps the rows.
			usersBefore := snapshotTable(t, tdb, "users")
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userTokenVersionPreviousVersion), version)
			assert.Equal(t, withoutColumns(usersBefore, userTokenVersionColumns), snapshotTable(t, tdb, "users"),
				"rollback removes the column and nothing else")
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			assert.Empty(t, snapshotUserTokenVersionColumns(t, tdb))
			assertTokenVersionUsersKept(t, tdb, false)

			// The down half is guarded: rolling back over a schema without the
			// column succeeds and drops nothing.
			require.NoError(t, tdb.m.Migrate(userTokenVersionVersion))
			_, err = tdb.sql.Exec("ALTER TABLE users DROP COLUMN token_version")
			require.NoError(t, err)
			require.NoError(t, tdb.m.Steps(-1), "the drop runs only when the column is present")
			assert.Empty(t, snapshotUserTokenVersionColumns(t, tdb))

			// Reapply.
			require.NoError(t, tdb.m.Migrate(userTokenVersionVersion))
			assert.Equal(t, want, snapshotUserTokenVersionColumns(t, tdb))
			assertTokenVersionUsersKept(t, tdb, true)
		})
	}
}
