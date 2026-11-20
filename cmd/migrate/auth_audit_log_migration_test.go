package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #454: 20261124120000_add_auth_audit_log creates auth_audit_log (one
// pseudonymous row per sign-in outcome, no foreign key, no soft delete) only
// when missing, with the columns and index names AutoMigrate produces;
// rollback drops that table and nothing else.

const (
	authAuditLogVersion         = 20261124120000
	authAuditLogPreviousVersion = userIdentitiesVersion
	authAuditLogMigrationName   = "20261124120000_add_auth_audit_log"
	authAuditLogTable           = "auth_audit_log"
)

// TestAuthAuditLogMigrationFiles needs no database: the pair exists with its
// headers, creates the table only when missing with the model's columns and
// index names, has no foreign key and alters nothing, and drops only that
// table. The newest-migration check moved to
// TestAuthAuditLogAttemptsMigrationFiles (#466).
func TestAuthAuditLogMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, authAuditLogMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, authAuditLogMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Sign-in audit log"), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Remove sign-in audit log"), "the down half has its header")
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, fragment := range []string{
		"create table if not exists auth_audit_log (",
		"id bigint unsigned not null auto_increment",
		"created_at datetime(3) not null",
		"method varchar(16) not null",
		"outcome varchar(16) not null",
		"reason varchar(64) not null",
		"user_id bigint unsigned null",
		"attempt_id bigint unsigned null",
		"request_id varchar(64) not null",
		"subject_ref varchar(64) null",
		"claim_roles text null",
		"claim_groups text null",
		"client_network varchar(64) not null",
		"user_agent_family varchar(64) not null",
		"key idx_auth_audit_log_created_at (created_at)",
		"key idx_auth_audit_log_user_id (user_id)",
		"key idx_auth_audit_log_subject_ref (subject_ref)",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		assert.Containsf(t, upSQL, fragment, "up declares %s", fragment)
	}
	var statements []string
	for _, line := range strings.Split(upSQL, "\n") {
		if !strings.HasPrefix(line, "--") {
			statements = append(statements, line)
		}
	}
	upStatements := strings.Join(statements, "\n")
	assert.NotContains(t, upStatements, "foreign key", "no foreign key")
	assert.NotContains(t, upStatements, "references", "no foreign key")
	assert.NotContains(t, upStatements, "deleted_at", "no soft delete")
	assert.NotContains(t, upStatements, "alter table", "no existing table is altered")
	assert.Equal(t, 1, strings.Count(upStatements, "create table"), "one table")
	assert.Contains(t, downSQL, "drop table if exists auth_audit_log;")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops the one table only")

	// The model declares the same widths and index names, and no default.
	for field, want := range map[string]string{
		"CreatedAt":       "not null;index:idx_auth_audit_log_created_at",
		"Method":          "type:varchar(16);not null",
		"Outcome":         "type:varchar(16);not null",
		"Reason":          "type:varchar(64);not null",
		"UserID":          "index:idx_auth_audit_log_user_id",
		"RequestID":       "type:varchar(64);not null",
		"SubjectRef":      "type:varchar(64);index:idx_auth_audit_log_subject_ref",
		"ClaimRoles":      "type:text",
		"ClaimGroups":     "type:text",
		"ClientNetwork":   "type:varchar(64);not null",
		"UserAgentFamily": "type:varchar(64);not null",
	} {
		tag, ok := reflectField(models.AuthAuditLog{}, field)
		require.Truef(t, ok, "AuthAuditLog.%s exists", field)
		assert.Containsf(t, tag, want, "AuthAuditLog.%s", field)
		assert.NotContainsf(t, tag, "default:", "AuthAuditLog.%s has no gorm default", field)
	}
	assert.Equal(t, authAuditLogTable, models.AuthAuditLog{}.TableName())

	assert.Greater(t, authAuditLogVersion, authAuditLogPreviousVersion)
	assert.Greater(t, latestMigrationVersion, authAuditLogVersion, "#466 added a later migration")

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+authAuditLogTable+`"`, "main_test.go's expected-table list names %s", authAuditLogTable)
}

func TestAuthAuditLogMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })
	tables := []string{authAuditLogTable}

	// The parity reference: the table as MigrateDatabase creates it on top of
	// the previous SQL head (wantModel), and the same without what #466 added
	// (want), which is what this migration creates at its version.
	reference := openMigrationTestDB(t, baseDSN, "aal_ref")
	require.NoError(t, reference.m.Migrate(authAuditLogPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), authAuditLogTable)
	runMigrateDatabase(t, reference.dsn)
	wantModel := snapshotTable(t, reference, authAuditLogTable)
	want := withoutAuthAuditAttempts(wantModel)
	assert.Contains(t, want.Indexes, "idx_auth_audit_log_created_at (created_at) non_unique=1")
	assert.Contains(t, want.Indexes, "idx_auth_audit_log_user_id (user_id) non_unique=1")
	assert.Contains(t, want.Indexes, "idx_auth_audit_log_subject_ref (subject_ref) non_unique=1")
	require.Len(t, want.Indexes, 4, "the primary key and the three indexes")
	assert.Empty(t, want.ForeignKeys)
	require.Len(t, want.Columns, 13)
	require.Len(t, wantModel.Columns, 15)

	assertShape := func(t *testing.T, tdb *migrationTestDB) {
		t.Helper()
		assert.Equal(t, want, snapshotTable(t, tdb, authAuditLogTable), "auth_audit_log as this migration creates it")
	}
	assertModelShape := func(t *testing.T, tdb *migrationTestDB) {
		t.Helper()
		assert.Equal(t, wantModel, snapshotTable(t, tdb, authAuditLogTable), "auth_audit_log as MigrateDatabase creates it")
	}

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "aal_fresh")
		require.NoError(t, tdb.m.Migrate(authAuditLogVersion))
		assertShape(t, tdb)
		_, err := tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family)
			VALUES (UTC_TIMESTAMP(3), 'password', 'refused', 'invalid_credentials', '', '', '')`)
		require.NoError(t, err)

		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
			"rollback drops the table and nothing else")
		require.NoError(t, tdb.m.Migrate(authAuditLogVersion))
		assertShape(t, tdb)

		require.NoError(t, tdb.m.Down())
		_, _, err = tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		name := "Upgrade/AutoMigrateFirst=false"
		if autoMigrateFirst {
			name = "Upgrade/AutoMigrateFirst=true"
		}
		t.Run(name, func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "aal_mig")
			require.NoError(t, tdb.m.Migrate(authAuditLogPreviousVersion))
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				_, err := tdb.sql.Exec(`INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family)
					VALUES (UTC_TIMESTAMP(3), 'refresh', 'success', '', 'req-1', '192.0.2.0/24', 'Chrome on Windows')`)
				require.NoError(t, err)
			}
			require.NoError(t, tdb.m.Migrate(authAuditLogVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogVersion), version)
			if autoMigrateFirst {
				// The table AutoMigrate created is kept as it is.
				assertModelShape(t, tdb)
			} else {
				assertShape(t, tdb)
			}
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM auth_audit_log").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}

			runMigrateDatabase(t, tdb.dsn)
			assertModelShape(t, tdb)
			require.ErrorIs(t, tdb.m.Migrate(authAuditLogVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(authAuditLogPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, tables), listTables(t, tdb.sql, tdb.name),
				"rollback drops the table and nothing else")

			require.NoError(t, tdb.m.Migrate(authAuditLogVersion))
			assertShape(t, tdb)
		})
	}
}

// withoutAuthAuditAttempts drops what #466 added to auth_audit_log from a
// snapshot: the attempts and aggregate_key columns and their unique index.
func withoutAuthAuditAttempts(schema tableSchema) tableSchema {
	out := withoutColumns(schema, authAuditLogAttemptsColumns)
	out.Indexes = nil
	for _, index := range schema.Indexes {
		if !strings.HasPrefix(index, authAuditLogAggregateIndex+" ") {
			out.Indexes = append(out.Indexes, index)
		}
	}
	return out
}
