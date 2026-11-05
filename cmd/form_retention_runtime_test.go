package main

// The retention purge through the real wiring: setupRouter over a SQLite file,
// exactly as main builds it, with the setting read by config.Load.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

		// The runtime has stopped: a row that is due stays until the next boot.
		later := seedOldSpamRow(t)
		time.Sleep(100 * time.Millisecond)
		assert.True(t, formSubmissionExists(t, later))

		// A live context on purpose: the combined handle also holds the AEO drain,
		// which may report an ended context even with nothing left to drain. The
		// ended-context case belongs to FormRetentionRuntime alone and is pinned by
		// TestFormRetentionRuntimeLifecycle.
		again, cancelAgain := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancelAgain()
		require.NoError(t, stop(again), "the handle main receives can be called twice")
		assert.Equal(t, 1, boot.logs.count(formRetentionEnabledLine))
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
