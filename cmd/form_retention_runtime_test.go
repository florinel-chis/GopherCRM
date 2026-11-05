package main

// The retention purge through the real wiring: setupRouter over a SQLite file,
// exactly as main builds it, with the setting read by config.Load.

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const formRetentionEnabledLine = `"msg":"form retention purge enabled"`

// formRetentionDaysFromEnv reads the setting the way main does.
func formRetentionDaysFromEnv(t *testing.T) int {
	t.Helper()
	t.Setenv("JWT_SECRET", aeoBootSecret)
	t.Setenv("DB_DRIVER", config.DriverSQLite)
	loaded, err := config.Load()
	require.NoError(t, err)
	return loaded.Forms.RetentionDays
}

func seedOldSpamRow(t *testing.T) uint {
	t.Helper()
	form := &models.Form{
		Name:     "Retention form",
		PublicID: "retention-form-" + time.Now().Format("150405.000000000"),
		Status:   models.FormStatusPublished,
		Fields:   []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
	}
	require.NoError(t, models.DB.Create(form).Error)
	row := &models.FormSubmission{
		FormID: form.ID,
		Status: models.FormSubmissionSpam,
		Email:  "visitor@example.com",
		Data:   map[string]string{"email": "visitor@example.com"},
	}
	row.CreatedAt = time.Now().UTC().Add(-30 * 24 * time.Hour)
	require.NoError(t, models.DB.Create(row).Error)
	return row.ID
}

func formSubmissionExists(t *testing.T, id uint) bool {
	t.Helper()
	var count int64
	require.NoError(t, models.DB.Unscoped().Model(&models.FormSubmission{}).Where("id = ?", id).Count(&count).Error)
	return count == 1
}

func TestFormRetentionProductionWiring(t *testing.T) {
	t.Run("unset: the purge stays off", func(t *testing.T) {
		t.Setenv("FORM_RETENTION_DAYS", "")
		require.NoError(t, os.Unsetenv("FORM_RETENTION_DAYS"))
		days := formRetentionDaysFromEnv(t)
		require.Zero(t, days)

		boot := bootAutomation(t, false)
		boot.cfg.Forms.RetentionDays = days
		id := seedOldSpamRow(t)

		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))

		assert.True(t, formSubmissionExists(t, id), "the old spam row survives boot and drain")
		assert.Zero(t, boot.logs.count(formRetentionEnabledLine))
	})

	t.Run("set to 3: the backlog goes without a request and the handle drains it", func(t *testing.T) {
		t.Setenv("FORM_RETENTION_DAYS", "3")
		days := formRetentionDaysFromEnv(t)
		require.Equal(t, 3, days)

		boot := bootAutomation(t, false)
		boot.cfg.Forms.RetentionDays = days
		id := seedOldSpamRow(t)

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
		for formSubmissionExists(t, id) {
			require.True(t, time.Now().Before(deadline), "the row is removed without any request")
			time.Sleep(20 * time.Millisecond)
		}
		assert.Equal(t, 1, boot.logs.count(formRetentionEnabledLine))
		assert.Equal(t, 1, boot.logs.count(`"retention_days":3`))

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))
		stopped = true

		// A live context on purpose: the combined handle also holds the AEO drain,
		// which may report an ended context even with nothing left to drain. The
		// ended-context case belongs to FormRetentionRuntime alone and is pinned by
		// TestFormRetentionRuntimeLifecycle.
		again, cancelAgain := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancelAgain()
		require.NoError(t, stop(again), "the handle main receives can be called twice")
		assert.Equal(t, 1, boot.logs.count(formRetentionEnabledLine))
	})

	// Between ticks the runtime is idle, so only a chunk parked in flight can tell
	// a handle that joins the purge from one that never stops it: the handle must
	// still be waiting at its deadline, and must return nil once the chunk ends.
	t.Run("set to 3: the handle main receives joins a chunk in flight", func(t *testing.T) {
		t.Setenv("FORM_RETENTION_DAYS", "3")
		days := formRetentionDaysFromEnv(t)
		require.Equal(t, 3, days)

		boot := bootAutomation(t, false)
		boot.cfg.Forms.RetentionDays = days
		id := seedOldSpamRow(t)

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		releaseChunk := sync.OnceFunc(func() { close(release) })
		var parked atomic.Int32
		require.NoError(t, models.DB.Callback().Delete().Before("gorm:delete").Register("form_retention_wiring:park", func(tx *gorm.DB) {
			if tx.Statement.Table != "form_submissions" {
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
		// Registered last so it runs first: the drain above and the database close
		// must not wait on a chunk this root still holds.
		t.Cleanup(releaseChunk)

		select {
		case <-entered:
		case <-time.After(automationBootTimeout):
			t.Fatal("the boot sweep never reached the submission delete")
		}

		short, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancelShort()
		err := stop(short)
		require.Error(t, err, "the handle does not return nil with a chunk in flight")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorContains(t, err, "form retention runtime did not stop")

		releaseChunk()
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx), "the handle returns nil once the chunk has ended")

		// The drain cancelled the chunk before its delete ran: it rolled back, was
		// not replayed, and the sweep went no further.
		assert.True(t, formSubmissionExists(t, id), "the cancelled chunk rolled back")
		assert.EqualValues(t, 1, parked.Load(), "no further delete starts after the drain")
		assert.Equal(t, 1, boot.logs.count(`"msg":"form retention sweep interrupted"`))
	})

	t.Run("the wiring helper uses the daily interval and builds nothing when off", func(t *testing.T) {
		boot := bootAutomation(t, false)
		repo := repository.NewFormRepository(models.DB)
		txManager := utils.NewTransactionManager(models.DB)

		on := newFormRetentionBackground(config.FormsConfig{RetentionDays: 3}, repo, txManager)
		require.NotNil(t, on.runtime)
		assert.Equal(t, service.FormRetentionSweepInterval, on.runtime.Interval())

		off := newFormRetentionBackground(boot.cfg.Forms, repo, txManager)
		assert.Nil(t, off.runtime)
		off.start(context.Background())
		assert.NoError(t, off.Stop(context.Background()))
		assert.Zero(t, boot.logs.count(formRetentionEnabledLine))
	})
}
