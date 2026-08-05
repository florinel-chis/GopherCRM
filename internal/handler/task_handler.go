package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
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

// Create godoc
// @Summary Create a new task
// @Description Create a new task (admin, support and sales roles only). Non-admin callers may only assign the task to themselves; admins may assign it to any active user. A task may reference a lead or a customer, but not both. The new task always starts in status "pending" regardless of the request body.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateTaskRequest true "Task creation request"
// @Success 201 {object} utils.APIResponse{data=models.Task} "Task created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, assignee is deactivated, or task links both a lead and a customer"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, support or sales role required, or non-admin assigning to another user"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Assignee, lead or customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tasks [post]
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
		if errors.Is(err, apperrors.ErrAssigneeNotFound) ||
			errors.Is(err, apperrors.ErrLeadNotFound) ||
			errors.Is(err, apperrors.ErrCustomerNotFound) {
			utils.RespondNotFound(c, err.Error())
		} else if errors.Is(err, apperrors.ErrInactiveUser) ||
			errors.Is(err, apperrors.ErrTaskLeadCustomerConflict) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, task)
	utils.RespondSuccess(c, http.StatusCreated, task)
}

// Get godoc
// @Summary Get a task by ID
// @Description Retrieve a single task. Admins can view any task; every other role can only view tasks assigned to them.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Success 200 {object} utils.APIResponse{data=models.Task} "Task retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid task ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Task is not assigned to you"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Task not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /tasks/{id} [get]
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

// List godoc
// @Summary List tasks
// @Description List tasks with page-based pagination. Admins see all tasks and may use search and sorting; every other role is silently narrowed to the tasks assigned to them, and the search, sort_by and sort_order parameters are ignored for them.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); non-numeric or non-positive values fall back to 1" default(1)
// @Param per_page query int false "Page size; values outside 1-100 fall back to 20" default(20)
// @Param sort_by query string false "Sort column; ignored unless one of the allowed values, and ignored entirely for non-admin callers" Enums(created_at, updated_at, title, status, priority, due_date)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Free-text search across task fields; admin only, and takes precedence over sort_by"
// @Success 200 {object} utils.APIResponse{data=object{tasks=[]models.Task,total=int},meta=utils.APIMeta} "Tasks retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tasks [get]
func (h *TaskHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.List")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	page, perPage := utils.ParsePaginationParams(c)
	offset := utils.CalculateOffset(page, perPage)

	// Parse and validate sort parameters
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	allowedSortColumns := map[string]bool{
		"created_at": true,
		"updated_at": true,
		"title":      true,
		"status":     true,
		"priority":   true,
		"due_date":   true,
	}

	if !allowedSortColumns[sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	search := c.Query("search")

	var tasks []models.Task
	var total int64
	var err error

	// Admin can list all tasks, non-admin users can only list their own tasks
	if currentUserRole != string(models.RoleAdmin) {
		tasks, total, err = h.taskService.GetByAssignee(currentUserID, offset, perPage)
	} else if search != "" {
		tasks, total, err = h.taskService.Search(search, offset, perPage, sortBy, sortOrder)
	} else if sortBy != "" {
		tasks, total, err = h.taskService.ListSorted(offset, perPage, sortBy, sortOrder)
	} else {
		tasks, total, err = h.taskService.List(offset, perPage)
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

// ListMyTasks godoc
// @Summary List tasks assigned to the current user
// @Description List the tasks assigned to the authenticated user, with page-based pagination. Available to every authenticated role, including admins, who also see only their own tasks here. Sorting and search are not supported on this endpoint.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); non-numeric or non-positive values fall back to 1" default(1)
// @Param per_page query int false "Page size; values outside 1-100 fall back to 20" default(20)
// @Success 200 {object} utils.APIResponse{data=object{tasks=[]models.Task,total=int},meta=utils.APIMeta} "Tasks retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tasks/my [get]
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

// Update godoc
// @Summary Update a task
// @Description Partially update a task; only the fields present in the request body are applied. Admins can update any task; every other role can only update tasks assigned to them. Only admins may change assigned_to_id (sending the current assignee back unchanged is not treated as a reassignment). A task that is already completed cannot be moved to another status, and a task cannot reference both a lead and a customer.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Param request body UpdateTaskRequest true "Task update request"
// @Success 200 {object} utils.APIResponse{data=models.Task} "Task updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid task ID, invalid request data, assignee is deactivated, task links both a lead and a customer, or the task is already completed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Task is not assigned to you, or only admins can reassign tasks"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Task, assignee, lead or customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tasks/{id} [put]
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
		utils.RespondNotFound(c, "Task not found")
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
	// Only an actual change of assignee counts as a reassignment; clients routinely
	// echo back the current assigned_to_id when editing other fields.
	if req.AssignedToID != 0 && req.AssignedToID != task.AssignedToID {
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
		if errors.Is(err, apperrors.ErrAssigneeNotFound) ||
			errors.Is(err, apperrors.ErrLeadNotFound) ||
			errors.Is(err, apperrors.ErrCustomerNotFound) {
			utils.RespondNotFound(c, err.Error())
		} else if errors.Is(err, apperrors.ErrInactiveUser) ||
			errors.Is(err, apperrors.ErrTaskLeadCustomerConflict) ||
			errors.Is(err, apperrors.ErrCompletedTaskModify) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, task)
	utils.RespondSuccess(c, http.StatusOK, task)
}

// Delete godoc
// @Summary Delete a task
// @Description Delete a task (admin role only). The role check runs before the ID is parsed, so non-admins always receive 403. Any failure while deleting is reported as 404.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid task ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Task not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /tasks/{id} [delete]
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
		utils.RespondNotFound(c, "Task not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}