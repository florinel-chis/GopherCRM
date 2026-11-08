package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Stale bulk operation recovery (#196).
//
// A bulk status batch writes its history row pending, and completed inside the
// batch transaction, so a row that is still pending belongs to a batch that did
// not commit: none of its records changed. The failed write that follows a
// rolled-back batch is a separate, bounded statement that can time out (the
// connection or the row held elsewhere). The sweep below gives every such row
// its terminal status: a pending row older than BulkOperationStaleAfter becomes
// failed.
//
// Rows already pending when this was deployed are the one exception: their
// batch recorded completed after its commit, so it may have committed. They are
// swept to failed all the same; the outcome is unknown, and that is documented,
// not stored.

const (
	// BulkOperationStaleAfter is how old a pending bulk_operations row must be
	// before the sweep fails it. A batch holds at most MaxBulkStatusItems
	// records and finishes in seconds, so an hour is far past any live batch.
	BulkOperationStaleAfter = time.Hour
	// BulkOperationSweepInterval is how often every replica sweeps.
	BulkOperationSweepInterval = 15 * time.Minute
)

// SweepStaleBulkOperations fails every live pending bulk operation created
// before now - BulkOperationStaleAfter and returns how many rows it changed.
func SweepStaleBulkOperations(ctx context.Context, repo repository.BulkOperationRepository, now time.Time) (int64, error) {
	return repo.MarkStalePendingFailed(ctx, now.Add(-BulkOperationStaleAfter))
}

// BulkOperationRecoveryOption configures NewBulkOperationRecoveryRuntime.
type BulkOperationRecoveryOption func(*BulkOperationRecoveryRuntime)

// WithBulkRecoveryClock replaces the wall clock the cutoff is computed from.
func WithBulkRecoveryClock(now func() time.Time) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if now != nil {
			r.now = now
		}
	}
}

// WithBulkRecoveryTicker replaces the interval ticker. newTicker returns the
// channel the loop waits on and the function that releases it.
func WithBulkRecoveryTicker(newTicker func(interval time.Duration) (<-chan time.Time, func())) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if newTicker != nil {
			r.newTicker = newTicker
		}
	}
}

// WithBulkRecoveryInterval replaces BulkOperationSweepInterval; a
// non-positive value keeps it.
func WithBulkRecoveryInterval(interval time.Duration) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if interval > 0 {
			r.interval = interval
		}
	}
}

// BulkOperationRecoveryRuntime owns the goroutine that sweeps once at Start and
// then on every interval tick. It is safe on every replica: two sweeps only
// compete for the same rows, and each row changes once. main drains it before
// closing the database: Stop cancels the sweep and joins the goroutine under
// the caller's deadline. A runtime runs once: Start after Stop does nothing.
type BulkOperationRecoveryRuntime struct {
	repo      repository.BulkOperationRepository
	logger    *logrus.Logger
	now       func() time.Time
	newTicker func(time.Duration) (<-chan time.Time, func())
	interval  time.Duration

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	// exited is closed by the goroutine when it returns; nil until Start.
	exited chan struct{}
}

// NewBulkOperationRecoveryRuntime builds the runtime over repo. A nil logger
// means utils.Logger.
func NewBulkOperationRecoveryRuntime(repo repository.BulkOperationRepository, logger *logrus.Logger, opts ...BulkOperationRecoveryOption) *BulkOperationRecoveryRuntime {
	r := &BulkOperationRecoveryRuntime{
		repo:     repo,
		logger:   logger,
		now:      time.Now,
		interval: BulkOperationSweepInterval,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Interval is the time between two scheduled sweeps.
func (r *BulkOperationRecoveryRuntime) Interval() time.Duration { return r.interval }

// Sweep runs one pass with the runtime's clock and logs its count.
func (r *BulkOperationRecoveryRuntime) Sweep(ctx context.Context) (int64, error) {
	failed, err := SweepStaleBulkOperations(ctx, r.repo, r.now())
	entry := r.log().WithField("failed", failed)
	switch {
	case err == nil && failed > 0:
		entry.Info("bulk operation recovery sweep failed stale pending rows")
	case err == nil:
		entry.Debug("bulk operation recovery sweep completed")
	case ctx.Err() != nil:
		entry.Info("bulk operation recovery sweep interrupted")
	default:
		entry.WithError(utils.LogError(err)).Warn("bulk operation recovery sweep failed")
	}
	return failed, err
}

func (r *BulkOperationRecoveryRuntime) log() *logrus.Logger {
	if r.logger != nil {
		return r.logger
	}
	return utils.Logger
}

// Start sweeps once immediately on a goroutine derived from ctx, then on every
// tick. Starting twice, or after Stop, is a no-op.
func (r *BulkOperationRecoveryRuntime) Start(ctx context.Context) {
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
	ticks, stopTicker := r.newTicker(r.interval)
	go r.loop(sweepCtx, ticks, stopTicker, exited)
}

func (r *BulkOperationRecoveryRuntime) loop(ctx context.Context, ticks <-chan time.Time, stopTicker func(), exited chan<- struct{}) {
	defer close(exited)
	defer stopTicker()
	_, _ = r.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		if ctx.Err() != nil {
			return
		}
		_, _ = r.Sweep(ctx)
	}
}

// Stop cancels the sweep and waits for the goroutine to return, or for ctx to
// end. A statement in flight finishes or is abandoned before Stop returns nil,
// and after that the runtime writes nothing. A deadline that passes first is an
// error. Once the goroutine has joined, a repeated Stop returns nil even on a
// context that has already ended.
func (r *BulkOperationRecoveryRuntime) Stop(ctx context.Context) error {
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
		return fmt.Errorf("bulk operation recovery runtime did not stop before the deadline: %w", ctx.Err())
	}
}
