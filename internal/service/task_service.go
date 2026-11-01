package service

import (
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// taskListPreloads are the associations the task listings carry. The lead is
// among them because RedactTaskLead decides on it, and the deal because
// RedactTaskDeal does: without them every such link would read as hidden to
// non-admins.
var taskListPreloads = []string{"AssignedTo", "Labels", "Lead", "Deal"}

type taskService struct {
	taskRepo     repository.TaskRepository
	userRepo     repository.UserRepository
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
	labelRepo    repository.LabelRepository
	dealRepo     repository.DealRepository
}

func NewTaskService(taskRepo repository.TaskRepository, userRepo repository.UserRepository, leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository, labelRepo repository.LabelRepository, dealRepo repository.DealRepository) TaskService {
	return &taskService{
		taskRepo:     taskRepo,
		userRepo:     userRepo,
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
		labelRepo:    labelRepo,
		dealRepo:     dealRepo,
	}
}

// Create persists a task without touching its label set. It has no acting
// user, so it cannot link a lead or a deal (see CanViewLead and CanViewDeal).
func (s *taskService) Create(task *models.Task) error {
	return s.CreateWithLabels(task, nil, 0)
}

// CreateWithLabels persists a task and attaches the referenced labels. With no
// label ids it is exactly Create, down to the repository call it makes. A lead
// link must be a lead actorID may see, a deal link a deal actorID may see; a
// deal_id of 0 means no link.
func (s *taskService) CreateWithLabels(task *models.Task, labelIDs []uint, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "task"), "TaskService", "Create")

	// Set default values
	if task.Status == "" {
		task.Status = models.TaskStatusPending
	}
	if task.Priority == "" {
		task.Priority = models.TaskPriorityMedium
	}

	// Validate assignee exists and is active
	assignee, err := s.userRepo.GetByID(task.AssignedToID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Assignee not found")
		return fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
	}

	if !assignee.IsActive {
		logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
		return fmt.Errorf("cannot assign task to inactive user: %w", apperrors.ErrInactiveUser)
	}

	// Validate lead if provided: it must exist and be visible to the actor.
	var lead *models.Lead
	if task.LeadID != nil {
		lead, err = checkLeadLink(s.leadRepo, s.userRepo, actorID, *task.LeadID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Lead link rejected")
			return err
		}
	}

	// Validate customer if provided
	if task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			return fmt.Errorf("customer not found: %w", apperrors.ErrCustomerNotFound)
		}
	}

	// Task cannot be linked to both lead and customer
	if task.LeadID != nil && task.CustomerID != nil {
		logger.Warn("Task cannot be linked to both lead and customer")
		return fmt.Errorf("task cannot be linked to both lead and customer: %w", apperrors.ErrTaskLeadCustomerConflict)
	}

	// Validate deal if provided: it must exist and be visible to the actor. It
	// coexists with a lead or a customer link and is not cross-checked against
	// them.
	if task.DealID != nil && *task.DealID == 0 {
		task.DealID = nil
	}
	var deal *models.Deal
	if task.DealID != nil {
		deal, err = s.acceptDealLink(logger, actorID, *task.DealID)
		if err != nil {
			return err
		}
	}

	labels, err := s.resolveLabels(labelIDs)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Unknown label referenced")
		return err
	}

	// The checked lead and deal are attached only after the write, for the
	// response, so GORM never saves them as associations of the new task.
	task.Lead = nil
	task.Deal = nil
	if len(labels) == 0 {
		err = s.taskRepo.Create(task)
	} else {
		err = s.taskRepo.CreateWithLabels(task, labels)
	}
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	task.Lead = lead
	task.Deal = deal

	logger.WithFields(map[string]interface{}{
		"task_id": task.ID,
	}).Info("Task created successfully")

	return nil
}

func (s *taskService) GetByID(id uint) (*models.Task, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_id", id), "TaskService", "GetByID")

	task, err := s.taskRepo.GetByID(id)
	if err != nil {
		// A missing row and a failing database are different outcomes; only the
		// former is wrapped in the not-found sentinel, so the handler can answer
		// 404 for one and 500 for the other instead of hiding both as 404.
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Task not found")
			return nil, fmt.Errorf("task %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to retrieve task")
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("task_id", id).Debug("Task retrieved successfully")
	return task, nil
}

func (s *taskService) GetByAssignee(assigneeID uint, offset, limit int) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"assignee_id": assigneeID,
		"offset":      offset,
		"limit":       limit,
	}), "TaskService", "GetByAssignee")

	tasks, err := s.taskRepo.GetByAssignedToID(assigneeID, offset, limit)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	total, err := s.taskRepo.CountByAssignedToID(assigneeID)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	logger.WithFields(map[string]interface{}{
		"assignee_id": assigneeID,
		"count":       len(tasks),
		"offset":      offset,
		"limit":       limit,
	}).Info("Tasks retrieved by assignee")

	return tasks, total, nil
}

// Update persists the task and leaves its label set untouched. It has no
// acting user, so it cannot link the task to another lead or deal.
func (s *taskService) Update(task *models.Task) error {
	return s.UpdateWithLabels(task, nil, false, false, 0)
}

// UpdateWithLabels persists the task and, when labelIDs is non-nil, REPLACES
// its label set with the referenced labels. A non-nil empty slice clears the
// set; nil leaves it exactly as it was. A lead link that differs from the
// stored one, or any lead link the request sent (leadIDSent), must be a lead
// actorID may see; a stored link the request did not mention is kept as it is.
// The deal link follows settleDealLink: dealIDSent tells a request that named
// deal_id (nil or 0 on the task then means a clear) from one that did not.
func (s *taskService) UpdateWithLabels(task *models.Task, labelIDs *[]uint, leadIDSent, dealIDSent bool, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_id", task.ID), "TaskService", "Update")

	// Get existing task to validate the update
	existingTask, err := s.taskRepo.GetByID(task.ID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Task not found")
		utils.LogServiceResponse(logger, err)
		return err
	}

	// Validate assignee if being changed
	var newAssignee *models.User
	if task.AssignedToID != existingTask.AssignedToID {
		assignee, err := s.userRepo.GetByID(task.AssignedToID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Assignee not found")
			return fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
		}

		if !assignee.IsActive {
			logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
			return fmt.Errorf("cannot assign task to inactive user: %w", apperrors.ErrInactiveUser)
		}
		newAssignee = assignee
	}

	// Validate lead if being changed or sent: it must exist and be visible to
	// the actor. The ids are compared, not the pointers, so an edit that leaves
	// lead_id out stays possible after the lead changed hands. A lead_id that
	// was sent is checked even when it equals the stored one: otherwise 200 for
	// the stored id and 404 for every other hidden one would reveal the link
	// the read side hides.
	leadChanged := linkChanged(task.LeadID, existingTask.LeadID)
	var lead *models.Lead
	if task.LeadID != nil && (leadChanged || leadIDSent) {
		lead, err = checkLeadLink(s.leadRepo, s.userRepo, actorID, *task.LeadID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Lead link rejected")
			return err
		}
	}

	// Validate customer if being changed
	customerChanged := linkChanged(task.CustomerID, existingTask.CustomerID)
	if customerChanged && task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			return fmt.Errorf("customer not found: %w", apperrors.ErrCustomerNotFound)
		}
	}

	// Task cannot be linked to both lead and customer
	if task.LeadID != nil && task.CustomerID != nil {
		logger.Warn("Task cannot be linked to both lead and customer")
		return fmt.Errorf("task cannot be linked to both lead and customer: %w", apperrors.ErrTaskLeadCustomerConflict)
	}

	// Cannot change status from completed to anything else
	if existingTask.Status == models.TaskStatusCompleted && task.Status != models.TaskStatusCompleted {
		logger.Warn("Cannot change status of completed task")
		return fmt.Errorf("cannot change status of completed task: %w", apperrors.ErrCompletedTaskModify)
	}

	deal, err := s.settleDealLink(logger, task, existingTask, dealIDSent, actorID)
	if err != nil {
		return err
	}

	var labels []models.Label
	if labelIDs != nil {
		labels, err = s.resolveLabels(*labelIDs)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Unknown label referenced")
			return err
		}
	}

	// Associations preloaded for the old links must not ride along into the
	// save: GORM writes a belongs-to association's id back over the foreign
	// key, which would silently undo a reassignment or a new lead_id.
	if leadChanged || task.LeadID == nil {
		task.Lead = nil
	}
	if customerChanged || task.CustomerID == nil {
		task.Customer = nil
	}
	if newAssignee != nil {
		task.AssignedTo = models.User{}
	}
	// Always detached, unlike the two above: the deal settled on is known here
	// and goes back on the task for the response below.
	task.Deal = nil

	if labelIDs == nil {
		err = s.taskRepo.Update(task)
	} else {
		err = s.taskRepo.UpdateWithLabels(task, labels)
	}
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	if leadChanged {
		task.Lead = lead
	}
	if newAssignee != nil {
		task.AssignedTo = *newAssignee
	}
	task.Deal = deal

	logger.WithField("task_id", task.ID).Info("Task updated successfully")
	return nil
}

// settleDealLink decides the deal_id an update commits, writes it onto the task
// and returns the deal to attach to the response:
//
//   - deal_id absent: the stored link stays, unchecked, whoever may see it, so
//     the assignee can keep editing a task whose deal has changed hands.
//   - deal_id sent as a deal (even the stored one): checkDealLink decides, so
//     the answer cannot reveal which hidden deal the task points at. A deal the
//     actor may see replaces even a link hidden from it.
//   - deal_id sent as a clear (nil or 0): the link goes when the actor may see
//     the stored deal, and an admin also when that deal was deleted. A link
//     hidden from the actor stays and no error is returned, so a clear can
//     neither reveal nor sever it; the read redaction then answers as if the
//     task had no link.
//
// A changed non-zero id without dealIDSent is checked like a sent one, so no
// path writes an unchecked link.
func (s *taskService) settleDealLink(logger *logrus.Entry, task, existing *models.Task, dealIDSent bool, actorID uint) (*models.Deal, error) {
	if task.DealID != nil && *task.DealID == 0 {
		task.DealID = nil
	}

	if task.DealID != nil {
		if !dealIDSent && !linkChanged(task.DealID, existing.DealID) {
			return existing.Deal, nil
		}
		return s.acceptDealLink(logger, actorID, *task.DealID)
	}

	if existing.DealID == nil {
		return nil, nil
	}
	if dealIDSent {
		viewer, err := leadViewerOf(s.userRepo, actorID)
		if err != nil {
			logger.WithError(utils.LogError(err)).Error("Failed to load the actor of a deal link clear")
			return nil, err
		}
		if CanViewDeal(viewer, linkedDeal(existing.DealID, existing.Deal)) {
			logger.WithField("deal_id", *existing.DealID).Info("Deal link cleared")
			return nil, nil
		}
		logger.WithField("deal_id", *existing.DealID).Info("Deal link kept: hidden from the actor")
	}
	task.DealID = existing.DealID
	return existing.Deal, nil
}

// acceptDealLink runs checkDealLink and logs the outcome. Log lines carry ids
// only: the error text is "unknown deal_id N", never the deal's title or notes.
func (s *taskService) acceptDealLink(logger *logrus.Entry, actorID, dealID uint) (*models.Deal, error) {
	logger = logger.WithField("deal_id", dealID)
	deal, err := checkDealLink(s.dealRepo, s.userRepo, actorID, dealID)
	if err != nil {
		if errors.Is(err, apperrors.ErrDealNotFound) {
			logger.WithError(utils.LogError(err)).Warn("Deal link rejected")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to check deal link")
		}
		return nil, err
	}
	logger.Info("Deal link accepted")
	return deal, nil
}

// resolveLabels turns a list of label ids into the labels themselves,
// rejecting the whole request if any id matches nothing. Duplicated ids are
// collapsed, so sending the same label twice attaches it once instead of
// tripping the "unknown label" check on the count comparison.
func (s *taskService) resolveLabels(labelIDs []uint) ([]models.Label, error) {
	if len(labelIDs) == 0 {
		return nil, nil
	}

	unique := make([]uint, 0, len(labelIDs))
	seen := make(map[uint]struct{}, len(labelIDs))
	for _, id := range labelIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	labels, err := s.labelRepo.FindByIDs(unique)
	if err != nil {
		return nil, err
	}
	if len(labels) == len(unique) {
		return labels, nil
	}

	found := make(map[uint]struct{}, len(labels))
	for _, label := range labels {
		found[label.ID] = struct{}{}
	}
	missing := make([]uint, 0, len(unique)-len(labels))
	for _, id := range unique {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}

	return nil, fmt.Errorf("unknown label ids %v: %w", missing, apperrors.ErrLabelNotFound)
}

// ListByLabel returns the tasks carrying one label, across every assignee.
func (s *taskService) ListByLabel(labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	return s.listByLabel(labelID, nil, offset, limit, sortBy, sortOrder)
}

// ListByLabelForAssignee returns the tasks carrying one label for a single
// assignee. Every role but admin is narrowed through here.
func (s *taskService) ListByLabelForAssignee(assigneeID, labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	return s.listByLabel(labelID, &assigneeID, offset, limit, sortBy, sortOrder)
}

func (s *taskService) listByLabel(labelID uint, assigneeID *uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"label_id": labelID,
		"offset":   offset,
		"limit":    limit,
	}), "TaskService", "ListByLabel")

	tasks, err := s.taskRepo.ListByLabel(labelID, assigneeID, offset, limit, sortBy, sortOrder, taskListPreloads...)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tasks by label")
		return nil, 0, err
	}

	total, err := s.taskRepo.CountByLabel(labelID, assigneeID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tasks by label")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tasks listed by label")
	return tasks, total, nil
}

// ListByDeal returns one page of a deal's live tasks for GET /deals/{id}/tasks.
// A deal that does not exist or was soft-deleted is ErrDealNotFound; one the
// viewer may not see is ErrForbidden. An admin gets every task of the deal.
// Any other viewer, which CanViewDeal has narrowed to the sales owner, gets
// only the tasks assigned to it, inside the query and before the limit, so the
// total counts only those: owning a deal does not widen who may read a task.
func (s *taskService) ListByDeal(viewer LeadViewer, dealID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"deal_id": dealID,
		"offset":  offset,
		"limit":   limit,
	}), "TaskService", "ListByDeal")

	deal, err := s.dealRepo.GetByID(dealID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Deal not found")
			return nil, 0, fmt.Errorf("deal %d not found: %w", dealID, apperrors.ErrDealNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to load deal")
		return nil, 0, err
	}
	if !CanViewDeal(viewer, deal) {
		logger.Warn("Deal hidden from the caller")
		return nil, 0, fmt.Errorf("deal %d is not visible to the caller: %w", dealID, apperrors.ErrForbidden)
	}

	var assigneeID *uint
	if viewer.Role != models.RoleAdmin {
		id := viewer.UserID
		assigneeID = &id
	}

	tasks, err := s.taskRepo.ListByDeal(dealID, assigneeID, offset, limit, sortBy, sortOrder, taskListPreloads...)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tasks by deal")
		return nil, 0, err
	}

	total, err := s.taskRepo.CountByDeal(dealID, assigneeID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tasks by deal")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tasks listed by deal")
	return tasks, total, nil
}

func (s *taskService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_id", id), "TaskService", "Delete")

	// Check if task exists. Only an absent row is reported as not-found; a failed
	// lookup is passed through unclassified so it surfaces as a server error.
	_, err := s.taskRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Task not found")
			return fmt.Errorf("task %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up task for deletion")
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.taskRepo.Delete(id); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("task_id", id).Info("Task deleted successfully")
	return nil
}

func (s *taskService) List(offset, limit int) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TaskService", "List")

	tasks, err := s.taskRepo.List(offset, limit)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	total, err := s.taskRepo.Count()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
		"total":  total,
	}).Info("Tasks listed successfully")

	return tasks, total, nil
}

func (s *taskService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TaskService", "ListSorted")

	tasks, err := s.taskRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, taskListPreloads...)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tasks sorted")
		return nil, 0, err
	}

	total, err := s.taskRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count tasks")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tasks listed sorted successfully")
	return tasks, total, nil
}

func (s *taskService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TaskService", "Search")

	tasks, err := s.taskRepo.Search(query, offset, limit, sortBy, sortOrder, taskListPreloads...)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to search tasks")
		return nil, 0, err
	}

	total, err := s.taskRepo.CountSearch(query)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Task search completed")
	return tasks, total, nil
}

func (s *taskService) GetPendingCount() (int64, error) {
	return s.taskRepo.CountPending()
}

// GetStatusCounts returns the live task count per status, for the dashboard's
// status distribution chart. Statuses with no rows are missing from the map —
// the handler owns the label set.
func (s *taskService) GetStatusCounts() (map[string]int64, error) {
	return s.taskRepo.CountByStatus()
}

// GetUpcoming returns the open tasks due soonest, across every assignee.
func (s *taskService) GetUpcoming(limit int) ([]models.Task, error) {
	return s.taskRepo.ListUpcoming(nil, limit)
}

// GetUpcomingByAssignee returns the open tasks due soonest for one user. Every
// role but admin is narrowed through here.
func (s *taskService) GetUpcomingByAssignee(assigneeID uint, limit int) ([]models.Task, error) {
	return s.taskRepo.ListUpcoming(&assigneeID, limit)
}

// GetDueWithin returns the open tasks due in [from, to], across every assignee.
func (s *taskService) GetDueWithin(from, to time.Time, limit int) ([]models.Task, error) {
	return s.taskRepo.ListDueBetween(nil, from, to, limit)
}

// GetDueWithinByAssignee returns the open tasks due in [from, to] for one user.
func (s *taskService) GetDueWithinByAssignee(assigneeID uint, from, to time.Time, limit int) ([]models.Task, error) {
	return s.taskRepo.ListDueBetween(&assigneeID, from, to, limit)
}

// GetRecentlyCompleted returns the most recently completed tasks across every
// assignee, for the admin activity feed.
func (s *taskService) GetRecentlyCompleted(limit int) ([]models.Task, error) {
	return s.taskRepo.ListRecentlyCompleted(nil, limit)
}

// GetRecentlyCompletedByAssignee returns the most recently completed tasks of
// one user. Every role but admin is narrowed through here.
func (s *taskService) GetRecentlyCompletedByAssignee(assigneeID uint, limit int) ([]models.Task, error) {
	return s.taskRepo.ListRecentlyCompleted(&assigneeID, limit)
}
