package service

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/lifecycle"
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
	loop     *lifecycle.Loop
}

// NewFormRetentionRuntime wraps a sweeper with a goroutine that ticks every
// interval. A non-positive interval means FormRetentionSweepInterval.
func NewFormRetentionRuntime(sweeper FormRetentionSweeper, interval time.Duration) *FormRetentionRuntime {
	if interval <= 0 {
		interval = FormRetentionSweepInterval
	}
	r := &FormRetentionRuntime{sweeper: sweeper, interval: interval}
	r.loop = lifecycle.NewLoop("form retention runtime", interval, r.sweep)
	return r
}

// Interval is the time between two scheduled sweeps.
func (r *FormRetentionRuntime) Interval() time.Duration { return r.interval }

// Start sweeps once immediately on a goroutine derived from ctx, so either the
// process-wide background cancel or Stop ends it. Starting twice, or after Stop,
// is a no-op.
func (r *FormRetentionRuntime) Start(ctx context.Context) { r.loop.Start(ctx) }

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

// Wake asks for a sweep now. A Wake before Start is buffered and causes one extra
// sweep after the first; after the goroutine has ended it does nothing.
func (r *FormRetentionRuntime) Wake() { r.loop.Wake() }

// Stop cancels the sweep and waits for the goroutine to return, or for ctx to
// end. A chunk in flight finishes or rolls back before Stop returns nil, and
// after that no row changes. A deadline that passes first is an error, and the
// chunk may then still be running. Repeated calls are safe: once the goroutine
// has joined, Stop returns nil even on a context that has already ended.
func (r *FormRetentionRuntime) Stop(ctx context.Context) error { return r.loop.Stop(ctx) }

// LogFormRetentionEnabled records at boot that this process will delete data,
// and after what age.
func LogFormRetentionEnabled(retentionDays int) {
	utils.Logger.WithField("retention_days", retentionDays).Info("form retention purge enabled")
}
