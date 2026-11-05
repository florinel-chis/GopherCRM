package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// FormRetentionSweepInterval is how often the running process purges form
// submissions past their retention.
const FormRetentionSweepInterval = 24 * time.Hour

// FormRetentionSweeper is the work the runtime schedules.
type FormRetentionSweeper interface {
	Sweep(ctx context.Context) (FormPurgeResult, error)
}

// FormRetentionRuntime owns the goroutine that sweeps once at Start and then on
// every interval or Wake. main drains it before closing the database: Stop
// cancels the sweep, which rolls back a chunk still in flight and starts no
// further one, and joins the goroutine under the caller's deadline. A runtime
// runs once: Start after Stop does nothing, so a drained process cannot begin
// deleting again.
type FormRetentionRuntime struct {
	sweeper  FormRetentionSweeper
	interval time.Duration

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	// exited is closed by the goroutine when it returns; nil until Start. A
	// closed or nil channel makes a repeated Stop a no-op whatever its context.
	exited chan struct{}
	wake   chan struct{}
}

// NewFormRetentionRuntime wraps a sweeper with a goroutine that ticks every
// interval. A non-positive interval means FormRetentionSweepInterval.
func NewFormRetentionRuntime(sweeper FormRetentionSweeper, interval time.Duration) *FormRetentionRuntime {
	if interval <= 0 {
		interval = FormRetentionSweepInterval
	}
	return &FormRetentionRuntime{sweeper: sweeper, interval: interval, wake: make(chan struct{}, 1)}
}

// Interval is the time between two scheduled sweeps.
func (r *FormRetentionRuntime) Interval() time.Duration { return r.interval }

// Start sweeps once immediately on a goroutine derived from ctx, so either the
// process-wide background cancel or Stop ends it. Starting twice, or after Stop,
// is a no-op.
func (r *FormRetentionRuntime) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	sweepCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	exited := make(chan struct{})
	r.exited = exited
	go r.loop(sweepCtx, exited)
}

func (r *FormRetentionRuntime) loop(ctx context.Context, exited chan<- struct{}) {
	defer close(exited)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		if ctx.Err() != nil {
			return
		}
		r.sweep(ctx)
	}
}

// sweep runs one pass and logs its counts. A failure is logged and the next tick
// tries again. Only counts reach the log: the purge's statements bind ids and
// timestamps, and utils.LogError cuts any quoted value from a driver's message.
func (r *FormRetentionRuntime) sweep(ctx context.Context) {
	result, err := r.sweeper.Sweep(ctx)
	entry := utils.Logger.WithFields(logrus.Fields{
		"spam_removed":    result.Spam,
		"pending_removed": result.Pending,
		"tokens_removed":  result.Tokens,
		"chunks":          result.Chunks,
	})
	switch {
	case err == nil:
		entry.Info("form retention sweep completed")
	case ctx.Err() != nil:
		entry.Info("form retention sweep interrupted")
	default:
		entry.WithError(utils.LogError(err)).Warn("form retention sweep failed")
	}
}

// Wake asks for a sweep now. It does nothing when no goroutine runs.
func (r *FormRetentionRuntime) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Stop cancels the sweep and waits for the goroutine to return, or for ctx to
// end. A chunk in flight finishes or rolls back before Stop returns nil, and
// after that no row changes. A deadline that passes first is an error, and the
// chunk may then still be running. Repeated calls are safe: once the goroutine
// has joined, Stop returns nil even on a context that has already ended.
func (r *FormRetentionRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.stopped = true
	cancel := r.cancel
	exited := r.exited
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if exited == nil {
		return nil
	}
	select {
	case <-exited:
		return nil
	default:
	}
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("form retention runtime did not stop before the deadline: %w", ctx.Err())
	}
}

// LogFormRetentionEnabled records at boot that this process will delete data,
// and after what age.
func LogFormRetentionEnabled(retentionDays int) {
	utils.Logger.WithField("retention_days", retentionDays).Info("form retention purge enabled")
}
