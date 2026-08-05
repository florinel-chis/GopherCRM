package service

import (
	"context"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
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

// bulkStatusError codes the failure kinds these operations can report. The
// sentinel is the cause, so callers classify with errors.Is; the code fixes the
// HTTP status and the details carry the offending IDs.
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
func (s *bulkOperationService) runBulkStatusUpdate(actorID uint, resourceType string, ids []uint, apply func(txRepo repository.BulkRepository) error) (*models.BulkStatusUpdateResult, error) {
	operation, err := s.CreateBulkOperation(actorID, resourceType, models.BulkAction, len(ids))
	if err != nil {
		return nil, err
	}

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok || tx == nil {
			return fmt.Errorf("transaction not found in context")
		}
		return apply(s.bulkRepo.WithTx(tx))
	})

	if err != nil {
		s.recordBulkStatusOutcome(operation.ID, models.StatusFailed)
		return nil, err
	}

	s.recordBulkStatusOutcome(operation.ID, models.StatusCompleted)
	return &models.BulkStatusUpdateResult{Updated: len(ids)}, nil
}

// recordBulkStatusOutcome updates the audit record. A failure to write it must
// not change what the caller is told about the records themselves, which are
// already committed (or already rolled back), so it is logged and swallowed.
func (s *bulkOperationService) recordBulkStatusOutcome(operationID uint, status models.BulkOperationStatus) {
	if err := s.UpdateBulkOperationStatus(operationID, status); err != nil {
		s.logger.WithError(err).WithField("operation_id", operationID).
			Warn("Failed to record bulk status operation outcome")
	}
}

// BulkSetLeadStatus sets one status on every listed lead.
//
// Admins may update any lead; sales users only leads they own, and a batch
// containing even one lead they do not own is refused in full, naming those
// leads. Support and customer users have no access to leads at all.
func (s *bulkOperationService) BulkSetLeadStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.LeadStatus) (*models.BulkStatusUpdateResult, error) {
	unique, err := normalizeBulkStatusIDs(ids)
	if err != nil {
		return nil, err
	}
	if !isValidLeadStatus(status) {
		return nil, bulkStatusInvalidInput(fmt.Sprintf("Invalid lead status: %s", status))
	}
	if actorRole != models.RoleAdmin && actorRole != models.RoleSales {
		return nil, bulkStatusForbidden("Only sales and admin users can update leads", nil)
	}

	return s.runBulkStatusUpdate(actorID, "leads", unique, func(txRepo repository.BulkRepository) error {
		leads, err := txRepo.GetLeadsByIDs(unique)
		if err != nil {
			return err
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

		return txRepo.SetLeadStatus(unique, status)
	})
}

// BulkSetTicketStatus sets one status on every listed ticket.
//
// Admins may update any ticket, support users only tickets assigned to them.
// Sales users are read-only on tickets and customers cannot update them at all,
// so both are refused outright. A closed ticket cannot be reopened here either:
// the single-record update refuses that transition, and the bulk endpoint must
// not be a way around it.
func (s *bulkOperationService) BulkSetTicketStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.TicketStatus) (*models.BulkStatusUpdateResult, error) {
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

	return s.runBulkStatusUpdate(actorID, "tickets", unique, func(txRepo repository.BulkRepository) error {
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

		return txRepo.SetTicketStatus(unique, status)
	})
}

// BulkSetTaskStatus sets one status on every listed task.
//
// Admins may update any task, everyone else only tasks assigned to them. A
// completed task's status is final for admins too, exactly as it is through the
// single-record update.
func (s *bulkOperationService) BulkSetTaskStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.TaskStatus) (*models.BulkStatusUpdateResult, error) {
	unique, err := normalizeBulkStatusIDs(ids)
	if err != nil {
		return nil, err
	}
	if !isValidTaskStatus(status) {
		return nil, bulkStatusInvalidInput(fmt.Sprintf("Invalid task status: %s", status))
	}

	return s.runBulkStatusUpdate(actorID, "tasks", unique, func(txRepo repository.BulkRepository) error {
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
