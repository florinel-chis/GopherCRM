package aeo

import (
	"context"
	"errors"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"

	"github.com/sirupsen/logrus"
)

// AEOSchedulerName identifies the scheduler goroutine in logs.
const AEOSchedulerName = "aeo-scheduler"

// RunStarter is the slice of the AEO service the scheduler needs. Depending on
// this instead of service.AEOService keeps internal/aeo free of an import cycle
// (the service already imports this package for Executor and Provider).
//
// StartScheduledRun receives the slot instant the scheduler computed with
// NextRunAt, never the time it woke up, so every replica sleeping towards the
// same configured hour names the same slot and only one of them starts it
// (apperrors.ErrAEOScheduleSlotTaken for the others).
type RunStarter interface {
	StartScheduledRun(ctx context.Context, slot time.Time) (*models.AEORun, error)
}

// NextRunAt returns the next occurrence of hour:00 in now's location, strictly
// after now. Hours outside 0..23 are clamped.
//
// The next-day case is built with time.Date rather than by adding 24 hours so
// that a DST transition still lands on the configured wall-clock hour.
func NextRunAt(now time.Time, hour int) time.Time {
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = 23
	}

	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, 0, 0, 0, now.Location())
	}
	return next
}

// StartScheduler launches the daily run scheduler on the system clock and
// returns its handle, which shutdown uses to join the goroutine. The goroutine
// exits when ctx is cancelled. A nil starter arms nothing and returns nil.
func StartScheduler(ctx context.Context, starter RunStarter, hour int) *Scheduler {
	if starter == nil {
		logScheduler().Warn("AEO scheduler not started: no run starter")
		return nil
	}
	scheduler := NewScheduler(starter, hour)
	scheduler.Start(ctx)
	return scheduler
}

// schedulerLoop runs the loop synchronously on the system clock until ctx is
// cancelled.
func schedulerLoop(ctx context.Context, starter RunStarter, hour int) {
	NewScheduler(starter, hour).run(ctx)
}

// triggerScheduledRun starts the scheduled run of slot. Every outcome —
// including a panic inside the service — is contained here so the loop always
// survives to schedule tomorrow.
//
// Incomplete configuration is an ordinary state of a scheduled deployment, not
// a fault: a profile not saved yet, no engine with credentials, or no active
// prompt. Each is logged once at info level and the loop sleeps until the next
// day; there is no retry and no error-level line, so an unconfigured module
// cannot flood the log a tick at a time. A slot another replica already
// started is the same kind of skip. No line carries the slot value.
func triggerScheduledRun(ctx context.Context, starter RunStarter, slot time.Time) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logScheduler().WithField("panic", recovered).
				Error("AEO scheduled run panicked")
		}
	}()

	run, err := starter.StartScheduledRun(ctx, slot)
	switch {
	case errors.Is(err, apperrors.ErrAEOScheduleSlotTaken):
		// Another replica (or an earlier start of this one) owns the slot.
		logScheduler().WithField("error_code", "schedule_slot_taken").
			Info("AEO scheduled run skipped: this slot was already started")
	case errors.Is(err, apperrors.ErrRunInProgress):
		// A manual run, on this or another replica, is still going. Skipping
		// is correct: the claim exists precisely to keep provider spend
		// bounded. The slot is not retried.
		logScheduler().Info("AEO scheduled run skipped: a run is already in progress")
	case errors.Is(err, ErrShuttingDown):
		logScheduler().Info("AEO scheduled run skipped: the runtime is shutting down")
	case errors.Is(err, apperrors.ErrProfileNotConfigured),
		errors.Is(err, apperrors.ErrNoProvidersConfigured),
		apperrors.IsNotFound(err):
		logScheduler().WithField("reason", err.Error()).
			Info("AEO scheduled run skipped: the module is not fully configured")
	case err != nil:
		logScheduler().WithField("error", err.Error()).Error("AEO scheduled run failed to start")
	case run != nil:
		logScheduler().WithField("run_id", run.ID).Info("AEO scheduled run started")
	}
}

func logScheduler() *logrus.Entry {
	return logProvider().WithField("scheduler", AEOSchedulerName)
}
