package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #435: 20261123120000_add_oidc_sign_in creates user_identities (the exact
// (issuer, subject) pair of an OpenID Connect identity, utf8mb4_bin on both
// columns, a foreign key to users) and oidc_login_attempts (single-use sign-in
// attempts, no foreign key), each only when missing, with the columns and
// index names AutoMigrate plus the collation guard produce; rollback drops
// those two tables and nothing else.

const (
	userIdentitiesVersion         = 20261123120000
	userIdentitiesPreviousVersion = quoteHistorySourceVersion
	userIdentitiesMigrationName   = "20261123120000_add_oidc_sign_in"
	userIdentitiesTable           = "user_identities"
	oidcLoginAttemptsTable        = "oidc_login_attempts"
)

// TestUserIdentitiesMigrationFiles needs no database: the pair exists with
// its headers, creates both tables only when missing with the model's columns,
// collations and index names, drops only them, and is older than the newest
// migration, which latestMigrationVersion names.
func TestUserIdentitiesMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, userIdentitiesMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, userIdentitiesMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: OIDC sign-in"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove OIDC sign-in"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, fragment := range []string{
		"create table if not exists user_identities (",
		"user_id bigint unsigned not null",
		"issuer varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"subject varchar(255) character set utf8mb4 collate utf8mb4_bin not null",
		"unique key idx_user_identities_issuer_subject (issuer, subject)",
		"key idx_user_identities_user_id (user_id)",
		"constraint fk_user_identities_user foreign key (user_id) references users (id) on delete restrict",
		"create table if not exists oidc_login_attempts (",
		"state_hash varchar(64) not null",
		"nonce varchar(64) not null",
		"code_verifier varchar(128) not null",
		"issuer varchar(2048) not null",
		"expires_at datetime(3) not null",
		"used_at datetime(3) null",
		"unique key idx_oidc_login_attempts_state_hash (state_hash)",
	} {
		assert.Containsf(t, upSQL, fragment, "up declares %s", fragment)
	}
	// The statements alone: the header comment names what is not there.
	var statements []string
	for _, line := range strings.Split(upSQL, "\n") {
		if !strings.HasPrefix(line, "--") {
			statements = append(statements, line)
		}
	}
	upStatements := strings.Join(statements, "\n")
	assert.NotContains(t, upStatements, "deleted_at", "neither table has a soft delete")
	assert.NotContains(t, upStatements, "alter table", "no existing table is altered")
	attempts := strings.SplitN(upSQL, "create table if not exists oidc_login_attempts", 2)[1]
	assert.NotContains(t, attempts, "foreign key", "the attempts carry no foreign key")
	assert.Contains(t, downSQL, "drop table if exists user_identities;")
	assert.Contains(t, downSQL, "drop table if exists oidc_login_attempts;")
	assert.Equal(t, 2, strings.Count(downSQL, "drop table"), "rollback drops the two tables only")

	// The model declares the same widths and index names.
	tag, ok := reflectField(models.UserIdentity{}, "Issuer")
	require.True(t, ok)
	assert.Contains(t, tag, "type:varchar(512);not null;uniqueIndex:idx_user_identities_issuer_subject,priority:1")
	tag, ok = reflectField(models.UserIdentity{}, "Subject")
	require.True(t, ok)
	assert.Contains(t, tag, "type:varchar(255);not null;uniqueIndex:idx_user_identities_issuer_subject,priority:2")
	tag, ok = reflectField(models.OIDCLoginAttempt{}, "StateHash")
	require.True(t, ok)
	assert.Contains(t, tag, "uniqueIndex:idx_oidc_login_attempts_state_hash")

	// Older than the newest migration (the newest-migration assertions moved
	// to TestAuthAuditLogMigrationFiles).
	assert.Less(t, userIdentitiesVersion, latestMigrationVersion)
	assert.Greater(t, userIdentitiesVersion, userIdentitiesPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	for _, table := range []string{userIdentitiesTable, oidcLoginAttemptsTable} {
		assert.Contains(t, string(source), `"`+table+`"`, "main_test.go's expected-table list names %s", table)
	}
}

// userIdentityCollations returns the collation of issuer and subject.
func userIdentityCollations(t *testing.T, db *sql.DB, schema string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT COLUMN_NAME, COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'user_identities' AND COLUMN_NAME IN ('issuer', 'subject')`, schema)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, collation string
		require.NoError(t, rows.Scan(&name, &collation))
		out[name] = collation
	}
	require.NoError(t, rows.Err())
	return out
}

// assertUserIdentitiesBinary is used by the fresh and upgrade roots too.
func assertUserIdentitiesBinary(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	assert.Equal(t, map[string]string{"issuer": "utf8mb4_bin", "subject": "utf8mb4_bin"}, userIdentityCollations(t, db, schema),
		"issuer and subject compare exactly")
}

// showCreateUserIdentities is SHOW CREATE TABLE user_identities, lower-cased.
func showCreateUserIdentities(t *testing.T, db *sql.DB) string {
	t.Helper()
	var name, ddl string
	require.NoError(t, db.QueryRow("SHOW CREATE TABLE user_identities").Scan(&name, &ddl))
	return strings.ToLower(ddl)
}

// assertIdentitySubjectsCaseSensitive links two subjects of one issuer that
// differ only in letter case: both insert, an exact repeat does not.
func assertIdentitySubjectsCaseSensitive(t *testing.T, db *sql.DB, email string) {
	t.Helper()
	result, err := db.Exec("INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at) VALUES (?, 'x', 'I', 'D', 'sales', 1, NOW(3), NOW(3))", email)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	const issuer = "https://idp.example.test"
	insert := func(subject string) error {
		_, err := db.Exec("INSERT INTO user_identities (created_at, updated_at, user_id, issuer, subject) VALUES (NOW(3), NOW(3), ?, ?, ?)", userID, issuer, subject)
		return err
	}
	require.NoError(t, insert("Subject-A"))
	require.NoError(t, insert("subject-a"), "a subject differing only in case is another identity")
	require.Error(t, insert("Subject-A"), "the exact pair is unique")
	require.Error(t, insertIdentityFor(db, 987654321, issuer, "orphan"), "user_id must name a user")
}

func insertIdentityFor(db *sql.DB, userID int64, issuer, subject string) error {
	_, err := db.Exec("INSERT INTO user_identities (created_at, updated_at, user_id, issuer, subject) VALUES (NOW(3), NOW(3), ?, ?, ?)", userID, issuer, subject)
	return err
}

func TestUserIdentitiesMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })
	tables := []string{userIdentitiesTable, oidcLoginAttemptsTable}

	// The parity reference: both tables as MigrateDatabase (AutoMigrate plus
	// the collation guard) creates them on top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "uid_ref")
	require.NoError(t, reference.m.Migrate(userIdentitiesPreviousVersion))
	for _, table := range tables {
		assert.NotContains(t, listTables(t, reference.sql, reference.name), table)
	}
	runMigrateDatabase(t, reference.dsn)
	wantIdentities := snapshotTable(t, reference, userIdentitiesTable)
	wantAttempts := snapshotTable(t, reference, oidcLoginAttemptsTable)
	assert.Contains(t, wantIdentities.Indexes, "idx_user_identities_issuer_subject (issuer,subject) non_unique=0")
	assert.Contains(t, wantIdentities.Indexes, "idx_user_identities_user_id (user_id) non_unique=1")
	assert.Equal(t, []string{"fk_user_identities_user user_id -> users.id"}, wantIdentities.ForeignKeys)
	assert.Contains(t, wantAttempts.Indexes, "idx_oidc_login_attempts_state_hash (state_hash) non_unique=0")
	assert.Empty(t, wantAttempts.ForeignKeys)
	assertUserIdentitiesBinary(t, reference.sql, reference.name)

	assertShape := func(t *testing.T, tdb *migrationTestDB) {
		t.Helper()
		// source belongs to 20261128120000_user_identity_source (#505), which
		// this migration does not create.
		assert.Equal(t, withoutColumns(wantIdentities, []string{"source"}),
			withoutColumns(snapshotTable(t, tdb, userIdentitiesTable), []string{"source"}), "user_identities as MigrateDatabase creates it")
		assert.Equal(t, wantAttempts, snapshotTable(t, tdb, oidcLoginAttemptsTable), "oidc_login_attempts as MigrateDatabase creates it")
		assertUserIdentitiesBinary(t, tdb.sql, tdb.name)
		ddl := showCreateUserIdentities(t, tdb.sql)
		assert.Contains(t, ddl, "unique key `idx_user_identities_issuer_subject` (`issuer`,`subject`)")
		assert.Contains(t, ddl, "utf8mb4_bin")
		assert.Contains(t, ddl, "constraint `fk_user_identities_user` foreign key (`user_id`) references `users` (`id`)")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "uid_fresh")
		require.NoError(t, tdb.m.Migrate(userIdentitiesVersion))
		assertShape(t, tdb)
		assertIdentitySubjectsCaseSensitive(t, tdb.sql, "fresh-identity@example.com")

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the two tables and nothing else")
		require.NoError(t, tdb.m.Migrate(userIdentitiesVersion))
		assertShape(t, tdb)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "uid_mig")
			require.NoError(t, tdb.m.Migrate(userIdentitiesPreviousVersion))
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assertIdentitySubjectsCaseSensitive(t, tdb.sql, "early-identity@example.com")
			}
			require.NoError(t, tdb.m.Migrate(userIdentitiesVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userIdentitiesVersion), version)
			assertShape(t, tdb)
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM user_identities").Scan(&count))
				assert.Equal(t, 2, count, "a table AutoMigrate created first is kept with its rows")
			}

			runMigrateDatabase(t, tdb.dsn)
			assertShape(t, tdb)
			require.ErrorIs(t, tdb.m.Migrate(userIdentitiesVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userIdentitiesPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the two tables and nothing else")

			require.NoError(t, tdb.m.Migrate(userIdentitiesVersion))
			assertShape(t, tdb)
		})
	}
}
