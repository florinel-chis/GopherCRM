package main

import (
	"context"
	"errors"

	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// automationBackground owns the follow-up automation worker of this process.
// Stop is what main calls at shutdown, through the combined handle
// setupDependencies returns (combineStops: automation first, then AEO), under
// the one drain deadline main passes, aeoDrainTimeout (15 s). A step
// transaction is a handful of statements, so an honest drain takes well under
// that; when the deadline passes anyway, main does not close the database
// under the work still using it (see main) and the next boot's reconciliation
// hands the run back to pending. A nil runtime (AUTOMATION_ENABLED=false) has
// nothing to stop.
type automationBackground struct {
	runtime *service.AutomationRuntime
}

func newAutomationBackground(runtime *service.AutomationRuntime) *automationBackground {
	return &automationBackground{runtime: runtime}
}

// start arms the worker on a context derived from ctx, so the process-wide
// background cancel stops it as well as Stop does.
func (b *automationBackground) start(ctx context.Context) {
	if b.runtime == nil {
		return
	}
	b.runtime.Start(ctx)
	utils.Logger.Info("Automation worker started")
}

// Stop closes admission, cancels the worker and waits for it under ctx. It
// returns nil once no owned work remains. Repeated calls are safe.
func (b *automationBackground) Stop(ctx context.Context) error {
	if b.runtime == nil {
		return nil
	}
	if err := b.runtime.Stop(ctx); err != nil {
		return err
	}
	utils.Logger.Info("Automation worker stopped")
	return nil
}

// combineStops joins several drain handles into the one main receives: every
// handle runs, in order, under the same context, and their errors are joined.
func combineStops(stops ...func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		for _, stop := range stops {
			if stop == nil {
				continue
			}
			if err := stop(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}
