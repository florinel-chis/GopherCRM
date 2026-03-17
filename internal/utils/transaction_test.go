package utils

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestTransactionManager_WithTransaction(t *testing.T) {
	db := setupTestDB(t)
	tm := NewTransactionManager(db)

	t.Run("successful transaction", func(t *testing.T) {
		ctx := context.Background()
		var executed bool

		err := tm.WithTransaction(ctx, func(ctx context.Context) error {
			tx, ok := GetTxFromContext(ctx)
			assert.True(t, ok)
			assert.NotNil(t, tx)
			executed = true
			return nil
		})

		assert.NoError(t, err)
		assert.True(t, executed)
	})

	t.Run("transaction rollback on error", func(t *testing.T) {
		ctx := context.Background()
		expectedErr := errors.New("test error")

		err := tm.WithTransaction(ctx, func(ctx context.Context) error {
			tx, ok := GetTxFromContext(ctx)
			assert.True(t, ok)
			assert.NotNil(t, tx)
			return expectedErr
		})

		assert.Equal(t, expectedErr, err)
	})

	t.Run("transaction timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		err := tm.WithTransaction(ctx, func(ctx context.Context) error {
			// Check if context is cancelled
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
				return nil
			}
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "context deadline exceeded")
	})
}

func TestTransactionManager_WithTransactionAndRetry(t *testing.T) {
	db := setupTestDB(t)
	tm := NewTransactionManager(db)

	t.Run("successful transaction on first attempt", func(t *testing.T) {
		ctx := context.Background()
		attempts := 0

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			return nil
		}, 3)

		assert.NoError(t, err)
		assert.Equal(t, 1, attempts)
	})

	t.Run("successful transaction after retries", func(t *testing.T) {
		ctx := context.Background()
		attempts := 0
		deadlockErr := errors.New("Deadlock found when trying to get lock")

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			if attempts < 3 {
				return deadlockErr
			}
			return nil
		}, 3)

		assert.NoError(t, err)
		assert.Equal(t, 3, attempts)
	})

	t.Run("failure after max retries", func(t *testing.T) {
		ctx := context.Background()
		attempts := 0
		deadlockErr := errors.New("Deadlock found when trying to get lock")

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			return deadlockErr
		}, 3)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "transaction failed after 4 attempts")
		assert.Equal(t, 4, attempts) // 1 initial + 3 retries
	})

	t.Run("non-retryable error fails immediately", func(t *testing.T) {
		ctx := context.Background()
		attempts := 0
		nonRetryableErr := errors.New("validation error")

		err := tm.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			attempts++
			return nonRetryableErr
		}, 3)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "validation error")
		assert.Equal(t, 1, attempts)
	})
}

func TestGetTxFromContext(t *testing.T) {
	t.Run("valid transaction in context", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.WithValue(context.Background(), "tx", db)

		tx, ok := GetTxFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, db, tx)
	})

	t.Run("no transaction in context", func(t *testing.T) {
		ctx := context.Background()

		tx, ok := GetTxFromContext(ctx)
		assert.False(t, ok)
		assert.Nil(t, tx)
	})

	t.Run("invalid transaction type in context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), "tx", "not a db")

		tx, ok := GetTxFromContext(ctx)
		assert.False(t, ok)
		assert.Nil(t, tx)
	})
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"deadlock error", errors.New("Deadlock found when trying to get lock"), true},
		{"lock timeout error", errors.New("Lock wait timeout exceeded"), true},
		{"mysql deadlock code", errors.New("Error 1213: Deadlock"), true},
		{"mysql lock timeout code", errors.New("Error 1205: Lock wait timeout"), true},
		{"generic deadlock", errors.New("deadlock detected"), true},
		{"validation error", errors.New("validation failed"), false},
		{"connection error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExecuteInTransaction(t *testing.T) {
	db := setupTestDB(t)
	tm := NewTransactionManager(db)

	t.Run("successful execution of multiple operations", func(t *testing.T) {
		ctx := context.Background()
		operation1Executed := false
		operation2Executed := false

		op1 := func(ctx context.Context) error {
			operation1Executed = true
			return nil
		}

		op2 := func(ctx context.Context) error {
			operation2Executed = true
			return nil
		}

		err := ExecuteInTransaction(ctx, tm, op1, op2)

		assert.NoError(t, err)
		assert.True(t, operation1Executed)
		assert.True(t, operation2Executed)
	})

	t.Run("rollback on operation failure", func(t *testing.T) {
		ctx := context.Background()
		operation1Executed := false
		operation2Executed := false
		expectedErr := errors.New("operation 2 failed")

		op1 := func(ctx context.Context) error {
			operation1Executed = true
			return nil
		}

		op2 := func(ctx context.Context) error {
			operation2Executed = true
			return expectedErr
		}

		err := ExecuteInTransaction(ctx, tm, op1, op2)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "operation 2 failed")
		assert.True(t, operation1Executed)
		assert.True(t, operation2Executed)
	})
}

func TestExecuteInTransactionWithRetry(t *testing.T) {
	db := setupTestDB(t)
	tm := NewTransactionManager(db)

	t.Run("successful execution with retry", func(t *testing.T) {
		ctx := context.Background()
		attempts := 0
		deadlockErr := errors.New("Deadlock found when trying to get lock")

		op := func(ctx context.Context) error {
			attempts++
			if attempts < 2 {
				return deadlockErr
			}
			return nil
		}

		err := ExecuteInTransactionWithRetry(ctx, tm, 3, op)

		assert.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})
}

func TestTransactionContext(t *testing.T) {
	db := setupTestDB(t)

	t.Run("create and use transaction context", func(t *testing.T) {
		txCtx := NewTransactionContext(db)
		assert.NotNil(t, txCtx)
		assert.Equal(t, db, txCtx.DB())
	})
}