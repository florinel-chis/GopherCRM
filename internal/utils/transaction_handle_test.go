package utils

// Regression roots for issue #107: TransactionManager.DB hands services a
// request-bound handle for the statements they run outside a managed
// callback. It must give up a pool wait when the request is cancelled, and
// join the caller's transaction instead of asking for a second connection.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTransactionManagerDBSQLite(t *testing.T) {
	runTransactionHandleScenarios(t, openTransactionSQLite(t))
}

func TestTransactionManagerDBMySQL(t *testing.T) {
	runTransactionHandleScenarios(t, openTransactionMySQL(t))
}

func runTransactionHandleScenarios(t *testing.T, db *gorm.DB) {
	tm := NewTransactionManager(db)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	t.Run("a cancelled request writes nothing", func(t *testing.T) {
		clearRecords(t, db)
		ctx, cancel := context.WithCancel(boundedContext(t))
		cancel()

		err := tm.DB(ctx).Create(&txRecord{Label: "never"}).Error

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("a request waiting for the only connection gives up when cancelled", func(t *testing.T) {
		clearRecords(t, db)
		holder := db.Begin()
		require.NoError(t, holder.Error)
		require.NoError(t, holder.Exec("SELECT 1").Error)
		released := false
		release := func() {
			if !released {
				released = true
				_ = holder.Rollback().Error
			}
		}
		defer release()

		waitsBefore := sqlDB.Stats().WaitCount
		ctx, cancel := context.WithCancel(boundedContext(t))
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- tm.DB(ctx).Create(&txRecord{Label: "queued"}).Error }()

		if !assert.Eventually(t, func() bool { return sqlDB.Stats().WaitCount > waitsBefore },
			scenarioTimeout, time.Millisecond, "the insert never queued for the connection") {
			cancel()
			release()
			<-done
			t.FailNow()
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			release()
			<-done
			t.Fatal("the cancelled insert kept waiting for the connection")
		}
		release()
		assert.Empty(t, persistedLabels(t, db))
	})

	t.Run("inside a managed transaction the handle joins it", func(t *testing.T) {
		clearRecords(t, db)
		failure := errors.New("refused after the write")
		waitsBefore := sqlDB.Stats().WaitCount

		err := tm.WithTransaction(boundedContext(t), func(ctx context.Context) error {
			if err := tm.DB(ctx).Create(&txRecord{Label: "joined"}).Error; err != nil {
				return err
			}
			return failure
		})

		require.ErrorIs(t, err, failure)
		assert.Equal(t, waitsBefore, sqlDB.Stats().WaitCount, "the handle must not wait for a second connection")
		assert.Empty(t, persistedLabels(t, db), "the write rolled back with the transaction it joined")
	})
}
