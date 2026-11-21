package service

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
)

type ticketService struct {
	ticketRepo   repository.TicketRepository
	customerRepo repository.CustomerRepository
	userRepo     repository.UserRepository

	// The ticket comment mail (#219), set by WithTicketCommentMail. Without
	// the delivery repository and the transaction manager,
	// CreateCommentWithCustomerEmail refuses; a nil mailer is recorded as
	// skipped (no_mailer).
	mailDeliveries repository.TicketMailDeliveryRepository
	txManager      *utils.TransactionManager
	mailer         mailer.Mailer

	// productName is the configuration read behind the product name of the
	// ticket reply subject (#292), set by WithTicketProductName. Nil means
	// "GopherCRM".
	productName MailProductNameSource

	// automation records the ticket events of Create and Update (#242) in the
	// write's transaction, set by WithTicketAutomationEvents together with
	// the transaction manager. Nil keeps the unwired behaviour: no transaction
	// and no event.
	automation repository.AutomationRepository

	// webhooks is the outbound webhook emitter (#345), set by
	// WithTicketWebhooks: Create emits ticket.created, Update ticket.updated
	// (#408) and Delete ticket.deleted (#437) in the ticket's transaction.
	// NewTicketService sets it to webhook.Nop, so it is never nil.
	webhooks webhook.Emitter

	// writesInTx is set by WithTicketWebhooks only when it gets both an
	// emitter and a transaction manager. It routes Create, Update and Delete
	// onto their transactional paths (Update with a locking read), whether or
	// not automation events are wired. A transaction manager supplied only by
	// another option (WithTicketCommentMail, WithTicketAutomationEvents) does
	// not set it.
	writesInTx bool
}

// WithTicketWebhooks makes Create insert the ticket and emit ticket.created
// through e in one transaction run by txManager (#345), and Update run as one
// transaction with a locking read that emits ticket.updated when a stored
// value changed (#408), also without automation events. Delete then
// soft-deletes the ticket and emits ticket.deleted for the active endpoints
// subscribed to ticket.deleted in one transaction (#437, #531). A non-nil
// txManager is stored as the service's transaction manager either way; the
// emitter and those transactional paths are set only when both e and
// txManager are non-nil, whatever manager another option supplies.
func WithTicketWebhooks(e webhook.Emitter, txManager *utils.TransactionManager) TicketServiceOption {
	return func(s *ticketService) {
		if txManager != nil {
			s.txManager = txManager
		}
		if e != nil && txManager != nil {
			s.webhooks = e
			s.writesInTx = true
		}
	}
}

// ticketWritesInTx reports whether Create, Update and Delete run in one
// transaction that emits their webhook events.
func (s *ticketService) ticketWritesInTx() bool {
	return s.writesInTx && s.txManager != nil
}

// WithTicketAutomationEvents makes Create and Update record their automation
// events (#242) in one transaction with the ticket write: Create inserts the
// ticket and ticket_created (to_value the stored status); Update locks the
// ticket row first (TicketLockingReader), runs the closed-reopen and assignee
// checks against the locked row, saves, and records ticket_updated plus
// ticket_status_changed when the status differs. Both events carry origin user
// and no actor (the TicketService interface carries none). A refused or failed
// write records nothing.
func WithTicketAutomationEvents(repo repository.AutomationRepository, txManager *utils.TransactionManager) TicketServiceOption {
	return func(s *ticketService) {
		s.automation = repo
		if txManager != nil {
			s.txManager = txManager
		}
	}
}

// ticketEventsWired reports whether ticket writes record automation events.
func (s *ticketService) ticketEventsWired() bool {
	return s.automation != nil && s.txManager != nil
}

// WithTicketProductName makes the ticket reply mail subject read its product
// name from source (general.company_name, through ResolveMailProductName) on
// every send. Without it the subject names "GopherCRM".
func WithTicketProductName(source MailProductNameSource) TicketServiceOption {
	return func(s *ticketService) {
		s.productName = source
	}
}

// TicketServiceOption configures NewTicketService.
type TicketServiceOption func(*ticketService)

// WithTicketCommentMail enables CreateCommentWithCustomerEmail (#219): the
// delivery records are written through deliveryRepo inside transactions run
// by txManager, and the mail goes out through m (nil records every requested
// send as skipped with no_mailer).
func WithTicketCommentMail(deliveryRepo repository.TicketMailDeliveryRepository, txManager *utils.TransactionManager, m mailer.Mailer) TicketServiceOption {
	return func(s *ticketService) {
		s.mailDeliveries = deliveryRepo
		s.txManager = txManager
		s.mailer = m
	}
}

func NewTicketService(ticketRepo repository.TicketRepository, customerRepo repository.CustomerRepository, userRepo repository.UserRepository, opts ...TicketServiceOption) TicketService {
	s := &ticketService{
		ticketRepo:   ticketRepo,
		customerRepo: customerRepo,
		userRepo:     userRepo,
		webhooks:     webhook.Nop,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *ticketService) Create(ticket *models.Ticket) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "ticket"), "TicketService", "Create")

	// Set default values
	if ticket.Status == "" {
		ticket.Status = models.TicketStatusOpen
	}
	if ticket.Priority == "" {
		ticket.Priority = models.TicketPriorityMedium
	}

	// Verify customer exists
	_, err := s.customerRepo.GetByID(ticket.CustomerID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer not found")
		return fmt.Errorf("customer not found: %w", apperrors.ErrCustomerNotFound)
	}

	// Verify assignee exists if specified
	if ticket.AssignedToID != nil {
		assignee, err := s.userRepo.GetByID(*ticket.AssignedToID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Assignee not found")
			return fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
		}
		// Only support and admin users can be assigned tickets
		if !permissions.ForUser(assignee).Has(permissions.AssignmentTicketAssignee) {
			logger.Warn("Invalid assignee role")
			return fmt.Errorf("tickets can only be assigned to support or admin users: %w", apperrors.ErrInvalidAssigneeRole)
		}
	}

	if !s.ticketEventsWired() && !s.ticketWritesInTx() {
		if err := s.ticketRepo.Create(ticket); err != nil {
			logger.WithError(utils.LogError(err)).Error("Failed to create ticket")
			return err
		}
		logger.WithField("ticket_id", ticket.ID).Info("Ticket created successfully")
		return nil
	}

	// The ticket, its ticket_created event and its ticket.created webhook
	// outbox rows (#345) commit together or not at all.
	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		if err := s.ticketRepo.WithTx(tx).Create(ticket); err != nil {
			return err
		}
		if err := automationTicketCreated(automationTx(s.automation, tx), ticket.ID, ticket.Status, models.AutomationOriginUser, nil); err != nil {
			return err
		}
		return s.webhooks.Bind(tx).Created(ctx, webhook.Ticket, ticket.ID)
	})
	if err != nil {
		ticket.ID = 0
		logger.WithError(utils.LogError(err)).Error("Failed to create ticket")
		return err
	}

	logger.WithFields(map[string]interface{}{"ticket_id": ticket.ID, "status": ticket.Status}).Info("Ticket created successfully")
	return nil
}

func (s *ticketService) GetByID(id uint) (*models.Ticket, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", id), "TicketService", "GetByID")

	ticket, err := s.ticketRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			return nil, fmt.Errorf("ticket %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to get ticket")
		return nil, err
	}

	logger.Debug("Ticket retrieved successfully")
	return ticket, nil
}

func (s *ticketService) GetByCustomer(customerID uint, offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"customer_id": customerID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "GetByCustomer")

	tickets, err := s.ticketRepo.GetByCustomerID(customerID, offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get tickets by customer")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountByCustomerID(customerID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tickets by customer")
		return nil, 0, err
	}

	logger.WithField("count", len(tickets)).Info("Tickets retrieved by customer")
	return tickets, total, nil
}

func (s *ticketService) GetByAssignee(assigneeID uint, offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"assignee_id": assigneeID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "GetByAssignee")

	tickets, err := s.ticketRepo.GetByAssignedToID(assigneeID, offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get tickets by assignee")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountByAssignedToID(assigneeID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tickets by assignee")
		return nil, 0, err
	}

	logger.WithField("count", len(tickets)).Info("Tickets retrieved by assignee")
	return tickets, total, nil
}

func (s *ticketService) Update(ticket *models.Ticket) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", ticket.ID), "TicketService", "Update")

	if s.ticketEventsWired() || s.ticketWritesInTx() {
		return s.updateWithEvents(ticket, logger)
	}

	// Get existing ticket to validate state transitions
	existing, err := s.ticketRepo.GetByID(ticket.ID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Ticket not found")
		return err
	}

	validatedAssignee, err := checkTicketUpdate(logger, existing, ticket, s.userRepo)
	if err != nil {
		return err
	}
	detachStaleTicketAssignee(ticket)

	if err := s.ticketRepo.Update(ticket); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to update ticket")
		return err
	}

	attachTicketAssignee(ticket, validatedAssignee, existing)
	logger.Info("Ticket updated successfully")
	return nil
}

// updateWithEvents is Update with automation events (#242) or webhooks
// (#408) wired; with webhooks it also emits ticket.updated, in the same
// transaction, when a stored value differs from the locked row's. It is one
// transaction that locks the ticket row first (GetByIDForUpdate), runs the
// closed-reopen and assignee checks against the row that locking read
// returns, saves the ticket, and records ticket_updated plus, when the status
// differs from the locked row's, ticket_status_changed. The locking read
// matters on MySQL: a plain read could answer from a snapshot older than a
// status committed by another connection, which would let a closed ticket be
// reopened or record a wrong from_value. A refused update and a failed write
// or event insert roll everything back and record no event.
func (s *ticketService) updateWithEvents(ticket *models.Ticket, logger *logrus.Entry) error {
	var existing *models.Ticket
	var validatedAssignee *models.User
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		tickets := s.ticketRepo.WithTx(tx)
		locker, ok := tickets.(repository.TicketLockingReader)
		if !ok {
			return errTicketLockingReadUnavailable
		}
		locked, err := locker.GetByIDForUpdate(ticket.ID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			return err
		}
		existing = locked
		validatedAssignee, err = checkTicketUpdate(logger, locked, ticket, s.userRepo.WithTx(tx))
		if err != nil {
			return err
		}
		detachStaleTicketAssignee(ticket)
		if err := tickets.Update(ticket); err != nil {
			logger.WithError(utils.LogError(err)).Error("Failed to update ticket")
			return err
		}
		if err := automationTicketUpdated(automationTx(s.automation, tx), ticket.ID, locked.Status, ticket.Status, models.AutomationOriginUser, nil); err != nil {
			return err
		}
		if !ticketStoredValuesChanged(locked, ticket) {
			return nil
		}
		return s.webhooks.Bind(tx).Updated(ctx, webhook.Ticket, ticket.ID)
	})
	if err != nil {
		return err
	}

	attachTicketAssignee(ticket, validatedAssignee, existing)
	logger.WithField("status_changed", existing.Status != ticket.Status).Info("Ticket updated successfully")
	return nil
}

// ticketStoredValuesChanged reports whether an update changed any column a
// ticket update can change: the rule of the ticket.updated webhook event
// (#408). due_at is compared as an instant; updated_at is not part of it.
func ticketStoredValuesChanged(before, after *models.Ticket) bool {
	return before.Title != after.Title ||
		before.Description != after.Description ||
		before.Status != after.Status ||
		before.Priority != after.Priority ||
		!equalUintPtr(before.AssignedToID, after.AssignedToID) ||
		before.Resolution != after.Resolution ||
		!equalTimePtr(before.DueAt, after.DueAt)
}

// errTicketLockingReadUnavailable is returned by a wired Update whose ticket
// repository offers no locking read; nothing is written.
var errTicketLockingReadUnavailable = errors.New("ticket repository has no locking read")

// checkTicketUpdate refuses reopening a closed ticket and an assignee change
// to a user that does not exist or is not support or admin, against the
// stored ticket existing. It returns the assignee it looked up, if any.
func checkTicketUpdate(logger *logrus.Entry, existing, ticket *models.Ticket, users repository.UserRepository) (*models.User, error) {
	// Validate status transitions
	if existing.Status == models.TicketStatusClosed && ticket.Status != models.TicketStatusClosed {
		logger.Warn("Cannot reopen closed ticket")
		return nil, fmt.Errorf("cannot reopen closed ticket: %w", apperrors.ErrClosedTicketReopen)
	}

	// Verify assignee if changed
	if ticket.AssignedToID != nil && (existing.AssignedToID == nil || *ticket.AssignedToID != *existing.AssignedToID) {
		assignee, err := users.GetByID(*ticket.AssignedToID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Assignee not found")
			return nil, fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
		}
		if !permissions.ForUser(assignee).Has(permissions.AssignmentTicketAssignee) {
			logger.Warn("Invalid assignee role")
			return nil, fmt.Errorf("tickets can only be assigned to support or admin users: %w", apperrors.ErrInvalidAssigneeRole)
		}
		return assignee, nil
	}
	return nil, nil
}

// detachStaleTicketAssignee drops a preloaded assignee that is not the one
// the foreign key names. The repository saves with GORM's Save, which writes a
// preloaded belongs-to id back over the foreign key. Compare the attached user
// with the foreign key, not with the validated change: the caller's read and
// the service's read are separate, so the latter may already show the
// requested user.
func detachStaleTicketAssignee(ticket *models.Ticket) {
	if ticket.AssignedTo != nil && (ticket.AssignedToID == nil || ticket.AssignedTo.ID != *ticket.AssignedToID) {
		ticket.AssignedTo = nil
	}
}

// attachTicketAssignee answers with the user the stored foreign key names.
// Without a lookup the key equals the one read before the write, so that
// read's user is the right one.
func attachTicketAssignee(ticket *models.Ticket, validatedAssignee *models.User, existing *models.Ticket) {
	if ticket.AssignedToID != nil {
		if validatedAssignee != nil {
			ticket.AssignedTo = validatedAssignee
		} else if ticket.AssignedTo == nil && existing != nil {
			ticket.AssignedTo = existing.AssignedTo
		}
	}
}

func (s *ticketService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", id), "TicketService", "Delete")

	// Check if ticket exists
	_, err := s.ticketRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			return fmt.Errorf("ticket %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up ticket for deletion")
		return err
	}

	// The repository locks the ticket, purges its mail deliveries, cancels its
	// open automation runs (record_deleted) and purges its notifications in the
	// transaction that soft-deletes it (#242). With webhooks wired (#437) that
	// transaction is a savepoint of one run by txManager, which then emits
	// ticket.deleted for the active endpoints subscribed to ticket.deleted: the
	// delete and its outbox rows commit together or not at all.
	if err := s.deleteTicket(id); err != nil {
		if isNotFound(err) {
			logger.Warn("Ticket deleted meanwhile")
			return fmt.Errorf("ticket %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete ticket")
		return err
	}

	logger.Info("Ticket deleted successfully")
	return nil
}

// deleteTicket soft-deletes the ticket, and with webhooks wired emits
// ticket.deleted in the same transaction, after the delete succeeded.
func (s *ticketService) deleteTicket(id uint) error {
	if !s.ticketWritesInTx() {
		return s.ticketRepo.Delete(id)
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		if err := s.ticketRepo.WithTx(tx).Delete(id); err != nil {
			return err
		}
		return s.webhooks.Bind(tx).Deleted(ctx, webhook.Ticket, id)
	})
}

func (s *ticketService) List(offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TicketService", "List")

	tickets, err := s.ticketRepo.List(offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tickets")
		return nil, 0, err
	}

	total, err := s.ticketRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tickets")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tickets listed successfully")
	return tickets, total, nil
}

func (s *ticketService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TicketService", "ListSorted")

	tickets, err := s.ticketRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, "Customer", "AssignedTo")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tickets sorted")
		return nil, 0, err
	}

	total, err := s.ticketRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tickets")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tickets listed sorted successfully")
	return tickets, total, nil
}

func (s *ticketService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TicketService", "Search")

	tickets, err := s.ticketRepo.Search(query, offset, limit, sortBy, sortOrder, "Customer", "AssignedTo")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to search tickets")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountSearch(query)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Ticket search completed")
	return tickets, total, nil
}

// ListAssigned lists the tickets assigned to one user, optionally narrowed by a
// free-text search, for GET /tickets as a support caller. The search, sort and
// pagination parameters can only narrow or reorder that set, never widen it.
func (s *ticketService) ListAssigned(assigneeID uint, search string, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"assignee_id": assigneeID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "ListAssigned")

	scope := repository.TicketAssigneeScope{AssignedToID: assigneeID, Search: search}
	tickets, err := s.ticketRepo.ListByAssigneeScope(scope, offset, limit, sortBy, sortOrder, "Customer", "AssignedTo")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list assigned tickets")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountByAssigneeScope(scope)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count assigned tickets")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Assigned tickets listed successfully")
	return tickets, total, nil
}

// ListFiltered lists one page of the tickets matching filter, with their
// customer and assignee, and the number of tickets the same filter matches
// before pagination. The log lines carry ids, offsets and counts only: never
// the search term, and the status and priority only as presence flags.
func (s *ticketService) ListFiltered(filter repository.TicketListFilter, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error) {
	fields := map[string]interface{}{
		"offset":          offset,
		"limit":           limit,
		"status_filter":   filter.Status != "",
		"priority_filter": filter.Priority != "",
		"search_filter":   filter.Search != "",
	}
	if filter.AssigneeID != nil {
		fields["assignee_id"] = *filter.AssigneeID
	}
	logger := utils.LogServiceCall(utils.Logger.WithFields(fields), "TicketService", "ListFiltered")

	tickets, err := s.ticketRepo.ListByFilter(filter, offset, limit, sortBy, sortOrder, "Customer", "AssignedTo")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list filtered tickets")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountByFilter(filter)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count filtered tickets")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Filtered tickets listed successfully")
	return tickets, total, nil
}

// GetByCustomerAndAssignee lists one customer's tickets that are assigned to
// one user, for GET /customers/:id/tickets as a support caller.
func (s *ticketService) GetByCustomerAndAssignee(customerID, assigneeID uint, offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"customer_id": customerID,
		"assignee_id": assigneeID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "GetByCustomerAndAssignee")

	scope := repository.TicketAssigneeScope{AssignedToID: assigneeID, CustomerID: &customerID}
	tickets, err := s.ticketRepo.ListByAssigneeScope(scope, offset, limit, "", "")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get assigned tickets by customer")
		return nil, 0, err
	}

	total, err := s.ticketRepo.CountByAssigneeScope(scope)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count assigned tickets by customer")
		return nil, 0, err
	}

	logger.WithField("count", len(tickets)).Info("Assigned tickets retrieved by customer")
	return tickets, total, nil
}

func (s *ticketService) GetOpenCount() (int64, error) {
	return s.ticketRepo.CountOpen()
}

// GetPriorityCounts returns the live ticket count per priority, for the
// dashboard's priority distribution chart. Priorities with no rows are missing
// from the map — the handler owns the label set.
func (s *ticketService) GetPriorityCounts() (map[string]int64, error) {
	return s.ticketRepo.CountByPriority()
}

// GetRecent returns the newest tickets across every assignee, for the dashboard
// widget and activity feed of the roles that see every ticket.
func (s *ticketService) GetRecent(limit int) ([]models.Ticket, error) {
	return s.ticketRepo.ListRecent(nil, limit)
}

// GetRecentByAssignee returns the newest tickets assigned to one user. The
// support role is narrowed through here.
func (s *ticketService) GetRecentByAssignee(assigneeID uint, limit int) ([]models.Ticket, error) {
	return s.ticketRepo.ListRecent(&assigneeID, limit)
}

// GetRecentlyResolved returns the tickets most recently resolved or closed
// across every assignee, for the activity feed.
func (s *ticketService) GetRecentlyResolved(limit int) ([]models.Ticket, error) {
	return s.ticketRepo.ListRecentlyResolved(nil, limit)
}

// GetRecentlyResolvedByAssignee returns the tickets most recently resolved or
// closed that are assigned to one user.
func (s *ticketService) GetRecentlyResolvedByAssignee(assigneeID uint, limit int) ([]models.Ticket, error) {
	return s.ticketRepo.ListRecentlyResolved(&assigneeID, limit)
}

func (s *ticketService) CreateComment(comment *models.TicketComment) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", comment.TicketID), "TicketService", "CreateComment")

	// Verify ticket exists
	_, err := s.ticketRepo.GetByID(comment.TicketID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			return fmt.Errorf("ticket %d not found: %w", comment.TicketID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to lookup ticket for comment")
		return err
	}

	// Verify author exists
	author, err := s.userRepo.GetByID(comment.AuthorUserID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Author not found")
			return fmt.Errorf("author not found: %w", apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to lookup comment author")
		return err
	}
	comment.Author = author

	// The first comment a staff user writes that is not an internal note is
	// the ticket's first response (#278): it is inserted together with the
	// first_responded_at stamp. Internal notes and other authors' comments are
	// plain inserts.
	if !comment.IsInternal && (author.Role == models.RoleAdmin || author.Role == models.RoleSupport) {
		err = s.ticketRepo.CreateCommentWithFirstResponse(comment)
	} else {
		err = s.ticketRepo.CreateComment(comment)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create ticket comment")
		return err
	}

	comment.UserID = comment.AuthorUserID
	comment.User = comment.Author
	comment.Content = comment.Body

	logger.WithField("comment_id", comment.ID).Info("Ticket comment created successfully")
	return nil
}

func (s *ticketService) ListComments(ticketID uint, includeInternal bool) ([]models.TicketComment, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", ticketID), "TicketService", "ListComments")

	// Verify ticket exists
	_, err := s.ticketRepo.GetByID(ticketID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			return nil, fmt.Errorf("ticket %d not found: %w", ticketID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to lookup ticket for listing comments")
		return nil, err
	}

	comments, err := s.ticketRepo.ListComments(ticketID, includeInternal)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list ticket comments")
		return nil, err
	}

	for i := range comments {
		comments[i].UserID = comments[i].AuthorUserID
		comments[i].User = comments[i].Author
		comments[i].Content = comments[i].Body
	}

	logger.WithField("count", len(comments)).Info("Ticket comments retrieved successfully")
	return comments, nil
}
