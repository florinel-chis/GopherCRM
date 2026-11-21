package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #532: 20261129120003_api_key_lifetime gives every API key without an
// expiry, or with one later than one year from now, the expiry NOW(3) + 1
// year, and revokes every key of a customer account that is not revoked yet.
// Two plain UPDATEs, no DDL. The down half changes nothing. The startup step
// models.capAPIKeyLifetimes does the same on every driver.

const (
	apiKeyLifetimeVersion         = 20261129120003
	apiKeyLifetimePreviousVersion = apiKeyRevokedAtVersion
	apiKeyLifetimeMigrationName   = "20261129120003_api_key_lifetime"
)

// TestAPIKeyLifetimeMigrationFiles needs no database: the pair exists with
// its headers, the up half is exactly the two UPDATEs and no DDL, the down
// half writes nothing, and a newer migration follows it.
func TestAPIKeyLifetimeMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, apiKeyLifetimeMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, apiKeyLifetimeMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: API key lifetime (#532)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Undo API key lifetime (#532)\n-- Date: "), "the down half has its header")
	for _, half := range []string{string(up), string(down)} {
		assert.Contains(t, half, "\n-- Description: ")
		assert.Contains(t, half, "MySQL 8 and MariaDB 10.11 accept this as written.")
	}
	assert.Contains(t, string(up), "NOW(3)", "the up half uses the database clock")

	statementsOf := func(sql string) []string {
		var lines []string
		for _, line := range strings.Split(strings.ToLower(sql), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
		return strings.Split(strings.TrimSuffix(strings.Join(lines, " "), ";"), ";")
	}
	upStatements := statementsOf(string(up))
	require.Len(t, upStatements, 2, "exactly two statements")
	assert.Equal(t, "update api_keys set expires_at = now(3) + interval 1 year where expires_at is null or expires_at > now(3) + interval 1 year",
		strings.TrimSpace(upStatements[0]))
	assert.Equal(t, "update api_keys set is_active = 0, revoked_at = now(3) where revoked_at is null and user_id in (select id from users where role = 'customer')",
		strings.TrimSpace(upStatements[1]))
	for _, forbidden := range []string{"alter", "create", "drop", "index", "delete", "insert", "updated_at"} {
		assert.NotContains(t, strings.Join(upStatements, ";"), forbidden, "the up half changes no schema and touches no updated_at")
	}
	downStatements := strings.Join(statementsOf(string(down)), ";")
	assert.Equal(t, "select 1", strings.TrimSpace(downStatements), "the down half changes nothing")
	for _, forbidden := range []string{"update", "delete", "alter"} {
		assert.NotContains(t, downStatements, forbidden)
	}

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestRefreshTokenSessionMigrationFiles (#546).
	assert.Less(t, apiKeyLifetimeVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Equal(t, apiKeyLifetimePreviousVersion+1, apiKeyLifetimeVersion)
}

// lifetimeFixture are the key ids the lifetime tests seed.
type lifetimeFixture struct {
	nullKey, farKey, nearKey, deletedKey, customerLive, customerRevoked int64
	nearExpiry                                                          string
}

const lifetimeOldRevokedAt = "2020-01-02 03:04:05.000"

// seedLifetimeRows writes a staff and a customer account and six keys as
// the previous release wrote them.
func seedLifetimeRows(t *testing.T, tdb *migrationTestDB) lifetimeFixture {
	t.Helper()
	user := func(email, role string) int64 {
		result, err := tdb.sql.Exec(`INSERT INTO users (email, password, first_name, last_name, role, is_active, created_at, updated_at)
			VALUES (?, 'x', 'K', 'L', ?, 1, NOW(3), NOW(3))`, email, role)
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		return id
	}
	staff := user("lifetime-staff@example.com", "sales")
	customer := user("lifetime-customer@example.com", "customer")
	key := func(label string, owner int64, expires, deleted, active, revoked string) int64 {
		result, err := tdb.sql.Exec(fmt.Sprintf(`INSERT INTO api_keys
			(created_at, updated_at, name, key_hash, prefix, user_id, expires_at, deleted_at, is_active, revoked_at)
			VALUES ('2024-05-06 07:08:09.000', '2024-05-06 07:08:09.000', ?, ?, 'abcd1234', ?, %s, %s, %s, %s)`,
			expires, deleted, active, revoked), label, "hash-"+label, owner)
		require.NoError(t, err)
		id, err := result.LastInsertId()
		require.NoError(t, err)
		return id
	}
	f := lifetimeFixture{
		nullKey:         key("null", staff, "NULL", "NULL", "1", "NULL"),
		farKey:          key("far", staff, "NOW(3) + INTERVAL 3 YEAR", "NULL", "1", "NULL"),
		nearKey:         key("near", staff, "NOW(3) + INTERVAL 30 DAY", "NULL", "1", "NULL"),
		deletedKey:      key("deleted", staff, "NULL", "NOW(3)", "1", "NULL"),
		customerLive:    key("customer-live", customer, "NOW(3) + INTERVAL 30 DAY", "NULL", "1", "NULL"),
		customerRevoked: key("customer-revoked", customer, "NOW(3) + INTERVAL 30 DAY", "NULL", "0", "'"+lifetimeOldRevokedAt+"'"),
	}
	f.nearExpiry = lifetimeColumn(t, tdb, f.nearKey, "expires_at")
	return f
}

// lifetimeColumn reads one datetime column of a key as text ("" for NULL).
func lifetimeColumn(t *testing.T, tdb *migrationTestDB, id int64, column string) string {
	t.Helper()
	var value string
	require.NoError(t, tdb.sql.QueryRow(fmt.Sprintf(
		"SELECT COALESCE(DATE_FORMAT(%s, '%%Y-%%m-%%d %%H:%%i:%%s.%%f'), '') FROM api_keys WHERE id = ?", column), id).Scan(&value))
	return value
}

// lifetimeRows is every column the migration may write, per key, as text.
func lifetimeRows(t *testing.T, tdb *migrationTestDB) []string {
	t.Helper()
	rows, err := tdb.sql.Query(`SELECT id, COALESCE(DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%s.%f'), 'NULL'), is_active,
		COALESCE(DATE_FORMAT(revoked_at, '%Y-%m-%d %H:%i:%s.%f'), 'NULL'), DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f')
		FROM api_keys ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var expires, revoked, updated string
		var active bool
		require.NoError(t, rows.Scan(&id, &expires, &active, &revoked, &updated))
		out = append(out, fmt.Sprintf("%d expires=%s active=%t revoked=%s updated=%s", id, expires, active, revoked, updated))
	}
	require.NoError(t, rows.Err())
	return out
}

// lifetimeBounds is the window a capped expiry must fall in, as text.
type lifetimeBounds struct{ lo, hi string }

// databaseCap reads NOW(3) + 1 year from the database's own clock.
func databaseCap(t *testing.T, tdb *migrationTestDB) string {
	t.Helper()
	var value string
	require.NoError(t, tdb.sql.QueryRow("SELECT DATE_FORMAT(NOW(3) + INTERVAL 1 YEAR, '%Y-%m-%d %H:%i:%s.%f')").Scan(&value))
	return value
}

// applicationCap is models.APIKeyExpiryCap(now) in the zone the connection
// writes times in (the DSN's loc), as the startup step stores it.
func applicationCap(t *testing.T, tdb *migrationTestDB) string {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(tdb.dsn)
	require.NoError(t, err)
	return models.APIKeyExpiryCap(time.Now()).In(cfg.Loc).Format("2006-01-02 15:04:05.000000")
}

func minString(a, b string) string {
	if a < b {
		return a
	}
	return b
}

func maxString(a, b string) string {
	if a > b {
		return a
	}
	return b
}

// assertLifetimeApplied checks the fixture after the data step: the NULL,
// far and soft-deleted keys are capped inside bounds (one second of slack
// for the rounding to milliseconds), the 30-day key is unchanged, the live
// customer key is revoked and the revoked one keeps its revoked_at, and every
// staff key stays active.
func assertLifetimeApplied(t *testing.T, tdb *migrationTestDB, f lifetimeFixture, b lifetimeBounds) {
	t.Helper()
	for name, id := range map[string]int64{"null": f.nullKey, "far": f.farKey, "deleted": f.deletedKey} {
		var inside bool
		require.NoError(t, tdb.sql.QueryRow(`SELECT expires_at >= ? - INTERVAL 1 SECOND AND expires_at <= ? + INTERVAL 1 SECOND
			FROM api_keys WHERE id = ?`, b.lo, b.hi, id).Scan(&inside))
		assert.True(t, inside, "the %s key is capped at one year: %s not in [%s, %s]", name,
			lifetimeColumn(t, tdb, id, "expires_at"), b.lo, b.hi)
	}
	assert.Equal(t, f.nearExpiry, lifetimeColumn(t, tdb, f.nearKey, "expires_at"), "an earlier expiry is unchanged")

	var active bool
	require.NoError(t, tdb.sql.QueryRow("SELECT is_active FROM api_keys WHERE id = ?", f.customerLive).Scan(&active))
	assert.False(t, active, "the live customer key is revoked")
	assert.NotEmpty(t, lifetimeColumn(t, tdb, f.customerLive, "revoked_at"), "and stamped")
	assert.Equal(t, lifetimeOldRevokedAt+"000", lifetimeColumn(t, tdb, f.customerRevoked, "revoked_at"), "an earlier revoked_at is kept")

	for _, id := range []int64{f.nullKey, f.farKey, f.nearKey, f.deletedKey} {
		require.NoError(t, tdb.sql.QueryRow("SELECT is_active FROM api_keys WHERE id = ?", id).Scan(&active))
		assert.True(t, active, "staff key %d stays active", id)
		assert.Empty(t, lifetimeColumn(t, tdb, id, "revoked_at"))
	}
	for _, id := range []int64{f.nullKey, f.farKey, f.nearKey, f.deletedKey, f.customerLive, f.customerRevoked} {
		assert.Equal(t, "2024-05-06 07:08:09.000000", lifetimeColumn(t, tdb, id, "updated_at"), "updated_at is not touched")
	}
}

func TestAPIKeyLifetimeMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "aklt_mig")
			require.NoError(t, tdb.m.Migrate(apiKeyLifetimePreviousVersion))
			f := seedLifetimeRows(t, tdb)
			schema := snapshotTable(t, tdb, apiKeysTable)

			bounds := lifetimeBounds{lo: databaseCap(t, tdb)}
			appLo := applicationCap(t, tdb)
			if autoMigrateFirst {
				// A new binary started before the operator migrated: the
				// startup step caps with the application's clock first.
				runMigrateDatabase(t, tdb.dsn)
				schema = snapshotTable(t, tdb, apiKeysTable)
			}
			require.NoError(t, tdb.m.Migrate(apiKeyLifetimeVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			bounds.hi = databaseCap(t, tdb)
			if autoMigrateFirst {
				// The values come from either clock; on a server in the
				// application's zone the two agree.
				bounds.lo = minString(bounds.lo, appLo)
				bounds.hi = maxString(bounds.hi, applicationCap(t, tdb))
			}
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(apiKeyLifetimeVersion), version)

			assert.Equal(t, schema, snapshotTable(t, tdb, apiKeysTable), "the api_keys schema is unchanged")
			assertLifetimeApplied(t, tdb, f, bounds)
			after := lifetimeRows(t, tdb)

			// Rollback keeps the values.
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(apiKeyLifetimePreviousVersion), version)
			assert.Equal(t, after, lifetimeRows(t, tdb), "the down half keeps the values")
			assert.Equal(t, schema, snapshotTable(t, tdb, apiKeysTable))

			// Re-up changes nothing, and neither does another boot.
			require.NoError(t, tdb.m.Migrate(apiKeyLifetimeVersion), "reapply")
			assert.Equal(t, after, lifetimeRows(t, tdb), "applying again changes nothing")
			require.ErrorIs(t, tdb.m.Migrate(apiKeyLifetimeVersion), migrate.ErrNoChange)
			if autoMigrateFirst {
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, after, lifetimeRows(t, tdb), "the startup step changes nothing afterwards")
			}
		})
	}
}
