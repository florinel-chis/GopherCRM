package utils

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransactionRecoveredNestedPanicCannotCommitFailedRollback(t *testing.T) {
	pool, db := openFailingSQLite(t)
	manager := NewTransactionManager(db)
	rollbackErr := errors.New("injected savepoint rollback failure")
	pool.inject(func(p *failingPool) { p.execHook = failAlways("ROLLBACK TO SAVEPOINT", rollbackErr) })
	marker := &panicMarker{reason: "recover inside the outer callback"}
	err := manager.WithTransaction(boundedContext(t), func(ctx context.Context) error {
		require.NoError(t, insertRecord(ctx, "outer"))
		recovered := callAndRecover(func() {
			_ = manager.WithTransaction(ctx, func(ctx context.Context) error {
				require.NoError(t, insertRecord(ctx, "inner-must-not-commit"))
				panic(marker)
			})
		})
		require.Same(t, marker, recovered)
		return nil
	})
	assert.ErrorIs(t, err, rollbackErr, "an incomplete nested rollback must prevent the root commit even after recovery")
	pool.reset()
	assert.Empty(t, persistedLabels(t, db), "failed inner work must never become committed outer success")
	require.NoError(t, manager.WithTransaction(boundedContext(t), func(ctx context.Context) error { return insertRecord(ctx, "pool-reusable") }))
}
