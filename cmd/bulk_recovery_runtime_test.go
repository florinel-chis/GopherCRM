package main

// The stale bulk operation sweep (#196) through the real wiring: setupRouter
// over a SQLite file, exactly as main builds it.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// seedBulkOperation writes one pending history row created at createdAt.
func seedBulkOperation(t *testing.T, createdAt time.Time) uint {
	t.Helper()
	var owner models.User
	if err := models.DB.Where("email = ?", "bulk-recovery@boot.test").First(&owner).Error; err != nil {
		owner = models.User{Email: "bulk-recovery@boot.test", Password: "x", FirstName: "Bulk", LastName: "Owner",
			Role: models.RoleAdmin, IsActive: true}
		require.NoError(t, models.DB.Create(&owner).Error)
	}
	row := &models.BulkOperation{UserID: owner.ID, ResourceType: "leads", Type: models.BulkAction,
		Status: models.StatusPending, TotalItems: 1}
	row.CreatedAt = createdAt
	row.UpdatedAt = createdAt
	require.NoError(t, models.DB.Create(row).Error)
	return row.ID
}

func bulkOperationStatus(t *testing.T, id uint) models.BulkOperationStatus {
	t.Helper()
	var row models.BulkOperation
	require.NoError(t, models.DB.First(&row, id).Error)
	return row.Status
}

func TestBulkOperationRecoveryProductionWiring(t *testing.T) {
	t.Run("the boot sweep fails a stale pending row without a request and leaves a fresh one", func(t *testing.T) {
		boot := bootAutomation(t, false)
		now := time.Now().UTC()
		stale := seedBulkOperation(t, now.Add(-service.BulkOperationStaleAfter-time.Hour))
		fresh := seedBulkOperation(t, now.Add(-time.Minute))

		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
				defer cancel()
				_ = stop(ctx)
			}
		})

		deadline := time.Now().Add(automationBootTimeout)
		for bulkOperationStatus(t, stale) != models.StatusFailed {
			require.True(t, time.Now().Before(deadline), "the stale row is failed at boot without any request")
			time.Sleep(20 * time.Millisecond)
		}
		assert.Equal(t, models.StatusPending, bulkOperationStatus(t, fresh), "a row younger than the horizon is left alone")

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))
		stopped = true
		// The joined sweep has logged its count, and nothing else.
		assert.Equal(t, 1, boot.logs.count(`"failed":1`))

		again, cancelAgain := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancelAgain()
		require.NoError(t, stop(again), "the handle main receives can be called twice")
		assert.Equal(t, models.StatusPending, bulkOperationStatus(t, fresh))
	})

	// Between ticks the runtime is idle, so only a sweep parked in flight can
	// tell a handle that joins the sweep from one that never stops it.
	t.Run("the handle main receives joins a sweep in flight", func(t *testing.T) {
		boot := bootAutomation(t, false)
		stale := seedBulkOperation(t, time.Now().UTC().Add(-service.BulkOperationStaleAfter-time.Hour))

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		releaseSweep := sync.OnceFunc(func() { close(release) })
		var parked atomic.Int32
		require.NoError(t, models.DB.Callback().Update().Before("gorm:update").Register("bulk_recovery_wiring:park", func(tx *gorm.DB) {
			if tx.Statement.Table != "bulk_operations" {
				return
			}
			parked.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}))

		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			_ = stop(ctx)
		})
		// Registered last so it runs first: the drain above and the database
		// close must not wait on a sweep this root still holds.
		t.Cleanup(releaseSweep)

		select {
		case <-entered:
		case <-time.After(automationBootTimeout):
			t.Fatal("the boot sweep never reached its update")
		}

		short, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancelShort()
		err := stop(short)
		require.Error(t, err, "the handle does not return nil with a sweep in flight")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorContains(t, err, "bulk operation recovery runtime did not stop")

		releaseSweep()
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx), "the handle returns nil once the sweep has ended")

		// The drain cancelled the sweep before its update ran, and nothing
		// started after it.
		assert.Equal(t, models.StatusPending, bulkOperationStatus(t, stale), "the cancelled update wrote nothing")
		assert.EqualValues(t, 1, parked.Load(), "no further update starts after the drain")
	})

	t.Run("the wiring helper sweeps every 15 minutes", func(t *testing.T) {
		bootAutomation(t, false)
		background := newBulkRecoveryBackground(repository.NewBulkOperationRepository(models.DB))
		assert.Equal(t, service.BulkOperationSweepInterval, background.runtime.Interval())
		assert.Equal(t, 15*time.Minute, service.BulkOperationSweepInterval)
		assert.Equal(t, time.Hour, service.BulkOperationStaleAfter)
		assert.NoError(t, background.Stop(context.Background()), "a runtime never started stops at once")
	})
}
