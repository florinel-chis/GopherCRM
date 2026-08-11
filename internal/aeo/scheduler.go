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
type RunStarter interface {
	StartRun(ctx context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error)
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

// StartScheduler launches the daily run scheduler and returns immediately. The
// goroutine exits when ctx is cancelled.
func StartScheduler(ctx context.Context, starter RunStarter, hour int) {
	if starter == nil {
		logScheduler().Warn("AEO scheduler not started: no run starter")
		return
	}
	go schedulerLoop(ctx, starter, hour)
}

func schedulerLoop(ctx context.Context, starter RunStarter, hour int) {
	logScheduler().WithField("hour", hour).Info("AEO scheduler started")

	for {
		next := NextRunAt(time.Now(), hour)
		logScheduler().WithField("next_run_at", next.Format(time.RFC3339)).
			Debug("AEO scheduler sleeping")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			logScheduler().Info("AEO scheduler stopped")
			return
		case <-timer.C:
		}

		triggerScheduledRun(ctx, starter)
	}
}

// triggerScheduledRun starts one scheduled run. Every outcome — including a
// panic inside the service — is contained here so the loop always survives to
// schedule tomorrow.
func triggerScheduledRun(ctx context.Context, starter RunStarter) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logScheduler().WithField("panic", recovered).
				Error("AEO scheduled run panicked")
		}
	}()

	run, err := starter.StartRun(ctx, TriggerScheduled, nil)
	switch {
	case errors.Is(err, apperrors.ErrRunInProgress):
		// A manual run is still going. Skipping is correct: the overlap guard
		// exists precisely to keep provider spend bounded.
		logScheduler().Info("AEO scheduled run skipped: a run is already in progress")
	case err != nil:
		logScheduler().WithField("error", err.Error()).Error("AEO scheduled run failed to start")
	case run != nil:
		logScheduler().WithField("run_id", run.ID).Info("AEO scheduled run started")
	}
}

func logScheduler() *logrus.Entry {
	return logProvider().WithField("scheduler", AEOSchedulerName)
}
