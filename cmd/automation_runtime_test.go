package main

// AC-15 through the real wiring: setupRouter builds every repository, service
// and handler over a SQLite file exactly as main does, reconciles stranded
// runs and starts the automation worker only when AUTOMATION_ENABLED is on,
// and returns the drain handle main calls before closing the database. A
// process without the worker owns no run, so it must not reconcile either:
// under the single-replica assumption the worker replica is the only one
// that may touch a run left in running.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const automationBootTimeout = 20 * time.Second

// automationBoot is one booted process with the automation worker.
type automationBoot struct {
	cfg  *config.Config
	logs *lockedLogBuffer
	repo repository.AutomationRepository
}

// bootAutomation performs the database half of main's startup over a fresh
// SQLite file with the AEO scheduler off and the automation worker as
// configured, polling every second so a root waits little. The router (and
// with it the reconciliation and the worker) is built by the root itself, so
// it can seed rows before setupRouter runs.
func bootAutomation(t *testing.T, enabled bool) *automationBoot {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	logs := &lockedLogBuffer{}
	utils.Logger.SetOutput(logs)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })

	cfg := cfgForBoot(t, enabled)
	require.NoError(t, models.InitDatabase(&cfg.Database))
	t.Cleanup(func() { _ = models.CloseDatabase() })
	require.NoError(t, models.MigrateDatabase())
	require.NoError(t, repository.NewConfigurationRepository(models.DB).InitializeDefaults())
	return &automationBoot{cfg: cfg, logs: logs, repo: repository.NewAutomationRepository(models.DB)}
}

// strandedRun leaves a run in running with an unconfirmed email, as a
// crashed process would, before the router is built.
func strandedRun(t *testing.T, repo repository.AutomationRepository, rule *models.AutomationRule, leadID uint) *models.AutomationRun {
	t.Helper()
	run := &models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: "lead", EntityID: leadID, DedupeKey: "stranded", Status: models.AutomationRunPending, NextPosition: 1}
	inserted, err := repo.CreateRunIgnoringDuplicate(run)
	require.NoError(t, err)
	require.True(t, inserted)
	rows, err := repo.ClaimRun(run.ID, models.AutomationRunPending, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, repo.CreateRunStep(&models.AutomationRunStep{RunID: run.ID, Position: 1, StepType: models.AutomationStepSendEmail, Outcome: models.AutomationOutcomeSending, ExecutedAt: time.Now()}))
	return run
}

func seedAutomationFixture(t *testing.T, repo repository.AutomationRepository) (*models.AutomationRule, *models.Lead) {
	t.Helper()
	owner := &models.User{Email: "owner@automation-boot.test", Password: "x", FirstName: "Own", LastName: "Er", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(owner).Error)
	lead := &models.Lead{FirstName: "Boot", LastName: "Lead", Email: "lead@automation-boot.test", OwnerID: owner.ID, Status: models.LeadStatusNew}
	require.NoError(t, models.DB.Create(lead).Error)
	rule := &models.AutomationRule{Name: "Boot rule", EntityType: "lead", TriggerType: "lead_created", Enabled: true, Version: 1,
		Steps: []models.AutomationStep{{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: "Welcome {{lead.id}}", Recipient: models.AutomationRecipientOwner}}}}
	require.NoError(t, repo.CreateRule(rule))
	return rule, lead
}

// awaitGoroutines polls from the test goroutine itself until the process
// holds no more goroutines than before the router was built. It must not use
// assert.Eventually: that evaluates its condition on a goroutine of its own,
// which the count would include.
func awaitGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(automationBootTimeout)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a drained runtime holds no goroutine: %d running, %d before the router was built", current, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func automationRun(t *testing.T, id uint) models.AutomationRun {
	t.Helper()
	var run models.AutomationRun
	require.NoError(t, models.DB.First(&run, id).Error)
	return run
}

func TestAutomationRuntimeLifecycle(t *testing.T) {
	t.Run("enabled: reconcile at boot, the worker runs rules and the drain leaves no goroutine", func(t *testing.T) {
		boot := bootAutomation(t, true)
		rule, lead := seedAutomationFixture(t, boot.repo)
		stranded := strandedRun(t, boot.repo, rule, lead.ID)

		baseline := runtime.NumGoroutine()
		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			_ = stop(ctx)
		})

		// Reconciliation ran before the worker: the unconfirmed email failed
		// the stranded run, and the log says so before the worker started.
		recovered := automationRun(t, stranded.ID)
		assert.Equal(t, models.AutomationRunFailed, recovered.Status)
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, recovered.ErrorCode)
		assert.Equal(t, 1, boot.logs.count(`"msg":"Recovered automation runs left behind by a previous process"`))
		assert.Equal(t, 1, boot.logs.count(`"msg":"Automation worker started"`))
		assert.Equal(t, 0, boot.logs.count(`"msg":"Automation worker not started"`))

		// A lead event recorded while the process runs becomes a completed run
		// with its notification within a few polls.
		require.NoError(t, boot.repo.RecordEvent(&models.AutomationEvent{EntityType: "lead", EntityID: lead.ID, EventType: "lead_created", Origin: "user"}))
		require.Eventually(t, func() bool {
			var runs []models.AutomationRun
			if err := models.DB.Where("rule_id = ? AND status = ?", rule.ID, models.AutomationRunCompleted).Find(&runs).Error; err != nil {
				return false
			}
			return len(runs) == 1
		}, automationBootTimeout, 50*time.Millisecond, "the worker executed the run")
		var notifications int64
		require.NoError(t, models.DB.Model(&models.Notification{}).Count(&notifications).Error)
		assert.EqualValues(t, 1, notifications)

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))
		assert.Equal(t, 1, boot.logs.count(`"msg":"Automation worker stopped"`))
		awaitGoroutines(t, baseline)

		// Nothing is written after Stop returned: an event recorded now stays
		// unprocessed and starts no run (the stranded run reconciled at boot is
		// the rule's only other run).
		require.NoError(t, boot.repo.RecordEvent(&models.AutomationEvent{EntityType: "lead", EntityID: lead.ID, EventType: "lead_created", Origin: "user"}))
		time.Sleep(1500 * time.Millisecond)
		events, err := boot.repo.ListUnprocessedEvents(10)
		require.NoError(t, err)
		assert.Len(t, events, 1)
		var runs int64
		require.NoError(t, models.DB.Model(&models.AutomationRun{}).Where("rule_id = ? AND id <> ?", rule.ID, stranded.ID).Count(&runs).Error)
		assert.EqualValues(t, 1, runs)
		require.NoError(t, stop(ctx), "stopping again is a no-op")
		require.NoError(t, models.CloseDatabase())
	})

	t.Run("disabled: no worker, no reconciliation, no goroutine, the API still serves", func(t *testing.T) {
		boot := bootAutomation(t, false)
		rule, lead := seedAutomationFixture(t, boot.repo)
		stranded := strandedRun(t, boot.repo, rule, lead.ID)
		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			_ = stop(ctx)
		})
		assert.Equal(t, 1, boot.logs.count(`"msg":"Automation worker not started"`))
		assert.Equal(t, 0, boot.logs.count(`"msg":"Automation worker started"`))

		// A replica without the worker owns no run and leaves the worker
		// replica's runs alone: the run in running with its email in sending
		// is exactly as it was, and nothing was logged about recovering it.
		untouched := automationRun(t, stranded.ID)
		assert.Equal(t, models.AutomationRunRunning, untouched.Status, "a non-worker process does not reconcile")
		assert.Empty(t, untouched.ErrorCode)
		var sendingRows int64
		require.NoError(t, models.DB.Model(&models.AutomationRunStep{}).
			Where("run_id = ? AND outcome = ?", stranded.ID, models.AutomationOutcomeSending).Count(&sendingRows).Error)
		assert.EqualValues(t, 1, sendingRows, "the sending row is left for the worker replica to finalize")
		assert.Equal(t, 0, boot.logs.count(`"msg":"Recovered automation runs left behind by a previous process"`))

		require.NoError(t, boot.repo.RecordEvent(&models.AutomationEvent{EntityType: "lead", EntityID: lead.ID, EventType: "lead_created", Origin: "user"}))
		time.Sleep(1500 * time.Millisecond)
		events, err := boot.repo.ListUnprocessedEvents(10)
		require.NoError(t, err)
		assert.Len(t, events, 1, "the event is recorded but nothing dispatches it")
		var runs int64
		require.NoError(t, models.DB.Model(&models.AutomationRun{}).Where("rule_id = ? AND id <> ?", rule.ID, stranded.ID).Count(&runs).Error)
		assert.Zero(t, runs)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, stop(ctx), "a process without a worker drains immediately")
		assert.Equal(t, 0, boot.logs.count(`"msg":"Automation worker stopped"`))
	})

	t.Run("combineStops runs every handle and joins their errors", func(t *testing.T) {
		calls := 0
		stop := combineStops(nil, func(context.Context) error { calls++; return nil }, func(context.Context) error { calls++; return context.DeadlineExceeded })
		err := stop(context.Background())
		assert.Equal(t, 2, calls)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NoError(t, combineStops()(context.Background()))
		assert.NoError(t, newAutomationBackground(nil).Stop(context.Background()))
	})
}

// cfgForBoot is the configuration bootAutomation opened the database with,
// rebuilt for setupRouter (which reads only the non-database settings).
func cfgForBoot(t *testing.T, enabled bool) *config.Config {
	t.Helper()
	return &config.Config{
		Database: config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(t.TempDir(), "unused.db"), LogLevel: "warn"},
		Server:   config.ServerConfig{Mode: "development"},
		JWT:      config.JWTConfig{Secret: aeoBootSecret, ExpiryHours: 24, AccessTokenMinutes: 15, RefreshTokenDays: 30, CookieSameSite: "Lax"},
		Logging:  config.LoggingConfig{Level: "debug", Format: "json"},
		API:      config.APIConfig{Prefix: "/api/v1", APIKeySecret: aeoBootSecret},
		SMTP:     config.SMTPConfig{From: "no-reply@localhost"},
		App:      config.AppConfig{BaseURL: "http://localhost:5173"},
		AEO:      config.AEOConfig{ScheduleEnabled: false, ScheduleHour: 6, QueryTimeoutSeconds: 30},
		Forms:    config.FormsConfig{PublicBaseURL: "http://localhost:8080"},
		Automation: config.AutomationConfig{
			Enabled:             enabled,
			PollIntervalSeconds: 1,
		},
	}
}
