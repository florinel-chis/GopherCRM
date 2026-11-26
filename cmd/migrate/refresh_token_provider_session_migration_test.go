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

// #634: 20261129120008_refresh_token_provider_session adds
// provider_session_hash to refresh_tokens, plus
// idx_refresh_tokens_provider_session_hash, each only when missing. Legacy
// rows keep ''; nothing is filled. The down half drops the index and the
// column.

const (
	refreshTokenProviderSessionVersion         = 20261129120008
	refreshTokenProviderSessionPreviousVersion = webhookEventTypesWidthVersion
	refreshTokenProviderSessionMigrationName   = "20261129120008_refresh_token_provider_session"
	refreshTokenProviderSessionIndex           = "idx_refresh_tokens_provider_session_hash"
)

// refreshTokenProviderSessionColumns is the column the migration adds.
var refreshTokenProviderSessionColumns = []string{"provider_session_hash"}

// TestRefreshTokenProviderSessionMigrationFiles needs no database: the pair
// exists with its headers, the up half adds exactly the column and the index
// behind INFORMATION_SCHEMA probes and fills nothing, the down half drops
// only those, the model declares the column as the up half adds it, and a
// newer migration follows it.
func TestRefreshTokenProviderSessionMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, refreshTokenProviderSessionMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, refreshTokenProviderSessionMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Refresh token provider session (#634)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove refresh token provider session (#634)\n-- Date: "), "the down half has its header")
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

	assert.Contains(t, upStatements, "information_schema.columns where table_schema = database() and table_name = 'refresh_tokens' and column_name = "+
		"'provider_session_hash'), 'select 1', 'alter table refresh_tokens add column provider_session_hash varchar(64) not null default '''''")
	assert.Contains(t, upStatements, "information_schema.statistics where table_schema = database() and table_name = 'refresh_tokens' and index_name = "+
		"'idx_refresh_tokens_provider_session_hash'), 'select 1', 'alter table refresh_tokens add index idx_refresh_tokens_provider_session_hash (provider_session_hash)'")
	assert.Equal(t, 2, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(upStatements, "add column"), "exactly one column")
	assert.Equal(t, 1, strings.Count(upStatements, "add index"), "exactly one index")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "references", "modify ", "unique"} {
		assert.NotContains(t, upStatements, forbidden, "the up half adds the column and the index and changes nothing else")
	}

	assert.Contains(t, downStatements, "index_name = 'idx_refresh_tokens_provider_session_hash'), "+
		"'alter table refresh_tokens drop index idx_refresh_tokens_provider_session_hash', 'select 1'")
	assert.Contains(t, downStatements, "column_name = 'provider_session_hash'), 'alter table refresh_tokens drop column provider_session_hash', 'select 1'")
	assert.Equal(t, 2, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(downStatements, " drop "), "the down half drops the index and the column only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "add column", "add index", "modify "} {
		assert.NotContains(t, downStatements, forbidden)
	}

	tag, ok := reflectField(models.RefreshToken{}, "ProviderSessionHash")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(64);not null;default:'';index:idx_refresh_tokens_provider_session_hash", tag)

	// Older than the newest migration (the newest-migration assertions moved
	// to TestFormTicketTitleFieldMigrationFiles, #804).
	version, err := strconv.Atoi(strings.SplitN(refreshTokenProviderSessionMigrationName, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, refreshTokenProviderSessionVersion, version)
	assert.Less(t, refreshTokenProviderSessionVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Equal(t, refreshTokenProviderSessionPreviousVersion+1, refreshTokenProviderSessionVersion)
}

// providerSessionRows renders every refresh token as text, ordered by id,
// with provider_session_hash when withColumn.
func providerSessionRows(t *testing.T, tdb *migrationTestDB, withColumn bool) []string {
	t.Helper()
	column := "''"
	if withColumn {
		column = "provider_session_hash"
	}
	rows, err := tdb.sql.Query(`SELECT id, user_id, token_hash, is_revoked, deleted_at IS NULL, family_id, sign_in_method,
		DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f'), ` + column + `
		FROM refresh_tokens ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, owner int64
		var hash, family, method, created, updated, provider string
		var revoked, live bool
		require.NoError(t, rows.Scan(&id, &owner, &hash, &revoked, &live, &family, &method, &created, &updated, &provider))
		out = append(out, fmt.Sprintf("%d owner=%d hash=%s revoked=%t live=%t family=%s method=%s created=%s updated=%s provider=%q",
			id, owner, hash, revoked, live, family, method, created, updated, provider))
	}
	require.NoError(t, rows.Err())
	return out
}

// seedProviderSessionRows writes one account and a live, a revoked and a
// soft-deleted refresh token of a password and a single sign-on session, as
// the previous release wrote them.
func seedProviderSessionRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	result, err := tdb.sql.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
		VALUES ('provider-session@example.com', 'x', 'P', 'S', 'sales', 1, NOW(3), NOW(3))`)
	require.NoError(t, err)
	owner, err := result.LastInsertId()
	require.NoError(t, err)
	for i, row := range []struct{ method, revoked, deleted string }{
		{"password", "0", "NULL"}, {"oidc", "0", "NULL"}, {"oidc", "1", "NULL"}, {"oidc", "0", "NOW(3)"},
	} {
		_, err := tdb.sql.Exec(fmt.Sprintf(`INSERT INTO refresh_tokens (created_at, updated_at, deleted_at, user_id, token_hash, expires_at,
			is_revoked, family_id, sign_in_method, session_started_at)
			VALUES ('2026-11-20 09:30:00.123', '2026-11-20 09:31:00.456', %s, ?, ?, NOW(3) + INTERVAL 1 DAY, %s, ?, ?, '2026-11-20 09:30:00.123')`,
			row.deleted, row.revoked), owner, fmt.Sprintf("provider-session-hash-%d", i), fmt.Sprintf("%032d", i), row.method)
		require.NoError(t, err)
	}
}

func TestRefreshTokenProviderSessionMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the column and the index as MigrateDatabase
	// builds them on top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "rtps_ref")
	require.NoError(t, reference.m.Migrate(refreshTokenProviderSessionPreviousVersion))
	before := snapshotTable(t, reference, refreshTokensTable)
	require.Empty(t, ownedColumns(before, refreshTokenProviderSessionColumns), "the previous head has no provider_session_hash")
	require.Empty(t, providerSessionIndexes(before))
	runMigrateDatabase(t, reference.dsn)
	afterAutoMigrate := snapshotTable(t, reference, refreshTokensTable)
	wantOwned := ownedColumns(afterAutoMigrate, refreshTokenProviderSessionColumns)
	require.Len(t, wantOwned, 1)
	assert.True(t, strings.HasPrefix(wantOwned[0], "provider_session_hash varchar(64) nullable=NO default="), wantOwned[0])
	wantIndexes := providerSessionIndexes(afterAutoMigrate)
	require.Equal(t, []string{refreshTokenProviderSessionIndex + " (provider_session_hash) non_unique=1"}, wantIndexes)
	assert.Equal(t, before, withoutProviderSession(afterAutoMigrate), "the migration owns only the column and the index")

	migrated := func(t *testing.T, tdb *migrationTestDB) tableSchema {
		t.Helper()
		got := snapshotTable(t, tdb, refreshTokensTable)
		assert.Equal(t, afterAutoMigrate, got, "the SQL migration and MigrateDatabase produce the same table")
		assert.Equal(t, wantOwned, ownedColumns(got, refreshTokenProviderSessionColumns))
		assert.Equal(t, wantIndexes, providerSessionIndexes(got))
		return got
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "rtps_fresh")
		require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), "the up applies on an empty database")
		migrated(t, tdb)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, refreshTokensTable), "rollback removes the column and the index only")
		require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionVersion))
		migrated(t, tdb)
		require.ErrorIs(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), migrate.ErrNoChange)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "rtps_mig")
			require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionPreviousVersion))
			seedProviderSessionRows(t, tdb)
			legacy := providerSessionRows(t, tdb, false)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				migrated(t, tdb)
			}

			require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(refreshTokenProviderSessionVersion), version)
			schema := migrated(t, tdb)
			after := providerSessionRows(t, tdb, true)
			assert.Equal(t, legacy, after, "every legacy row keeps '' and nothing else changes")

			// Applying the up half again changes nothing.
			require.NoError(t, tdb.m.Force(refreshTokenProviderSessionPreviousVersion))
			require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), "the up half runs again")
			assert.Equal(t, schema, snapshotTable(t, tdb, refreshTokensTable), "a second run changes no schema")
			assert.Equal(t, after, providerSessionRows(t, tdb, true), "a second run changes no row")

			// Rollback removes exactly the column and the index and keeps
			// every row; running it again changes nothing.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(refreshTokenProviderSessionPreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, refreshTokensTable), "rollback removes the column and the index only")
			assert.Equal(t, legacy, providerSessionRows(t, tdb, false), "rollback keeps every row")
			require.NoError(t, tdb.m.Force(refreshTokenProviderSessionVersion))
			require.NoError(t, tdb.m.Steps(-1), "the down half runs again")
			assert.Equal(t, before, snapshotTable(t, tdb, refreshTokensTable))

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), "reapply")
			migrated(t, tdb)
			assert.Equal(t, after, providerSessionRows(t, tdb, true))
			require.ErrorIs(t, tdb.m.Migrate(refreshTokenProviderSessionVersion), migrate.ErrNoChange)
		})
	}
}

// providerSessionIndexes keeps the index lines of a snapshot this migration
// owns.
func providerSessionIndexes(schema tableSchema) []string {
	out := []string{}
	for _, line := range schema.Indexes {
		if strings.HasPrefix(line, refreshTokenProviderSessionIndex+" ") {
			out = append(out, line)
		}
	}
	return out
}

// withoutProviderSession drops the column and the index this migration owns
// from a snapshot.
func withoutProviderSession(schema tableSchema) tableSchema {
	out := withoutColumns(schema, refreshTokenProviderSessionColumns)
	var indexes []string
	for _, line := range out.Indexes {
		if !strings.HasPrefix(line, refreshTokenProviderSessionIndex+" ") {
			indexes = append(indexes, line)
		}
	}
	out.Indexes = indexes
	return out
}
