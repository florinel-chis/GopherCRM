package utils

// Regression roots for issue #106: panic rollback, nested savepoints, and
// cancellation/retry semantics of TransactionManager, run against a real
// one-connection SQLite pool and a disposable one-connection MySQL schema.
//
// Every scenario asserts on persisted rows read outside any transaction, and
// every wait is bounded by a context: with a single pooled connection, a
// manager that leaked a transaction or tried to begin a second root
// transaction would block until the deadline and fail here deterministically.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// txRecord is the only table these tests write; its labels name the step
// that wrote each row, so the persisted set spells out what was committed.
type txRecord struct {
	ID    uint   `gorm:"primaryKey"`
	Label string `gorm:"size:64;not null"`
}

func (txRecord) TableName() string { return "tx_regression_records" }

// scenarioTimeout bounds every manager call and verification query. It is
// generous for a loaded CI host but far below the 180s check timeout, so a
// leaked connection fails the test instead of the whole package.
const scenarioTimeout = 20 * time.Second

var txTestDBSeq atomic.Uint64

type panicMarker struct{ reason string }

func boundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	t.Cleanup(cancel)
	return ctx
}

// limitPoolToOneConnection mirrors the production SQLite pool, and applies the
// same bound to MySQL so that both engines prove nested calls never need a
// second root connection.
func limitPoolToOneConnection(t *testing.T, db *gorm.DB) *sql.DB {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// sqliteFileDSN is a file-backed database with the production pragmas. A file
// is deliberate: database/sql discards a connection whose transaction was
// rolled back by context cancellation, and an in-memory database would vanish
// with its only connection.
func sqliteFileDSN(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("tx_regression_%d.db", txTestDBSeq.Add(1)))
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func openTransactionSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(sqliteFileDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	limitPoolToOneConnection(t, db)
	require.NoError(t, db.AutoMigrate(&txRecord{}))
	return db
}

// openTransactionMySQL creates a uniquely named disposable schema on the
// server identified by TEST_MYSQL_DSN and drops it afterwards. The named
// acceptance checks require the variable; the ordinary `go test ./...` run on
// a machine without a MySQL server skips, which the acceptance parser rejects.
func openTransactionMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, scenarioTimeout, scenarioTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_tx_%d_%d", time.Now().UnixNano(), txTestDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err)
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	// Registered after the DROP cleanup, so it runs first and the schema is
	// dropped only once its last connection is closed.
	limitPoolToOneConnection(t, db)
	require.NoError(t, db.AutoMigrate(&txRecord{}))
	return db
}

// insertRecord writes through the transaction carried by ctx, exactly as the
// services do via repositories' WithTx.
func insertRecord(ctx context.Context, label string) error {
	tx, ok := GetTxFromContext(ctx)
	if !ok {
		return ErrNoTransaction
	}
	return tx.Create(&txRecord{Label: label}).Error
}

// persistedLabels reads committed rows outside any transaction, sorted by
// label. On the one-connection pool this also proves the connection is free.
func persistedLabels(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	labels := []string{}
	require.NoError(t, db.WithContext(boundedContext(t)).Model(&txRecord{}).Order("label").Pluck("label", &labels).Error)
	return labels
}

func clearRecords(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.WithContext(boundedContext(t)).Exec("DELETE FROM tx_regression_records").Error)
}

func callAndRecover(fn func()) (recovered interface{}) {
	defer func() { recovered = recover() }()
	fn()
	return nil
}

func TestTransactionPanicSQLite(t *testing.T) {
	runTransactionPanicScenarios(t, openTransactionSQLite(t))
}

func TestTransactionPanicMySQL(t *testing.T) {
	runTransactionPanicScenarios(t, openTransactionMySQL(t))
}

func TestTransactionNestedSQLite(t *testing.T) {
	runTransactionNestedScenarios(t, openTransactionSQLite(t))
}

func TestTransactionNestedMySQL(t *testing.T) {
	runTransactionNestedScenarios(t, openTransactionMySQL(t))
}

func TestTransactionCancellationRetrySQLite(t *testing.T) {
	runTransactionCancellationRetryScenarios(t, openTransactionSQLite(t))
}

func TestTransactionCancellationRetryMySQL(t *testing.T) {
	runTransactionCancellationRetryScenarios(t, openTransactionMySQL(t))
}

func runTransactionPanicScenarios(t *testing.T, db *gorm.DB) {
	tm := NewTransactionManager(db)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	t.Run("callback panic rolls back the writes and reaches the caller unchanged", func(t *testing.T) {
		clearRecords(t, db)
		marker := &panicMarker{reason: "callback exploded"}

		recovered := callAndRecover(func() {
			_ = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "doomed"))
				panic(marker)
			})
		})

		require.Same(t, marker, recovered, "the caller must recover the identical panic value")
		assert.Equal(t, 0, sqlDB.Stats().InUse, "the rolled-back transaction must release its connection")
		assert.Empty(t, persistedLabels(t, db))

		// The only pooled connection must serve the next transaction; a leaked
		// transaction would make this BEGIN wait until the bounded deadline.
		require.NoError(t, tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "after-panic")
		}))
		assert.Equal(t, []string{"after-panic"}, persistedLabels(t, db))
	})

	t.Run("panic values of any type are propagated as they are", func(t *testing.T) {
		clearRecords(t, db)
		recovered := callAndRecover(func() {
			_ = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "doomed"))
				panic("plain string panic")
			})
		})
		assert.Equal(t, "plain string panic", recovered)
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("nested panic recovered by the outer callback keeps only the outer writes", func(t *testing.T) {
		clearRecords(t, db)
		marker := &panicMarker{reason: "inner exploded"}
		var recovered interface{}

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer-before"); err != nil {
				return err
			}
			recovered = callAndRecover(func() {
				_ = tm.WithTransaction(ctx, func(ctx context.Context) error {
					require.NoError(t, insertRecord(ctx, "inner"))
					panic(marker)
				})
			})
			return insertRecord(ctx, "outer-after")
		})

		require.NoError(t, err)
		require.Same(t, marker, recovered)
		assert.Equal(t, []string{"outer-after", "outer-before"}, persistedLabels(t, db))
		assert.Equal(t, 0, sqlDB.Stats().InUse)
	})

	t.Run("nested panic not recovered by the outer callback rolls back everything", func(t *testing.T) {
		clearRecords(t, db)
		marker := &panicMarker{reason: "unhandled inner panic"}

		recovered := callAndRecover(func() {
			_ = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "outer"))
				return tm.WithTransaction(ctx, func(ctx context.Context) error {
					require.NoError(t, insertRecord(ctx, "inner"))
					panic(marker)
				})
			})
		})

		require.Same(t, marker, recovered)
		assert.Equal(t, 0, sqlDB.Stats().InUse)
		assert.Empty(t, persistedLabels(t, db))
	})
}

func runTransactionNestedScenarios(t *testing.T, db *gorm.DB) {
	tm := NewTransactionManager(db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.Equal(t, 1, sqlDB.Stats().MaxOpenConnections, "the scenarios rely on a single pooled connection")

	t.Run("nested call reuses the active transaction without a second connection", func(t *testing.T) {
		clearRecords(t, db)
		waitsBefore := sqlDB.Stats().WaitCount
		var outerPool, innerPool gorm.ConnPool

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			outer, ok := GetTxFromContext(ctx)
			require.True(t, ok)
			outerPool = outer.Statement.ConnPool
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			return tm.WithTransaction(ctx, func(ctx context.Context) error {
				inner, ok := GetTxFromContext(ctx)
				require.True(t, ok)
				innerPool = inner.Statement.ConnPool
				return insertRecord(ctx, "inner")
			})
		})

		require.NoError(t, err)
		assert.True(t, outerPool == innerPool, "the nested call must run on the outer transaction's connection")
		assert.Equal(t, waitsBefore, sqlDB.Stats().WaitCount, "the nested call must not wait for a second connection")
		assert.Equal(t, []string{"inner", "outer"}, persistedLabels(t, db))
	})

	t.Run("inner failure rolls back to its savepoint while the outer commits", func(t *testing.T) {
		clearRecords(t, db)
		innerFailure := errors.New("inner unit rejected")

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer-before"); err != nil {
				return err
			}
			innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				return innerFailure
			})
			require.Same(t, innerFailure, innerErr, "a clean savepoint rollback returns the callback error itself")
			return insertRecord(ctx, "outer-after")
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"outer-after", "outer-before"}, persistedLabels(t, db))
	})

	t.Run("successful inner writes stay provisional until the outer commits", func(t *testing.T) {
		clearRecords(t, db)
		outerFailure := errors.New("outer unit rejected")

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			require.NoError(t, tm.WithTransaction(ctx, func(ctx context.Context) error {
				return insertRecord(ctx, "inner")
			}))
			return outerFailure
		})

		require.Same(t, outerFailure, err)
		assert.Empty(t, persistedLabels(t, db), "an outer rollback must discard successful nested writes")
		assert.Equal(t, 0, sqlDB.Stats().InUse)
	})

	t.Run("multiple nesting levels roll back exactly their own subtree", func(t *testing.T) {
		clearRecords(t, db)
		levelTwoFailure := errors.New("level two rejected")
		levelThreeFailure := errors.New("level three rejected")

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "l1"); err != nil {
				return err
			}

			// Level two fails after a successful level three: both are discarded.
			err := tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "l2-a"); err != nil {
					return err
				}
				require.NoError(t, tm.WithTransaction(ctx, func(ctx context.Context) error {
					return insertRecord(ctx, "l3-a")
				}))
				return levelTwoFailure
			})
			require.Same(t, levelTwoFailure, err)

			// Level two swallows a failing level three: only level three is discarded.
			err = tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "l2-b"); err != nil {
					return err
				}
				innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
					if err := insertRecord(ctx, "l3-b"); err != nil {
						return err
					}
					return levelThreeFailure
				})
				require.Same(t, levelThreeFailure, innerErr)
				return nil
			})
			require.NoError(t, err)

			// Level two propagates a failing level three: both are discarded.
			err = tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "l2-c"); err != nil {
					return err
				}
				return tm.WithTransaction(ctx, func(ctx context.Context) error {
					if err := insertRecord(ctx, "l3-c"); err != nil {
						return err
					}
					return levelThreeFailure
				})
			})
			require.Same(t, levelThreeFailure, err)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"l1", "l2-b"}, persistedLabels(t, db))
	})

	t.Run("a cancelled inner context cannot prevent the savepoint rollback", func(t *testing.T) {
		clearRecords(t, db)

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer-before"); err != nil {
				return err
			}
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			innerErr := tm.WithTransaction(innerCtx, func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "inner-before-cancel"))
				cancel()
				err := insertRecord(ctx, "inner-after-cancel")
				require.ErrorIs(t, err, context.Canceled, "statements on the inner handle follow the inner context")
				return err
			})
			require.ErrorIs(t, innerErr, context.Canceled)
			require.NotErrorIs(t, innerErr, ErrRollbackFailed, "the rollback runs on the enclosing context")
			return insertRecord(ctx, "outer-after")
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"outer-after", "outer-before"}, persistedLabels(t, db))
	})

	t.Run("an already cancelled inner context is refused before the callback runs", func(t *testing.T) {
		clearRecords(t, db)
		innerCalled := false

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			innerCtx, cancel := context.WithCancel(ctx)
			cancel()
			innerErr := tm.WithTransaction(innerCtx, func(ctx context.Context) error {
				innerCalled = true
				return nil
			})
			require.ErrorIs(t, innerErr, ErrSavepointFailed)
			require.ErrorIs(t, innerErr, context.Canceled)
			return insertRecord(ctx, "outer")
		})

		require.NoError(t, err)
		assert.False(t, innerCalled)
		assert.Equal(t, []string{"outer"}, persistedLabels(t, db))
	})

	t.Run("cancelling the root context discards every level and frees the connection", func(t *testing.T) {
		clearRecords(t, db)
		rootCtx, cancel := context.WithCancel(boundedContext(t))
		defer cancel()

		err := tm.WithTransaction(rootCtx, func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			return tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				cancel()
				return nil
			})
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrCommitFailed)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, persistedLabels(t, db))
		require.NoError(t, tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "after-cancel")
		}))
		assert.Equal(t, []string{"after-cancel"}, persistedLabels(t, db))
	})
}

func runTransactionCancellationRetryScenarios(t *testing.T, db *gorm.DB) {
	tm := NewTransactionManager(db)
	retryable := errors.New("Deadlock found when trying to get lock; try restarting transaction")

	t.Run("a cancelled context fails before any attempt runs", func(t *testing.T) {
		clearRecords(t, db)
		ctx, cancel := context.WithCancel(boundedContext(t))
		cancel()
		called := false

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			called = true
			return insertRecord(ctx, "never")
		}, 3)

		require.ErrorIs(t, err, ErrBeginFailed)
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, called)
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("cancellation during the callback prevents the commit and frees the pool", func(t *testing.T) {
		clearRecords(t, db)
		ctx, cancel := context.WithCancel(boundedContext(t))
		defer cancel()

		err := tm.WithTransaction(ctx, func(ctx context.Context) error {
			if err := insertRecord(ctx, "written-then-cancelled"); err != nil {
				return err
			}
			cancel()
			return nil
		})

		require.ErrorIs(t, err, ErrCommitFailed)
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, persistedLabels(t, db))
		require.NoError(t, tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "after-cancel")
		}))
		assert.Equal(t, []string{"after-cancel"}, persistedLabels(t, db))
	})

	t.Run("cancellation during a retryable failure stops the retry loop", func(t *testing.T) {
		clearRecords(t, db)
		ctx, cancel := context.WithCancel(boundedContext(t))
		defer cancel()
		attempts := 0

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, "cancelled-attempt"); err != nil {
				return err
			}
			cancel()
			return retryable
		}, 5)

		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, retryable)
		assert.Contains(t, err.Error(), "retry abandoned after 1 attempts")
		assert.Equal(t, 1, attempts, "no attempt may start on a dead context")
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("root retry persists only the successful attempt", func(t *testing.T) {
		clearRecords(t, db)
		attempts := 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, fmt.Sprintf("attempt-%d", attempts)); err != nil {
				return err
			}
			if attempts < 3 {
				return retryable
			}
			return nil
		}, 3)

		require.NoError(t, err)
		assert.Equal(t, 3, attempts)
		assert.Equal(t, []string{"attempt-3"}, persistedLabels(t, db))
	})

	t.Run("retry exhaustion is bounded and leaves no writes", func(t *testing.T) {
		clearRecords(t, db)
		attempts := 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, fmt.Sprintf("attempt-%d", attempts)); err != nil {
				return err
			}
			return retryable
		}, 2)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "transaction failed after 3 attempts")
		assert.ErrorIs(t, err, retryable)
		assert.Equal(t, 3, attempts)
		assert.Empty(t, persistedLabels(t, db))

		// The pool is intact after the exhausted retries.
		require.NoError(t, tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "after-exhaustion")
		}))
		assert.Equal(t, []string{"after-exhaustion"}, persistedLabels(t, db))
	})

	t.Run("a non-retryable error fails once and keeps its identity", func(t *testing.T) {
		clearRecords(t, db)
		failure := errors.New("validation rejected the record")
		attempts := 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, "rejected"); err != nil {
				return err
			}
			return failure
		}, 3)

		require.Same(t, failure, err)
		assert.Equal(t, 1, attempts)
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("a negative retry count is rejected without running the callback", func(t *testing.T) {
		clearRecords(t, db)
		called := false

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			called = true
			return nil
		}, -1)
		require.ErrorIs(t, err, ErrInvalidRetryCount)
		assert.False(t, called)

		// The same applies inside an active transaction.
		err = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			nestedErr := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
				called = true
				return nil
			}, -2)
			require.ErrorIs(t, nestedErr, ErrInvalidRetryCount)
			return insertRecord(ctx, "outer")
		})
		require.NoError(t, err)
		assert.False(t, called)
		assert.Equal(t, []string{"outer"}, persistedLabels(t, db))
	})

	t.Run("a nested retryable failure is deferred to the root retry", func(t *testing.T) {
		clearRecords(t, db)
		outerAttempts, innerCalls := 0, 0
		var deferredErr error

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			outerAttempts++
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			innerErr := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
				innerCalls++
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				if innerCalls == 1 {
					return retryable
				}
				return nil
			}, 5)
			if outerAttempts == 1 {
				deferredErr = innerErr
			}
			return innerErr
		}, 3)

		require.NoError(t, err)
		assert.Equal(t, 2, outerAttempts, "the root attempt replays the whole unit once")
		assert.Equal(t, 2, innerCalls, "the nested call never retries inside the active transaction")
		require.ErrorIs(t, deferredErr, ErrNestedRetryDeferred)
		require.ErrorIs(t, deferredErr, retryable)
		assert.Equal(t, []string{"inner", "outer"}, persistedLabels(t, db))
	})

	t.Run("a swallowed nested retryable failure leaves no inner writes behind", func(t *testing.T) {
		clearRecords(t, db)
		innerCalls := 0

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			innerErr := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
				innerCalls++
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				return retryable
			}, 3)
			require.ErrorIs(t, innerErr, ErrNestedRetryDeferred)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, innerCalls)
		assert.Equal(t, []string{"outer"}, persistedLabels(t, db), "the outer commit must not carry the rolled-back inner writes")
	})

	t.Run("an expired deadline fails before the first attempt and after a failed one", func(t *testing.T) {
		clearRecords(t, db)
		expired, cancel := context.WithDeadline(boundedContext(t), time.Now().Add(-time.Second))
		defer cancel()
		called := false

		err := tm.WithTransactionAndRetry(expired, func(ctx context.Context) error {
			called = true
			return nil
		}, 3)
		require.ErrorIs(t, err, ErrBeginFailed)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, called)

		// A deadline that passes while the attempt runs ends the loop with
		// both the deadline and the attempt's failure visible.
		short, cancelShort := context.WithTimeout(boundedContext(t), scenarioTimeout)
		defer cancelShort()
		attempts := 0
		err = tm.WithTransactionAndRetry(short, func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, "deadline-attempt"); err != nil {
				return err
			}
			cancelShort()
			return retryable
		}, 10)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, retryable)
		assert.Equal(t, 1, attempts)
		assert.Empty(t, persistedLabels(t, db))
	})
}
