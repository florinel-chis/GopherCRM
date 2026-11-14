package service

// Form ticket intake (#213).
//
// A form with create_ticket (which requires double opt-in and excludes
// create_lead) opens one support ticket when a pending submission is
// confirmed, for the live customer whose email matches the confirmed address.
// Everything happens in the confirmation transaction, after the token claim:
//
//	token (claimed) -> customer (locked) -> ticket + key row (one savepoint)
//	-> ticket_created event -> submission update -> form mail rows
//
// The key row's unique (form_id, idempotency_key) index is the guarantee that
// a key opens at most one ticket. The insert goes first, with no pre-read; a
// taken key rolls the savepoint back (the ticket with it) and the committed key
// row decides between duplicate (same customer: the submission links the
// existing ticket) and key_conflict (another customer: no link). A submission
// without a client key uses submission-<submission id>, which deduplicates only
// that one submission.
//
// The public answers never change: SubmitPublic answers with the form's
// outcome and ConfirmSubmission with success whatever the ticket outcome. The
// outcome is recorded on the submission for staff. A service built without
// WithFormTicketIntake records unavailable instead of refusing, because a
// refusal would change the public page, roll the token claim back and leave
// the link failing on every retry.
//
// The ticket text is fixed and holds no submitted value: the title is
// "Form: <form name>" (cut to 255 runes), the description names the form and
// submission ids. Log lines carry ids, the outcome and error codes only.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// formIdempotencyKeyPattern is the accepted shape of a client key.
var formIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// FormIdempotencyKeyRule is the message a refused key gets; it never quotes
// the value.
const FormIdempotencyKeyRule = "must be 1 to 128 characters of A-Z, a-z, 0-9, '.', '_', ':' or '-' and must not start with \"submission-\""

// formTicketTitleMaxLength mirrors the varchar(255) tickets.title column.
const formTicketTitleMaxLength = 255

// The stages the test seam is called at, inside the confirmation transaction.
const (
	formTicketStageAfterKey        = "after_key_insert"
	formTicketStageAfterEvent      = "after_event_insert"
	formTicketStageAfterSubmission = "after_submission_update"
	formTicketStageAfterMail       = "after_mail_record"
)

// ValidFormIdempotencyKey reports whether a client idempotency key is
// acceptable: ^[A-Za-z0-9._:-]{1,128}$ and not starting with the server
// prefix "submission-".
func ValidFormIdempotencyKey(key string) bool {
	return formIdempotencyKeyPattern.MatchString(key) && !strings.HasPrefix(key, models.FormServerKeyPrefix)
}

// WithFormTicketIntake wires ticket intake: confirmed submissions of
// create_ticket forms open tickets through repo, bound to the confirmation
// transaction that txManager (the service's own) carries on the context.
func WithFormTicketIntake(repo repository.FormTicketRepository, txManager *utils.TransactionManager) FormServiceOption {
	return func(s *formService) {
		s.ticketRepo = repo
		s.ticketTx = txManager
	}
}

// ticketRepoFor binds the intake repository to the transaction ctx carries,
// or returns nil when intake is not wired.
func (s *formService) ticketRepoFor(ctx context.Context) repository.FormTicketRepository {
	if s.ticketRepo == nil {
		return nil
	}
	txManager := s.ticketTx
	if txManager == nil {
		txManager = s.txManager
	}
	return s.ticketRepo.WithTx(txManager.DB(ctx))
}

// ticketSeam runs the test hook of a stage; nil in production.
func (s *formService) ticketSeam(stage string) error {
	if s.ticketIntakeSeam == nil {
		return nil
	}
	return s.ticketIntakeSeam(stage)
}

// formTicketKey is the key a confirmation uses: the stored client key, else
// the server key submission-<submission id>.
func formTicketKey(submission *models.FormSubmission) string {
	if submission.IdempotencyKey != "" {
		return submission.IdempotencyKey
	}
	return fmt.Sprintf("%s%d", models.FormServerKeyPrefix, submission.ID)
}

// formTicketTitle is the fixed title of a form ticket.
func formTicketTitle(form *models.Form) string {
	return truncate("Form: "+form.Name, formTicketTitleMaxLength)
}

// formTicketDescription is the fixed description of a form ticket: ids only.
func formTicketDescription(form *models.Form, submission *models.FormSubmission) string {
	return fmt.Sprintf("Submitted through form %d, submission %d.", form.ID, submission.ID)
}

// applySubmissionTicket decides and records the ticket outcome of a confirmed
// submission of a create_ticket form on submission (TicketOutcome, TicketID);
// the caller's UpdateSubmission persists it. Any error rolls the whole
// confirmation back, token claim included.
func (s *formService) applySubmissionTicket(ticketRepo repository.FormTicketRepository, events repository.AutomationRepository, form *models.Form, submission *models.FormSubmission) error {
	submission.TicketID = nil
	if ticketRepo == nil {
		submission.TicketOutcome = models.FormTicketOutcomeUnavailable
		return nil
	}

	customer, err := ticketRepo.LockLiveCustomerByEmail(submission.Email)
	if err != nil {
		if apperrors.IsNotFound(err) {
			submission.TicketOutcome = models.FormTicketOutcomeNoCustomer
			return nil
		}
		return fmt.Errorf("form ticket customer match: %w", err)
	}

	key := formTicketKey(submission)
	ticket := &models.Ticket{
		Title:       formTicketTitle(form),
		Description: formTicketDescription(form, submission),
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityMedium,
		CustomerID:  customer.ID,
	}
	row := &models.FormTicketKey{
		FormID:         form.ID,
		IdempotencyKey: key,
		SubmissionID:   submission.ID,
		CustomerID:     customer.ID,
	}

	err = ticketRepo.CreateTicketWithKey(ticket, row)
	if errors.Is(err, repository.ErrFormTicketKeyTaken) {
		existing, readErr := ticketRepo.GetKeyForUpdate(form.ID, key)
		if readErr != nil {
			return fmt.Errorf("form ticket key read: %w", readErr)
		}
		if existing.CustomerID == customer.ID {
			ticketID := existing.TicketID
			submission.TicketID = &ticketID
			submission.TicketOutcome = models.FormTicketOutcomeDuplicate
		} else {
			submission.TicketOutcome = models.FormTicketOutcomeKeyConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.ticketSeam(formTicketStageAfterKey); err != nil {
		return err
	}

	if err := automationTicketCreated(events, ticket.ID, ticket.Status, models.AutomationOriginForm, nil); err != nil {
		return err
	}
	if err := s.ticketSeam(formTicketStageAfterEvent); err != nil {
		return err
	}

	ticketID := ticket.ID
	submission.TicketID = &ticketID
	submission.TicketOutcome = models.FormTicketOutcomeCreated
	return nil
}

// logFormTicketOutcome records a committed ticket outcome with ids and the
// outcome only: never the key, the address, a value, the form name or the
// ticket text.
func logFormTicketOutcome(logger *logrus.Entry, form *models.Form, submission *models.FormSubmission) {
	entry := logger.WithField("form_id", form.ID).
		WithField("submission_id", submission.ID).
		WithField("ticket_outcome", submission.TicketOutcome)
	if submission.TicketID != nil {
		entry = entry.WithField("ticket_id", *submission.TicketID)
	}
	entry.Info("Form ticket intake recorded")
}
