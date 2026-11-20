package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #506: 20261129120000_add_scim_group_members creates scim_group_members (one
// row per member of a SCIM Group: the exact groups-claim value, utf8mb4_bin,
// and a foreign key to users; the pair is unique) only when missing, with the
// columns and index names AutoMigrate plus the collation guard produce;
// rollback drops that table and nothing else.

const (
	scimGroupMembersVersion         = 20261129120000
	scimGroupMembersPreviousVersion = userIdentitySourceVersion
	scimGroupMembersMigrationName   = "20261129120000_add_scim_group_members"
	scimGroupMembersTable           = "scim_group_members"
)

// TestSCIMGroupMembersMigrationFiles needs no database: the pair exists with
// its headers, creates the table only when missing with the model's columns,
// collation and index names, drops only it, the model declares the same, and
// it is the newest migration of the directory, which latestMigrationVersion
// names.
func TestSCIMGroupMembersMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, scimGroupMembersMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, scimGroupMembersMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: SCIM group members (#506)"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove SCIM group members (#506)"), "the down half has its header")

	statementsOf := func(sql string) string {
		var lines []string
		for _, line := range strings.Split(strings.ToLower(sql), "\n") {
			if !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	upStatements, downStatements := statementsOf(string(up)), statementsOf(string(down))
	for _, fragment := range []string{
		"create table if not exists scim_group_members (",
		"group_value varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"user_id bigint unsigned not null",
		"unique key idx_scim_group_members_group_user (group_value, user_id)",
		"key idx_scim_group_members_user_id (user_id)",
		"constraint fk_scim_group_members_user foreign key (user_id) references users (id) on delete restrict",
	} {
		assert.Containsf(t, upStatements, fragment, "up declares %s", fragment)
	}
	assert.Equal(t, 1, strings.Count(upStatements, "create table"), "one table")
	for _, forbidden := range []string{"deleted_at", "alter table", "insert ", "update ", "delete from", "drop "} {
		assert.NotContains(t, upStatements, forbidden, "the up half creates the one table and nothing else")
	}
	assert.Contains(t, downStatements, "drop table if exists scim_group_members;")
	assert.Equal(t, 1, strings.Count(downStatements, "drop "), "rollback drops the one table only")

	// The model declares the same width and index names.
	tag, ok := reflectField(models.SCIMGroupMember{}, "GroupValue")
	require.True(t, ok)
	assert.Contains(t, tag, "type:varchar(256);not null;uniqueIndex:idx_scim_group_members_group_user,priority:1")
	tag, ok = reflectField(models.SCIMGroupMember{}, "UserID")
	require.True(t, ok)
	assert.Contains(t, tag, "uniqueIndex:idx_scim_group_members_group_user,priority:2")
	assert.Contains(t, tag, "index:idx_scim_group_members_user_id")
	tag, ok = reflectField(models.SCIMGroupMember{}, "User")
	require.True(t, ok)
	assert.Contains(t, tag, "constraint:OnDelete:RESTRICT")
	assert.Equal(t, scimGroupMembersTable, models.SCIMGroupMember{}.TableName())
	assert.Equal(t, 256, models.SCIMGroupMemberValueMaxLength)

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from
	// TestUserIdentitySourceMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, scimGroupMembersMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, scimGroupMembersVersion, version)
	assert.Equal(t, scimGroupMembersVersion, latestMigrationVersion)
	assert.Greater(t, scimGroupMembersVersion, scimGroupMembersPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+scimGroupMembersTable+`"`, "main_test.go's expected-table list names the table")
}

// scimGroupValueCollation returns the collation of group_value.
func scimGroupValueCollation(t *testing.T, db *sql.DB, schema string) string {
	t.Helper()
	var collation string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'scim_group_members' AND COLUMN_NAME = 'group_value'`, schema).Scan(&collation))
	return collation
}

// assertSCIMGroupValuesExact stores Sales and sales for one user as two rows,
// refuses an exact duplicate and an unknown user, and removes its rows again.
func assertSCIMGroupValuesExact(t *testing.T, db *sql.DB, email string) {
	t.Helper()
	result, err := db.Exec("INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at) VALUES (?, 'x', 'G', 'M', 'sales', 1, NOW(3), NOW(3))", email)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	insert := func(userID int64, value string) error {
		_, err := db.Exec("INSERT INTO scim_group_members (created_at, group_value, user_id) VALUES (NOW(3), ?, ?)", value, userID)
		return err
	}
	require.NoError(t, insert(userID, "Sales"))
	require.NoError(t, insert(userID, "sales"), "a value differing only in case is another group")
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scim_group_members WHERE user_id = ?", userID).Scan(&count))
	assert.Equal(t, 2, count, "Sales and sales are two rows")
	require.Error(t, insert(userID, "Sales"), "the pair is unique")
	require.Error(t, insert(987654321, "Sales"), "user_id must name a user")
	_, err = db.Exec("DELETE FROM users WHERE id = ?", userID)
	require.Error(t, err, "a user with memberships cannot be deleted (ON DELETE RESTRICT)")
	_, err = db.Exec("DELETE FROM scim_group_members WHERE user_id = ?", userID)
	require.NoError(t, err)
}

func TestSCIMGroupMembersMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })
	tables := []string{scimGroupMembersTable}

	// The parity reference: the table as MigrateDatabase (AutoMigrate plus
	// the collation guard) creates it on top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "sgm_ref")
	require.NoError(t, reference.m.Migrate(scimGroupMembersPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), scimGroupMembersTable)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, scimGroupMembersTable)
	assert.Contains(t, want.Indexes, "idx_scim_group_members_group_user (group_value,user_id) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_scim_group_members_user_id (user_id) non_unique=1")
	assert.Equal(t, []string{"fk_scim_group_members_user user_id -> users.id"}, want.ForeignKeys)
	assert.Equal(t, "utf8mb4_bin", scimGroupValueCollation(t, reference.sql, reference.name))

	assertShape := func(t *testing.T, tdb *migrationTestDB) {
		t.Helper()
		assert.Equal(t, want, snapshotTable(t, tdb, scimGroupMembersTable), "scim_group_members as MigrateDatabase creates it")
		assert.Equal(t, "utf8mb4_bin", scimGroupValueCollation(t, tdb.sql, tdb.name), "group_value compares exactly")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "sgm_fresh")
		require.NoError(t, tdb.m.Migrate(scimGroupMembersVersion))
		assertShape(t, tdb)
		assertSCIMGroupValuesExact(t, tdb.sql, "fresh-group-member@example.com")

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the one table and nothing else")
		require.NoError(t, tdb.m.Migrate(scimGroupMembersVersion))
		assertShape(t, tdb)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "sgm_mig")
			require.NoError(t, tdb.m.Migrate(scimGroupMembersPreviousVersion))
			var keptUser int64
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assertSCIMGroupValuesExact(t, tdb.sql, "early-group-member@example.com")
				result, err := tdb.sql.Exec("INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at) VALUES ('kept-member@example.com', 'x', 'K', 'M', 'sales', 1, NOW(3), NOW(3))")
				require.NoError(t, err)
				keptUser, err = result.LastInsertId()
				require.NoError(t, err)
				_, err = tdb.sql.Exec("INSERT INTO scim_group_members (created_at, group_value, user_id) VALUES (NOW(3), 'Admins', ?)", keptUser)
				require.NoError(t, err)
			}
			require.NoError(t, tdb.m.Migrate(scimGroupMembersVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(scimGroupMembersVersion), version)
			assertShape(t, tdb)
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM scim_group_members WHERE user_id = ?", keptUser).Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			runMigrateDatabase(t, tdb.dsn)
			assertShape(t, tdb)
			require.ErrorIs(t, tdb.m.Migrate(scimGroupMembersVersion), migrate.ErrNoChange)
			assertSCIMGroupValuesExact(t, tdb.sql, "upgraded-group-member@example.com")

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(scimGroupMembersPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the one table and nothing else")

			require.NoError(t, tdb.m.Migrate(scimGroupMembersVersion))
			assertShape(t, tdb)
		})
	}
}
