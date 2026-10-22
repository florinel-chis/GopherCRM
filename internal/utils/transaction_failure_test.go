package utils

// Deterministic finalization failures for issue #106. A small connection-pool
// wrapper around the real SQLite driver injects BEGIN, COMMIT, ROLLBACK and
// SAVEPOINT failures at the database/sql boundary GORM uses, so every scenario
// is reproducible without timing, a second connection or a new dependency.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// failingPool is the root connection pool handed to GORM. Injection fields
// are read by the transactions it begins; reset clears them between scenarios.
type failingPool struct {
	db *sql.DB

	mu              sync.Mutex
	begins          int
	beginErr        error
	commitErr       error
	commitBeforeErr bool
	rollbackErr     error
	execHook        func(query string) error
}

func (p *failingPool) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beginErr, p.commitErr, p.commitBeforeErr, p.rollbackErr, p.execHook = nil, nil, false, nil, nil
}

func (p *failingPool) inject(configure func(p *failingPool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	configure(p)
}

func (p *failingPool) beginCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.begins
}

func (p *failingPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *failingPool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *failingPool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

func (p *failingPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

func (p *failingPool) GetDBConn() (*sql.DB, error) { return p.db, nil }

// BeginTx implements gorm.ConnPoolBeginner so that GORM receives a wrapped
// transaction whose finalization can fail on demand.
func (p *failingPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	p.mu.Lock()
	p.begins++
	err := p.beginErr
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &failingTx{pool: p, tx: tx}, nil
}

type failingTx struct {
	pool *failingPool
	tx   *sql.Tx
}

func (t *failingTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, query)
}

func (t *failingTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	t.pool.mu.Lock()
	hook := t.pool.execHook
	t.pool.mu.Unlock()
	if hook != nil {
		if err := hook(query); err != nil {
			return nil, err
		}
	}
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *failingTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *failingTx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// Commit reports the injected error. With commitBeforeErr the real COMMIT is
// performed first, reproducing the ambiguous case where the server applied
// the work but the client saw a failure.
func (t *failingTx) Commit() error {
	t.pool.mu.Lock()
	err, before := t.pool.commitErr, t.pool.commitBeforeErr
	t.pool.mu.Unlock()
	if err == nil {
		return t.tx.Commit()
	}
	if before {
		_ = t.tx.Commit()
	} else {
		_ = t.tx.Rollback()
	}
	return err
}

// Rollback reports the injected error after finalizing the real transaction,
// which is what database/sql does too: a failed ROLLBACK still marks the
// transaction done and releases (or discards) its connection.
func (t *failingTx) Rollback() error {
	t.pool.mu.Lock()
	err := t.pool.rollbackErr
	t.pool.mu.Unlock()
	if err == nil {
		return t.tx.Rollback()
	}
	_ = t.tx.Rollback()
	return err
}

// failOnce returns an exec hook that fails the first statement with the given
// prefix and lets every later one through.
func failOnce(prefix string, err error) func(query string) error {
	fired := false
	return func(query string) error {
		if !fired && strings.HasPrefix(query, prefix) {
			fired = true
			return err
		}
		return nil
	}
}

func failAlways(prefix string, err error) func(query string) error {
	return func(query string) error {
		if strings.HasPrefix(query, prefix) {
			return err
		}
		return nil
	}
}

// failAlwaysCounting is failAlways plus a count of how often the failing
// statement was attempted, so a test can prove the manager stopped issuing
// statements on a transaction it already knows to be broken.
func failAlwaysCounting(prefix string, err error) (func(query string) error, *atomic.Int32) {
	attempts := &atomic.Int32{}
	return func(query string) error {
		if strings.HasPrefix(query, prefix) {
			attempts.Add(1)
			return err
		}
		return nil
	}, attempts
}

func openFailingSQLite(t *testing.T) (*failingPool, *gorm.DB) {
	t.Helper()
	sqlDB, err := sql.Open(sqlite.DriverName, sqliteFileDSN(t))
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	pool := &failingPool{db: sqlDB}
	db, err := gorm.Open(&sqlite.Dialector{Conn: pool}, &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&txRecord{}))
	return pool, db
}

func TestTransactionFinalizationFailures(t *testing.T) {
	pool, db := openFailingSQLite(t)
	tm := NewTransactionManager(db)
	retryable := errors.New("Deadlock found when trying to get lock; try restarting transaction")

	scenario := func(name string, run func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			pool.reset()
			t.Cleanup(pool.reset)
			clearRecords(t, db)
			run(t)
		})
	}

	// assertPoolUsable proves the single connection serves a new transaction
	// and that the earlier failure left no write behind except those listed.
	assertPoolUsable := func(t *testing.T, expected ...string) {
		t.Helper()
		pool.reset()
		assert.Equal(t, 0, pool.db.Stats().InUse)
		require.NoError(t, tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "after-failure")
		}))
		assert.ElementsMatch(t, append(expected, "after-failure"), persistedLabels(t, db))
	}

	scenario("begin failure never runs the callback", func(t *testing.T) {
		beginErr := errors.New("injected: cannot begin")
		pool.inject(func(p *failingPool) { p.beginErr = beginErr })
		called := false

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			called = true
			return nil
		})

		require.ErrorIs(t, err, ErrBeginFailed)
		require.ErrorIs(t, err, beginErr)
		assert.False(t, called)
		assertPoolUsable(t)
	})

	scenario("commit failure is reported and leaves no writes", func(t *testing.T) {
		commitErr := errors.New("injected: commit refused")
		pool.inject(func(p *failingPool) { p.commitErr = commitErr })

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return insertRecord(ctx, "uncommitted")
		})

		require.ErrorIs(t, err, ErrCommitFailed)
		require.ErrorIs(t, err, commitErr)
		assert.NotErrorIs(t, err, ErrRollbackFailed)
		assert.Empty(t, persistedLabels(t, db))
		assertPoolUsable(t)
	})

	scenario("an ambiguous commit is never replayed by the retry helper", func(t *testing.T) {
		// The server applied the COMMIT but the client saw an error whose
		// text would otherwise qualify for a retry.
		pool.inject(func(p *failingPool) {
			p.commitErr = retryable
			p.commitBeforeErr = true
		})
		attempts := 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			return insertRecord(ctx, "ambiguous")
		}, 3)

		require.ErrorIs(t, err, ErrCommitFailed)
		require.ErrorIs(t, err, retryable)
		assert.Equal(t, 1, attempts, "a failed commit must not be replayed")
		assert.Equal(t, []string{"ambiguous"}, persistedLabels(t, db), "the committed work is reported as failed, not duplicated")
		assertPoolUsable(t, "ambiguous")
	})

	scenario("rollback failure keeps the callback error inspectable and blocks replay", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback lost")
		pool.inject(func(p *failingPool) { p.rollbackErr = rollbackErr })
		attempts := 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			attempts++
			if err := insertRecord(ctx, "rolled-back"); err != nil {
				return err
			}
			return retryable
		}, 3)

		require.ErrorIs(t, err, retryable)
		require.ErrorIs(t, err, ErrRollbackFailed)
		require.ErrorIs(t, err, rollbackErr)
		assert.Equal(t, 1, attempts, "an attempt whose rollback failed is not replayed")
		assertPoolUsable(t)
	})

	scenario("a plain callback error survives a failed rollback unchanged in identity", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback lost")
		callbackErr := errors.New("business rule violated")
		pool.inject(func(p *failingPool) { p.rollbackErr = rollbackErr })

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return callbackErr
		})

		require.ErrorIs(t, err, callbackErr)
		require.ErrorIs(t, err, ErrRollbackFailed)
		assert.NotErrorIs(t, err, ErrCommitFailed)
	})

	scenario("ErrTxDone on rollback means the context already finalized the transaction", func(t *testing.T) {
		callbackErr := errors.New("business rule violated")
		pool.inject(func(p *failingPool) { p.rollbackErr = sql.ErrTxDone })

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			return callbackErr
		})

		require.Same(t, callbackErr, err, "an already finalized transaction is a clean rollback")
	})

	scenario("rollback failure during a panic still releases the connection and preserves the panic", func(t *testing.T) {
		pool.inject(func(p *failingPool) { p.rollbackErr = errors.New("injected: rollback lost") })
		marker := &panicMarker{reason: "panic with broken rollback"}

		recovered := callAndRecover(func() {
			_ = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "doomed"))
				panic(marker)
			})
		})

		require.Same(t, marker, recovered)
		assertPoolUsable(t)
	})

	scenario("savepoint creation failure prevents the nested callback", func(t *testing.T) {
		savepointErr := errors.New("injected: savepoint refused")
		pool.inject(func(p *failingPool) { p.execHook = failAlways("SAVEPOINT", savepointErr) })
		innerCalled := false
		beginsBefore := pool.beginCount()

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				innerCalled = true
				return insertRecord(ctx, "inner")
			})
			require.ErrorIs(t, innerErr, ErrSavepointFailed)
			require.ErrorIs(t, innerErr, savepointErr)
			return nil
		})

		require.NoError(t, err)
		assert.False(t, innerCalled)
		assert.Equal(t, []string{"outer"}, persistedLabels(t, db))
		assert.Equal(t, beginsBefore+1, pool.beginCount(), "the nested call must not begin a second root transaction")
	})

	scenario("savepoint rollback failure is joined with the callback error", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback to savepoint refused")
		innerFailure := errors.New("inner unit rejected")
		pool.inject(func(p *failingPool) { p.execHook = failAlways("ROLLBACK TO SAVEPOINT", rollbackErr) })

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				return innerFailure
			})
			require.ErrorIs(t, innerErr, innerFailure)
			require.ErrorIs(t, innerErr, ErrRollbackFailed)
			require.ErrorIs(t, innerErr, rollbackErr)
			return innerErr
		})

		require.ErrorIs(t, err, innerFailure)
		require.ErrorIs(t, err, ErrRollbackFailed)
		assert.Empty(t, persistedLabels(t, db), "propagating the failure rolls the whole transaction back")
		assertPoolUsable(t)
	})

	scenario("a nested failure that destroyed the transaction still reaches the root retry", func(t *testing.T) {
		// InnoDB rolls the whole transaction back on a deadlock, so the
		// ROLLBACK TO SAVEPOINT that follows fails. The root retry must still
		// replay the unit from scratch; the second attempt runs clean.
		pool.inject(func(p *failingPool) {
			p.execHook = failOnce("ROLLBACK TO SAVEPOINT", errors.New("SAVEPOINT gocrm_sp does not exist"))
		})
		outerAttempts, innerCalls := 0, 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			outerAttempts++
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			return tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
				innerCalls++
				if err := insertRecord(ctx, "inner"); err != nil {
					return err
				}
				if innerCalls == 1 {
					return retryable
				}
				return nil
			}, 5)
		}, 3)

		require.NoError(t, err)
		assert.Equal(t, 2, outerAttempts)
		assert.Equal(t, 2, innerCalls)
		assert.Equal(t, []string{"inner", "outer"}, persistedLabels(t, db))
	})

	scenario("nested calls are refused when GORM disables nested transactions", func(t *testing.T) {
		strict, err := gorm.Open(&sqlite.Dialector{Conn: pool}, &gorm.Config{Logger: gormlogger.Discard, DisableNestedTransaction: true})
		require.NoError(t, err)
		strictTM := NewTransactionManager(strict)
		innerCalled := false

		err = strictTM.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := insertRecord(ctx, "outer"); err != nil {
				return err
			}
			innerErr := strictTM.WithTransaction(ctx, func(ctx context.Context) error {
				innerCalled = true
				return nil
			})
			require.ErrorIs(t, innerErr, ErrNestedTransactionsDisabled)
			return nil
		})

		require.NoError(t, err)
		assert.False(t, innerCalled)
		assert.Equal(t, []string{"outer"}, persistedLabels(t, db))
	})

	scenario("nested panic with a failing savepoint rollback still propagates the panic", func(t *testing.T) {
		pool.inject(func(p *failingPool) {
			p.execHook = failAlways("ROLLBACK TO SAVEPOINT", errors.New("injected: rollback to savepoint refused"))
		})
		marker := &panicMarker{reason: "nested panic"}

		recovered := callAndRecover(func() {
			_ = tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
				return tm.WithTransaction(ctx, func(ctx context.Context) error {
					require.NoError(t, insertRecord(ctx, "inner"))
					panic(marker)
				})
			})
		})

		require.Same(t, marker, recovered)
		assert.Empty(t, persistedLabels(t, db), "the root rollback discards the inner writes")
		assertPoolUsable(t)
	})

	// The scenarios below reproduce the integrity loss found by the independent
	// regression in transaction_recovery_test.go: a ROLLBACK TO SAVEPOINT that
	// fails must poison the transaction so that nothing a caller does afterwards
	// (recovering the panic, swallowing the inner error, returning nil) turns
	// the possibly retained inner writes into a commit.

	scenario("a recovered nested panic with a failed savepoint rollback poisons the root commit", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback to savepoint refused")
		pool.inject(func(p *failingPool) { p.execHook = failAlways("ROLLBACK TO SAVEPOINT", rollbackErr) })
		marker := &panicMarker{reason: "recovered by the outer callback"}
		beginsBefore := pool.beginCount()
		var recovered interface{}

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			require.NoError(t, insertRecord(ctx, "outer"))
			recovered = callAndRecover(func() {
				_ = tm.WithTransaction(ctx, func(ctx context.Context) error {
					require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
					panic(marker)
				})
			})
			return nil
		})

		require.Same(t, marker, recovered, "the outer callback recovers the identical panic value")
		require.Error(t, err, "a nil outer return cannot commit a transaction whose nested rollback failed")
		assert.ErrorIs(t, err, ErrTransactionPoisoned)
		assert.ErrorIs(t, err, ErrRollbackFailed)
		assert.ErrorIs(t, err, rollbackErr)
		assert.NotErrorIs(t, err, ErrCommitFailed, "the root never attempted the commit")
		assert.Equal(t, beginsBefore+1, pool.beginCount())
		assertPoolUsable(t)
	})

	scenario("a swallowed nested rollback failure cannot be committed by the outer callback", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback to savepoint refused")
		innerFailure := errors.New("inner unit rejected")
		pool.inject(func(p *failingPool) { p.execHook = failAlways("ROLLBACK TO SAVEPOINT", rollbackErr) })

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			require.NoError(t, insertRecord(ctx, "outer"))
			innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
				return innerFailure
			})
			require.ErrorIs(t, innerErr, innerFailure)
			require.ErrorIs(t, innerErr, ErrTransactionPoisoned, "the nested call already reports that the transaction cannot commit")
			require.ErrorIs(t, innerErr, rollbackErr)
			return nil // handled, as far as the caller knows
		})

		require.ErrorIs(t, err, ErrTransactionPoisoned)
		require.ErrorIs(t, err, ErrRollbackFailed)
		require.ErrorIs(t, err, rollbackErr)
		assert.NotErrorIs(t, err, innerFailure, "the callback did not return the inner error, so the root does not invent it")
		assertPoolUsable(t)
	})

	scenario("poison reaches the root through every nesting level with a single rollback attempt", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback to savepoint refused")
		hook, rollbackAttempts := failAlwaysCounting("ROLLBACK TO SAVEPOINT", rollbackErr)
		pool.inject(func(p *failingPool) { p.execHook = hook })
		marker := &panicMarker{reason: "level three exploded"}
		var levelTwoErr error

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			require.NoError(t, insertRecord(ctx, "l1"))
			levelTwoErr = tm.WithTransaction(ctx, func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "l2"))
				recovered := callAndRecover(func() {
					_ = tm.WithTransaction(ctx, func(ctx context.Context) error {
						require.NoError(t, insertRecord(ctx, "l3"))
						panic(marker)
					})
				})
				require.Same(t, marker, recovered)
				return nil // level two believes it handled the panic
			})
			return nil // level one believes level two succeeded
		})

		require.ErrorIs(t, levelTwoErr, ErrTransactionPoisoned, "an intermediate level cannot report success once poisoned")
		require.ErrorIs(t, levelTwoErr, rollbackErr)
		require.ErrorIs(t, err, ErrTransactionPoisoned)
		require.ErrorIs(t, err, rollbackErr)
		assert.Equal(t, int32(1), rollbackAttempts.Load(), "enclosing levels issue no further statements on a poisoned transaction")
		assertPoolUsable(t)
	})

	scenario("a poisoned transaction refuses further nested work before running the callback", func(t *testing.T) {
		rollbackErr := errors.New("injected: rollback to savepoint refused")
		pool.inject(func(p *failingPool) { p.execHook = failAlways("ROLLBACK TO SAVEPOINT", rollbackErr) })
		laterCalled := false

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			innerErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
				return errors.New("inner unit rejected")
			})
			require.ErrorIs(t, innerErr, ErrTransactionPoisoned)

			laterErr := tm.WithTransaction(ctx, func(ctx context.Context) error {
				laterCalled = true
				return insertRecord(ctx, "later")
			})
			require.ErrorIs(t, laterErr, ErrTransactionPoisoned)
			require.ErrorIs(t, laterErr, rollbackErr)
			return nil
		})

		require.ErrorIs(t, err, ErrTransactionPoisoned)
		assert.False(t, laterCalled, "no new nested unit may start on a poisoned transaction")
		assertPoolUsable(t)
	})

	scenario("a poisoned root whose own rollback also fails reports both failures", func(t *testing.T) {
		savepointRollbackErr := errors.New("injected: rollback to savepoint refused")
		rootRollbackErr := errors.New("injected: root rollback lost")
		pool.inject(func(p *failingPool) {
			p.execHook = failAlways("ROLLBACK TO SAVEPOINT", savepointRollbackErr)
			p.rollbackErr = rootRollbackErr
		})
		marker := &panicMarker{reason: "recovered by the outer callback"}

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			recovered := callAndRecover(func() {
				_ = tm.WithTransaction(ctx, func(ctx context.Context) error {
					require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
					panic(marker)
				})
			})
			require.Same(t, marker, recovered)
			return nil
		})

		require.ErrorIs(t, err, ErrTransactionPoisoned)
		require.ErrorIs(t, err, ErrRollbackFailed)
		require.ErrorIs(t, err, savepointRollbackErr)
		require.ErrorIs(t, err, rootRollbackErr)
		assert.NotErrorIs(t, err, ErrCommitFailed)
		assertPoolUsable(t)
	})

	scenario("the retry helper neither commits nor replays a swallowed poisoned attempt", func(t *testing.T) {
		// The inner rollback fails with a non-retryable error and the outer
		// callback swallows the deferred failure. Nothing retryable remains in
		// the root's error, so the attempt is not replayed; and because the
		// root rolled back cleanly, nothing is persisted either.
		pool.inject(func(p *failingPool) {
			p.execHook = failAlways("ROLLBACK TO SAVEPOINT", errors.New("SAVEPOINT gocrm_sp does not exist"))
		})
		outerAttempts, innerCalls := 0, 0

		err := tm.WithTransactionAndRetry(boundedContext(t), func(ctx context.Context) error {
			outerAttempts++
			require.NoError(t, insertRecord(ctx, "outer"))
			innerErr := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
				innerCalls++
				require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
				return retryable
			}, 5)
			require.ErrorIs(t, innerErr, ErrNestedRetryDeferred)
			require.ErrorIs(t, innerErr, ErrTransactionPoisoned)
			return nil // the outer unit wrongly treats the deferred failure as handled
		}, 3)

		require.ErrorIs(t, err, ErrTransactionPoisoned)
		require.ErrorIs(t, err, ErrRollbackFailed)
		assert.NotErrorIs(t, err, retryable, "the swallowed inner error is not resurrected")
		assert.Equal(t, 1, outerAttempts, "a poison error without retryable cause is not replayed")
		assert.Equal(t, 1, innerCalls)
		assertPoolUsable(t)
	})
}
