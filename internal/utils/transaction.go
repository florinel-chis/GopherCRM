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
	tx := tm.db.Begin()
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
		"deadlock",
		"lock wait timeout",
		"error 1213",
		"error 1205",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

// ExecuteInTransaction executes multiple operations within a single transaction
func ExecuteInTransaction(ctx context.Context, tm *TransactionManager, operations ...func(ctx context.Context) error) error {
	return tm.WithTransaction(ctx, func(txCtx context.Context) error {
		for _, op := range operations {
			if err := op(txCtx); err != nil {
				return err
			}
		}
		return nil
	})
}

// ExecuteInTransactionWithRetry executes multiple operations within a transaction with retry logic
func ExecuteInTransactionWithRetry(ctx context.Context, tm *TransactionManager, maxRetries int, operations ...func(ctx context.Context) error) error {
	return tm.WithTransactionAndRetry(ctx, func(txCtx context.Context) error {
		for _, op := range operations {
			if err := op(txCtx); err != nil {
				return err
			}
		}
		return nil
	}, maxRetries)
}

// TransactionContext wraps a gorm.DB for use as a transaction context
type TransactionContext struct {
	db *gorm.DB
}

// NewTransactionContext creates a new TransactionContext
func NewTransactionContext(db *gorm.DB) *TransactionContext {
	return &TransactionContext{db: db}
}

// DB returns the underlying gorm.DB
func (tc *TransactionContext) DB() *gorm.DB {
	return tc.db
}
