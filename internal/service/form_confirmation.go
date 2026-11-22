package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
)

// ConfirmSubmission spends a confirmation token: the submission becomes
// confirmed, the lead is created or attached, and the notification and
// follow-up mail go out. Every rejection maps to ErrInvalidConfirmationToken.
//
// The token lookup, its claim, the pending check of the submission, the lead
// write and the submission update are one transaction. The claim is a
// conditional UPDATE that stamps the token only while it is still unused and
// unexpired and must affect exactly one row, so two requests that both read the
// token as spendable cannot both succeed: the second claim finds nothing to
// stamp and is rejected. A failure after the claim rolls the whole unit back,
// token included, so the link stays usable for a retry instead of being spent
// on a confirmation that never happened. Mail goes out only once the
// transaction has committed; a notification about a confirmation that was then
// rolled back would announce a lead that does not exist.
//
// The transaction runs on the visitor's request context: a request cancelled
// while it waits for a connection or for the token's row lock rolls back
// before anything is claimed, so the link stays usable. Like a submission it
// is a transaction root and refuses a ctx that already carries a transaction
// (utils.ErrTransactionRootRequired): its mail could not be taken back if an
// enclosing transaction rolled back.
func (s *formService) ConfirmSubmission(ctx context.Context, rawToken string) error {
	if utils.InTransaction(ctx) {
		return utils.ErrTransactionRootRequired
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("handler", "confirm"), "FormService", "ConfirmSubmission")

	if strings.TrimSpace(rawToken) == "" {
		return fmt.Errorf("confirmation token missing: %w", ErrInvalidConfirmationToken)
	}
	hash := hashOpaqueToken(rawToken, s.tokenSecret)

	var (
		form       *models.Form
		submission *models.FormSubmission
		jobs       []formMailJob
	)
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		form, submission, err = s.confirmInTransaction(ctx, logger, s.repo.WithTx(tx), s.leadRepo.WithTx(tx), automationTx(s.automation, tx), s.ticketRepoFor(ctx), s.webhooks.Bind(tx), hash)
		if err != nil {
			return err
		}
		jobs, err = s.recordAcceptedMail(s.repo.WithTx(tx), form, submission)
		if err != nil {
			return err
		}
		if form.CreateTicket {
			return s.ticketSeam(formTicketStageAfterMail)
		}
		return nil
	})
	if err != nil {
		// A rejected link is the visitor's problem and is logged where it is
		// detected; anything else is a failure of ours.
		if !errors.Is(err, ErrInvalidConfirmationToken) {
			utils.LogServiceResponse(logger, err)
		}
		return err
	}

	// Only committed state is announced: the team is told about a lead that
	// exists and the visitor gets the content for a confirmation that stuck.
	s.deliverMail(ctx, jobs)

	if form.CreateTicket {
		logFormTicketOutcome(logger, form, submission)
	}
	logger.WithField("submission_id", submission.ID).Info("Form submission confirmed")
	return nil
}

// confirmInTransaction performs a confirmation on transaction-bound
// repositories and returns the form and the now-confirmed submission for the
// mail that follows the commit. Any error discards every write, the token claim
// included.
//
// For a create_ticket form (#213) ticketRepo (nil when ticket intake is not
// wired) opens the ticket after the claim and before the submission update:
// the lock order is token, customer, ticket, key, submission.
func (s *formService) confirmInTransaction(ctx context.Context, logger *logrus.Entry, formRepo repository.FormRepository, leadRepo repository.LeadRepository, events repository.AutomationRepository, ticketRepo repository.FormTicketRepository, hooks webhook.TxEmitter, hash string) (*models.Form, *models.FormSubmission, error) {
	// The lookup already excludes spent and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := formRepo.GetConfirmationTokenByHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, fmt.Errorf("confirmation token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.UsedAt != nil || !stored.ExpiresAt.After(time.Now()) {
		return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
	}

	// Claim the token before reading anything else. The claim is the one
	// write competing requests contend on: whoever stamps the row first owns
	// the confirmation, and every other request finds no row to stamp — even
	// one that passed the lookup a moment ago, or one whose token expired
	// between the lookup and this statement.
	if err := formRepo.MarkConfirmationTokenUsed(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, fmt.Errorf("failed to mark confirmation token used: %w", err)
	}

	submission, err := formRepo.GetSubmissionByID(stored.SubmissionID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, err
	}
	if submission.Status != models.FormSubmissionPending {
		return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
	}

	form, err := formRepo.GetByID(submission.FormID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, err
	}

	// A submission stored before values were limited to their columns may
	// hold one the lead insert would reject. Turn it away: the rollback hands
	// the token back, the visitor is told the link is no longer valid and
	// re-submits, where validation now reports the over-long field.
	if name, wide := overlongColumnValue(submission.Data); wide {
		logger.WithField("submission_id", submission.ID).WithField("field", name).
			Warn("Pending submission holds a value wider than its column; confirmation refused")
		return nil, nil, fmt.Errorf("pending submission field %q exceeds its column: %w", name, ErrInvalidConfirmationToken)
	}

	confirmedAt := time.Now()
	submission.Status = models.FormSubmissionConfirmed
	submission.ConfirmedAt = &confirmedAt
	// The confirmation finalises the submission, so this is where its
	// attribution is derived and committed (#220), from the values and the
	// page URL stored when it arrived.
	applyFormAttribution(submission)

	if form.CreateLead {
		if err := s.applySubmissionLead(ctx, leadRepo, events, hooks, form, submission); err != nil {
			return nil, nil, err
		}
	}
	if form.CreateTicket {
		if err := s.applySubmissionTicket(ctx, ticketRepo, events, hooks, form, submission); err != nil {
			return nil, nil, err
		}
	}
	if err := formRepo.UpdateSubmission(submission); err != nil {
		return nil, nil, err
	}
	if form.CreateTicket {
		if err := s.ticketSeam(formTicketStageAfterSubmission); err != nil {
			return nil, nil, err
		}
	}
	return form, submission, nil
}
