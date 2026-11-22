package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// ExecuteDue claims every pending run and every waiting run whose resume_at
// has passed, one conditional claim each, and executes it.
func (e *AutomationEngine) ExecuteDue(ctx context.Context) error {
	now := e.now()
	runs, err := e.repo.ListClaimableRuns(now, automationClaimBatch)
	if err != nil {
		return err
	}
	for i := range runs {
		if ctx.Err() != nil {
			break
		}
		listed := runs[i]
		if e.beforeClaim != nil {
			e.beforeClaim(listed.ID)
		}
		// Each claim reads the clock afresh: the runs of a batch execute one
		// after another, and a claim stamped with the time read before the
		// loop would store a heartbeat_at that is already stale once the
		// batch has run past automationRunLeaseStaleAfter, letting another
		// engine's sweep recover a run this engine is executing.
		claimNow := e.now()
		claimed, err := e.repo.ClaimRun(listed.ID, listed.Status, e.instanceID, claimNow)
		if err != nil {
			e.logger().WithFields(logrus.Fields{"run_id": listed.ID, "operation": "claim"}).
				WithError(utils.LogError(err)).Error("Automation run claim failed")
			continue
		}
		if claimed != 1 {
			continue
		}
		// The batch is a plain read taken before the claims. Between that read
		// and this claim another engine may have claimed the run, executed a
		// step and released it back to the same status at a later position
		// with more attempts; the claim still matches. The executor therefore
		// runs from the row as stored after the claim, never from the batch
		// snapshot, so its fenced writes name the current position.
		run, ok := e.loadClaimedRun(listed.ID)
		if !ok {
			continue
		}
		e.executeRun(ctx, run)
	}
	return nil
}

// loadClaimedRun re-reads a run this engine has just claimed, with a plain
// read by primary key, and returns it only while it is still running under
// this engine. A run that moved in between (cancelled, recovered) is skipped.
// A read that fails leaves the run running under this lease, unrenewed: the
// stale sweep of any engine recovers it once the lease expires.
func (e *AutomationEngine) loadClaimedRun(id uint) (models.AutomationRun, bool) {
	current, err := e.repo.GetRunByID(id)
	if err != nil {
		if !isNotFound(err) {
			e.logger().WithFields(logrus.Fields{"run_id": id, "operation": "claim_read"}).
				WithError(utils.LogError(err)).Warn("Claimed automation run could not be read; the stale sweep will recover it")
		}
		return models.AutomationRun{}, false
	}
	if current.Status != models.AutomationRunRunning || current.OwnerInstance == nil || *current.OwnerInstance != e.instanceID {
		e.logger().WithFields(logrus.Fields{"run_id": id, "operation": "claim_read"}).
			Info("Claimed automation run moved before execution; skipped")
		return models.AutomationRun{}, false
	}
	run := *current
	run.Steps = nil
	return run, true
}

// executeRun runs the claimed run under its lease: one heartbeat goroutine
// renews heartbeat_at every heartbeatInterval while the steps execute, and is
// joined before executeRun returns, so nothing renews a run whose executor
// has returned. A renewal refused because the run is no longer owned (a
// cancel, a stale recovery by another engine) cancels the run's context: the
// executor starts no further step and writes nothing more for the run.
func (e *AutomationEngine) executeRun(ctx context.Context, run models.AutomationRun) {
	logger := e.logger().WithFields(logrus.Fields{
		"run_id": run.ID, "rule_id": run.RuleID, "entity_type": run.EntityType, "entity_id": run.EntityID,
	})
	runCtx, cancelRun := context.WithCancel(ctx)
	var leaseLost atomic.Bool
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		e.renewLease(runCtx, run.ID, &leaseLost, cancelRun, logger)
	}()
	defer func() {
		cancelRun()
		<-heartbeatDone
	}()
	e.runSteps(runCtx, run, &leaseLost, logger)
}

// renewLease renews the run's heartbeat every heartbeatInterval until ctx
// ends. A renewal refused with ErrAutomationRunNotOwned marks the lease lost
// and cancels the run; any other failure is logged and retried at the next
// interval.
func (e *AutomationEngine) renewLease(ctx context.Context, runID uint, lost *atomic.Bool, cancel context.CancelFunc, logger *logrus.Entry) {
	ticker := time.NewTicker(e.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		err := e.repo.RenewRunLease(runID, e.instanceID, e.now().UTC())
		switch {
		case err == nil:
		case errors.Is(err, repository.ErrAutomationRunNotOwned):
			if ctx.Err() != nil {
				// The run ended while the renewal was in flight.
				return
			}
			lost.Store(true)
			cancel()
			logger.WithFields(logrus.Fields{"operation": "heartbeat", "error_code": "run_not_owned"}).
				Warn("Automation run lease lost; the executor stops")
			if e.afterLeaseLost != nil {
				e.afterLeaseLost(runID)
			}
			return
		case ctx.Err() != nil:
			return
		default:
			logger.WithField("operation", "heartbeat").WithError(utils.LogError(err)).
				Warn("Could not renew the automation run lease")
		}
	}
}

// runSteps runs the claimed run step by step until it waits, finishes, is
// cancelled under the executor, fails, loses its lease, or the runtime stops
// admitting work.
func (e *AutomationEngine) runSteps(ctx context.Context, run models.AutomationRun, leaseLost *atomic.Bool, logger *logrus.Entry) {
	rule, err := e.repo.GetRuleByID(run.RuleID)
	if err != nil {
		if isNotFound(err) {
			e.cancelRunning(run, models.AutomationCancelRuleDeleted)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Automation rule could not be loaded")
		e.releaseOrFail(run, logger)
		return
	}
	if rule.Version != run.RuleVersion {
		e.cancelRunning(run, models.AutomationCancelRuleChanged)
		return
	}

	for guard := 0; guard <= models.AutomationMaxSteps; guard++ {
		if run.NextPosition > len(rule.Steps) {
			now := e.now().UTC()
			e.runWrite(run.ID, "complete", func(r repository.AutomationRepository) (int64, error) {
				return r.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
					Status: models.AutomationRunCompleted, NextPosition: run.NextPosition, FinishedAt: &now,
				}, now)
			})
			logger.WithField("status", models.AutomationRunCompleted).Info("Automation run finished")
			return
		}
		if e.beforeStep != nil {
			e.beforeStep(run.ID, run.NextPosition)
		}
		if leaseLost.Load() {
			logger.WithFields(logrus.Fields{"operation": "heartbeat_loss", "position": run.NextPosition}).
				Info("Automation run lease lost; no further step")
			return
		}
		if ctx.Err() != nil {
			if !leaseLost.Load() {
				e.releaseRun(run)
			}
			return
		}
		release, err := e.owner.admit()
		if err != nil {
			e.releaseRun(run)
			return
		}
		result, err := e.executeStep(&run, rule, logger)
		release()
		if err != nil {
			if errors.Is(err, errRunNotAdvanced) {
				logger.Info("Automation run changed under the executor; stopping")
				return
			}
			logger.WithField("position", run.NextPosition).WithError(utils.LogError(err)).Error("Automation step failed")
			e.releaseOrFail(run, logger)
			return
		}
		if result.terminal {
			return
		}
		run.NextPosition = result.nextPosition
	}
	logger.Warn("Automation run exceeded the step cap; released")
	e.releaseRun(run)
}

// releaseRun hands the run back to pending at its position, so the next
// tick (on any engine) continues it. Fenced on the owner: a run this engine
// no longer owns is left as it is.
func (e *AutomationEngine) releaseRun(run models.AutomationRun) {
	now := e.now()
	e.runWrite(run.ID, "release", func(r repository.AutomationRepository) (int64, error) {
		return r.ReleaseRun(run.ID, run.NextPosition, e.instanceID, now)
	})
}

// releaseOrFail releases a run whose step attempt failed, or fails it once
// the attempts are exhausted.
func (e *AutomationEngine) releaseOrFail(run models.AutomationRun, logger *logrus.Entry) {
	if run.Attempts+1 >= automationMaxAttempts {
		now := e.now().UTC()
		e.runWrite(run.ID, "fail", func(r repository.AutomationRepository) (int64, error) {
			return r.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
				Status: models.AutomationRunFailed, NextPosition: run.NextPosition, FinishedAt: &now, ErrorCode: models.AutomationErrorStepFailed,
			}, now)
		})
		logger.WithField("error_code", models.AutomationErrorStepFailed).Warn("Automation run failed after repeated attempts")
		return
	}
	e.releaseRun(run)
}

// cancelRunning cancels a run the executor holds, conditionally.
func (e *AutomationEngine) cancelRunning(run models.AutomationRun, reason string) {
	now := e.now().UTC()
	e.runWrite(run.ID, "cancel", func(r repository.AutomationRepository) (int64, error) {
		return r.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
			Status: models.AutomationRunCancelled, NextPosition: run.NextPosition, FinishedAt: &now, CancelReason: reason,
		}, now)
	})
}

// runWrite performs one owner-fenced write that ends the executor's hold on a
// run (complete, fail, release, cancel) in its own short transaction, replayed
// like a step on a deadlock, a lock wait or SQLite busy. Its first statement
// is the write, so on SQLite it waits for the write lock under busy_timeout
// instead of failing on a stale read snapshot. A write that still fails, or
// matches nothing because the run moved, leaves the run as it is: a run still
// running under this engine's lease is recovered by the stale sweep once the
// lease expires. It runs on a background context so a release after Stop
// still lands.
func (e *AutomationEngine) runWrite(runID uint, operation string, write func(r repository.AutomationRepository) (int64, error)) {
	if e.txManager == nil {
		if _, err := write(e.repo); err != nil {
			e.logger().WithFields(logrus.Fields{"run_id": runID, "operation": operation}).
				WithError(utils.LogError(err)).Warn("Automation run write failed; the stale sweep will recover the run")
		}
		return
	}
	var rows int64
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		rows, err = write(e.repo.WithTx(tx))
		return err
	}, automationTxRetries)
	if err != nil {
		e.logger().WithFields(logrus.Fields{"run_id": runID, "operation": operation}).
			WithError(utils.LogError(err)).Warn("Automation run write failed; the stale sweep will recover the run")
		return
	}
	if rows == 0 {
		e.logger().WithFields(logrus.Fields{"run_id": runID, "operation": operation}).
			Info("Automation run write matched nothing; the run moved under the executor")
	}
}

// nextAdvance continues the run at next, or completes it when next lies
// past the last step.
func nextAdvance(stepCount, next int, now time.Time) repository.AutomationRunAdvance {
	if next > stepCount {
		return repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: next, FinishedAt: &now}
	}
	return repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: next}
}

// advance performs the owner-fenced conditional advance and turns a miss into
// errRunNotAdvanced, logged once as a fenced write.
func (e *AutomationEngine) advance(r repository.AutomationRepository, run *models.AutomationRun, position int, adv repository.AutomationRunAdvance, now time.Time) error {
	rows, err := r.AdvanceRun(run.ID, position, e.instanceID, adv, now)
	if err != nil {
		return err
	}
	if rows != 1 {
		e.logger().WithFields(logrus.Fields{"operation": "fenced_advance", "run_id": run.ID, "position": position}).
			Info("Automation run advance matched nothing; the run moved under the executor")
		return errRunNotAdvanced
	}
	return nil
}
