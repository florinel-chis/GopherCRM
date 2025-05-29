package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TaskHandler struct {
	taskService service.TaskService
}

func NewTaskHandler(taskService service.TaskService) *TaskHandler {
	return &TaskHandler{taskService: taskService}
}

type CreateTaskRequest struct {
	Title        string                `json:"title" binding:"required"`
	Description  string                `json:"description,omitempty"`
	Priority     models.TaskPriority   `json:"priority,omitempty" binding:"omitempty,oneof=low medium high"`
	DueDate      *time.Time            `json:"due_date,omitempty"`
	AssignedToID uint                  `json:"assigned_to_id" binding:"required"`
	LeadID       *uint                 `json:"lead_id,omitempty"`
	CustomerID   *uint                 `json:"customer_id,omitempty"`
}

type UpdateTaskRequest struct {
	Title        string                `json:"title,omitempty"`
	Description  string                `json:"description,omitempty"`
	Status       models.TaskStatus     `json:"status,omitempty" binding:"omitempty,oneof=pending in_progress completed cancelled"`
	Priority     models.TaskPriority   `json:"priority,omitempty" binding:"omitempty,oneof=low medium high"`
	DueDate      *time.Time            `json:"due_date,omitempty"`
	AssignedToID uint                  `json:"assigned_to_id,omitempty"`
	LeadID       *uint                 `json:"lead_id,omitempty"`
	CustomerID   *uint                 `json:"customer_id,omitempty"`
}

func (h *TaskHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.Create")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	// Role-based access control
	if currentUserRole != string(models.RoleAdmin) && currentUserRole != string(models.RoleSupport) && currentUserRole != string(models.RoleSales) {
		utils.RespondForbidden(c, "Only admin, support, and sales users can create tasks")
		return
	}

	var req CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Non-admin users can only assign tasks to themselves or other users in same hierarchy
	if currentUserRole != string(models.RoleAdmin) {
		if req.AssignedToID != currentUserID {
			utils.RespondForbidden(c, "You can only assign tasks to yourself")
			return
		}
	}

	task := &models.Task{
		Title:        req.Title,
		Description:  req.Description,
		Priority:     req.Priority,
		DueDate:      req.DueDate,
		AssignedToID: req.AssignedToID,
		LeadID:       req.LeadID,
		CustomerID:   req.CustomerID,
		Status:       models.TaskStatusPending,
	}

	if err := h.taskService.Create(task); err != nil {
		logger.WithError(err).Error("Failed to create task")
		if err.Error() == "assignee not found" {
			utils.RespondNotFound(c, err.Error())
		} else if err.Error() == "lead not found" {
			utils.RespondNotFound(c, err.Error())
		} else if err.Error() == "customer not found" {
			utils.RespondNotFound(c, err.Error())
		} else if err.Error() == "cannot assign task to inactive user" ||
			err.Error() == "task cannot be linked to both lead and customer" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, task)
	utils.RespondSuccess(c, http.StatusCreated, task)
}

func (h *TaskHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.Get")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid task ID")
		return
	}

	task, err := h.taskService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Task not found")
		utils.RespondNotFound(c, "Task not found")
		return
	}

	// Check permissions
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	// Admin can view all tasks
	// Non-admin users can only view tasks assigned to them
	if currentUserRole != string(models.RoleAdmin) && task.AssignedToID != currentUserID {
		utils.RespondForbidden(c, "You can only view tasks assigned to you")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, task)
	utils.RespondSuccess(c, http.StatusOK, task)
}

func (h *TaskHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.List")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	page, perPage := utils.ParsePaginationParams(c)
	offset := utils.CalculateOffset(page, perPage)

	var tasks []models.Task
	var total int64
	var err error

	// Admin can list all tasks, non-admin users can only list their own tasks
	if currentUserRole == string(models.RoleAdmin) {
		tasks, total, err = h.taskService.List(offset, perPage)
	} else {
		tasks, total, err = h.taskService.GetByAssignee(currentUserID, offset, perPage)
	}

	if err != nil {
		logger.WithError(err).Error("Failed to list tasks")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: int64(utils.CalculateTotalPages(total, perPage)),
	}

	responseData := gin.H{"tasks": tasks, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *TaskHandler) ListMyTasks(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.ListMyTasks")

	currentUserID := c.GetUint("user_id")
	page, perPage := utils.ParsePaginationParams(c)
	offset := utils.CalculateOffset(page, perPage)

	tasks, total, err := h.taskService.GetByAssignee(currentUserID, offset, perPage)
	if err != nil {
		logger.WithError(err).Error("Failed to list my tasks")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: int64(utils.CalculateTotalPages(total, perPage)),
	}

	responseData := gin.H{"tasks": tasks, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *TaskHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.Update")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid task ID")
		return
	}

	var req UpdateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Get existing task
	task, err := h.taskService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Task not found")
		if err == gorm.ErrRecordNotFound {
			utils.RespondNotFound(c, "Task not found")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	// Check permissions
	// Admin can update any task
	// Non-admin users can only update tasks assigned to them
	if currentUserRole != string(models.RoleAdmin) && task.AssignedToID != currentUserID {
		utils.RespondForbidden(c, "You can only update tasks assigned to you")
		return
	}

	// Apply updates
	if req.Title != "" {
		task.Title = req.Title
	}
	if req.Description != "" {
		task.Description = req.Description
	}
	if req.Status != "" {
		task.Status = req.Status
	}
	if req.Priority != "" {
		task.Priority = req.Priority
	}
	if req.DueDate != nil {
		task.DueDate = req.DueDate
	}
	if req.AssignedToID != 0 {
		// Only admin can reassign tasks
		if currentUserRole != string(models.RoleAdmin) {
			utils.RespondForbidden(c, "Only admins can reassign tasks")
			return
		}
		task.AssignedToID = req.AssignedToID
	}
	if req.LeadID != nil {
		task.LeadID = req.LeadID
	}
	if req.CustomerID != nil {
		task.CustomerID = req.CustomerID
	}

	if err := h.taskService.Update(task); err != nil {
		logger.WithError(err).Error("Failed to update task")
		if err.Error() == "assignee not found" ||
			err.Error() == "lead not found" ||
			err.Error() == "customer not found" {
			utils.RespondNotFound(c, err.Error())
		} else if err.Error() == "cannot assign task to inactive user" ||
			err.Error() == "task cannot be linked to both lead and customer" ||
			err.Error() == "cannot change status of completed task" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, task)
	utils.RespondSuccess(c, http.StatusOK, task)
}

func (h *TaskHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.Delete")

	currentUserRole := c.GetString("user_role")

	// Only admin users can delete tasks
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can delete tasks")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid task ID")
		return
	}

	if err := h.taskService.Delete(uint(id)); err != nil {
		logger.WithError(err).Error("Failed to delete task")
		if err == gorm.ErrRecordNotFound {
			utils.RespondNotFound(c, "Task not found")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}