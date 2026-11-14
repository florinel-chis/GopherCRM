package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Bulk status updates.
//
// These three operations back POST /leads/bulk/status, /tickets/bulk/status and
// /tasks/bulk/status. They are all-or-nothing, which is the one property that
// shapes everything below: every listed record is read and checked — it exists,
// the caller may change it, the transition is legal — before any record is
// written, and both halves run inside a single transaction. A batch that fails
// leaves the database exactly as it was, and the error names the records that
// caused the refusal so the client can show them.
//
// Authorization is not a lighter version of the per-record rules. Whichever
// endpoint a change arrives through, it means the same thing, so each of these
// mirrors the corresponding single-record update: sales users may only touch
// leads they own, support users only tickets assigned to them, everyone but an
// admin only tasks assigned to them, a completed task's status is fixed and a
// closed ticket cannot be reopened.

// bulkStatusForbidden, bulkStatusNotFound and bulkStatusInvalidInput build the
// errors these operations report. The sentinel is the cause, so callers
// classify with errors.Is; the code fixes the HTTP status and the details carry
// the offending IDs.
func bulkStatusForbidden(message string, ids []uint) error {
	err := apperrors.Wrap(apperrors.ErrForbidden, apperrors.CodeInsufficientPermissions, message)
	if len(ids) > 0 {
		err = err.WithDetail("forbidden_ids", ids)
	}
	return err
}

func bulkStatusNotFound(resource string, ids []uint) error {
	return apperrors.Wrap(apperrors.ErrNotFound, apperrors.CodeNotFound,
		fmt.Sprintf("One or more %s were not found", resource)).WithDetail("missing_ids", ids)
}

func bulkStatusInvalidInput(message string) *apperrors.AppError {
	return apperrors.New(apperrors.CodeInvalidInput, message)
}

// normalizeBulkStatusIDs rejects a batch that is empty or over the cap and
// collapses repeated IDs, which name one record and so count once.
func normalizeBulkStatusIDs(ids []uint) ([]uint, error) {
	if len(ids) == 0 {
		return nil, bulkStatusInvalidInput("At least one ID is required")
	}
	if len(ids) > models.MaxBulkStatusItems {
		return nil, bulkStatusInvalidInput(
			fmt.Sprintf("A bulk status update accepts at most %d IDs", models.MaxBulkStatusItems)).
			WithDetail("max", models.MaxBulkStatusItems).
			WithDetail("received", len(ids))
	}

	seen := make(map[uint]struct{}, len(ids))
	unique := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, bulkStatusInvalidInput("IDs must be greater than zero")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

// missingIDs reports which of the requested IDs the read did not return.
func missingIDs(requested []uint, found map[uint]struct{}) []uint {
	var missing []uint
	for _, id := range requested {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func isValidLeadStatus(status models.LeadStatus) bool {
	switch status {
	case models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusQualified,
		models.LeadStatusUnqualified, models.LeadStatusConverted:
		return true
	default:
		return false
	}
}

func isValidTicketStatus(status models.TicketStatus) bool {
	switch status {
	case models.TicketStatusOpen, models.TicketStatusInProgress,
		models.TicketStatusResolved, models.TicketStatusClosed:
		return true
	default:
		return false
	}
}

func isValidTaskStatus(status models.TaskStatus) bool {
	switch status {
	case models.TaskStatusPending, models.TaskStatusInProgress,
		models.TaskStatusCompleted, models.TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// runBulkStatusUpdate records the operation, runs apply inside one transaction
// and reports the outcome against that record. apply returning an error rolls
// the transaction back, so nothing apply wrote survives a refusal.
//
// ctx is the caller's request. The history row and the transaction are bound
// to it, so a request cancelled before the history row is written leaves no
// trace, and one cancelled while the batch waits for a connection or a row
// lock rolls the batch back.
//
// completed is the last write of the batch transaction (#196):
// BulkOperationRepository.WithTx(tx).CompletePending, a conditional update by
// primary key from pending. It commits with the records or not at all, so a
// row that is still pending belongs to a batch that did not commit. When it
// finds no pending row — the stale-pending sweep already failed it — it
// returns apperrors.ErrBulkOperationNotPending and the whole batch rolls back.
//
// After a batch that rolled back, failed is recorded against the history row
// on a context that keeps the request's values but not its cancellation,
// bounded by bulkOutcomeTimeout. An outcome write that cannot finish within the
// bound (the connection or the row is still held elsewhere) is logged and the
// row stays pending until the sweep (BulkOperationRecoveryRuntime) fails it.
//
// A batch is a transaction root: its history row and its failed outcome are
// committed on their own, which an enclosing transaction could not roll back,
// so a ctx that already carries a transaction is refused with
// utils.ErrTransactionRootRequired before anything is written.
func (s *bulkStatusService) runBulkStatusUpdate(ctx context.Context, actorID uint, resourceType string, ids []uint, apply func(txRepo repository.BulkStatusRepository, events repository.AutomationRepository) error) (*models.BulkStatusUpdateResult, error) {
	if utils.InTransaction(ctx) {
		return nil, utils.ErrTransactionRootRequired
	}
	operation, err := s.createBulkOperation(ctx, actorID, resourceType, models.BulkAction, len(ids))
	if err != nil {
		return nil, err
	}

	err = s.transactionMgr.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok || tx == nil {
			return fmt.Errorf("transaction not found in context")
		}
		if err := apply(s.statusRepo.WithTx(tx), automationTx(s.automation, tx)); err != nil {
			return err
		}
		return s.bulkOperationRepo.WithTx(tx).CompletePending(ctx, operation.ID)
	})

	if err != nil {
		s.recordBulkStatusOutcome(ctx, operation.ID)
		return nil, err
	}

	return &models.BulkStatusUpdateResult{Updated: len(ids)}, nil
}

// bulkOutcomeTimeout bounds the write that records a failed batch's outcome
// once the batch itself has rolled back or been abandoned.
var bulkOutcomeTimeout = 5 * time.Second

// recordBulkStatusOutcome sets the history row of a batch that did not commit
// to failed. A failure to write it must not change what the caller is told
// about the records themselves, which are already rolled back, so it is logged
// (operation_id and the error only) and swallowed; the row then stays pending
// until the stale-pending sweep fails it. A row that is no longer pending (the
// sweep got there first) is already terminal: that is not a failure and is not
// logged.
func (s *bulkStatusService) recordBulkStatusOutcome(ctx context.Context, operationID uint) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bulkOutcomeTimeout)
	defer cancel()
	err := s.updateBulkOperationStatus(ctx, operationID, models.StatusFailed)
	if err == nil || errors.Is(err, apperrors.ErrBulkOperationNotPending) {
		return
	}
	s.logger.WithError(utils.LogError(err)).WithField("operation_id", operationID).
		Warn("Failed to record bulk status operation outcome")
}

// BulkSetLeadStatus sets one status on every listed lead.
//
// Admins may update any lead; sales users only leads they own, and a batch
// containing even one lead they do not own is refused in full, naming those
// leads. Support and customer users have no access to leads at all.
func (s *bulkStatusService) BulkSetLeadStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.LeadStatus) (*models.BulkStatusUpdateResult, error) {
	unique, err := normalizeBulkStatusIDs(ids)
	if err != nil {
		return nil, err
	}
	if !isValidLeadStatus(status) {
		return nil, bulkStatusInvalidInput(fmt.Sprintf("Invalid lead status: %s", status))
	}
	// Only the conversion, which creates the customer, sets converted.
	if status == models.LeadStatusConverted {
		return nil, ConvertedStatusRefusal()
	}
	if actorRole != models.RoleAdmin && actorRole != models.RoleSales {
		return nil, bulkStatusForbidden("Only sales and admin users can update leads", nil)
	}

	return s.runBulkStatusUpdate(ctx, actorID, "leads", unique, func(txRepo repository.BulkStatusRepository, events repository.AutomationRepository) error {
		// A locking read in id order (#182). Bulk lead status takes no
		// company lock: SetLeadStatus never writes company_id. It locks only
		// its leads, in the "leads by id ascending" step of the shared order
		// companies -> leads -> customers -> deals, so a company delete either
		// waits for the batch at its UnlinkLeads or makes the batch wait here.
		leads, err := txRepo.GetLeadsByIDs(unique)
		if err != nil {
			return err
		}
		if s.afterLeadLock != nil {
			s.afterLeadLock(unique)
		}

		found := make(map[uint]struct{}, len(leads))
		notOwned := make([]uint, 0)
		for _, lead := range leads {
			found[lead.ID] = struct{}{}
			if actorRole == models.RoleSales && lead.OwnerID != actorID {
				notOwned = append(notOwned, lead.ID)
			}
		}

		if missing := missingIDs(unique, found); len(missing) > 0 {
			return bulkStatusNotFound("leads", missing)
		}
		if len(notOwned) > 0 {
			return bulkStatusForbidden("You can only update your own leads", notOwned)
		}

		if err := txRepo.SetLeadStatus(unique, status); err != nil {
			return err
		}
		// The events ride in the same transaction as the write: every lead
		// gets lead_updated, and the ones whose status actually changed
		// lead_status_changed with the previous and the new value. A refused
		// batch rolls everything back, events included.
		actor := actorID
		for _, lead := range leads {
			if err := automationLeadUpdated(events, lead.ID, lead.Status, status, models.AutomationOriginBulk, &actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// BulkSetTicketStatus sets one status on every listed ticket.
//
// Admins may update any ticket, support users only tickets assigned to them.
// Sales users are read-only on tickets and customers cannot update them at all,
// so both are refused outright. A closed ticket cannot be reopened here either:
// the single-record update refuses that transition, and the bulk endpoint must
// not be a way around it.
func (s *bulkStatusService) BulkSetTicketStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.TicketStatus) (*models.BulkStatusUpdateResult, error) {
	unique, err := normalizeBulkStatusIDs(ids)
	if err != nil {
		return nil, err
	}
	if !isValidTicketStatus(status) {
		return nil, bulkStatusInvalidInput(fmt.Sprintf("Invalid ticket status: %s", status))
	}
	switch actorRole {
	case models.RoleAdmin, models.RoleSupport:
	case models.RoleSales:
		return nil, bulkStatusForbidden("Sales users cannot update tickets", nil)
	case models.RoleCustomer:
		return nil, bulkStatusForbidden("Customers cannot update tickets", nil)
	default:
		return nil, bulkStatusForbidden("Your role cannot update tickets", nil)
	}

	return s.runBulkStatusUpdate(ctx, actorID, "tickets", unique, func(txRepo repository.BulkStatusRepository, events repository.AutomationRepository) error {
		// A locking read in id order (#242): the rows are locked before the
		// checks, and the events below carry the status this read returned.
		tickets, err := txRepo.GetTicketsByIDs(unique)
		if err != nil {
			return err
		}

		found := make(map[uint]struct{}, len(tickets))
		notAssigned := make([]uint, 0)
		closed := make([]uint, 0)
		for _, ticket := range tickets {
			found[ticket.ID] = struct{}{}
			if actorRole == models.RoleSupport &&
				(ticket.AssignedToID == nil || *ticket.AssignedToID != actorID) {
				notAssigned = append(notAssigned, ticket.ID)
			}
			if ticket.Status == models.TicketStatusClosed && status != models.TicketStatusClosed {
				closed = append(closed, ticket.ID)
			}
		}

		if missing := missingIDs(unique, found); len(missing) > 0 {
			return bulkStatusNotFound("tickets", missing)
		}
		if len(notAssigned) > 0 {
			return bulkStatusForbidden("You can only update tickets assigned to you", notAssigned)
		}
		if len(closed) > 0 {
			return apperrors.Wrap(apperrors.ErrClosedTicketReopen, apperrors.CodeInvalidStatusTransition,
				"Cannot reopen closed tickets").WithDetail("closed_ids", closed)
		}

		if err := txRepo.SetTicketStatus(unique, status); err != nil {
			return err
		}
		// The events ride in the batch transaction (#242): every ticket gets
		// ticket_updated, and the ones whose locked status differs
		// ticket_status_changed, origin bulk, the caller as actor. A refused
		// batch returned above, before any write, and records nothing.
		actor := actorID
		for _, ticket := range tickets {
			if err := automationTicketUpdated(events, ticket.ID, ticket.Status, status, models.AutomationOriginBulk, &actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// BulkSetTaskStatus sets one status on every listed task.
//
// Admins may update any task, everyone else only tasks assigned to them. A
// completed task's status is final for admins too, exactly as it is through the
// single-record update.
func (s *bulkStatusService) BulkSetTaskStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.TaskStatus) (*models.BulkStatusUpdateResult, error) {
	unique, err := normalizeBulkStatusIDs(ids)
	if err != nil {
		return nil, err
	}
	if !isValidTaskStatus(status) {
		return nil, bulkStatusInvalidInput(fmt.Sprintf("Invalid task status: %s", status))
	}

	return s.runBulkStatusUpdate(ctx, actorID, "tasks", unique, func(txRepo repository.BulkStatusRepository, _ repository.AutomationRepository) error {
		tasks, err := txRepo.GetTasksByIDs(unique)
		if err != nil {
			return err
		}

		found := make(map[uint]struct{}, len(tasks))
		notAssigned := make([]uint, 0)
		completed := make([]uint, 0)
		for _, task := range tasks {
			found[task.ID] = struct{}{}
			if actorRole != models.RoleAdmin && task.AssignedToID != actorID {
				notAssigned = append(notAssigned, task.ID)
			}
			if task.Status == models.TaskStatusCompleted && status != models.TaskStatusCompleted {
				completed = append(completed, task.ID)
			}
		}

		if missing := missingIDs(unique, found); len(missing) > 0 {
			return bulkStatusNotFound("tasks", missing)
		}
		if len(notAssigned) > 0 {
			return bulkStatusForbidden("You can only update tasks assigned to you", notAssigned)
		}
		if len(completed) > 0 {
			return apperrors.Wrap(apperrors.ErrCompletedTaskModify, apperrors.CodeInvalidStatusTransition,
				"Cannot change the status of completed tasks").WithDetail("completed_ids", completed)
		}

		return txRepo.SetTaskStatus(unique, status)
	})
}

type bulkStatusService struct {
	bulkOperationRepo repository.BulkOperationRepository
	statusRepo        repository.BulkStatusRepository
	transactionMgr    repository.TransactionManager
	logger            *logrus.Logger
	// automation records the outbox events of every lead (issue #164) and
	// every ticket (#242) a bulk status update changes, inside the bulk
	// transaction, origin bulk. Nil means no automation.
	automation repository.AutomationRepository
	// afterLeadLock is an instance-local test seam (#182), nil in production:
	// it runs inside the lead batch right after the locking read of the leads.
	afterLeadLock func(ids []uint)
}

// BulkStatusServiceOption configures NewBulkStatusService beyond its required
// dependencies.
type BulkStatusServiceOption func(*bulkStatusService)

// WithBulkAutomationEvents makes the bulk lead status update record
// lead_updated and, when the status changed, lead_status_changed for every
// lead it wrote, and the bulk ticket status update ticket_updated and
// ticket_status_changed for every ticket (#242).
func WithBulkAutomationEvents(repo repository.AutomationRepository) BulkStatusServiceOption {
	return func(s *bulkStatusService) { s.automation = repo }
}

func NewBulkStatusService(
	bulkOperationRepo repository.BulkOperationRepository,
	statusRepo repository.BulkStatusRepository,
	transactionMgr repository.TransactionManager,
	logger *logrus.Logger,
	opts ...BulkStatusServiceOption,
) BulkStatusService {
	s := &bulkStatusService{
		bulkOperationRepo: bulkOperationRepo,
		statusRepo:        statusRepo,
		transactionMgr:    transactionMgr,
		logger:            logger,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// createBulkOperation writes the pending history row of one bulk status update
// before the batch runs.
func (s *bulkStatusService) createBulkOperation(ctx context.Context, userID uint, resourceType string, operationType models.BulkOperationType, totalItems int) (*models.BulkOperation, error) {
	operation := &models.BulkOperation{
		UserID:        userID,
		ResourceType:  resourceType,
		Type:          operationType,
		OperationType: operationType,
		Status:        models.StatusPending,
		TotalItems:    totalItems,
	}

	err := s.bulkOperationRepo.Create(ctx, operation)
	if err != nil {
		return nil, fmt.Errorf("failed to create bulk operation: %w", err)
	}

	return operation, nil
}

func (s *bulkStatusService) updateBulkOperationStatus(ctx context.Context, id uint, status models.BulkOperationStatus) error {
	return s.bulkOperationRepo.UpdateStatus(ctx, id, status)
}
