package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"

	"github.com/sirupsen/logrus"
)

// ErrAEOShuttingDown is returned by StartRun and StartPromptRun once the AEO
// runtime has begun shutting down: admission is closed before any owned work is
// joined, so a request that races the shutdown is refused rather than started
// and immediately cancelled. It is the service-level name of aeo.ErrShuttingDown
// so handlers and the scheduler classify the same sentinel.
var ErrAEOShuttingDown = aeo.ErrShuttingDown

// aeoRunCleanupTimeout bounds the status write that fails a run whose creation
// was interrupted by shutdown. It is independent of the run context, which is
// already cancelled at that point, and short enough to stay inside the drain
// deadline main allows.
const aeoRunCleanupTimeout = 5 * time.Second

// aeoRunOwner is the lifecycle of every background run an AEO service starts.
//
// It is the single answer to "what is still running": a start registers itself
// before touching the database, the executor goroutine inherits that
// registration and releases it once the run row is terminal, and shutdown
// first closes admission, then cancels the shared run context, then waits for
// the count to reach zero or for its deadline. Admission and the count share
// one mutex, so a start cannot slip between "admission closed" and "wait": it
// is either counted before the close or refused after it.
//
// The owner deliberately never takes the service's startMu. That mutex
// serializes the overlap guard with the run insert and may be held by a start
// blocked in a database call; shutdown must not queue behind it. The blocked
// start is counted as owned work instead, so the deadline governs.
type aeoRunOwner struct {
	mu sync.Mutex

	// ctx is cancelled by the first shutdown. Every run and every admission
	// read bound to the database carries it.
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int

	// drained is closed exactly once, when admission is closed and the last
	// owned unit of work has released.
	drained  chan struct{}
	signaled bool
}

func newAEORunOwner() *aeoRunOwner {
	ctx, cancel := context.WithCancel(context.Background())
	return &aeoRunOwner{ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

// admit registers one unit of owned work. It fails with ErrAEOShuttingDown once
// shutdown has started. The returned release is idempotent.
func (o *aeoRunOwner) admit() (release func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.ctx.Err() != nil {
		return nil, ErrAEOShuttingDown
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

// signalIfDrained closes drained when nothing can be, or is, running. Callers
// hold o.mu.
func (o *aeoRunOwner) signalIfDrained() {
	if o.closed && o.active == 0 && !o.signaled {
		o.signaled = true
		close(o.drained)
	}
}

// activeCount reports the owned units of work still registered.
func (o *aeoRunOwner) activeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active
}

// shutdown closes admission, cancels every owned run and waits for them to
// release. It returns nil once nothing owned remains, or ctx's error — with the
// number of still-active units — when the deadline passes first. It is safe to
// call repeatedly and concurrently: later calls only wait.
func (o *aeoRunOwner) shutdown(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.cancel()
	}
	o.signalIfDrained()
	o.mu.Unlock()

	select {
	case <-o.drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("aeo runtime: %d run(s) still active at the shutdown deadline: %w", o.activeCount(), ctx.Err())
	}
}

// AEORuntime is the AEO service together with the ownership of its background
// work. main wires this instead of a bare AEOService so that shutdown can stop
// admitting runs, cancel the ones in flight, join them and only then close the
// database handle they write to.
type AEORuntime struct {
	svc *aeoService
}

// NewAEORuntime wires an AEO service exactly as NewAEOService does and returns
// the runtime that owns its runs. Service exposes the request-facing surface.
func NewAEORuntime(repo repository.AEORepository, executor aeo.Executor, providers []aeo.Provider, txManager repository.TransactionManager, opts ...AEOServiceOption) *AEORuntime {
	return &AEORuntime{svc: newAEOService(repo, executor, providers, txManager, opts...)}
}

// Service is the AEOService the handlers and the scheduler use.
func (r *AEORuntime) Service() AEOService { return r.svc }

// Shutdown stops admitting runs, cancels every manual, scheduled and
// single-prompt run in flight — the cancellation reaches provider calls and
// repository writes — and waits for all of them to finalize. It returns nil
// only once no owned work remains, so the caller may close the database
// afterwards without a late write racing it. When ctx expires first it returns
// an error naming the active count; the caller then follows the crash policy:
// exit without closing the database and let the next boot's reconciliation
// fail the rows left in "running". Repeated and concurrent calls are safe.
func (r *AEORuntime) Shutdown(ctx context.Context) error {
	return r.svc.lifecycle.shutdown(ctx)
}

// ActiveRuns reports how many owned units of work — starts in progress and
// executing runs — are registered right now.
func (r *AEORuntime) ActiveRuns() int { return r.svc.lifecycle.activeCount() }

// runInBackground executes an admitted run on the owner's context and releases
// the admission once the engine has finalized the row. It is the only place a
// run's ownership ends.
func (s *aeoService) runInBackground(ctx context.Context, executor aeo.Executor, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile, release func(), logger *logrus.Entry) {
	defer release()

	err := executor.Execute(ctx, run, prompts, profile)
	switch {
	case err == nil:
	case ctx.Err() != nil, errors.Is(err, context.Canceled):
		logger.WithFields(map[string]interface{}{
			"run_id": run.ID,
			"status": run.Status,
		}).Warn("AEO run interrupted by shutdown")
	default:
		logger.WithField("run_id", run.ID).WithError(utils.LogError(err)).Error("AEO run failed")
	}
}

// failInterruptedRun records a terminal state for a run whose creation was
// overtaken by shutdown before the executor took it over: the row would
// otherwise stay "running" until the stale sweep or the next boot recovers it.
// The write uses a short fresh context because the run context is already
// cancelled. A run that never got an id was never inserted.
func (s *aeoService) failInterruptedRun(run *models.AEORun, logger *logrus.Entry) {
	if run == nil || run.ID == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), aeoRunCleanupTimeout)
	defer cancel()

	now := time.Now().UTC()
	run.Status = models.AEORunStatusFailed
	run.FailedQueries = run.TotalQueries
	run.CompletedAt = &now
	if err := s.repo.WithContext(cleanupCtx).UpdateRun(run); err != nil {
		logger.WithField("run_id", run.ID).WithError(utils.LogError(err)).
			Warn("Could not fail an AEO run interrupted at creation; left for stale-run recovery")
		return
	}
	logger.WithField("run_id", run.ID).Warn("AEO run interrupted by shutdown before it started")
}
