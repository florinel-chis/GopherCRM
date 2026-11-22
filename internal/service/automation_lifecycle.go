package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/florinel-chis/gophercrm/internal/lifecycle"
	"github.com/sirupsen/logrus"
)

// newAutomationOwner builds the lifecycle of the worker's owned work: the
// admission count, the context every owned unit runs on and the drain the
// runtime waits for. Same shape as the AEO run owner: register before the
// first database call, release after the last write, never close the pool
// while the count is non-zero. An already drained owner reports success
// whatever the caller's context: a repeated Stop after a deadline has passed
// is a no-op, not a failure.
func newAutomationOwner() *lifecycle.Owner {
	return lifecycle.NewOwner(ErrAutomationShuttingDown, func(active int, err error) error {
		return fmt.Errorf("automation runtime: %d unit(s) of work still active at the shutdown deadline: %w", active, err)
	})
}

// Shutdown closes admission, cancels owned work and waits for it to release.
func (e *AutomationEngine) Shutdown(ctx context.Context) error {
	return e.owner.Shutdown(ctx)
}

// ActiveWork reports the admitted units of work still registered.
func (e *AutomationEngine) ActiveWork() int {
	return e.owner.Active()
}

// Context is the owned context; it ends when Shutdown begins.
func (e *AutomationEngine) Context() context.Context {
	return e.owner.Context()
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
