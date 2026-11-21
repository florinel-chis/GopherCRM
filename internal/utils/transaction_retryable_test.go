package utils

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gosqlite "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestRetryReasonOf(t *testing.T) {
	deadlock := &mysqldriver.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"}
	lockWait := &mysqldriver.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded; try restarting transaction"}
	recordChanged := &mysqldriver.MySQLError{Number: 1020, Message: "Record has changed since last read in table 't'"}

	tests := []struct {
		name string
		err  error
		want RetryReason
	}{
		{"typed 1213", deadlock, RetryDeadlock},
		{"typed 1205", lockWait, RetryLockWaitTimeout},
		{"typed 1020", recordChanged, RetryRecordChanged},
		{"wrapped 1213", fmt.Errorf("x: %w", deadlock), RetryDeadlock},
		{"wrapped 1205", fmt.Errorf("x: %w", lockWait), RetryLockWaitTimeout},
		{"wrapped 1020", fmt.Errorf("x: %w", recordChanged), RetryRecordChanged},
		{"joined 1213", errors.Join(errors.New("context canceled"), deadlock), RetryDeadlock},
		{"nested retry deferred 1213", fmt.Errorf("%w: %w", ErrNestedRetryDeferred, deadlock), RetryDeadlock},
		{"typed 1062 duplicate key", &mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry"}, RetryNone},
		{"typed 1452 foreign key", &mysqldriver.MySQLError{Number: 1452, Message: "Cannot add or update a child row"}, RetryNone},
		{"nil", nil, RetryNone},
		{"text deadlock", errors.New("deadlock detected"), RetryNone},
		{"text Error 1213", errors.New("Error 1213: Deadlock found when trying to get lock"), RetryNone},
		{"text Lock wait timeout", errors.New("Lock wait timeout exceeded"), RetryNone},
		{"text SQLITE_BUSY", errors.New("database is locked (5) (SQLITE_BUSY)"), RetryNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RetryReasonOf(tt.err))
			assert.Equal(t, tt.want != RetryNone, IsRetryable(tt.err))
		})
	}
}

// TestTransactionRetryableErrorSQLite provokes real SQLite errors: a second
// connection holding BEGIN IMMEDIATE makes a write on the first answer
// SQLITE_BUSY at once (busy_timeout 0), and a duplicate primary key answers a
// UNIQUE constraint violation.
func TestTransactionRetryableErrorSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retryable.db")
	dsn := path + "?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	limitPoolToOneConnection(t, db)
	require.NoError(t, db.AutoMigrate(&txRecord{}))
	tm := NewTransactionManager(db)

	holderDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holderDB.Close() })
	holder, err := holderDB.Conn(boundedContext(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })

	hold := func(t *testing.T) {
		t.Helper()
		_, err := holder.ExecContext(boundedContext(t), "BEGIN IMMEDIATE")
		require.NoError(t, err)
	}
	release := func(t *testing.T) {
		t.Helper()
		_, err := holder.ExecContext(boundedContext(t), "ROLLBACK")
		require.NoError(t, err)
	}
	assertRealBusy := func(t *testing.T, err error) {
		t.Helper()
		var sqliteErr *gosqlite.Error
		require.True(t, errors.As(err, &sqliteErr), "a real driver error, got %v", err)
		assert.Contains(t, []int{5, 6}, sqliteErr.Code()&0xff, "SQLITE_BUSY or SQLITE_LOCKED")
		assert.Equal(t, RetrySQLiteBusy, RetryReasonOf(err))
	}

	t.Run("a write behind BEGIN IMMEDIATE is a real busy error", func(t *testing.T) {
		clearRecords(t, db)
		hold(t)
		err := db.WithContext(boundedContext(t)).Create(&txRecord{Label: "blocked"}).Error
		release(t)
		assertRealBusy(t, err)
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("a busy attempt is replayed once and commits", func(t *testing.T) {
		clearRecords(t, db)
		hold(t)
		attempts := 0
		var first error
		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			err := insertRecord(ctx, fmt.Sprintf("attempt-%d", attempts))
			if attempts == 1 {
				first = err
				release(t)
			}
			return err
		}, 1)
		require.NoError(t, err)
		assertRealBusy(t, first)
		assert.Equal(t, 2, attempts)
		assert.Equal(t, []string{"attempt-2"}, persistedLabels(t, db))
	})

	t.Run("a unique violation is not replayed", func(t *testing.T) {
		clearRecords(t, db)
		require.NoError(t, db.WithContext(boundedContext(t)).Create(&txRecord{ID: 1, Label: "seed"}).Error)
		attempts := 0
		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			tx, _ := GetTxFromContext(ctx)
			return tx.Create(&txRecord{ID: 1, Label: "duplicate"}).Error
		}, 1)
		require.Error(t, err)
		var sqliteErr *gosqlite.Error
		require.True(t, errors.As(err, &sqliteErr), "a real driver error, got %v", err)
		assert.Equal(t, 19, sqliteErr.Code()&0xff, "SQLITE_CONSTRAINT")
		assert.Equal(t, RetryNone, RetryReasonOf(err))
		assert.Equal(t, 1, attempts)
		assert.Equal(t, []string{"seed"}, persistedLabels(t, db))
	})
}

// openRetryablePeerMySQL opens a separate pool on the schema db uses, closed
// when the (sub)test ends, so it is gone before the schema is dropped.
func openRetryablePeerMySQL(t *testing.T, db *gorm.DB) *sql.DB {
	t.Helper()
	var name string
	require.NoError(t, db.WithContext(boundedContext(t)).Raw("SELECT DATABASE()").Scan(&name).Error)
	cfg, err := mysqldriver.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
	require.NoError(t, err)
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, scenarioTimeout, scenarioTimeout
	peer, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	require.NoError(t, peer.PingContext(boundedContext(t)))
	return peer
}

// deadlockFirstWait is the statement the first connection of the deadlock
// subtest sends while the second holds the row it updates. The test passes no
// arguments, so the driver sends it as COM_QUERY text and PROCESSLIST.INFO
// shows it verbatim on both engines.
const deadlockFirstWait = "UPDATE tx_regression_records SET label = 'first' WHERE id = 2"

// lockWaitPollInterval spaces the PROCESSLIST polls of awaitLockWait. It is a
// poll interval, not a delay that orders the scenario.
const lockWaitPollInterval = 10 * time.Millisecond

// awaitLockWait returns once connection threadID has been seen executing
// statement on two consecutive polls of information_schema.PROCESSLIST. An
// uncontended primary-key update finishes in microseconds, so one still
// executing a poll interval later waits for a row lock. PROCESSLIST exists on
// MySQL 8 and MariaDB 10.11 and shows the same user's threads without the
// PROCESS privilege. INNODB_TRX is not usable here: the server serves it from a
// cache it refreshes only when the previous read is more than about 100 ms
// old, so a continuous poll keeps reading the snapshot taken before the wait
// began, and MySQL 8 reports some lock waits as RUNNING. done receives the
// statement's result; if it returns before the wait is seen, the test fails at
// once with that result. Every probe error fails the test.
func awaitLockWait(t *testing.T, peer *sql.DB, threadID int64, statement string, done <-chan error) {
	t.Helper()
	ctx := boundedContext(t)
	ticker := time.NewTicker(lockWaitPollInterval)
	defer ticker.Stop()
	seen := 0
	for {
		var command string
		var info sql.NullString
		err := peer.QueryRowContext(ctx, `SELECT COMMAND, INFO FROM information_schema.PROCESSLIST WHERE ID = ?`,
			threadID).Scan(&command, &info)
		require.NoError(t, err, "reading connection %d from information_schema.PROCESSLIST", threadID)
		if (command == "Query" || command == "Execute") && info.Valid && info.String == statement {
			seen++
			if seen >= 2 {
				return
			}
		} else {
			seen = 0
		}
		select {
		case err := <-done:
			t.Fatalf("connection %d finished %q before it was seen waiting for a lock: %v", threadID, statement, err)
		case <-ctx.Done():
			t.Fatalf("connection %d never waited for a lock on %q", threadID, statement)
		case <-ticker.C:
		}
	}
}

func mysqlErrorNumber(t *testing.T, err error) uint16 {
	t.Helper()
	var driverErr *mysqldriver.MySQLError
	require.True(t, errors.As(err, &driverErr), "a real driver error, got %v", err)
	return driverErr.Number
}

func seedTwoRecords(t *testing.T, db *gorm.DB) {
	t.Helper()
	clearRecords(t, db)
	ctx := boundedContext(t)
	require.NoError(t, db.WithContext(ctx).Create(&txRecord{ID: 1, Label: "one"}).Error)
	require.NoError(t, db.WithContext(ctx).Create(&txRecord{ID: 2, Label: "two"}).Error)
}

func TestTransactionRetryableErrorMySQL(t *testing.T) {
	db := openTransactionMySQL(t)

	t.Run("a real deadlock is RetryDeadlock", func(t *testing.T) {
		seedTwoRecords(t, db)
		peer := openRetryablePeerMySQL(t, db)
		ctx := boundedContext(t)

		first, err := peer.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = first.Rollback() }()
		second, err := peer.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = second.Rollback() }()

		var firstThread int64
		require.NoError(t, first.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&firstThread))
		_, err = first.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'first' WHERE id = 1")
		require.NoError(t, err)
		_, err = second.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'second' WHERE id = 2")
		require.NoError(t, err)

		firstDone := make(chan error, 1)
		go func() {
			_, err := first.ExecContext(ctx, deadlockFirstWait)
			firstDone <- err
		}()
		awaitLockWait(t, peer, firstThread, deadlockFirstWait, firstDone)
		secondDone := make(chan error, 1)
		go func() {
			_, err := second.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'second' WHERE id = 1")
			secondDone <- err
		}()

		var errs [2]error
		for i, ch := range []chan error{firstDone, secondDone} {
			select {
			case errs[i] = <-ch:
			case <-ctx.Done():
				t.Fatal("the deadlock was not resolved in time")
			}
		}
		victims := 0
		for i, err := range errs {
			if err == nil {
				survivor := []*sql.Tx{first, second}[i]
				require.NoError(t, survivor.Commit())
				continue
			}
			victims++
			assert.EqualValues(t, 1213, mysqlErrorNumber(t, err))
			assert.Equal(t, RetryDeadlock, RetryReasonOf(err))
		}
		assert.Equal(t, 1, victims, "exactly one connection is the deadlock victim")
	})

	t.Run("a real lock wait timeout is RetryLockWaitTimeout", func(t *testing.T) {
		seedTwoRecords(t, db)
		peer := openRetryablePeerMySQL(t, db)
		ctx := boundedContext(t)

		holder, err := peer.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		_, err = holder.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'held' WHERE id = 1")
		require.NoError(t, err)

		waiterConn, err := peer.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = waiterConn.Close() }()
		_, err = waiterConn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 1")
		require.NoError(t, err)
		waiter, err := waiterConn.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = waiter.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'waiter' WHERE id = 1")
		_ = waiter.Rollback()
		require.Error(t, err)
		assert.EqualValues(t, 1205, mysqlErrorNumber(t, err))
		assert.Equal(t, RetryLockWaitTimeout, RetryReasonOf(err))
	})

	t.Run("a real duplicate key is RetryNone", func(t *testing.T) {
		seedTwoRecords(t, db)
		err := db.WithContext(boundedContext(t)).Create(&txRecord{ID: 1, Label: "duplicate"}).Error
		require.Error(t, err)
		assert.EqualValues(t, 1062, mysqlErrorNumber(t, err))
		assert.Equal(t, RetryNone, RetryReasonOf(err))
	})

	// The manager's pool holds one connection (openTransactionMySQL), so the
	// session variable set here is the one every attempt runs on.
	t.Run("a real record changed error is RetryRecordChanged and replayed", func(t *testing.T) {
		seedTwoRecords(t, db)
		ctx := boundedContext(t)
		err := db.WithContext(ctx).Exec("SET SESSION innodb_snapshot_isolation = ON").Error
		if err != nil {
			// MySQL 8 has no such variable; only MariaDB raises 1020.
			assert.EqualValues(t, 1193, mysqlErrorNumber(t, err), "unknown system variable")
			t.Logf("the server refuses innodb_snapshot_isolation with %d", mysqlErrorNumber(t, err))
			return
		}
		t.Cleanup(func() {
			_ = db.WithContext(context.Background()).Exec("SET SESSION innodb_snapshot_isolation = OFF").Error
		})
		peer := openRetryablePeerMySQL(t, db)
		tm := NewTransactionManager(db)

		attempts := 0
		var first error
		err = tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			tx, _ := GetTxFromContext(ctx)
			var label string
			if err := tx.Raw("SELECT label FROM tx_regression_records WHERE id = 1").Scan(&label).Error; err != nil {
				return err
			}
			if attempts == 1 {
				if _, err := peer.ExecContext(ctx, "UPDATE tx_regression_records SET label = 'concurrent' WHERE id = 1"); err != nil {
					return fmt.Errorf("concurrent update: %v", err)
				}
			}
			err := tx.Exec("UPDATE tx_regression_records SET label = ? WHERE id = 1", fmt.Sprintf("attempt-%d", attempts)).Error
			if attempts == 1 {
				first = err
			}
			return err
		}, 1)
		require.NoError(t, err)
		require.Error(t, first, "attempt 1 updates a row changed since its snapshot")
		assert.EqualValues(t, 1020, mysqlErrorNumber(t, first))
		assert.Equal(t, RetryRecordChanged, RetryReasonOf(first))
		assert.Equal(t, 2, attempts)
		t.Logf("attempt 1 failed with %d, attempt %d committed", mysqlErrorNumber(t, first), attempts)

		var label string
		require.NoError(t, db.WithContext(ctx).Raw("SELECT label FROM tx_regression_records WHERE id = 1").Scan(&label).Error)
		assert.Equal(t, "attempt-2", label)
	})
}
