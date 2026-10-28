package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
)

// AutomationRuntime owns the automation worker goroutine. main wires it
// through cmd/automation_runtime.go and drains it before closing the
// database: Stop closes admission, cancels the worker context and waits for
// the goroutine and every admitted step to release under the caller's
// deadline. The worker assumes a single API replica (no run leases).
type AutomationRuntime struct {
	engine   *AutomationEngine
	interval time.Duration

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// wake lets a caller (a test, mostly) trigger a tick without waiting for
	// the interval.
	wake chan struct{}
}

// NewAutomationRuntime wraps the engine with a worker that ticks every
// interval (at least a second).
func NewAutomationRuntime(engine *AutomationEngine, interval time.Duration) *AutomationRuntime {
	if interval < time.Second {
		interval = time.Second
	}
	return &AutomationRuntime{engine: engine, interval: interval, wake: make(chan struct{}, 1)}
}

// Engine is the engine the worker drives.
func (r *AutomationRuntime) Engine() *AutomationEngine { return r.engine }

// Start arms the worker on a context derived from ctx and from the engine's
// owned context, so either the process-wide background cancel or Stop ends
// it. The first tick runs immediately. Starting twice is a no-op.
func (r *AutomationRuntime) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	workerCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go r.loop(workerCtx)
}

func (r *AutomationRuntime) loop(ctx context.Context) {
	defer r.wg.Done()
	owned := r.engine.Context()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-owned.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		if ctx.Err() != nil || owned.Err() != nil {
			return
		}
		r.tick(ctx)
	}
}

func (r *AutomationRuntime) tick(ctx context.Context) {
	if err := r.engine.Tick(ctx); err != nil && ctx.Err() == nil {
		r.engine.logger().WithError(utils.LogError(err)).Warn("Automation worker tick reported errors")
	}
}

// Wake asks the worker for a tick now. It does nothing when no worker runs.
func (r *AutomationRuntime) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Running reports whether the worker goroutine has been started and not yet
// stopped.
func (r *AutomationRuntime) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// Stop closes admission, cancels the worker context and waits for the
// goroutine and every admitted unit of work to finish, or for ctx to end. A
// step whose transaction is in flight finishes or rolls back before Stop
// returns; nothing is written afterwards. Repeated calls are safe.
func (r *AutomationRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	cancel := r.cancel
	r.started = false
	r.mu.Unlock()

	// The worker context goes first so the loop claims no further run while
	// admission closes; Shutdown then closes admission, cancels the owned
	// context and waits for every admitted step to release.
	if cancel != nil {
		cancel()
	}
	var errs []error
	if err := r.engine.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, errors.New("automation worker did not stop before the deadline: "+ctx.Err().Error()))
	}
	return errors.Join(errs...)
}
