package main

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// aeoDrainTimeout is how long shutdown waits for the AEO scheduler and every
// active run to finish after they have been cancelled. Provider calls abort as
// soon as their context is cancelled and the engine's finalizing write is
// itself bounded (aeo.EngineOptions.CleanupTimeout, 5s by default), so an
// honest drain takes well under this. When the deadline passes anyway — a
// database write that ignores cancellation, typically — main does not close the
// database handle under the work that is still using it: it exits and lets the
// next boot's reconciliation fail the rows left in "running".
const aeoDrainTimeout = 15 * time.Second

// aeoSchedulerClock is the scheduler's time source. It is a variable so the
// startup tests can deliver the configured hour on demand instead of waiting
// for it; production never reassigns it.
var aeoSchedulerClock aeo.SchedulerClock = aeo.SystemClock{}

// aeoBackground owns the AEO background work of this process: the daily
// scheduler, when enabled, and every run the service starts. Stop is what main
// calls at shutdown, through the handle setupDependencies returns.
type aeoBackground struct {
	runs *service.AEORuntime

	// scheduler is nil when scheduling is disabled: no goroutine and no timer
	// exist then, there is nothing to stop.
	scheduler     *aeo.Scheduler
	stopScheduler context.CancelFunc
}

// newAEORuntime wraps the service runtime. The scheduler is armed separately so
// setupDependencies can keep the enabled/disabled decision — and its log line —
// in one visible place.
func newAEORuntime(runs *service.AEORuntime) *aeoBackground {
	return &aeoBackground{runs: runs, stopScheduler: func() {}}
}

// startScheduler arms the daily run at hour (local time) on a context derived
// from ctx, so cancelling the process-wide background context stops it as
// before, and Stop can stop and join it on its own as well. It is armed
// regardless of which engines hold credentials right now: the service resolves
// the current providers — environment keys overlaid with the administrator's
// stored keys — when a tick is due, so a deployment that boots with no key and
// receives one through the settings UI uses it at the next due run without a restart. A
// tick that finds the module unconfigured is logged once and the loop sleeps
// until the next day.
func (r *aeoBackground) startScheduler(ctx context.Context, hour int) {
	schedulerCtx, cancel := context.WithCancel(ctx)
	r.stopScheduler = cancel
	r.scheduler = aeo.NewScheduler(r.runs.Service(), hour, aeo.WithSchedulerClock(aeoSchedulerClock))
	r.scheduler.Start(schedulerCtx)
}

// Stop cancels the scheduler, closes run admission and cancels owned work
// before waiting for either component. A scheduled start may itself be waiting
// for a database connection on the run owner's context, so waiting for the
// scheduler first would prevent that cancellation from reaching it.
// It returns nil once no owned work remains — the caller
// may then close the database — or ctx's error when the deadline passes with
// work still active. Repeated calls are safe and return nil once drained.
func (r *aeoBackground) Stop(ctx context.Context) error {
	r.stopScheduler()
	var errs []error
	if err := r.runs.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if r.scheduler != nil {
		if err := r.scheduler.Wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// shutdownFailure combines the HTTP drain and AEO drain outcomes into the one
// error main reports before exiting without closing the database.
func shutdownFailure(shutdownErr, drainErr error) error {
	return errors.Join(shutdownErr, drainErr)
}

// drainAEO runs the stop function main received from setupRouter under the
// drain deadline and reports whether the database may be closed. A failed
// drain is logged with the deadline so an operator reading the shutdown log
// knows why the process skipped the close; the limit is deliberate, not a bug.
func drainAEO(stop func(context.Context) error, timeout time.Duration) error {
	if stop == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	started := time.Now()
	if err := stop(ctx); err != nil {
		utils.Logger.WithError(err).WithField("deadline", timeout.String()).
			Warn("AEO background work did not drain before the shutdown deadline")
		return err
	}
	utils.Logger.WithField("duration", time.Since(started).String()).Info("AEO background work drained")
	return nil
}
