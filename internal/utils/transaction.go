package utils

import (
	"context"
	"errors"

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

// WithTransactionAndRetry executes fn within a transaction with retry logic
func (tm *TransactionManager) WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = tm.WithTransaction(ctx, fn)
		if err == nil {
			return nil
		}
	}
	return err
}

// GetTxFromContext retrieves the transaction from the context
func GetTxFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(txKey).(*gorm.DB)
	return tx, ok
}
