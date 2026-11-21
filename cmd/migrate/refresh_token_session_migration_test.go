package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #546: 20261129120004_refresh_token_session adds family_id, sign_in_method
// and session_started_at to refresh_tokens, plus idx_refresh_tokens_family_id,
// each only when missing, and fills the rows that hold no value yet: family_id
// is the row's id as a decimal string, sign_in_method is oidc when the owner
// has any user_identities row and password otherwise, session_started_at is
// created_at. The down half drops the index and the three columns. The
// startup step models.backfillRefreshTokenSessions does the same fill on
// every driver.

const (
	refreshTokenSessionVersion         = 20261129120004
	refreshTokenSessionPreviousVersion = apiKeyLifetimeVersion
	refreshTokenSessionMigrationName   = "20261129120004_refresh_token_session"
	refreshTokensTable                 = "refresh_tokens"
	refreshTokenFamilyIndex            = "idx_refresh_tokens_family_id"
)

// refreshTokenSessionColumns are the columns the migration adds.
var refreshTokenSessionColumns = []string{"family_id", "sign_in_method", "session_started_at"}

var newFamilyIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestRefreshTokenSessionMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the three columns and the index
// behind INFORMATION_SCHEMA probes and then runs the four fills, the down half
// drops only those, the model declares the columns as the up half adds them,
// and it is the newest migration of the directory, which
// latestMigrationVersion names.
func TestRefreshTokenSessionMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, refreshTokenSessionMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, refreshTokenSessionMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Refresh token session (#546)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove refresh token session (#546)\n-- Date: "), "the down half has its header")
	for _, half := range []string{string(up), string(down)} {
		assert.Contains(t, half, "\n-- Description: ")
		assert.Contains(t, half, "MySQL 8 and MariaDB 10.11 accept this as written.")
	}

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

	probe := "information_schema.columns where table_schema = database() and table_name = 'refresh_tokens' and column_name = "
	assert.Contains(t, upStatements, probe+"'family_id'), 'select 1', 'alter table refresh_tokens add column family_id varchar(64) not null default '''''")
	assert.Contains(t, upStatements, probe+"'sign_in_method'), 'select 1', 'alter table refresh_tokens add column sign_in_method varchar(16) not null default '''''")
	assert.Contains(t, upStatements, probe+"'session_started_at'), 'select 1', 'alter table refresh_tokens add column session_started_at datetime(3) null'")
	assert.Contains(t, upStatements, "information_schema.statistics where table_schema = database() and table_name = 'refresh_tokens' and index_name = 'idx_refresh_tokens_family_id'), 'select 1', 'alter table refresh_tokens add index idx_refresh_tokens_family_id (family_id)'")
	assert.Equal(t, 4, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 3, strings.Count(upStatements, "add column"), "exactly three columns")
	assert.Equal(t, 1, strings.Count(upStatements, "add index"), "exactly one index")
	assert.Contains(t, upStatements, "\nupdate refresh_tokens set family_id = cast(id as char) where family_id = '';\n")
	assert.Contains(t, upStatements, "\nupdate refresh_tokens set sign_in_method = 'oidc' where sign_in_method = '' and user_id in (select user_id from user_identities);\n")
	assert.Contains(t, upStatements, "\nupdate refresh_tokens set sign_in_method = 'password' where sign_in_method = '';\n")
	assert.Contains(t, upStatements, "\nupdate refresh_tokens set session_started_at = created_at where session_started_at is null;\n")
	assert.Equal(t, 4, strings.Count(upStatements, "\nupdate "), "exactly four fills")
	for _, forbidden := range []string{"insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "unique", "updated_at"} {
		assert.NotContains(t, upStatements, forbidden, "the up half adds the columns and the index, fills them and changes nothing else")
	}

	assert.Contains(t, downStatements, "index_name = 'idx_refresh_tokens_family_id'), 'alter table refresh_tokens drop index idx_refresh_tokens_family_id', 'select 1'")
	for _, column := range refreshTokenSessionColumns {
		assert.Contains(t, downStatements, fmt.Sprintf("column_name = '%s'), 'alter table refresh_tokens drop column %s', 'select 1'", column, column))
	}
	assert.Equal(t, 4, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 4, strings.Count(downStatements, " drop "), "the down half drops the index and the three columns only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "add column", "add index", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The model declares the columns as the up half adds them; the start has
	// no type override, so AutoMigrate builds datetime(3) NULL on MySQL.
	for field, want := range map[string]string{
		"FamilyID":         "type:varchar(64);not null;default:'';index:idx_refresh_tokens_family_id",
		"SignInMethod":     "type:varchar(16);not null;default:''",
		"SessionStartedAt": "",
	} {
		tag, ok := reflectField(models.RefreshToken{}, field)
		require.True(t, ok, field)
		assert.Equal(t, want, tag, field)
	}
	assert.Equal(t, "password", models.RefreshTokenSignInPassword)
	assert.Equal(t, "oidc", models.RefreshTokenSignInOIDC)

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestAPIKeyLifetimeMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, refreshTokenSessionMigrationName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, refreshTokenSessionVersion, version)
	assert.Equal(t, refreshTokenSessionVersion, latestMigrationVersion)
	assert.Equal(t, refreshTokenSessionPreviousVersion+1, refreshTokenSessionVersion)
}

// sessionFixtureRow is one refresh token the migration test seeds.
type sessionFixtureRow struct {
	id        int64
	method    string // the sign_in_method the fill must give it
	createdAt string
}

// seedSessionRows writes a password account, an account with an oidc
// identity and one with only a scim identity, and live, revoked and
// soft-deleted refresh tokens for them, as the previous release wrote them.
func seedSessionRows(t *testing.T, tdb *migrationTestDB) []sessionFixtureRow {
	t.Helper()
	user := func(email string) int64 {
		result, err := tdb.sql.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
			VALUES (?, 'x', 'R', 'S', 'sales', 1, NOW(3), NOW(3))`, email)
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		return id
	}
	password := user("session-password@example.com")
	oidc := user("session-oidc@example.com")
	scim := user("session-scim@example.com")
	for owner, source := range map[int64]string{oidc: "oidc", scim: "scim"} {
		_, err := tdb.sql.Exec(`INSERT INTO user_identities (created_at, updated_at, user_id, issuer, subject, source)
			VALUES (NOW(3), NOW(3), ?, 'https://issuer.example.com', ?, ?)`, owner, fmt.Sprintf("sub-%d", owner), source)
		require.NoError(t, err)
	}
	n := 0
	token := func(owner int64, method, revoked, deleted string) sessionFixtureRow {
		n++
		created := fmt.Sprintf("2024-05-0%d 07:08:09.123", n)
		result, err := tdb.sql.Exec(fmt.Sprintf(`INSERT INTO refresh_tokens (created_at, updated_at, deleted_at, user_id, token_hash, expires_at, is_revoked)
			VALUES (?, '2024-06-07 08:09:10.456', %s, ?, ?, NOW(3) + INTERVAL 30 DAY, %s)`, deleted, revoked),
			created, owner, fmt.Sprintf("session-hash-%d", n))
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		return sessionFixtureRow{id: id, method: method, createdAt: created + "000"}
	}
	return []sessionFixtureRow{
		token(password, "password", "0", "NULL"),
		token(password, "password", "1", "NULL"),
		token(password, "password", "0", "NOW(3)"),
		token(oidc, "oidc", "0", "NULL"),
		token(oidc, "oidc", "1", "NULL"),
		token(scim, "oidc", "0", "NOW(3)"),
	}
}

// sessionRows renders every refresh token with the columns the migration
// owns or must keep, as text, ordered by id.
func sessionRows(t *testing.T, tdb *migrationTestDB, withSession bool) []string {
	t.Helper()
	session := "''"
	if withSession {
		session = `CONCAT(family_id, ' ', sign_in_method, ' ', COALESCE(DATE_FORMAT(session_started_at, '%Y-%m-%d %H:%i:%s.%f'), 'NULL'))`
	}
	rows, err := tdb.sql.Query(`SELECT id, user_id, token_hash, is_revoked, deleted_at IS NULL,
		DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f'), ` + session + `
		FROM refresh_tokens ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, owner int64
		var hash, created, updated, values string
		var revoked, live bool
		require.NoError(t, rows.Scan(&id, &owner, &hash, &revoked, &live, &created, &updated, &values))
		out = append(out, fmt.Sprintf("%d owner=%d hash=%s revoked=%t live=%t created=%s updated=%s session=%s",
			id, owner, hash, revoked, live, created, updated, values))
	}
	require.NoError(t, rows.Err())
	return out
}

// assertSessionFilled checks the values the fill gives each seeded row.
func assertSessionFilled(t *testing.T, tdb *migrationTestDB, seeded []sessionFixtureRow) {
	t.Helper()
	for _, row := range seeded {
		var family, method, started, updated string
		require.NoError(t, tdb.sql.QueryRow(`SELECT family_id, sign_in_method,
			COALESCE(DATE_FORMAT(session_started_at, '%Y-%m-%d %H:%i:%s.%f'), ''), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f')
			FROM refresh_tokens WHERE id = ?`, row.id).Scan(&family, &method, &started, &updated))
		assert.Equal(t, strconv.FormatInt(row.id, 10), family, "row %d is its own family", row.id)
		assert.False(t, newFamilyIDPattern.MatchString(family), "a legacy family never looks like a new one")
		assert.Equal(t, row.method, method, "row %d sign-in method", row.id)
		assert.Equal(t, row.createdAt, started, "row %d starts at created_at", row.id)
		assert.Equal(t, "2024-06-07 08:09:10.456000", updated, "updated_at is not touched")
	}
}

func TestRefreshTokenSessionMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the columns and the index as MigrateDatabase
	// builds them on top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "rts_ref")
	require.NoError(t, reference.m.Migrate(refreshTokenSessionPreviousVersion))
	before := snapshotTable(t, reference, refreshTokensTable)
	require.Empty(t, ownedColumns(before, refreshTokenSessionColumns), "the previous head has none of the columns")
	require.Empty(t, sessionIndexes(before))
	runMigrateDatabase(t, reference.dsn)
	afterAutoMigrate := snapshotTable(t, reference, refreshTokensTable)
	wantOwned := ownedColumns(afterAutoMigrate, refreshTokenSessionColumns)
	require.Len(t, wantOwned, 3)
	for _, line := range wantOwned {
		switch {
		case strings.HasPrefix(line, "family_id "):
			assert.True(t, strings.HasPrefix(line, "family_id varchar(64) nullable=NO default="), line)
		case strings.HasPrefix(line, "sign_in_method "):
			assert.True(t, strings.HasPrefix(line, "sign_in_method varchar(16) nullable=NO default="), line)
		default:
			assert.True(t, strings.HasPrefix(line, "session_started_at datetime(3) nullable=YES default="), line)
		}
	}
	wantIndexes := sessionIndexes(afterAutoMigrate)
	require.Equal(t, []string{refreshTokenFamilyIndex + " (family_id) non_unique=1"}, wantIndexes)

	// migrated asserts that the table is base plus exactly the columns and
	// the index as AutoMigrate builds them.
	migrated := func(t *testing.T, tdb *migrationTestDB, base tableSchema) tableSchema {
		t.Helper()
		got := snapshotTable(t, tdb, refreshTokensTable)
		assert.Equal(t, wantOwned, ownedColumns(got, refreshTokenSessionColumns), "the columns match AutoMigrate")
		assert.Equal(t, wantIndexes, sessionIndexes(got), "the index matches AutoMigrate")
		assert.Equal(t, withoutSession(base), withoutSession(got), "nothing else changes")
		return got
	}

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "rts_mig")
			require.NoError(t, tdb.m.Migrate(refreshTokenSessionPreviousVersion))
			seeded := seedSessionRows(t, tdb)
			legacy := sessionRows(t, tdb, false)
			base := snapshotTable(t, tdb, refreshTokensTable)
			var current int64
			if autoMigrateFirst {
				// A new binary started before the operator migrated: the
				// startup step fills the rows first, and a current session
				// is issued before the migration runs.
				runMigrateDatabase(t, tdb.dsn)
				base = snapshotTable(t, tdb, refreshTokensTable)
				assertSessionFilled(t, tdb, seeded)
				result, err := tdb.sql.Exec(`INSERT INTO refresh_tokens (created_at, updated_at, user_id, token_hash, expires_at, is_revoked,
					family_id, sign_in_method, session_started_at)
					VALUES ('2024-07-01 00:00:00.000', '2024-07-01 00:00:00.000', (SELECT MIN(id) FROM users), 'session-current',
					NOW(3) + INTERVAL 30 DAY, 0, '0123456789abcdef0123456789abcdef', 'oidc', '2024-06-30 12:00:00.000')`)
				require.NoError(t, err)
				current, err = result.LastInsertId()
				require.NoError(t, err)
			}
			preUp := sessionRows(t, tdb, autoMigrateFirst)

			require.NoError(t, tdb.m.Migrate(refreshTokenSessionVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(refreshTokenSessionVersion), version)
			schema := migrated(t, tdb, base)
			assertSessionFilled(t, tdb, seeded)
			after := sessionRows(t, tdb, true)
			if autoMigrateFirst {
				assert.Equal(t, preUp, after, "the up half changes no row the startup step filled")
				var family, method, started string
				require.NoError(t, tdb.sql.QueryRow(`SELECT family_id, sign_in_method, DATE_FORMAT(session_started_at, '%Y-%m-%d %H:%i:%s.%f')
					FROM refresh_tokens WHERE id = ?`, current).Scan(&family, &method, &started))
				assert.Equal(t, "0123456789abcdef0123456789abcdef", family, "a row holding values is untouched")
				assert.Equal(t, "oidc", method)
				assert.Equal(t, "2024-06-30 12:00:00.000000", started)
			}

			// Applying the up half again changes nothing.
			require.NoError(t, tdb.m.Force(refreshTokenSessionPreviousVersion))
			require.NoError(t, tdb.m.Migrate(refreshTokenSessionVersion), "the up half runs again")
			assert.Equal(t, schema, snapshotTable(t, tdb, refreshTokensTable), "a second run changes no schema")
			assert.Equal(t, after, sessionRows(t, tdb, true), "a second run changes no row")
			require.ErrorIs(t, tdb.m.Migrate(refreshTokenSessionVersion), migrate.ErrNoChange)

			// Rollback removes exactly the columns and the index and keeps
			// every row.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(refreshTokenSessionPreviousVersion), version)
			rolledBack := snapshotTable(t, tdb, refreshTokensTable)
			assert.Empty(t, ownedColumns(rolledBack, refreshTokenSessionColumns), "the columns are gone")
			assert.Empty(t, sessionIndexes(rolledBack), "the index is gone")
			assert.Equal(t, withoutSession(schema), rolledBack, "rollback removes the session columns only")
			kept := sessionRows(t, tdb, false)
			if autoMigrateFirst {
				require.Len(t, kept, len(legacy)+1)
				kept = kept[:len(legacy)]
			}
			assert.Equal(t, legacy, kept, "rollback keeps every row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(refreshTokenSessionVersion), "reapply")
			migrated(t, tdb, base)
			assertSessionFilled(t, tdb, seeded)
			require.ErrorIs(t, tdb.m.Migrate(refreshTokenSessionVersion), migrate.ErrNoChange)
		})
	}
}

// sessionIndexes keeps the index lines of a snapshot this migration owns.
func sessionIndexes(schema tableSchema) []string {
	out := []string{}
	for _, line := range schema.Indexes {
		if strings.HasPrefix(line, refreshTokenFamilyIndex+" ") {
			out = append(out, line)
		}
	}
	return out
}

// withoutSession drops the columns and the index this migration owns from a
// snapshot.
func withoutSession(schema tableSchema) tableSchema {
	out := withoutColumns(schema, refreshTokenSessionColumns)
	var indexes []string
	for _, line := range out.Indexes {
		if !strings.HasPrefix(line, refreshTokenFamilyIndex+" ") {
			indexes = append(indexes, line)
		}
	}
	out.Indexes = indexes
	return out
}
