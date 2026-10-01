package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ErrNoTransaction is returned when no transaction is found in the context
var ErrNoTransaction = errors.New("no transaction found in context")

type contextKey string

const txKey contextKey = "tx"

// TransactionManager manages database transactions
type TransactionManager struct {
	db *gorm.DB
}

// NewTransactionManager creates a new TransactionManager
func NewTransactionManager(db *gorm.DB) *TransactionManager {
	return &TransactionManager{db: db}
}

// WithTransaction executes fn within a database transaction
func (tm *TransactionManager) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	// Bind the caller's context so a cancelled or expired request does not sit
	// waiting for a connection. That wait is unbounded otherwise, and acute on
	// SQLite where the pool is capped at a single open connection.
	tx := tm.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}

	txCtx := context.WithValue(ctx, txKey, tx)
	if err := fn(txCtx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// WithTransactionAndRetry executes fn within a transaction with retry logic for retryable errors
func (tm *TransactionManager) WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error {
	var lastErr error
	attempts := 0

	for attempts = 0; attempts <= maxRetries; attempts++ {
		lastErr = tm.WithTransaction(ctx, fn)
		if lastErr == nil {
			return nil
		}
		// Only retry on retryable errors (deadlocks, lock timeouts)
		if !isRetryableError(lastErr) {
			return lastErr
		}
	}

	return fmt.Errorf("transaction failed after %d attempts: %w", attempts, lastErr)
}

// GetTxFromContext retrieves the transaction from the context
func GetTxFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(txKey).(*gorm.DB)
	return tx, ok
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
