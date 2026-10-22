package utils

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

// ErrNoTransaction is returned when no transaction is found in the context
var ErrNoTransaction = errors.New("no transaction found in context")

// Sentinel errors describing how a unit of work failed. Every error returned
// by the manager can be classified with errors.Is: the callback's own error is
// always preserved (unwrapped on a clean rollback, joined otherwise), and a
// failure of BEGIN, SAVEPOINT, COMMIT or ROLLBACK is tagged with one of these.
var (
	// ErrInvalidRetryCount is returned by WithTransactionAndRetry for a
	// negative retry count. The callback is never invoked.
	ErrInvalidRetryCount = errors.New("transaction retry count must not be negative")
	// ErrBeginFailed tags a root transaction that could not be started: the
	// context was already done or BEGIN failed. The callback never ran and
	// nothing was written.
	ErrBeginFailed = errors.New("transaction could not be started")
	// ErrSavepointFailed tags a nested call whose savepoint could not be
	// created. The callback never ran; the enclosing transaction is untouched.
	ErrSavepointFailed = errors.New("transaction savepoint could not be created")
	// ErrCommitFailed tags a COMMIT that returned an error. The outcome is
	// ambiguous from the client's point of view (the server may or may not
	// have applied the work), which is why the retry helper never replays
	// an attempt that failed at commit.
	ErrCommitFailed = errors.New("transaction commit failed")
	// ErrRollbackFailed tags a ROLLBACK or ROLLBACK TO SAVEPOINT that returned
	// an error. It is joined to the error that triggered the rollback, so both
	// remain inspectable.
	ErrRollbackFailed = errors.New("transaction rollback failed")
	// ErrTransactionPoisoned tags a transaction in which a ROLLBACK TO
	// SAVEPOINT failed. The inner writes may still be present, so the
	// transaction can no longer be committed by anyone: every enclosing managed
	// call returns this error even when its callback recovered the panic or
	// swallowed the inner error, further nested calls are refused before their
	// callback runs, and the root rolls everything back instead of committing.
	// The error always also matches ErrRollbackFailed and the driver error.
	ErrTransactionPoisoned = errors.New("transaction cannot commit after a failed nested rollback")
	// ErrNestedTransactionsDisabled is returned when a nested call would need
	// a savepoint but the GORM connection disables nested transactions. The
	// callback is not run: silently joining the outer transaction would let a
	// failing inner unit leak its writes into the outer commit.
	ErrNestedTransactionsDisabled = errors.New("nested transaction refused: savepoints are disabled on this connection")
	// ErrNestedRetryDeferred tags a retryable failure that happened inside an
	// active transaction. WithTransactionAndRetry does not replay the callback
	// there: a deadlock on MySQL has already destroyed the enclosing
	// transaction, so the only safe retry is a fresh root attempt. The wrapped
	// error keeps its retryable classification for that enclosing retry.
	ErrNestedRetryDeferred = errors.New("retryable failure inside an active transaction is deferred to the enclosing transaction")
)

type contextKey string

const (
	txKey      contextKey = "tx"
	txStateKey contextKey = "tx-state"
)

// savepointSeq numbers savepoints so that nested calls, siblings and
// re-entrant calls on the same transaction never reuse an active name.
var savepointSeq atomic.Uint64

// txState is shared by every managed call on one transaction. It records the
// first failed savepoint rollback: from then on the transaction may still hold
// writes that should have been discarded, and nothing on it may commit.
type txState struct {
	mu    sync.Mutex
	cause error
}

// poison records the failed rollback. The first cause wins; later failures on
// an already poisoned transaction add nothing to the decision.
func (s *txState) poison(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause == nil {
		s.cause = cause
	}
}

// poisoned returns the recorded cause, or nil while the transaction is sound.
func (s *txState) poisoned() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

// poisonResult combines a callback's outcome with the poison that forbids a
// commit. The result always matches ErrTransactionPoisoned, the cause (which
// carries ErrRollbackFailed and the driver error) and the callback's error.
// A cause the error already carries is not repeated.
func poisonResult(err, cause error) error {
	switch {
	case err == nil:
		return fmt.Errorf("%w: %w", ErrTransactionPoisoned, cause)
	case errors.Is(err, cause) && errors.Is(err, ErrTransactionPoisoned):
		return err
	case errors.Is(err, cause):
		return errors.Join(err, ErrTransactionPoisoned)
	default:
		return errors.Join(err, fmt.Errorf("%w: %w", ErrTransactionPoisoned, cause))
	}
}

// TransactionManager manages database transactions.
//
// Semantics, in short:
//
//   - A call whose context carries no transaction begins one on the root DB,
//     runs fn with the transaction in the context, commits on nil and rolls
//     back on error. The caller's context is bound to BEGIN and to every
//     statement, so a dead context never waits for a pooled connection.
//   - A call whose context already carries an active transaction (one started
//     by this manager, or any *gorm.DB bound to a TxCommitter) reuses it under
//     a SAVEPOINT. It never acquires a second root connection, which would
//     deadlock on SQLite's single-connection pool. The inner unit is rolled
//     back to its savepoint on error and stays provisional on success: the
//     outermost call decides whether anything is committed.
//   - A panic (or runtime.Goexit) unwinding through fn rolls the transaction
//     or savepoint back and continues to propagate untouched, so a caller's
//     recover sees the identical panic value.
//   - Finalization failures are reported, never hidden: the callback's error
//     is preserved and ErrBeginFailed, ErrSavepointFailed, ErrCommitFailed and
//     ErrRollbackFailed tag what went wrong.
//   - A failed ROLLBACK TO SAVEPOINT poisons the whole transaction
//     (ErrTransactionPoisoned). The inner writes may still be there, so a
//     caller that recovers the panic or swallows the inner error cannot turn
//     them into a commit: every enclosing managed call returns the error, new
//     nested calls are refused before their callback runs, and the root rolls
//     back instead of committing.
type TransactionManager struct {
	db *gorm.DB
}

// NewTransactionManager creates a new TransactionManager
func NewTransactionManager(db *gorm.DB) *TransactionManager {
	return &TransactionManager{db: db}
}

// WithTransaction executes fn within a database transaction. See
// TransactionManager for the root, nested, panic and failure semantics.
func (tm *TransactionManager) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if tx := activeTransaction(ctx); tx != nil {
		return tm.runNested(ctx, tx, fn)
	}
	_, err := tm.runRoot(ctx, fn)
	return err
}

// WithTransactionAndRetry executes fn within a transaction with retry logic
// for retryable errors (deadlocks, lock waits, SQLite busy/locked).
//
// Each attempt is a fresh root transaction, so a failed attempt leaves no
// writes behind. An attempt is replayed only when its rollback completed
// cleanly: a commit or rollback failure is ambiguous and is returned as is.
// Retries stop once ctx is done and after maxRetries replays. A negative
// maxRetries is rejected without running fn.
//
// Inside an active transaction the callback runs exactly once under a
// savepoint; a retryable failure is rolled back to that savepoint and returned
// tagged with ErrNestedRetryDeferred so that the enclosing root attempt, if
// it retries, replays the whole unit instead of committing partial state.
func (tm *TransactionManager) WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error {
	if maxRetries < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidRetryCount, maxRetries)
	}

	if tx := activeTransaction(ctx); tx != nil {
		err := tm.runNested(ctx, tx, fn)
		if err != nil && isRetryableError(err) {
			return fmt.Errorf("%w: %w", ErrNestedRetryDeferred, err)
		}
		return err
	}

	attempts := 0
	for {
		attempts++
		replayable, err := tm.runRoot(ctx, fn)
		if err == nil {
			return nil
		}
		if !replayable || !isRetryableError(err) {
			return err
		}
		if attempts > maxRetries {
			return fmt.Errorf("transaction failed after %d attempts: %w", attempts, err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("transaction retry abandoned after %d attempts: %w", attempts, errors.Join(ctxErr, err))
		}
	}
}

// GetTxFromContext retrieves the transaction from the context
func GetTxFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(txKey).(*gorm.DB)
	return tx, ok
}

// runRoot begins a transaction on the root DB and runs fn inside it. The
// boolean reports whether the attempt ended with a clean rollback of a
// callback error, i.e. whether a retry helper may safely replay it.
func (tm *TransactionManager) runRoot(ctx context.Context, fn func(ctx context.Context) error) (replayable bool, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("%w: %w", ErrBeginFailed, ctxErr)
	}

	// Bind the caller's context so a cancelled or expired request does not sit
	// waiting for a connection. That wait is unbounded otherwise, and acute on
	// SQLite where the pool is capped at a single open connection.
	tx := tm.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return false, fmt.Errorf("%w: %w", ErrBeginFailed, tx.Error)
	}

	state := &txState{}
	finished := false
	defer func() {
		if finished {
			return
		}
		// fn is unwinding through a panic or runtime.Goexit. Release the
		// connection now; the panic itself is not recovered here, so it
		// reaches the caller with its identical value and stack. A rollback
		// error has no return path on this route and is dropped.
		_ = rollbackTx(tx)
	}()
	txCtx := context.WithValue(context.WithValue(ctx, txKey, tx), txStateKey, state)
	err = fn(txCtx)
	finished = true

	// A nested rollback that failed somewhere below leaves the transaction
	// holding writes that should be gone. Whatever the callback returned, the
	// only safe outcome is a root rollback; a nil return is turned into the
	// poison error so a recovered panic or swallowed inner error can never
	// become a commit.
	if cause := state.poisoned(); cause != nil {
		err = poisonResult(err, cause)
	}

	if err != nil {
		if rbErr := rollbackTx(tx); rbErr != nil {
			return false, errors.Join(err, fmt.Errorf("%w: %w", ErrRollbackFailed, rbErr))
		}
		// The root ROLLBACK discarded everything, poisoned or not, so a
		// retry helper may replay the attempt from scratch.
		return true, err
	}

	if commitErr := commitTx(tx); commitErr != nil {
		// A commit refused because ctx ended reports either the context
		// error or sql.ErrTxDone, depending on whether database/sql's own
		// rollback ran first. Attach the context error so callers can always
		// classify the cancellation.
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(commitErr, ctxErr) {
			commitErr = fmt.Errorf("%w: %w", ctxErr, commitErr)
		}
		err = fmt.Errorf("%w: %w", ErrCommitFailed, commitErr)
		// database/sql has usually finalized the transaction already (the
		// rollback then reports ErrTxDone and is ignored). When the commit
		// was refused before reaching the driver, e.g. because the context
		// ended, this releases the connection instead of leaving that to a
		// background goroutine.
		if rbErr := rollbackTx(tx); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %w", ErrRollbackFailed, rbErr))
		}
		return false, err
	}
	return false, nil
}

// runNested runs fn under a savepoint of the transaction already carried by
// ctx. The savepoint is created with the caller's context, but rolled back
// with the enclosing transaction's context: cancelling the inner context must
// not prevent the inner writes from being discarded.
//
// A ROLLBACK TO SAVEPOINT that fails poisons the shared transaction state. A
// poisoned transaction issues no further statements from the manager: new
// nested calls are refused, enclosing levels skip their own savepoint rollback
// and return the poison, and the managed root performs the single ROLLBACK
// that discards everything.
func (tm *TransactionManager) runNested(ctx context.Context, tx *gorm.DB, fn func(ctx context.Context) error) (err error) {
	state, _ := ctx.Value(txStateKey).(*txState)
	ownsState := state == nil
	if ownsState {
		// The transaction was not begun by this manager. The state created
		// here still binds every managed call below this one, so a deeper
		// failed rollback reaches the owner through this call's error; the
		// owner is then responsible for rolling back.
		state = &txState{}
	}
	if cause := state.poisoned(); cause != nil {
		return poisonResult(nil, cause)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w: %w", ErrSavepointFailed, ctxErr)
	}
	if tx.DisableNestedTransaction {
		return ErrNestedTransactionsDisabled
	}

	name := fmt.Sprintf("gocrm_sp_%d", savepointSeq.Add(1))
	inner := tx.WithContext(ctx)
	// The statements are issued directly rather than through the dialector's
	// SavePoint/RollbackTo helpers: the SQLite dialector discards their
	// errors, and a savepoint that silently failed would turn the inner unit
	// into a bare join of the outer transaction. MySQL/InnoDB and SQLite share
	// this syntax.
	if execErr := inner.Exec("SAVEPOINT " + name).Error; execErr != nil {
		return fmt.Errorf("%w: %w", ErrSavepointFailed, execErr)
	}

	// rollback discards this level's writes. When that fails the transaction
	// is poisoned: the writes may still be there and no one may commit them.
	// A transaction already poisoned by a deeper level is left alone; its
	// state is unknown and the root ROLLBACK is the cleanup.
	rollback := func() {
		if state.poisoned() != nil {
			return
		}
		if rbErr := rollbackToSavepoint(tx, name); rbErr != nil {
			state.poison(fmt.Errorf("%w: %w", ErrRollbackFailed, rbErr))
		}
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		// Panic or runtime.Goexit: discard the inner writes and let the
		// panic continue to whoever recovers it. A failed rollback has no
		// return path here, but the poison it recorded reaches whoever
		// recovers through the enclosing calls' return values.
		rollback()
	}()
	txCtx := context.WithValue(ctx, txKey, inner)
	if ownsState {
		txCtx = context.WithValue(txCtx, txStateKey, state)
	}
	err = fn(txCtx)
	finished = true

	if err != nil {
		rollback()
	}
	if cause := state.poisoned(); cause != nil {
		// Either this level's rollback failed, or a deeper one did and the
		// callback handled or swallowed it. Neither may look like success.
		return poisonResult(err, cause)
	}
	// Success leaves the savepoint in place; it is released with the outer
	// transaction. The inner writes remain provisional until the root commits.
	return err
}

// activeTransaction returns the transaction carried by ctx, or nil when the
// context holds none or holds a handle that is not inside a transaction.
func activeTransaction(ctx context.Context) *gorm.DB {
	tx, ok := ctx.Value(txKey).(*gorm.DB)
	if !ok || tx == nil || tx.Statement == nil {
		return nil
	}
	if committer, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok || committer == nil {
		return nil
	}
	return tx
}

// committerOf returns the transaction behind tx. The GORM Commit/Rollback
// helpers are bypassed because they accumulate every error on the shared
// handle; a rollback issued after a failed commit would otherwise report both.
func committerOf(tx *gorm.DB) (gorm.TxCommitter, error) {
	committer, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	if !ok || committer == nil {
		return nil, gorm.ErrInvalidTransaction
	}
	return committer, nil
}

func commitTx(tx *gorm.DB) error {
	committer, err := committerOf(tx)
	if err != nil {
		return err
	}
	return committer.Commit()
}

// rollbackTx rolls the root transaction back. sql.ErrTxDone means database/sql
// already finalized it, which happens when the bound context ends; the work is
// discarded either way, so that is not reported as a rollback failure.
func rollbackTx(tx *gorm.DB) error {
	committer, err := committerOf(tx)
	if err != nil {
		return err
	}
	if err := committer.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

func rollbackToSavepoint(tx *gorm.DB, name string) error {
	return tx.Exec("ROLLBACK TO SAVEPOINT " + name).Error
}

// isRetryableError checks if an error is retryable (e.g., deadlocks, lock timeouts)
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := strings.ToLower(err.Error())
	retryablePatterns := []string{
		// MySQL
		"deadlock",
		"lock wait timeout",
		"error 1213",
		"error 1205",
		// SQLite: write contention surfaces as SQLITE_BUSY/SQLITE_LOCKED, which
		// clears once the holding connection commits, so it is worth retrying.
		"database is locked",
		"database table is locked",
		"sqlite_busy",
		"sqlite_locked",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}
