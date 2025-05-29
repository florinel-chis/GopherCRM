package service

import (
	"errors"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.WithError(err).Warn("Assignee not found")
			return errors.New("assignee not found")
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	if !assignee.IsActive {
		logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
		return errors.New("cannot assign task to inactive user")
	}

	// Validate lead if provided
	if task.LeadID != nil {
		_, err := s.leadRepo.GetByID(*task.LeadID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.WithError(err).Warn("Lead not found")
				return errors.New("lead not found")
			}
			utils.LogServiceResponse(logger, err)
			return err
		}
	}

	// Validate customer if provided
	if task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.WithError(err).Warn("Customer not found")
				return errors.New("customer not found")
			}
			utils.LogServiceResponse(logger, err)
			return err
		}
	}

	// Task cannot be linked to both lead and customer
	if task.LeadID != nil && task.CustomerID != nil {
		logger.Warn("Task cannot be linked to both lead and customer")
		return errors.New("task cannot be linked to both lead and customer")
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.WithError(err).Warn("Task not found")
		} else {
			logger.WithError(err).Error("Failed to get task")
		}
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.WithError(err).Warn("Task not found")
		} else {
			logger.WithError(err).Error("Failed to get existing task")
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	// Validate assignee if being changed
	if task.AssignedToID != existingTask.AssignedToID {
		assignee, err := s.userRepo.GetByID(task.AssignedToID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.WithError(err).Warn("Assignee not found")
				return errors.New("assignee not found")
			}
			utils.LogServiceResponse(logger, err)
			return err
		}

		if !assignee.IsActive {
			logger.WithField("assignee_id", task.AssignedToID).Warn("Cannot assign task to inactive user")
			return errors.New("cannot assign task to inactive user")
		}
	}

	// Validate lead if being changed
	if task.LeadID != existingTask.LeadID && task.LeadID != nil {
		_, err := s.leadRepo.GetByID(*task.LeadID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.WithError(err).Warn("Lead not found")
				return errors.New("lead not found")
			}
			utils.LogServiceResponse(logger, err)
			return err
		}
	}

	// Validate customer if being changed
	if task.CustomerID != existingTask.CustomerID && task.CustomerID != nil {
		_, err := s.customerRepo.GetByID(*task.CustomerID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.WithError(err).Warn("Customer not found")
				return errors.New("customer not found")
			}
			utils.LogServiceResponse(logger, err)
			return err
		}
	}

	// Task cannot be linked to both lead and customer
	if task.LeadID != nil && task.CustomerID != nil {
		logger.Warn("Task cannot be linked to both lead and customer")
		return errors.New("task cannot be linked to both lead and customer")
	}

	// Cannot change status from completed to anything else
	if existingTask.Status == models.TaskStatusCompleted && task.Status != models.TaskStatusCompleted {
		logger.Warn("Cannot change status of completed task")
		return errors.New("cannot change status of completed task")
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

	// Check if task exists
	_, err := s.taskRepo.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.WithError(err).Warn("Task not found")
		} else {
			logger.WithError(err).Error("Failed to get task")
		}
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