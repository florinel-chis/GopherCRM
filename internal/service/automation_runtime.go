package service

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/lifecycle"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// AutomationRuntime owns the automation worker goroutine. main wires it
// through cmd/automation_runtime.go and drains it before closing the
// database: Stop closes admission, cancels the worker context and waits for
// the goroutine and every admitted step to release under the caller's
// deadline. Several replicas sharing one MySQL/MariaDB database may each run
// a worker (#237): every claim carries the engine's instance id and a
// heartbeat lease, each tick starts with the stale sweep, and the heartbeat
// goroutine of a run is joined before its executor returns, so once Stop
// returns nil no heartbeat is written either.
type AutomationRuntime struct {
	engine   *AutomationEngine
	interval time.Duration
	// loop is the worker goroutine; it also ends when the engine's owned
	// context does.
	loop *lifecycle.Loop
}

// NewAutomationRuntime wraps the engine with a worker that ticks every
// interval (at least a second).
func NewAutomationRuntime(engine *AutomationEngine, interval time.Duration) *AutomationRuntime {
	if interval < time.Second {
		interval = time.Second
	}
	r := &AutomationRuntime{engine: engine, interval: interval}
	r.loop = lifecycle.NewLoop("automation worker", interval, r.tick, lifecycle.WithDone(engine.Context().Done()))
	return r
}

// Engine is the engine the worker drives.
func (r *AutomationRuntime) Engine() *AutomationEngine { return r.engine }

// SweepStaleRuns runs the engine's stale sweep once, outside the worker:
// main calls it at boot, before the worker and the HTTP server start.
func (r *AutomationRuntime) SweepStaleRuns(ctx context.Context) (recovered int, failed int, err error) {
	return r.engine.SweepStaleRuns(ctx)
}

// Start arms the worker on a context derived from ctx and from the engine's
// owned context, so either the process-wide background cancel or Stop ends
// it. The first tick runs immediately. Starting twice, or after Stop, is a
// no-op.
func (r *AutomationRuntime) Start(ctx context.Context) { r.loop.Start(ctx) }

func (r *AutomationRuntime) tick(ctx context.Context) {
	if err := r.engine.Tick(ctx); err != nil && ctx.Err() == nil {
		r.engine.logger().WithError(utils.LogError(err)).Warn("Automation worker tick reported errors")
	}
}

// Wake asks the worker for a tick now. It does nothing when no worker runs.
func (r *AutomationRuntime) Wake() { r.loop.Wake() }

// Running reports whether the worker goroutine has been started and not yet
// stopped. It stays false after Stop, whatever a later Start does.
func (r *AutomationRuntime) Running() bool { return r.loop.Running() }

// endedContext is a context that has already ended: a Loop.Stop on it cancels
// the loop and returns without waiting.
var endedContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// Stop closes admission, cancels the worker context and waits for the
// goroutine and every admitted unit of work to finish, or for ctx to end. A
// step whose transaction is in flight finishes or rolls back before Stop
// returns; nothing is written afterwards. Repeated calls are safe: once the
// worker has exited and the engine has drained, Stop returns nil even on a
// context that has already ended. A stopped worker is never armed again.
func (r *AutomationRuntime) Stop(ctx context.Context) error {
	// The worker context goes first so the loop claims no further run while
	// admission closes; Shutdown then closes admission, cancels the owned
	// context and waits for every admitted step to release, and the worker
	// goroutine is joined last.
	_ = r.loop.Stop(endedContext)
	var errs []error
	if err := r.engine.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.loop.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
