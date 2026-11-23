package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #515: 20261129120007_widen_webhook_event_types widens
// webhook_endpoints.event_types from varchar(255) to varchar(512) NOT NULL,
// so an endpoint can subscribe to all twelve event types once deals joined
// the catalogue. The down half narrows it back only while no row is longer
// than 255 bytes; otherwise it fails, under any sql_mode, and truncates
// nothing.

const (
	webhookEventTypesWidthVersion         = 20261129120007
	webhookEventTypesWidthPreviousVersion = authAuditLogAPIKeyIDVersion
	webhookEventTypesWidthMigrationName   = "20261129120007_widen_webhook_event_types"
)

// The event_types line of a webhook_endpoints snapshot before and after the
// widening, on MySQL 8 and MariaDB 10.11 alike.
const (
	eventTypesColumnBeforeWidening = "event_types varchar(255) nullable=NO default=<none> "
	eventTypesColumnAfterWidening  = "event_types varchar(512) nullable=NO default=<none> "
)

// withEventTypesBeforeWidening maps the event_types column of a
// webhook_endpoints snapshot to its width before #515, so the roots of the
// earlier webhook_endpoints migrations, which compare against what
// MigrateDatabase builds today, check only what they own. The width itself is
// compared by TestWebhookEventTypesWidthMigration.
func withEventTypesBeforeWidening(schema tableSchema) tableSchema {
	out := tableSchema{Indexes: schema.Indexes, ForeignKeys: schema.ForeignKeys}
	for _, line := range schema.Columns {
		if line == eventTypesColumnAfterWidening {
			line = eventTypesColumnBeforeWidening
		}
		out.Columns = append(out.Columns, line)
	}
	return out
}

// snapshotEndpointsBeforeWidening is snapshotTable of webhook_endpoints
// through withEventTypesBeforeWidening.
func snapshotEndpointsBeforeWidening(t *testing.T, tdb *migrationTestDB) tableSchema {
	t.Helper()
	return withEventTypesBeforeWidening(snapshotTable(t, tdb, webhookEndpointsTable))
}

// TestWebhookEventTypesWidthMigrationFiles needs no database: the pair exists
// with its headers, the up half is the one MODIFY, the down half the guarded
// MODIFY back and the model declares varchar(512); a newer migration follows
// it.
func TestWebhookEventTypesWidthMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, webhookEventTypesWidthMigrationName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, webhookEventTypesWidthMigrationName+".down.sql"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(up), "-- Migration: Widen webhook endpoint event types (#515)\n-- Date: "), "the up half has its header")
	assert.True(t, strings.HasPrefix(string(down), "-- Migration: Narrow webhook endpoint event types (#515)\n-- Date: "), "the down half has its header")
	for _, half := range []string{string(up), string(down)} {
		assert.Contains(t, half, "\n-- Description: ")
		assert.Contains(t, half, "MySQL 8 and MariaDB 10.11 accept this as written.")
	}
	assert.Contains(t, string(down), "longer than 255 bytes", "the description names what blocks the rollback")
	assert.Contains(t, string(down), "Nothing is truncated")

	statementsOf := func(sql string) string {
		var lines []string
		for _, line := range strings.Split(strings.ToLower(sql), "\n") {
			if !strings.HasPrefix(line, "--") && strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	upStatements, downStatements := statementsOf(string(up)), statementsOf(string(down))

	assert.Equal(t, "alter table webhook_endpoints modify event_types varchar(512) not null;", upStatements, "the up half is the one MODIFY")

	assert.Contains(t, downStatements, "exists(select 1 from webhook_endpoints where length(event_types) > 255), "+
		"'select webhook_event_types_longer_than_255_bytes_block_this_rollback from webhook_endpoints', "+
		"'alter table webhook_endpoints modify event_types varchar(255) not null'",
		"a row longer than 255 bytes selects an unknown column instead of narrowing")
	assert.Equal(t, 1, strings.Count(downStatements, "execute stmt;"))
	assert.Equal(t, 1, strings.Count(downStatements, "modify "), "one MODIFY")
	for _, forbidden := range []string{"update ", "insert ", "delete ", "drop ", "add column", "create ", "substring", "left("} {
		assert.NotContains(t, downStatements, forbidden, "the down half writes and truncates no row")
	}

	tag, ok := reflectField(models.WebhookEndpoint{}, "EventTypes")
	require.True(t, ok)
	assert.Equal(t, "type:varchar(512);not null", tag)

	// The newest-migration and latestMigrationVersion assertions moved to
	// TestRefreshTokenProviderSessionMigrationFiles (#634).
	assert.Less(t, webhookEventTypesWidthVersion, latestMigrationVersion, "a newer migration follows it")
	assert.Equal(t, webhookEventTypesWidthPreviousVersion+1, webhookEventTypesWidthVersion)
}

// widthEndpointTypes is the populated row's subscription.
const widthEndpointTypes = "lead.created,ticket.created"

// insertWidthEndpointRow writes one endpoint and returns its id.
func insertWidthEndpointRow(t *testing.T, db *sql.DB, url, eventTypes string) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO webhook_endpoints (created_at, updated_at, url, event_types, secret_sealed, is_active,
		previous_secret_sealed, previous_secret_expires_at, consecutive_failures, paused_until)
		VALUES ('2026-11-20 09:30:00.123', '2026-11-20 09:30:00.123', ?, ?, 'enc:v1:kept-sealed', 1, 'enc:v1:kept-previous',
		'2026-11-21 09:30:00.456', 2, '2026-11-20 09:31:00.789')`, url, eventTypes)
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

// widthEndpointRow reads every column of the row as text.
func widthEndpointRow(t *testing.T, db *sql.DB, id int64) []string {
	t.Helper()
	var created, updated, url, types, sealed, active, previous, previousExpires, failures, paused string
	require.NoError(t, db.QueryRow(`SELECT CAST(created_at AS CHAR), CAST(updated_at AS CHAR), url, event_types, secret_sealed,
		CAST(is_active AS CHAR), previous_secret_sealed, CAST(previous_secret_expires_at AS CHAR), CAST(consecutive_failures AS CHAR),
		CAST(paused_until AS CHAR) FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&created, &updated, &url, &types, &sealed, &active, &previous, &previousExpires, &failures, &paused))
	return []string{created, updated, url, types, sealed, active, previous, previousExpires, failures, paused}
}

// openMigrateInstance opens a second migrate instance on dsn's database.
func openMigrateInstance(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	driver, err := mysql.WithInstance(db, &mysql.Config{})
	require.NoError(t, err)
	path, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://"+path, "mysql", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func TestWebhookEventTypesWidthMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table at the previous SQL head (before), and
	// as MigrateDatabase builds it on top of that head (want).
	reference := openMigrationTestDB(t, baseDSN, "wetw_ref")
	require.NoError(t, reference.m.Migrate(webhookEventTypesWidthPreviousVersion))
	before := snapshotTable(t, reference, webhookEndpointsTable)
	require.Contains(t, before.Columns, eventTypesColumnBeforeWidening, "the previous head has varchar(255)")
	runMigrateDatabase(t, reference.dsn)
	want := snapshotTable(t, reference, webhookEndpointsTable)
	require.Contains(t, want.Columns, eventTypesColumnAfterWidening, "MigrateDatabase builds varchar(512)")
	assert.Equal(t, before, withEventTypesBeforeWidening(want), "the migration owns only the width of event_types")

	columnOf := func(tdb *migrationTestDB) (string, string) {
		t.Helper()
		var typ, collation string
		require.NoError(t, tdb.sql.QueryRow(`SELECT COLUMN_TYPE, COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'event_types'`, tdb.name).Scan(&typ, &collation))
		return strings.ToLower(typ), collation
	}
	_, wantCollation := columnOf(reference)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wetw_fresh")
		require.NoError(t, tdb.m.Migrate(webhookEventTypesWidthVersion), "the up applies on an empty database")
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "a fresh SQL schema carries what MigrateDatabase builds")
		_, collation := columnOf(tdb)
		assert.Equal(t, wantCollation, collation, "the column keeps the collation MigrateDatabase gives it")

		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable), "rollback narrows event_types only")
		require.NoError(t, tdb.m.Migrate(webhookEventTypesWidthVersion))
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
		require.ErrorIs(t, tdb.m.Migrate(webhookEventTypesWidthVersion), migrate.ErrNoChange)

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "wetw_mig")
			require.NoError(t, tdb.m.Migrate(webhookEventTypesWidthPreviousVersion))
			const url = "https://example.com/populated"
			id := insertWidthEndpointRow(t, tdb.sql, url, widthEndpointTypes)
			kept := widthEndpointRow(t, tdb.sql, id)
			if autoMigrateFirst {
				// A new binary started before the operator migrated.
				runMigrateDatabase(t, tdb.dsn)
				assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
			}

			require.NoError(t, tdb.m.Migrate(webhookEventTypesWidthVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(webhookEventTypesWidthVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "the SQL migration and MigrateDatabase produce the same table")
			assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "the row is byte for byte the same")

			runMigrateDatabase(t, tdb.dsn)
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable), "MigrateDatabase does not change the table")
			require.ErrorIs(t, tdb.m.Migrate(webhookEventTypesWidthVersion), migrate.ErrNoChange)

			require.NoError(t, tdb.m.Steps(-1))
			assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable), "rollback narrows event_types only")
			assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "rollback keeps the row")

			require.NoError(t, tdb.m.Migrate(webhookEventTypesWidthVersion), "reapply")
			assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
			assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id))
		})
	}

	// #726: the release run that timed out here (45154161780, attempt 1,
	// Migrations (MySQL 8)) did not hang in this subtest. The 10-minute
	// package alarm fired 3 s into it, while lenient.Force was taking the
	// migration lock: the lock goroutine was runnable, writing its GET_LOCK
	// packet, and no goroutine had waited a minute. The 45 roots that ran
	// before it took 565 s there and 239 s on the passing attempt 2 (2.4x
	// slower), so the package ran out of its -timeout budget on a slow
	// runner. Locally the subtest passed five runs in a row on MySQL 8 and on
	// MariaDB 10.11 and never blocked. The waits this subtest could still meet are
	// bounded anyway: a metadata lock on webhook_endpoints by the session's
	// lock_wait_timeout (migrateLockWaitSeconds, error 1205 instead of a
	// year), the migration lock by GET_LOCK's 10 s inside golang-migrate's
	// 15 s LockTimeout (ErrLocked), and an in-flight statement or a Close
	// behind one by stepDeadline, which fails the test naming the step and
	// kills the test database's sessions. The lenient instance is closed
	// before the strict one continues, so one migrate session drives the
	// database from then on.
	t.Run("a row longer than 255 bytes blocks the down migration", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wetw_long")
		guard := newStepDeadline(t, baseDSN, tdb.name)
		strict := openMigrateInstance(t, boundedMigrateDSN(t, tdb.dsn, nil))
		require.NoError(t, guard.must(t, "strict up", func() error { return strict.Migrate(webhookEventTypesWidthVersion) }))
		all := "customer.created,customer.deleted,customer.updated,deal.created,deal.deleted,deal.updated," +
			"lead.created,lead.deleted,lead.updated,ticket.created,ticket.deleted,ticket.updated"
		require.Len(t, all, 173)
		long := all + "," + strings.Repeat("z", 300-len(all)-1)
		require.Len(t, long, 300)
		id := insertWidthEndpointRow(t, tdb.sql, "https://example.com/long", long)
		kept := widthEndpointRow(t, tdb.sql, id)
		assertBlocked := func(t *testing.T, err error) {
			t.Helper()
			require.Error(t, err, "the down migration must refuse")
			assert.Contains(t, err.Error(), "webhook_event_types_longer_than_255_bytes_block_this_rollback", "the error names the reason")
			assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "the row is unchanged")
			typ, _ := columnOf(tdb)
			assert.Equal(t, "varchar(512)", typ, "the column stays varchar(512)")
		}

		assertBlocked(t, guard.must(t, "strict down with a long row", func() error { return strict.Steps(-1) }))

		// The same under a session without strict mode, where a plain MODIFY
		// would truncate the value with a warning.
		lenient := openMigrateInstance(t, boundedMigrateDSN(t, tdb.dsn, map[string]string{"sql_mode": "''"}))
		require.NoError(t, guard.must(t, "lenient force", func() error { return lenient.Force(webhookEventTypesWidthVersion) }))
		assertBlocked(t, guard.must(t, "lenient down with a long row", func() error { return lenient.Steps(-1) }))
		require.NoError(t, guard.must(t, "lenient close", func() error { return closeMigrateInstance(lenient) }), "both halves of the lenient instance close")

		// Once the subscription is shortened the down migration runs and
		// keeps the row, and the up applies again.
		require.NoError(t, guard.must(t, "strict force", func() error { return strict.Force(webhookEventTypesWidthVersion) }))
		_, err := tdb.sql.Exec("UPDATE webhook_endpoints SET event_types = ? WHERE id = ?", widthEndpointTypes, id)
		require.NoError(t, err)
		shortened := widthEndpointRow(t, tdb.sql, id)
		require.NoError(t, guard.must(t, "strict down with the row shortened", func() error { return strict.Steps(-1) }))
		version, dirty := guardedVersion(t, guard, strict)
		assert.False(t, dirty)
		assert.Equal(t, uint(webhookEventTypesWidthPreviousVersion), version)
		assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable))
		assert.Equal(t, shortened, widthEndpointRow(t, tdb.sql, id), "the down migration keeps the row")
		require.NoError(t, guard.must(t, "strict up again", func() error { return strict.Migrate(webhookEventTypesWidthVersion) }), "the up applies again")
		assert.Equal(t, want, snapshotTable(t, tdb, webhookEndpointsTable))
		require.NoError(t, guard.must(t, "strict close", func() error { return closeMigrateInstance(strict) }))
	})

	t.Run("a held migration lock fails the down migration within seconds", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wetw_lock")
		guard := newStepDeadline(t, baseDSN, tdb.name)
		m := openMigrateInstance(t, boundedMigrateDSN(t, tdb.dsn, nil))
		require.NoError(t, guard.must(t, "up", func() error { return m.Migrate(webhookEventTypesWidthVersion) }))
		id := insertWidthEndpointRow(t, tdb.sql, "https://example.com/locked", widthEndpointTypes)
		kept := widthEndpointRow(t, tdb.sql, id)

		// Another session takes the advisory lock under the id Mysql.Lock
		// derives from "<database>:<migrations table>".
		ctx := context.Background()
		holder, err := tdb.sql.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = holder.Close() })
		lockID, err := database.GenerateAdvisoryLockId(fmt.Sprintf("%s:%s", tdb.name, mysql.DefaultMigrationsTable))
		require.NoError(t, err)
		var got sql.NullInt64
		require.NoError(t, holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockID).Scan(&got))
		require.True(t, got.Valid && got.Int64 == 1, "the holder takes the migration lock")

		start := time.Now()
		err = guard.must(t, "down while the migration lock is held", func() error { return m.Steps(-1) })
		elapsed := time.Since(start)
		require.Error(t, err, "the down migration must not run while another session holds the lock")
		assert.True(t, errors.Is(err, database.ErrLocked) || errors.Is(err, migrate.ErrLockTimeout), "a lock error, got: %v", err)
		assert.Less(t, elapsed, 20*time.Second, "the lock wait is bounded")
		version, dirty := guardedVersion(t, guard, m)
		assert.False(t, dirty)
		assert.Equal(t, uint(webhookEventTypesWidthVersion), version, "the version is unchanged")
		assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "the row is unchanged")
		typ, _ := columnOf(tdb)
		assert.Equal(t, "varchar(512)", typ, "the column stays varchar(512)")

		require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockID).Scan(&got))
		require.True(t, got.Valid && got.Int64 == 1, "the holder releases the migration lock")
		require.NoError(t, guard.must(t, "down after the lock is released", func() error { return m.Steps(-1) }))
		version, dirty = guardedVersion(t, guard, m)
		assert.False(t, dirty)
		assert.Equal(t, uint(webhookEventTypesWidthPreviousVersion), version)
		assert.Equal(t, before, snapshotTable(t, tdb, webhookEndpointsTable))
		assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "the down migration keeps the row")
		require.NoError(t, guard.must(t, "close", func() error { return closeMigrateInstance(m) }))
	})

	t.Run("a held metadata lock fails the narrowing within seconds", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "wetw_mdl")
		guard := newStepDeadline(t, baseDSN, tdb.name)
		m := openMigrateInstance(t, boundedMigrateDSN(t, tdb.dsn, nil))
		require.NoError(t, guard.must(t, "up", func() error { return m.Migrate(webhookEventTypesWidthVersion) }))
		id := insertWidthEndpointRow(t, tdb.sql, "https://example.com/mdl", widthEndpointTypes)
		kept := widthEndpointRow(t, tdb.sql, id)

		// An open transaction that read webhook_endpoints holds a shared
		// metadata lock until it ends; the ALTER of the down half needs an
		// exclusive one. The holder's session keeps the server's own
		// lock_wait_timeout, so only the migrate session times out.
		ctx := context.Background()
		holder, err := tdb.sql.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = holder.Close() })
		tx, err := holder.BeginTx(ctx, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback() })
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM webhook_endpoints").Scan(&count))
		require.Equal(t, 1, count)

		start := time.Now()
		err = guard.must(t, "narrowing while a transaction holds webhook_endpoints", func() error { return m.Steps(-1) })
		elapsed := time.Since(start)
		require.Error(t, err, "the narrowing must not wait for the holder")
		assert.Less(t, elapsed, 20*time.Second, "the metadata lock wait is bounded")
		// database.Error has no Unwrap: reach the driver error through OrigErr.
		var dbErr database.Error
		require.ErrorAs(t, err, &dbErr)
		var myErr *mysqldriver.MySQLError
		require.ErrorAs(t, dbErr.OrigErr, &myErr)
		assert.Equal(t, uint16(1205), myErr.Number, "lock wait timeout exceeded")
		typ, _ := columnOf(tdb)
		assert.Equal(t, "varchar(512)", typ, "the column stays varchar(512)")

		require.NoError(t, tx.Rollback())
		require.NoError(t, guard.must(t, "force after the holder rolled back", func() error { return m.Force(webhookEventTypesWidthVersion) }))
		require.NoError(t, guard.must(t, "down after the holder rolled back", func() error { return m.Steps(-1) }))
		typ, _ = columnOf(tdb)
		assert.Equal(t, "varchar(255)", typ, "the column is narrowed")
		version, dirty := guardedVersion(t, guard, m)
		assert.False(t, dirty)
		assert.Equal(t, uint(webhookEventTypesWidthPreviousVersion), version)
		assert.Equal(t, kept, widthEndpointRow(t, tdb.sql, id), "the down migration keeps the row")
		require.NoError(t, guard.must(t, "close", func() error { return closeMigrateInstance(m) }))
	})

	t.Run("a blocked step fails at its deadline", func(t *testing.T) {
		victim := openMigrationTestDB(t, baseDSN, "wetw_kill")
		bystander := openMigrationTestDB(t, baseDSN, "wetw_keep")
		ctx := context.Background()
		sessionOn := func(t *testing.T, db *sql.DB) (*sql.Conn, int64) {
			t.Helper()
			conn, err := db.Conn(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			var id int64
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id))
			return conn, id
		}
		_, idleID := sessionOn(t, victim.sql)
		other, otherID := sessionOn(t, bystander.sql)

		guard := newStepDeadline(t, baseDSN, victim.name)
		guard.limit = time.Second
		guard.grace = 250 * time.Millisecond
		block := make(chan struct{})
		t.Cleanup(func() { close(block) })

		const step = "the step that never returns"
		start := time.Now()
		err := guard.run(step, func() error { <-block; return nil })
		elapsed := time.Since(start)
		var overrun *stepOverrunError
		require.ErrorAs(t, err, &overrun)
		assert.GreaterOrEqual(t, elapsed, guard.limit, "not before the deadline")
		assert.Less(t, elapsed, 3*time.Second, "reported within 3 s")
		msg := err.Error()
		assert.Contains(t, msg, step, "the message names the step")
		assert.Contains(t, msg, victim.name, "the message names the database")
		assert.Contains(t, msg, fmt.Sprintf("id=%d ", idleID), "the message lists the test database's session")
		assert.NotContains(t, msg, fmt.Sprintf("id=%d ", otherID), "another database's session is not listed")
		if cfg, err := mysqldriver.ParseDSN(baseDSN); err == nil && cfg.Passwd != "" {
			assert.NotContains(t, msg, cfg.Passwd, "the message carries no password")
		}

		// The kill reached only the test database: its idle session ends,
		// the other database's session still answers.
		alive := func(id int64) bool {
			var n int
			require.NoError(t, guard.server.QueryRowContext(ctx, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.PROCESSLIST WHERE ID = ?", id).Scan(&n))
			return n > 0
		}
		assert.Eventually(t, func() bool { return !alive(idleID) }, 5*time.Second, 50*time.Millisecond, "the test database's session is killed")
		var one int
		require.NoError(t, other.QueryRowContext(ctx, "SELECT 1").Scan(&one), "the other database's session is alive")
		assert.True(t, alive(otherID))
	})
}

// migrateLockWaitSeconds is the lock_wait_timeout of the migrate sessions
// TestWebhookEventTypesWidthMigration drives: a metadata-lock wait of the
// ALTER fails with error 1205 after it instead of the server default of a
// year.
const migrateLockWaitSeconds = "5"

// boundedMigrateDSN returns dsn with lock_wait_timeout=migrateLockWaitSeconds
// and the extra session variables set; every other parameter of dsn is kept.
func boundedMigrateDSN(t *testing.T, dsn string, extra map[string]string) string {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["lock_wait_timeout"] = migrateLockWaitSeconds
	for key, value := range extra {
		cfg.Params[key] = value
	}
	return cfg.FormatDSN()
}

// closeMigrateInstance closes m and reports either half's error.
func closeMigrateInstance(m *migrate.Migrate) error {
	sourceErr, databaseErr := m.Close()
	if sourceErr != nil || databaseErr != nil {
		return fmt.Errorf("close migrate instance: source: %v, database: %v", sourceErr, databaseErr)
	}
	return nil
}

// guardedVersion reads m's version under guard.
func guardedVersion(t *testing.T, guard *stepDeadline, m *migrate.Migrate) (uint, bool) {
	t.Helper()
	var version uint
	var dirty bool
	require.NoError(t, guard.must(t, "version", func() error {
		var err error
		version, dirty, err = m.Version()
		return err
	}))
	return version, dirty
}

// The bounds of one migrate call: how long it may run, and how long the
// guard then waits for it to return after killing the sessions.
const (
	migrateStepLimit = 30 * time.Second
	migrateStepGrace = 5 * time.Second
)

// stepDeadline runs steps against one test database with a time limit. When
// a step overruns it lists the database's sessions, kills them from a
// server-level handle (so a Close or a DROP DATABASE behind them cannot
// hang either) and reports the step. It never prints a DSN.
type stepDeadline struct {
	server *sql.DB
	dbName string
	limit  time.Duration
	grace  time.Duration
}

// stepOverrunError is what stepDeadline.run returns for a step that did not
// return within its limit.
type stepOverrunError struct{ msg string }

func (e *stepOverrunError) Error() string { return e.msg }

func newStepDeadline(t *testing.T, baseDSN, dbName string) *stepDeadline {
	t.Helper()
	server, err := sql.Open("mysql", requireMySQLTestDSN(t, baseDSN, ""))
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	return &stepDeadline{server: server, dbName: dbName, limit: migrateStepLimit, grace: migrateStepGrace}
}

// run calls fn and returns its error, or a *stepOverrunError when fn has not
// returned within s.limit. fn runs on its own goroutine and may outlive the
// test after an overrun, so it must not touch a testing.TB.
func (s *stepDeadline) run(step string, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(s.limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
	}

	sessions, killErr := s.killSessions()
	after := "has not returned"
	select {
	case <-done:
		after = "returned"
	case <-time.After(s.grace):
	}
	msg := fmt.Sprintf("%s did not return within %s; sessions on test database %s: [%s]; killed them, and the step %s within %s after the kill",
		step, s.limit, s.dbName, strings.Join(sessions, "; "), after, s.grace)
	if killErr != nil {
		msg += fmt.Sprintf("; listing or killing them failed: %v", killErr)
	}
	return &stepOverrunError{msg: msg}
}

// must is run for the test goroutine: an overrun fails t, any other result
// is returned.
func (s *stepDeadline) must(t *testing.T, step string, fn func() error) error {
	t.Helper()
	err := s.run(step, fn)
	var overrun *stepOverrunError
	if errors.As(err, &overrun) {
		t.Fatal(overrun.msg)
	}
	return err
}

// killSessions lists the sessions whose default database is s.dbName, other
// than the listing session itself, and kills each. The server handle names
// no database, so none of its own sessions match.
func (s *stepDeadline) killSessions() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.server.QueryContext(ctx, `SELECT ID, COALESCE(COMMAND, ''), COALESCE(STATE, ''), COALESCE(TIME, 0)
		FROM INFORMATION_SCHEMA.PROCESSLIST WHERE DB = ? AND ID <> CONNECTION_ID() ORDER BY ID`, s.dbName)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var ids []uint64
	var sessions []string
	for rows.Next() {
		var id uint64
		var command, state string
		var seconds int64
		if err := rows.Scan(&id, &command, &state, &seconds); err != nil {
			_ = rows.Close()
			return sessions, fmt.Errorf("list sessions: %w", err)
		}
		ids = append(ids, id)
		sessions = append(sessions, fmt.Sprintf("id=%d command=%s state=%q time=%ds", id, command, state, seconds))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return sessions, fmt.Errorf("list sessions: %w", err)
	}
	_ = rows.Close()
	var errs []error
	for _, id := range ids {
		if _, err := s.server.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", id)); err != nil {
			var myErr *mysqldriver.MySQLError
			if errors.As(err, &myErr) && myErr.Number == 1094 { // already gone
				continue
			}
			errs = append(errs, fmt.Errorf("kill session %d: %w", id, err))
		}
	}
	return sessions, errors.Join(errs...)
}
