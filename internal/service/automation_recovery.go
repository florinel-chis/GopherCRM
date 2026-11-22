package service

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// SweepStaleRuns recovers the running runs whose lease went stale: the
// heartbeat is NULL (a run claimed before #237) or older than
// automationRunLeaseStaleAfter. It runs at boot, before the worker starts,
// and at the start of every tick, on every engine. Selection is a plain read;
// each recovery is a conditional update by primary key that re-checks the
// status, the observed owner and the staleness, so a run whose owner renewed
// its lease, that another engine recovered or re-claimed, or that finished
// meanwhile is left alone. A run whose last step row is a send_email still in
// sending is failed with email_unconfirmed together with that row (the mail
// may have gone out and is never sent again); every other stale run goes back
// to pending at its next_position with attempts unchanged. It returns how
// many runs went back to pending and how many failed.
func (e *AutomationEngine) SweepStaleRuns(ctx context.Context) (recovered int, failed int, err error) {
	now := e.now().UTC()
	cutoff := now.Add(-automationRunLeaseStaleAfter)
	runs, err := e.repo.ListStaleRunningRuns(cutoff, automationSweepBatch)
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for i := range runs {
		if ctx.Err() != nil {
			break
		}
		outcome, err := e.recoverStaleRun(runs[i], cutoff, now)
		if err != nil {
			errs = append(errs, err)
			e.logger().WithFields(logrus.Fields{"operation": "sweep", "run_id": runs[i].ID}).
				WithError(utils.LogError(err)).Error("Automation stale run recovery failed")
			continue
		}
		switch outcome {
		case automationRecoveredPending:
			recovered++
		case automationRecoveredFailed:
			failed++
		}
	}
	return recovered, failed, errors.Join(errs...)
}

// recoverStaleRun recovers one stale run in one transaction: the run row
// first, then the sending step row, the lock order of the email step. It
// returns the outcome, or "" when the run was spared because it moved since
// the sweep read it.
func (e *AutomationEngine) recoverStaleRun(run models.AutomationRun, cutoff, now time.Time) (string, error) {
	logger := e.logger().WithFields(logrus.Fields{
		"operation": "takeover", "run_id": run.ID, "rule_id": run.RuleID,
		"entity_type": run.EntityType, "entity_id": run.EntityID, "position": run.NextPosition,
	})
	last, err := e.repo.LastRunStep(run.ID)
	if err != nil {
		return "", err
	}
	unconfirmed := last != nil && last.StepType == models.AutomationStepSendEmail && last.Outcome == models.AutomationOutcomeSending
	failCode := ""
	if unconfirmed {
		failCode = models.AutomationErrorEmailUnconfirmed
	}
	if e.beforeStaleRecover != nil {
		e.beforeStaleRecover(run.ID)
	}
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		rows, err := r.RecoverStaleRun(run.ID, run.OwnerInstance, cutoff, failCode, now)
		if err != nil {
			return err
		}
		if rows != 1 {
			return errRunNotAdvanced
		}
		if e.afterStaleRecover != nil {
			e.afterStaleRecover(run.ID)
		}
		// The decision above came from a read taken before this transaction.
		// Re-read the last step row now that the run row is held by the
		// conditional update (run row first, step row second). A sending row is
		// inserted only by the run's owner while it holds the run row, so this
		// read sees every sending row committed before the update, and none can
		// appear after it. When the row changed since the pre-read (a stale
		// owner committed its first email transaction, or stamped the row), the
		// recovery rolls back and the next sweep decides on the current row; the
		// owner meanwhile keeps its run.
		current, err := r.LastRunStep(run.ID)
		if err != nil {
			return err
		}
		nowUnconfirmed := current != nil && current.StepType == models.AutomationStepSendEmail && current.Outcome == models.AutomationOutcomeSending
		if nowUnconfirmed != unconfirmed || (unconfirmed && current.ID != last.ID) {
			return errRunNotAdvanced
		}
		if !unconfirmed {
			return nil
		}
		stamped, err := r.FinishSendingStep(last.ID, models.AutomationOutcomeFailed, models.AutomationErrorEmailUnconfirmed, last.Details)
		if err != nil {
			return err
		}
		if stamped != 1 {
			// The executor stamped the row after the sweep read it: the next
			// sweep decides on what the row says now.
			return errRunNotAdvanced
		}
		return nil
	}, automationTxRetries)
	if errors.Is(err, errRunNotAdvanced) {
		logger.Info("Automation run spared by the sweep: it moved since it was read")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	outcome := automationRecoveredPending
	entry := logger.WithField("outcome", outcome)
	if unconfirmed {
		outcome = automationRecoveredFailed
		entry = logger.WithFields(logrus.Fields{"outcome": outcome, "error_code": models.AutomationErrorEmailUnconfirmed})
	}
	entry.Warn("Automation run recovered from a stale lease")
	if e.recoveryObserver != nil {
		e.recoveryObserver(run.ID, outcome)
	}
	return outcome, nil
}
