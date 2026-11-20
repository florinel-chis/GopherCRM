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
)

// #466: 20261125120000_auth_audit_log_attempts adds auth_audit_log.attempts
// (bigint unsigned NOT NULL DEFAULT 1), aggregate_key (varchar(191) NULL) and
// the unique index idx_auth_audit_log_aggregate_key, each only when missing,
// so a refused API key adds to one row per reason, account, network and
// minute. No backfill. The down half drops the index and the two columns.

const (
	authAuditLogAttemptsVersion         = 20261125120000
	authAuditLogAttemptsPreviousVersion = authAuditLogVersion
	authAuditLogAttemptsMigrationName   = "20261125120000_auth_audit_log_attempts"
	authAuditLogAggregateIndex          = "idx_auth_audit_log_aggregate_key"
)

// authAuditLogAttemptsColumns are the columns the migration adds.
var authAuditLogAttemptsColumns = []string{"attempts", "aggregate_key"}

// TestAuthAuditLogAttemptsMigrationFiles needs no database: the pair exists
// with its headers, the up half adds exactly the two columns and the unique
// index, each behind an INFORMATION_SCHEMA probe, the down half drops only
// them, and the model declares the same. The newest-migration check moved to
// TestSCIMTokensMigrationFiles (#468).
func TestAuthAuditLogAttemptsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, authAuditLogAttemptsMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, authAuditLogAttemptsMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Sign-in audit log attempts"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove sign-in audit log attempts"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	statementsOf := func(sql string) string {
		var lines []string
		for _, line := range strings.Split(sql, "\n") {
			if !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	upStatements, downStatements := statementsOf(upSQL), statementsOf(downSQL)

	assert.Contains(t, upStatements, "table_name = 'auth_audit_log' and column_name = 'attempts'), 'select 1', 'alter table auth_audit_log add column attempts bigint unsigned not null default 1'",
		"attempts is added only when missing")
	assert.Contains(t, upStatements, "table_name = 'auth_audit_log' and column_name = 'aggregate_key'), 'select 1', 'alter table auth_audit_log add column aggregate_key varchar(191) null'",
		"aggregate_key is added only when missing")
	assert.Contains(t, upStatements, "information_schema.statistics where table_schema = database() and table_name = 'auth_audit_log' and index_name = 'idx_auth_audit_log_aggregate_key'), 'select 1', 'alter table auth_audit_log add unique key idx_auth_audit_log_aggregate_key (aggregate_key)'",
		"the unique index is added only when missing")
	assert.Equal(t, 3, strings.Count(upStatements, "execute stmt;"))
	assert.Equal(t, 2, strings.Count(upStatements, "add column"), "exactly two columns")
	assert.Equal(t, 1, strings.Count(upStatements, "add unique key"), "exactly one index")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "create table", "foreign key", "modify column"} {
		assert.NotContains(t, upStatements, forbidden, "the up half writes no row, drops and changes nothing")
	}

	assert.Contains(t, downStatements, "index_name = 'idx_auth_audit_log_aggregate_key'), 'alter table auth_audit_log drop index idx_auth_audit_log_aggregate_key', 'select 1'")
	assert.Contains(t, downStatements, "column_name = 'aggregate_key'), 'alter table auth_audit_log drop column aggregate_key', 'select 1'")
	assert.Contains(t, downStatements, "column_name = 'attempts'), 'alter table auth_audit_log drop column attempts', 'select 1'")
	assert.Equal(t, 3, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 3, strings.Count(downStatements, " drop "), "the down half drops the index and the two columns only")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop table", "add column", "modify column"} {
		assert.NotContains(t, downStatements, forbidden)
	}
	assert.Less(t, strings.Index(downStatements, "drop index"), strings.Index(downStatements, "drop column aggregate_key"), "the index goes before its column")

	// The model declares the same.
	tag, ok := reflectField(models.AuthAuditLog{}, "Attempts")
	require.True(t, ok)
	assert.Equal(t, "not null;default:1", tag)
	tag, ok = reflectField(models.AuthAuditLog{}, "AggregateKey")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(191);uniqueIndex:"+authAuditLogAggregateIndex, tag)
	assert.Equal(t, 191, models.AuthAuditAggregateKeyMaxLength)

	// The newest-migration assertion moved to TestSCIMTokensMigrationFiles
	// (#468).
	assert.Less(t, authAuditLogAttemptsVersion, latestMigrationVersion, "#468 added a later migration")
	assert.Greater(t, authAuditLogAttemptsVersion, authAuditLogAttemptsPreviousVersion)
}

// insertAuthAuditRow writes one row as #454 wrote it, without the new
// columns.
func insertAuthAuditRow(t *testing.T, tdb *migrationTestDB, requestID string) {
	t.Helper()
	_, err := tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family)
		VALUES (UTC_TIMESTAMP(3), 'api_key', 'refused', 'key_invalid', ?, '192.0.2.0/24', '')`, requestID)
	require.NoError(t, err)
}

// assertAuthAuditRowsKept checks that the rows written before the migration
// are all there and read attempts 1 and no aggregate key.
func assertAuthAuditRowsKept(t *testing.T, tdb *migrationTestDB, want int) {
	t.Helper()
	var count, ones, keyless int
	require.NoError(t, tdb.sql.QueryRow(`SELECT COUNT(*), COALESCE(SUM(attempts = 1), 0), COALESCE(SUM(aggregate_key IS NULL), 0) FROM auth_audit_log`).
		Scan(&count, &ones, &keyless))
	assert.Equal(t, want, count, "every row is kept")
	assert.Equal(t, want, ones, "existing rows read attempts 1")
	assert.Equal(t, want, keyless, "existing rows have no aggregate key")
}

func TestAuthAuditLogAttemptsMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as MigrateDatabase builds it on top of
	// the previous SQL head, and the table at that head.
	reference := openMigrationTestDB(t, baseDSN, "aaa_ref")
	require.NoError(t, reference.m.Migrate(authAuditLogAttemptsPreviousVersion))
	before := snapshotTable(t, reference, authAuditLogTable)
	require.Len(t, before.Columns, 13)
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, authAuditLogTable)
	require.Len(t, want.Columns, 15)
	assert.Equal(t, before, withoutAuthAuditAttempts(want), "the migration owns only the two columns and the index")
	assert.Contains(t, want.Indexes, authAuditLogAggregateIndex+" (aggregate_key) non_unique=0")
	owned := ownedColumns(want, authAuditLogAttemptsColumns)
	require.Len(t, owned, 2)
	for _, line := range owned {
		switch {
		case strings.HasPrefix(line, "attempts "):
			assert.Contains(t, line, "attempts bigint")
			assert.Contains(t, line, "unsigned nullable=NO default=1")
		case strings.HasPrefix(line, "aggregate_key "):
			assert.Contains(t, line, "aggregate_key varchar(191) nullable=YES")
		}
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "aaa_fresh")
		require.NoError(t, tdb.m.Migrate(authAuditLogAttemptsVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "a fresh SQL schema carries what MigrateDatabase builds")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, authAuditLogTable), "rollback removes the two columns and the index only")
		require.NoError(t, tdb.m.Migrate(authAuditLogAttemptsVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
		require.ErrorIs(t, tdb.m.Migrate(authAuditLogAttemptsVersion), migrate.ErrNoChange)

		// The unique key holds: a second row with the same key is refused,
		// any number of NULL keys are not.
		for i := 0; i < 2; i++ {
			insertAuthAuditRow(t, tdb, fmt.Sprintf("req-null-%d", i))
		}
		_, err := tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family, attempts, aggregate_key)
			VALUES (UTC_TIMESTAMP(3), 'api_key', 'refused', 'key_invalid', '', '', '', 1, 'k')`)
		require.NoError(t, err)
		_, err = tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family, attempts, aggregate_key)
			VALUES (UTC_TIMESTAMP(3), 'api_key', 'refused', 'key_invalid', '', '', '', 1, 'k')`)
		require.Error(t, err, "a duplicate aggregate key is refused")

		require.NoError(t, tdb.m.Down())
		_, _, err = tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "aaa_mig")
			require.NoError(t, tdb.m.Migrate(authAuditLogAttemptsPreviousVersion))
			insertAuthAuditRow(t, tdb, "req-old-1")
			insertAuthAuditRow(t, tdb, "req-old-2")
			rows := 2
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
			}

			require.NoError(t, tdb.m.Migrate(authAuditLogAttemptsVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogAttemptsVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "the SQL migration and MigrateDatabase produce the same table")
			assertAuthAuditRowsKept(t, tdb, rows)

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(authAuditLogAttemptsVersion), migrate.ErrNoChange)

			// Rollback keeps every row and removes only what #466 added.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogAttemptsPreviousVersion), version)
			assert.Equal(t, before, snapshotTable(t, tdb, authAuditLogTable), "rollback removes the two columns and the index only")
			var count int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM auth_audit_log").Scan(&count))
			assert.Equal(t, rows, count, "rollback keeps every row")

			// And it applies again from there.
			require.NoError(t, tdb.m.Migrate(authAuditLogAttemptsVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable))
			assertAuthAuditRowsKept(t, tdb, rows)
			require.ErrorIs(t, tdb.m.Migrate(authAuditLogAttemptsVersion), migrate.ErrNoChange)
		})
	}
}
