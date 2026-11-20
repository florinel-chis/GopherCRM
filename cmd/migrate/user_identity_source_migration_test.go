package main

import (
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

// #505: 20261128120000_user_identity_source adds user_identities.source
// (varchar(8) NOT NULL DEFAULT 'oidc') only when missing. SCIM create writes
// scim, a sign-in oidc; the default backfills every existing row as oidc. No
// index. The down half drops the column.

const (
	userIdentitySourceVersion         = 20261128120000
	userIdentitySourcePreviousVersion = webhookSecretRotationVersion
	userIdentitySourceMigrationName   = "20261128120000_user_identity_source"
)

// userIdentitySourceColumns are the columns the migration adds.
var userIdentitySourceColumns = []string{"source"}

// TestUserIdentitySourceMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly source behind an
// INFORMATION_SCHEMA probe, the down half drops only it, the model declares
// the same, and it is the newest migration of the directory, which
// latestMigrationVersion names.
func TestUserIdentitySourceMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, userIdentitySourceMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, userIdentitySourceMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: User identity source (#505)"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove user identity source (#505)"), "the down half has its header")

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

	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'user_identities' and column_name = 'source'), 'select 1', 'alter table user_identities add column source varchar(8) not null default ''oidc'''",
		"source is added only when missing")
	assert.Equal(t, 1, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upStatements, "add column"), "exactly one column")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "index", "unique", "collate"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, adds no index or key and changes nothing else")
	}

	assert.Contains(t, downStatements, "column_name = 'source'), 'alter table user_identities drop column source', 'select 1'")
	assert.Equal(t, 1, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downStatements, " drop "), "the down half drops source only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "drop index", "add column", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the same: not null, default oidc, no index.
	tag, ok := reflectField(models.UserIdentity{}, "Source")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(8);not null;default:oidc", tag)
	assert.Equal(t, "oidc", models.UserIdentitySourceOIDC)
	assert.Equal(t, "scim", models.UserIdentitySourceSCIM)

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from
	// TestWebhookSecretRotationMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, userIdentitySourceMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, userIdentitySourceVersion, version)
	assert.Equal(t, userIdentitySourceVersion, latestMigrationVersion)
	assert.Greater(t, userIdentitySourceVersion, userIdentitySourcePreviousVersion)
}

// insertSourceIdentityRow writes one user and one identity as #435 wrote
// them, without source, and returns the identity id.
func insertSourceIdentityRow(t *testing.T, tdb *migrationTestDB, subject string) int64 {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
		VALUES (?, 'x', 'I', 'D', 'sales', 1, NOW(3), NOW(3))`, subject+"@example.com")
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	result, err = tdb.sql.Exec(`INSERT INTO user_identities (created_at, updated_at, user_id, issuer, subject)
		VALUES (NOW(3), NOW(3), ?, 'https://idp.example.test/505', ?)`, userID, subject)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertSourceIdentityRowKept checks that the identity written before the
// migration is there unchanged and reads oidc.
func assertSourceIdentityRowKept(t *testing.T, tdb *migrationTestDB, id int64, subject string) {
	t.Helper()
	var issuer, gotSubject, source string
	require.NoError(t, tdb.sql.QueryRow(`SELECT issuer, subject, source FROM user_identities WHERE id = ?`, id).
		Scan(&issuer, &gotSubject, &source))
	assert.Equal(t, "https://idp.example.test/505", issuer)
	assert.Equal(t, subject, gotSubject)
	assert.Equal(t, models.UserIdentitySourceOIDC, source, "an existing row reads oidc")
}

func TestUserIdentitySourceMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table at the previous SQL head (before), and
	// as MigrateDatabase builds it on top of that head (want).
	reference := openMigrationTestDB(t, baseDSN, "uis_ref")
	require.NoError(t, reference.m.Migrate(userIdentitySourcePreviousVersion))
	before := snapshotTable(t, reference, userIdentitiesTable)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, userIdentitiesTable)
	require.Len(t, want.Columns, len(before.Columns)+1)
	assert.Equal(t, before, withoutColumns(want, userIdentitySourceColumns), "the migration owns only source")
	owned := ownedColumns(want, userIdentitySourceColumns)
	require.Len(t, owned, 1)
	assert.Contains(t, owned[0], "source varchar(8) nullable=NO default=")
	assert.Contains(t, owned[0], "oidc")

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "uis_fresh")
		require.NoError(t, tdb.m.Migrate(userIdentitySourceVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable), "a fresh SQL schema carries what MigrateDatabase builds")
		assertUserIdentitiesBinary(t, tdb.sql, tdb.name)
		id := insertSourceIdentityRow(t, tdb, "fresh-subject")
		assertSourceIdentityRowKept(t, tdb, id, "fresh-subject")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, userIdentitiesTable), "rollback removes source only")
		require.NoError(t, tdb.m.Migrate(userIdentitySourceVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable))
		require.ErrorIs(t, tdb.m.Migrate(userIdentitySourceVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "uis_mig")
			require.NoError(t, tdb.m.Migrate(userIdentitySourcePreviousVersion))
			const subject = "populated-subject"
			id := insertSourceIdentityRow(t, tdb, subject)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable))
			}

			require.NoError(t, tdb.m.Migrate(userIdentitySourceVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userIdentitySourceVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable), "the SQL migration and MigrateDatabase produce the same table")
			assertUserIdentitiesBinary(t, tdb.sql, tdb.name)
			assertSourceIdentityRowKept(t, tdb, id, subject)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(userIdentitySourceVersion), migrate.ErrNoChange)

			// Rollback keeps the row and removes only what #505 added.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(userIdentitySourcePreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, userIdentitiesTable), "rollback removes source only")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM user_identities WHERE id = ? AND subject = ?", id, subject).Scan(&count))
			assert.Equal(t, 1, count, "rollback keeps the identity row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(userIdentitySourceVersion), "reapply")
			assert.Equal(t, want, snapshotTable(t, tdb, userIdentitiesTable))
			assertSourceIdentityRowKept(t, tdb, id, subject)
			require.ErrorIs(t, tdb.m.Migrate(userIdentitySourceVersion), migrate.ErrNoChange)
		})
	}
}
