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
// The description is fixed and holds no submitted value: it names the form
// and submission ids. The title is "Form: <form name>" unless the form's
// ticket_title_field (#804) names a field that the stored form still has as a
// text or select field and the submission holds a non-blank value for it; the
// title is then "<form name>: <value>", control characters replaced by spaces
// and trimmed. Either title is cut to 255 runes. Log lines carry ids, the
// outcome and error codes only, never the title.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
)

// formIdempotencyKeyPattern is the accepted shape of a client key.
var formIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// FormIdempotencyKeyRule is the message a refused key gets; it never quotes
// the value, nor the reserved server prefix (a refused key may be exactly that
// prefix, and the answer must not echo it).
const FormIdempotencyKeyRule = "must be 1 to 128 characters of A-Z, a-z, 0-9, '.', '_', ':' or '-' and must not use the prefix reserved for server keys"

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

// formTicketTitle is the title of a form ticket: "<form name>: <value>" when
// the form's ticket_title_field names a current text or select field and the
// submission holds a non-blank value for it, else "Form: <form name>". Either
// is cut to 255 runes.
func formTicketTitle(form *models.Form, submission *models.FormSubmission) string {
	if value := formTicketTitleValue(form, submission); value != "" {
		return truncate(form.Name+": "+value, formTicketTitleMaxLength)
	}
	return truncate("Form: "+form.Name, formTicketTitleMaxLength)
}

// formTicketTitleValue is the cleaned submitted value of the configured title
// field, or "" when there is none to use: no setting, a field the stored form
// no longer has as a text or select field, or a blank value.
func formTicketTitleValue(form *models.Form, submission *models.FormSubmission) string {
	name := form.TicketTitleField
	if name == "" {
		return ""
	}
	usable := false
	for _, field := range form.Fields {
		if field.Name == name {
			usable = field.Type == models.FormFieldText || field.Type == models.FormFieldSelect
			break
		}
	}
	if !usable {
		return ""
	}
	value := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, submission.Data[name])
	return strings.TrimSpace(value)
}

// formTicketDescription is the fixed description of a form ticket: ids only.
func formTicketDescription(form *models.Form, submission *models.FormSubmission) string {
	return fmt.Sprintf("Submitted through form %d, submission %d.", form.ID, submission.ID)
}

// applySubmissionTicket decides and records the ticket outcome of a confirmed
// submission of a create_ticket form on submission (TicketOutcome, TicketID);
// the caller's UpdateSubmission persists it. Any error rolls the whole
// confirmation back, token claim included. hooks (webhook.NopTx without
// webhooks) emits ticket.created for the outcome created only, never for a
// duplicate (#345).
func (s *formService) applySubmissionTicket(ctx context.Context, ticketRepo repository.FormTicketRepository, events repository.AutomationRepository, hooks webhook.TxEmitter, form *models.Form, submission *models.FormSubmission) error {
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
		Title:       formTicketTitle(form, submission),
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
	if err := hooks.Created(ctx, webhook.Ticket, ticket.ID); err != nil {
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
