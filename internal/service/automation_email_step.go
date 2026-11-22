package service

import (
	"context"
	"errors"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// executeEmailStep is the two-transaction email step described at the top of
// the file.
func (e *AutomationEngine) executeEmailStep(run *models.AutomationRun, rule *models.AutomationRule, step models.AutomationStep, logger *logrus.Entry) (stepResult, error) {
	pos := step.Position
	stepCount := len(rule.Steps)

	// A row already at this position belongs to an earlier attempt. One in
	// sending may or may not have reached the relay: it is finalized as
	// unconfirmed and the run fails, so the mail is never sent twice.
	existing, err := e.repo.GetRunStep(run.ID, pos)
	switch {
	case err == nil && existing.Outcome == models.AutomationOutcomeSending:
		if err := e.failUnconfirmedEmail(run, existing); err != nil {
			return stepResult{}, err
		}
		logger.WithField("error_code", models.AutomationErrorEmailUnconfirmed).Warn("Automation email left unconfirmed; run failed")
		return stepResult{terminal: true}, nil
	case err == nil:
		return e.continuePastRecordedEmail(run, existing, stepCount, logger)
	case !isNotFound(err):
		return stepResult{}, err
	}

	var (
		result      stepResult
		done        bool
		doneOutcome string
		to          string
		subject     string
		body        string
		stepRowID   uint
		recipient   uint
	)
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		done = false
		rec, err := e.lockRecord(r, run)
		if err != nil {
			return err
		}
		if e.afterRecordRead != nil {
			e.afterRecordRead(run.ID, pos)
		}
		now := e.now().UTC()
		if rec == nil {
			done, doneOutcome = true, models.AutomationRunCancelled
			result = stepResult{terminal: true}
			return e.advance(r, run, pos, repository.AutomationRunAdvance{
				Status: models.AutomationRunCancelled, NextPosition: pos, FinishedAt: &now, CancelReason: models.AutomationCancelRecordDeleted,
			}, now)
		}
		// The conditional check of this half: the run row under lock, still
		// running at this position under this engine's own lease, else the
		// run moved (cancelled, recovered or re-claimed by another engine)
		// and nothing is recorded or sent.
		current, err := r.GetRunByIDForUpdate(run.ID)
		if err != nil {
			if isNotFound(err) {
				return errRunNotAdvanced
			}
			return err
		}
		if !e.holdsRun(current, pos) {
			logger.WithField("operation", "email_claim").Info("Automation email refused: the run is no longer held by this executor")
			return errRunNotAdvanced
		}
		users := e.users.WithTx(tx)
		user, available, err := resolveRecipient(users, step.Config, rec)
		if err != nil {
			return err
		}
		if !available {
			done, doneOutcome = true, models.AutomationOutcomeSkipped
			adv := nextAdvance(stepCount, pos+1, now)
			if err := e.advance(r, run, pos, adv, now); err != nil {
				return err
			}
			if err := r.CreateRunStep(&models.AutomationRunStep{
				RunID: run.ID, Position: pos, StepType: step.StepType,
				Outcome: models.AutomationOutcomeSkipped, ErrorCode: models.AutomationErrorRecipientUnavailable,
				Details: map[string]interface{}{}, ExecutedAt: now,
			}); err != nil {
				return err
			}
			result = stepResult{terminal: adv.Status != models.AutomationRunRunning, nextPosition: pos + 1}
			return nil
		}
		owner := loadOwner(users, rec)
		to = strings.TrimSpace(user.Email)
		subject = renderPlaceholders(step.Config.Subject, rec, owner)
		body = renderPlaceholders(step.Config.Body, rec, owner)
		recipient = user.ID
		row := &models.AutomationRunStep{
			RunID: run.ID, Position: pos, StepType: step.StepType,
			Outcome: models.AutomationOutcomeSending, Details: map[string]interface{}{"recipient_id": user.ID}, ExecutedAt: now,
		}
		if err := r.CreateRunStep(row); err != nil {
			return err
		}
		stepRowID = row.ID
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	if done {
		logger.WithField("outcome", doneOutcome).Info("Automation email step resolved without a send")
		return result, nil
	}

	if e.beforeSend != nil {
		e.beforeSend(run.ID, pos)
	}
	// The pre-send check (#237): a plain read of the run just before the
	// relay is called. A run that another engine's sweep failed as
	// email_unconfirmed, that was cancelled or re-claimed while this executor
	// was paused is not sent and not stamped. The window between this read
	// and Send stays open; the row then means "outcome unknown" and nothing
	// resends it.
	current, err := e.repo.GetRunByID(run.ID)
	if err != nil {
		if isNotFound(err) {
			return stepResult{}, errRunNotAdvanced
		}
		return stepResult{}, err
	}
	if !e.holdsRun(current, pos) {
		logger.WithField("operation", "pre_send_check").Warn("Automation email not sent: the run is no longer held by this executor")
		return stepResult{}, errRunNotAdvanced
	}
	var sendErr error
	if e.mailer == nil {
		sendErr = errors.New("no mailer is configured")
	} else {
		sendErr = e.mailer.Send(to, subject, body)
	}
	if sendErr != nil {
		// The transport error may quote the address; only the fact is logged.
		logger.WithField("error_code", models.AutomationErrorMailFailed).Warn("Automation email delivery failed")
	}
	if e.afterSend != nil {
		if err := e.afterSend(run.ID, pos); err != nil {
			return stepResult{}, err
		}
	}

	advanced, stampedRow := false, false
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		now := e.now().UTC()
		outcome, errorCode := models.AutomationOutcomeSucceeded, ""
		if sendErr != nil {
			outcome, errorCode = models.AutomationOutcomeFailed, models.AutomationErrorMailFailed
		}
		// The run row first, then the step row: the order of the first half
		// and of the stale sweep, so they queue instead of deadlocking.
		adv := nextAdvance(stepCount, pos+1, now)
		rows, err := r.AdvanceRun(run.ID, pos, e.instanceID, adv, now)
		if err != nil {
			return err
		}
		// Stamped only while the row is still sending: a row the stale sweep
		// already failed as email_unconfirmed keeps that outcome.
		stamped, err := r.FinishSendingStep(stepRowID, outcome, errorCode, map[string]interface{}{"recipient_id": recipient})
		if err != nil {
			return err
		}
		advanced, stampedRow = rows == 1, stamped == 1
		if rows != 1 {
			// Cancelled or recovered between the two halves: the run stays as
			// the cancel or the sweep left it.
			result = stepResult{terminal: true}
			return nil
		}
		result = stepResult{terminal: adv.Status != models.AutomationRunRunning, nextPosition: pos + 1}
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	if !stampedRow {
		logger.WithField("operation", "email_stamp").Warn("Automation email outcome not recorded: the step row was already finalized")
	}
	if !advanced {
		logger.WithField("operation", "fenced_advance").Info("Automation run moved between the two email halves; stopping")
		return result, nil
	}
	entry := logger.WithField("outcome", models.AutomationOutcomeSucceeded)
	if sendErr != nil {
		entry = logger.WithFields(logrus.Fields{"outcome": models.AutomationOutcomeFailed, "error_code": models.AutomationErrorMailFailed})
	}
	entry.Info("Automation step executed")
	return result, nil
}

// continuePastRecordedEmail handles a claimed run whose current position
// already holds a finalized send_email row: an earlier executor recorded the
// step (its late stamp landed after its fenced advance missed, for example
// because a sweep recovered the run in between). The step is never repeated.
// A row failed with email_unconfirmed fails the run with that code; any other
// outcome advances the run past the position as that executor's advance would
// have. Both writes are owner-fenced, so a run this engine no longer holds is
// left alone.
func (e *AutomationEngine) continuePastRecordedEmail(run *models.AutomationRun, existing *models.AutomationRunStep, stepCount int, logger *logrus.Entry) (stepResult, error) {
	pos := existing.Position
	if existing.ErrorCode == models.AutomationErrorEmailUnconfirmed {
		if err := e.failUnconfirmedEmail(run, existing); err != nil {
			return stepResult{}, err
		}
		logger.WithFields(logrus.Fields{"operation": "recorded_email", "error_code": models.AutomationErrorEmailUnconfirmed}).
			Warn("Automation email step already recorded; run failed")
		return stepResult{terminal: true}, nil
	}
	var result stepResult
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		now := e.now().UTC()
		adv := nextAdvance(stepCount, pos+1, now)
		if err := e.advance(e.repo.WithTx(tx), run, pos, adv, now); err != nil {
			return err
		}
		result = stepResult{terminal: adv.Status != models.AutomationRunRunning, nextPosition: pos + 1}
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	logger.WithFields(logrus.Fields{"operation": "recorded_email", "outcome": existing.Outcome}).
		Info("Automation email step already recorded; run advanced past it")
	return result, nil
}

// holdsRun reports whether the run row is still running at position under
// this engine's own lease.
func (e *AutomationEngine) holdsRun(run *models.AutomationRun, position int) bool {
	return run.Status == models.AutomationRunRunning && run.NextPosition == position &&
		run.OwnerInstance != nil && *run.OwnerInstance == e.instanceID
}

// failUnconfirmedEmail fails the run with email_unconfirmed through the
// owner-fenced advance and, in the same transaction, stamps the sending row
// failed with the same code (only while it is still sending). An advance that
// matches nothing rolls both back and stops the executor.
func (e *AutomationEngine) failUnconfirmedEmail(run *models.AutomationRun, row *models.AutomationRunStep) error {
	return e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		now := e.now().UTC()
		if err := e.advance(r, run, run.NextPosition, repository.AutomationRunAdvance{
			Status: models.AutomationRunFailed, NextPosition: run.NextPosition, FinishedAt: &now, ErrorCode: models.AutomationErrorEmailUnconfirmed,
		}, now); err != nil {
			return err
		}
		_, err := r.FinishSendingStep(row.ID, models.AutomationOutcomeFailed, models.AutomationErrorEmailUnconfirmed, row.Details)
		return err
	}, automationTxRetries)
}
