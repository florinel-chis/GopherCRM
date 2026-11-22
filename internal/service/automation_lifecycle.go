package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
)

// automationOwner is the lifecycle of the worker's owned work: the admission
// count, the context every owned unit runs on and the drain the runtime waits
// for. Same shape as the AEO run owner: register before the first database
// call, release after the last write, never close the pool while the count is
// non-zero.
type automationOwner struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   int
	drained  chan struct{}
	signaled bool
}

func newAutomationOwner() *automationOwner {
	ctx, cancel := context.WithCancel(context.Background())
	return &automationOwner{ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

// admit registers one unit of owned work; it fails once shutdown has begun.
func (o *automationOwner) admit() (release func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.ctx.Err() != nil {
		return nil, ErrAutomationShuttingDown
	}
	o.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.active--
			o.signalIfDrained()
		})
	}, nil
}

func (o *automationOwner) signalIfDrained() {
	if o.closed && o.active == 0 && !o.signaled {
		o.signaled = true
		close(o.drained)
	}
}

func (o *automationOwner) activeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active
}

// shutdown closes admission, cancels the owned context and waits for every
// admitted unit to release, or for ctx to end.
func (o *automationOwner) shutdown(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.cancel()
	}
	o.signalIfDrained()
	o.mu.Unlock()

	// An already drained owner reports success whatever the caller's
	// context: a repeated Stop after a deadline has passed is a no-op, not a
	// failure. Only an owner that still holds work waits on ctx.
	select {
	case <-o.drained:
		return nil
	default:
	}
	select {
	case <-o.drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("automation runtime: %d unit(s) of work still active at the shutdown deadline: %w", o.activeCount(), ctx.Err())
	}
}

// Shutdown closes admission, cancels owned work and waits for it to release.
func (e *AutomationEngine) Shutdown(ctx context.Context) error {
	return e.owner.shutdown(ctx)
}

// ActiveWork reports the admitted units of work still registered.
func (e *AutomationEngine) ActiveWork() int {
	return e.owner.activeCount()
}

// Context is the owned context; it ends when Shutdown begins.
func (e *AutomationEngine) Context() context.Context {
	return e.owner.ctx
}

// Tick is one worker pass: the stale sweep, dispatch, schedules, due runs.
// Each phase keeps going after a failure in one item; the errors are joined
// for the log. Sweeping first means a run abandoned by a crashed worker is
// recovered within the stale horizon, without a restart, and claimed by the
// same pass.
func (e *AutomationEngine) Tick(ctx context.Context) error {
	var errs []error
	recovered, failed, err := e.SweepStaleRuns(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	if recovered > 0 || failed > 0 {
		e.logger().WithFields(logrus.Fields{"recovered": recovered, "failed": failed}).
			Warn("Automation runs recovered from stale leases")
	}
	if _, err := e.Dispatch(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.RunSchedules(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.ExecuteDue(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
