package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Ticket comment mail (#219).
//
// A staff comment that is not an internal note may be emailed to the ticket's
// customer. The comment, its first_responded_at stamp and a pending
// ticket_mail_deliveries row are written in one transaction on the request
// context. After the commit, with no transaction held, the customer's current
// address is read live, one plaintext mail is sent and the row is stamped by a
// conditional update on status pending: sent, failed with an error code,
// log_only or skipped with a reason. The call answers nil whatever the mail
// outcome; the outcome travels on comment.Delivery. There is no retry: a row
// left pending (the stamp failed, or the process died after the commit) means
// the outcome is unknown, and nothing ever sends it again.
//
// The row holds ids, a status, an error code and timestamps. The log lines of
// the path carry operation names, ticket_id, comment_id, delivery_id, status
// and error_code: never the address, the title, the body, the subject or the
// mailer's error text, which may quote the address.

// ticketCommentMailStampTimeout bounds the post-commit stamp.
const ticketCommentMailStampTimeout = 5 * time.Second

// ErrTicketCommentMailUnavailable is returned by CreateCommentWithCustomerEmail
// when the service was built without WithTicketCommentMail (no delivery
// repository or no transaction manager). Nothing is written.
var ErrTicketCommentMailUnavailable = errors.New("ticket comment mail is not available: no delivery repository or transaction manager is wired")

// ErrTicketCommentEmailNotAllowed is returned by CreateCommentWithCustomerEmail
// for an internal note or an author whose role is neither admin nor support.
// Nothing is written and nothing is sent.
var ErrTicketCommentEmailNotAllowed = errors.New("only a staff comment that is not an internal note may be emailed to the customer")

// TicketCommentEmailSubject is the fixed subject of a ticket comment mail: no
// request value reaches a header.
func TicketCommentEmailSubject(ticketID uint) string {
	return fmt.Sprintf("Ticket %s from GopherCRM", models.TicketNumber(ticketID))
}

// RenderTicketCommentEmail is the plaintext body: the ticket number, the ticket
// title and the text of the one comment being sent. No other comment of the
// ticket, and never an internal note, is part of it.
func RenderTicketCommentEmail(ticketID uint, title, commentBody string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket %s\n", models.TicketNumber(ticketID))
	fmt.Fprintf(&b, "Title: %s\n", title)
	b.WriteString("\nA new reply was added to your ticket:\n\n")
	b.WriteString(commentBody)
	b.WriteString("\n")
	return b.String()
}

func ticketMailLogger(ticketID uint, delivery *models.TicketMailDelivery, commentID uint) *logrus.Entry {
	fields := logrus.Fields{
		"service":    "TicketService",
		"method":     "CreateCommentWithCustomerEmail",
		"ticket_id":  ticketID,
		"comment_id": commentID,
	}
	if delivery != nil {
		fields["delivery_id"] = delivery.ID
		fields["status"] = delivery.Status
		fields["error_code"] = delivery.ErrorCode
	}
	return utils.Logger.WithFields(fields)
}

// CreateCommentWithCustomerEmail: see the TicketService interface.
func (s *ticketService) CreateCommentWithCustomerEmail(ctx context.Context, comment *models.TicketComment) error {
	logger := ticketMailLogger(comment.TicketID, nil, 0)

	if s.mailDeliveries == nil || s.txManager == nil {
		logger.Error("Ticket comment mail refused: no delivery repository or transaction manager is wired")
		return ErrTicketCommentMailUnavailable
	}
	if utils.InTransaction(ctx) {
		logger.Error("Ticket comment mail refused: called inside an enclosing transaction")
		return utils.ErrTransactionRootRequired
	}
	if comment.IsInternal {
		logger.Warn("Ticket comment mail refused: internal note")
		return ErrTicketCommentEmailNotAllowed
	}

	// The reads that precede the transaction are bound to the request.
	root := s.txManager.DB(ctx)
	ticket, err := s.ticketRepo.WithTx(root).GetByID(comment.TicketID)
	if err != nil {
		if isNotFound(err) {
			logger.Warn("Ticket not found")
			return fmt.Errorf("ticket %d not found: %w", comment.TicketID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up ticket for comment")
		return err
	}
	author, err := s.userRepo.WithTx(root).GetByID(comment.AuthorUserID)
	if err != nil {
		if isNotFound(err) {
			logger.Warn("Author not found")
			return fmt.Errorf("author not found: %w", apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up comment author")
		return err
	}
	if author.Role != models.RoleAdmin && author.Role != models.RoleSupport {
		logger.Warn("Ticket comment mail refused: author is not staff")
		return ErrTicketCommentEmailNotAllowed
	}
	comment.Author = author

	// One transaction: the comment, the first_responded_at stamp (a savepoint
	// of this transaction) and the pending delivery row.
	var delivery *models.TicketMailDelivery
	err = s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		if err := s.ticketRepo.WithTx(tx).CreateCommentWithFirstResponse(comment); err != nil {
			return err
		}
		now := time.Now().UTC()
		row := &models.TicketMailDelivery{
			CreatedAt:  now,
			UpdatedAt:  now,
			TicketID:   ticket.ID,
			CommentID:  comment.ID,
			CustomerID: ticket.CustomerID,
			Status:     models.TicketMailStatusPending,
		}
		if err := s.mailDeliveries.WithTx(tx).Create(ctx, row); err != nil {
			return err
		}
		delivery = row
		return nil
	})
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create ticket comment with its mail delivery")
		return err
	}

	comment.UserID = comment.AuthorUserID
	comment.User = comment.Author
	comment.Content = comment.Body
	comment.Delivery = delivery

	s.deliverTicketComment(ctx, ticket, comment, delivery)
	return nil
}

// deliverTicketComment runs after the commit, with no transaction held: the
// live customer read, at most one Send, then the conditional stamp.
func (s *ticketService) deliverTicketComment(ctx context.Context, ticket *models.Ticket, comment *models.TicketComment, delivery *models.TicketMailDelivery) {
	outcome := s.sendTicketComment(ctx, ticket, comment)
	outcome.Now = time.Now()
	if outcome.Status == models.TicketMailStatusSent {
		sentAt := outcome.Now.UTC()
		outcome.SentAt = &sentAt
	}

	stampCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ticketCommentMailStampTimeout)
	defer cancel()
	stampErr := s.mailDeliveries.WithTx(s.txManager.DB(stampCtx)).FinishPending(stampCtx, delivery.ID, outcome)
	if stampErr != nil {
		// The stored row stays pending, and so does the answer: the outcome
		// is unknown to the database and nothing will send it again.
		ticketMailLogger(ticket.ID, delivery, comment.ID).Warn("Ticket comment mail outcome could not be recorded")
		return
	}

	delivery.Status = outcome.Status
	delivery.ErrorCode = outcome.ErrorCode
	delivery.SentAt = outcome.SentAt
	delivery.UpdatedAt = outcome.Now.UTC()

	logger := ticketMailLogger(ticket.ID, delivery, comment.ID)
	if delivery.Status == models.TicketMailStatusFailed {
		logger.Warn("Ticket comment mail failed")
	} else {
		logger.Info("Ticket comment mail recorded")
	}
}

// sendTicketComment decides the outcome. The mailer is checked before the
// customer is read; Send is called at most once.
func (s *ticketService) sendTicketComment(ctx context.Context, ticket *models.Ticket, comment *models.TicketComment) repository.TicketMailDeliveryOutcome {
	if s.mailer == nil {
		return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusSkipped, ErrorCode: models.TicketMailErrorNoMailer}
	}

	// A live read: an erased customer is soft-deleted and so not found, and an
	// address changed since the ticket was written is the one that is used.
	customer, err := s.customerRepo.WithTx(s.txManager.DB(ctx)).GetByID(ticket.CustomerID)
	if err != nil {
		if isNotFound(err) {
			return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusSkipped, ErrorCode: models.TicketMailErrorCustomerUnavailable}
		}
		return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusFailed, ErrorCode: models.TicketMailErrorLookupFailed}
	}
	to := strings.TrimSpace(customer.Email)
	if to == "" {
		return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusSkipped, ErrorCode: models.TicketMailErrorNoRecipient}
	}

	// The mailer's own error is classified, never logged or stored: a
	// transport error may quote the address.
	if err := s.mailer.Send(to, TicketCommentEmailSubject(ticket.ID), RenderTicketCommentEmail(ticket.ID, ticket.Title, comment.Body)); err != nil {
		return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusFailed, ErrorCode: ticketMailErrorCode(err)}
	}
	if reporter, ok := s.mailer.(mailer.LogOnlyReporter); ok && reporter.LogsOnly() {
		return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusLogOnly}
	}
	return repository.TicketMailDeliveryOutcome{Status: models.TicketMailStatusSent}
}

// ticketMailErrorCode classifies a Send failure without keeping its text.
func ticketMailErrorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return models.TicketMailErrorTimeout
	case errors.Is(err, mailer.ErrInvalidAddress), errors.Is(err, mailer.ErrInvalidHeader):
		return models.TicketMailErrorInvalidRecipient
	default:
		return models.TicketMailErrorMailFailed
	}
}
