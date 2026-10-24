package aeo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrShuttingDown is returned by a run starter whose process is shutting down.
// No new run is admitted once the AEO runtime has begun draining; a manual
// request gets this error and the scheduler logs the skipped tick. It lives in
// this package rather than in the service so the scheduler can classify it
// without importing the service (which already imports this package).
var ErrShuttingDown = errors.New("the AEO runtime is shutting down; no new run is accepted")

// SchedulerClock is the scheduler's source of time. The production scheduler
// uses the system clock; tests substitute a clock whose timer they fire on
// demand, so a "due" tick arrives inside a test instead of at the configured
// hour.
type SchedulerClock interface {
	Now() time.Time
	// After returns a channel that receives once d has elapsed, plus a stop
	// function that releases the timer early (reporting whether it was still
	// pending), mirroring time.Timer.
	After(d time.Duration) (<-chan time.Time, func() bool)
}

// SystemClock is the SchedulerClock backed by the time package.
type SystemClock struct{}

// Now returns time.Now.
func (SystemClock) Now() time.Time { return time.Now() }

// After arms a time.Timer.
func (SystemClock) After(d time.Duration) (<-chan time.Time, func() bool) {
	timer := time.NewTimer(d)
	return timer.C, timer.Stop
}

// SchedulerOption customizes a Scheduler at construction time.
type SchedulerOption func(*Scheduler)

// WithSchedulerClock replaces the system clock. A nil clock keeps the default.
func WithSchedulerClock(clock SchedulerClock) SchedulerOption {
	return func(s *Scheduler) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// Scheduler owns the daily-run goroutine. It is armed whenever scheduling is
// enabled, independently of whether any engine has credentials at boot: the
// credentials are administrator-editable and are resolved by the run starter
// when a tick is due, so a key stored after startup is picked up by the next
// scheduled run without a restart.
//
// Start launches the loop and Wait joins it, so shutdown can prove the
// goroutine is gone before the database closes rather than merely asking it to
// stop.
type Scheduler struct {
	starter RunStarter
	hour    int
	clock   SchedulerClock

	startOnce sync.Once
	started   atomic.Bool
	done      chan struct{}
}

// NewScheduler builds a scheduler for the given starter and local hour. The
// loop is not running until Start is called.
func NewScheduler(starter RunStarter, hour int, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		starter: starter,
		hour:    hour,
		clock:   SystemClock{},
		done:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches the loop on its own goroutine and returns immediately. The
// loop exits when ctx is cancelled. Calling Start twice is a no-op; a nil
// receiver or a nil starter never arms anything and Wait returns at once.
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if s.starter == nil {
			logScheduler().Warn("AEO scheduler not started: no run starter")
			close(s.done)
			return
		}
		// Logged here, synchronously, so the operator sees the decision in
		// startup order and tests can assert on it without racing the goroutine.
		logScheduler().WithField("hour", s.hour).Info("AEO scheduler started")
		go func() {
			defer close(s.done)
			s.run(ctx)
		}()
	})
}

// Done is closed once the loop has exited (or was never armed).
func (s *Scheduler) Done() <-chan struct{} {
	if s == nil || !s.started.Load() {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

// Wait blocks until the loop has exited or ctx expires. It does not stop the
// loop itself: cancel the context passed to Start first. A scheduler that was
// never started returns immediately.
func (s *Scheduler) Wait(ctx context.Context) error {
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("aeo scheduler did not stop before the deadline: %w", ctx.Err())
	}
}

// run is the loop body. Every wait is computed from the scheduler's clock so a
// substituted clock controls both "when is the next run" and "has it arrived".
func (s *Scheduler) run(ctx context.Context) {
	for {
		now := s.clock.Now()
		next := NextRunAt(now, s.hour)
		logScheduler().WithField("next_run_at", next.Format(time.RFC3339)).
			Debug("AEO scheduler sleeping")

		wait, stop := s.clock.After(next.Sub(now))
		select {
		case <-ctx.Done():
			stop()
			logScheduler().Info("AEO scheduler stopped")
			return
		case <-wait:
		}

		triggerScheduledRun(ctx, s.starter)
	}
}
