package service

import (
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type taskService struct {
	taskRepo     repository.TaskRepository
	userRepo     repository.UserRepository
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
}

func NewTaskService(taskRepo repository.TaskRepository, userRepo repository.UserRepository, leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository) TaskService {
	return &taskService{
		taskRepo:     taskRepo,
		userRepo:     userRepo,
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
	}
}

func (s *taskService) Create(task *models.Task) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_title", task.Title), "TaskService", "Create")

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
		logger.WithError(err).Warn("Assignee not found")
		return fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
	}

	if !assignee.IsActive {
		logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
		return fmt.Errorf("cannot assign task to inactive user: %w", apperrors.ErrInactiveUser)
	}

	// Validate lead if provided
	if task.LeadID != nil {
		_, err := s.leadRepo.GetByID(*task.LeadID)
		if err != nil {
			logger.WithError(err).Warn("Lead not found")
			return fmt.Errorf("lead not found: %w", apperrors.ErrLeadNotFound)
		}
	}

	// Validate customer if provided
	if task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			logger.WithError(err).Warn("Customer not found")
			return fmt.Errorf("customer not found: %w", apperrors.ErrCustomerNotFound)
		}
	}

	// Task cannot be linked to both lead and customer
	if task.LeadID != nil && task.CustomerID != nil {
		logger.Warn("Task cannot be linked to both lead and customer")
		return fmt.Errorf("task cannot be linked to both lead and customer: %w", apperrors.ErrTaskLeadCustomerConflict)
	}

	if err := s.taskRepo.Create(task); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithFields(map[string]interface{}{
		"task_id":    task.ID,
		"task_title": task.Title,
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
			logger.WithError(err).Warn("Task not found")
			return nil, fmt.Errorf("task %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(err).Error("Failed to retrieve task")
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

func (s *taskService) Update(task *models.Task) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_id", task.ID), "TaskService", "Update")

	// Get existing task to validate the update
	existingTask, err := s.taskRepo.GetByID(task.ID)
	if err != nil {
		logger.WithError(err).Warn("Task not found")
		utils.LogServiceResponse(logger, err)
		return err
	}

	// Validate assignee if being changed
	if task.AssignedToID != existingTask.AssignedToID {
		assignee, err := s.userRepo.GetByID(task.AssignedToID)
		if err != nil {
			logger.WithError(err).Warn("Assignee not found")
			return fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
		}

		if !assignee.IsActive {
			logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
			return fmt.Errorf("cannot assign task to inactive user: %w", apperrors.ErrInactiveUser)
		}
	}

	// Validate lead if being changed
	if task.LeadID != existingTask.LeadID && task.LeadID != nil {
		_, err := s.leadRepo.GetByID(*task.LeadID)
		if err != nil {
			logger.WithError(err).Warn("Lead not found")
			return fmt.Errorf("lead not found: %w", apperrors.ErrLeadNotFound)
		}
	}

	// Validate customer if being changed
	if task.CustomerID != existingTask.CustomerID && task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			logger.WithError(err).Warn("Customer not found")
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

	if err := s.taskRepo.Update(task); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("task_id", task.ID).Info("Task updated successfully")
	return nil
}

func (s *taskService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("task_id", id), "TaskService", "Delete")

	// Check if task exists. Only an absent row is reported as not-found; a failed
	// lookup is passed through unclassified so it surfaces as a server error.
	_, err := s.taskRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Task not found")
			return fmt.Errorf("task %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(err).Error("Failed to look up task for deletion")
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
		"offset":     offset,
		"limit":      limit,
		"sort_by":    sortBy,
		"sort_order": sortOrder,
	}), "TaskService", "ListSorted")

	tasks, err := s.taskRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, "AssignedTo")
	if err != nil {
		logger.WithError(err).Error("Failed to list tasks sorted")
		return nil, 0, err
	}

	total, err := s.taskRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count tasks")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Tasks listed sorted successfully")
	return tasks, total, nil
}

func (s *taskService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"query":  query,
		"offset": offset,
		"limit":  limit,
	}), "TaskService", "Search")

	tasks, err := s.taskRepo.Search(query, offset, limit, sortBy, sortOrder, "AssignedTo")
	if err != nil {
		logger.WithError(err).Error("Failed to search tasks")
		return nil, 0, err
	}

	total, err := s.taskRepo.CountSearch(query)
	if err != nil {
		logger.WithError(err).Error("Failed to count search results")
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

// GetRecentlyCompleted returns the most recently completed tasks, for the
// activity feed.
func (s *taskService) GetRecentlyCompleted(limit int) ([]models.Task, error) {
	return s.taskRepo.ListRecentlyCompleted(limit)
}