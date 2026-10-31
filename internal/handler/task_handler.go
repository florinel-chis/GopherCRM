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

// @Description Body of POST /tasks.
type CreateTaskRequest struct {
	// Short summary of the task. Required (non-empty). Not length-checked by the API; the column is 255 characters wide.
	Title string `json:"title" binding:"required" example:"Call back about the renewal"`
	// Free-text details.
	Description string `json:"description,omitempty" example:"Customer asked for a quote on the 50-seat plan."`
	// Priority; medium when omitted or empty.
	Priority models.TaskPriority `json:"priority,omitempty" binding:"omitempty,oneof=low medium high" example:"high"`
	// Due date and time (RFC 3339). Optional.
	DueDate *time.Time `json:"due_date,omitempty" example:"2026-11-05T09:00:00Z"`
	// ID of the user to assign the task to. Required (non-zero). Non-admin callers must send their own user id; admins may name any active user.
	AssignedToID uint `json:"assigned_to_id" binding:"required" example:"7"`
	// ID of a lead to link. The lead must exist and be visible to the caller (admins any lead, sales users only the leads they own, every other role none), otherwise 404. Cannot be combined with customer_id.
	LeadID *uint `json:"lead_id,omitempty" example:"12"`
	// ID of a customer to link. The customer must exist, otherwise 404. Cannot be combined with lead_id.
	CustomerID *uint `json:"customer_id,omitempty" example:"3"`
	// IDs of existing labels to attach, at most 100, each positive (the cap
	// bounds the IN clause a single request can produce). Absent or empty means
	// no labels; a repeated id is attached once; an id that matches no label
	// rejects the whole request with 400 INVALID_REFERENCE.
	LabelIDs []uint `json:"label_ids,omitempty" binding:"omitempty,max=100,dive,gt=0" example:"1,4"`
}

// @Description Body of PUT /tasks/{id}. Every field is optional; absent or empty fields are left unchanged.
type UpdateTaskRequest struct {
	// New title; an absent or empty value leaves the title unchanged.
	Title string `json:"title,omitempty" example:"Call back about the renewal"`
	// New description; an absent or empty value leaves it unchanged (it cannot be cleared).
	Description string `json:"description,omitempty" example:"Quote sent, waiting for the signature."`
	// New status; absent or empty leaves it unchanged. A completed task cannot be moved to any other status (400).
	Status models.TaskStatus `json:"status,omitempty" binding:"omitempty,oneof=pending in_progress completed cancelled" example:"in_progress"`
	// New priority; absent or empty leaves it unchanged.
	Priority models.TaskPriority `json:"priority,omitempty" binding:"omitempty,oneof=low medium high" example:"medium"`
	// New due date (RFC 3339); absent or null leaves it unchanged (it cannot be cleared).
	DueDate *time.Time `json:"due_date,omitempty" example:"2026-11-06T09:00:00Z"`
	// New assignee; absent, 0 or the current assignee leaves it unchanged. Any other value is admin-only (403 otherwise) and must name an existing, active user.
	AssignedToID uint `json:"assigned_to_id,omitempty" example:"7"`
	// Lead to link. When present it must name a lead the caller may see (404 otherwise), even when it equals the stored link. Absent keeps the stored link; null is the same as absent. Cannot be combined with customer_id.
	LeadID *uint `json:"lead_id,omitempty" example:"12"`
	// Customer to link. When present without lead_id it replaces the association: the lead link is cleared and this customer (which must exist) is linked. Absent (or null) keeps the stored link.
	CustomerID *uint `json:"customer_id,omitempty" example:"3"`
	// Replacement label set, at most 100 ids, each positive. The field is a
	// pointer so the cases stay distinguishable: absent (or null) leaves the
	// labels untouched, while a present array, even an empty one, replaces the
	// whole set. An id that matches no label rejects the whole request with 400
	// INVALID_REFERENCE.
	LabelIDs *[]uint `json:"label_ids,omitempty" binding:"omitempty,max=100,dive,gt=0" example:"1,4"`
}

// TaskListData is the data payload of GET /tasks and GET /tasks/my. It is a
// documentation-only type: the handlers build the same shape inline.
//
// @Description Data payload of GET /tasks and GET /tasks/my: one page of tasks and the total before pagination.
type TaskListData struct {
	// The page of tasks. Unsorted pages come in ascending id order; a sorted page is ordered by the sort column, then id in the same direction.
	Tasks []models.Task `json:"tasks"`
	// Number of tasks matching the caller's scope and filters before pagination; the same value as meta.total.
	Total int64 `json:"total" example:"42"`
}

// Create godoc
// @Summary Create a new task
// @Description Create a task. Roles: admin, sales and support (customers get 403 before the body is read). Non-admin callers may only assign the task to themselves; admins may assign it to any active user. The task always starts in status pending, whatever the body says, and priority defaults to medium. A task may link a lead or a customer, but not both. lead_id must name a lead the caller may see (admins any lead, sales users only the leads they own, every other role none); a hidden lead is answered with 404 exactly like a missing one. label_ids attaches existing labels (a repeated id is attached once); any id that matches no label rejects the whole request. The checks run in this order: role, body binding, self-assignment, assignee (exists, active), lead, customer, lead-and-customer conflict, labels. No automation event and no notification is produced. lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateTaskRequest true "Task to create"
// @Success 201 {object} utils.APIResponse{data=models.Task} "Task created; assigned_to is a zero-valued object in this response"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; BAD_REQUEST when the assignee is deactivated or both lead_id and customer_id are given; INVALID_REFERENCE when a label id matches no label"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - the caller is a customer, or a non-admin named another user as assignee"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - the assignee, the lead (missing or hidden from the caller) or the customer does not exist"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

	if err := h.taskService.CreateWithLabels(task, req.LabelIDs, currentUserID); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create task")
		if errors.Is(err, apperrors.ErrAssigneeNotFound) ||
			errors.Is(err, apperrors.ErrLeadNotFound) ||
			errors.Is(err, apperrors.ErrCustomerNotFound) {
			utils.RespondNotFound(c, err.Error())
		} else if errors.Is(err, apperrors.ErrLabelNotFound) {
			// A bad label id is a bad reference in the body, not a missing
			// resource at the requested path, so it is a 400 like the other
			// payload-level rejections here.
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
		} else if errors.Is(err, apperrors.ErrInactiveUser) ||
			errors.Is(err, apperrors.ErrTaskLeadCustomerConflict) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	service.RedactTaskLead(leadViewer(c), task)
	utils.LogHandlerResponse(logger, http.StatusCreated, task)
	utils.RespondSuccess(c, http.StatusCreated, task)
}

// Get godoc
// @Summary Get a task by ID
// @Description Retrieve one task with its assignee, lead, customer and labels. Roles: every authenticated role; admins may read any task, every other role only tasks assigned to them (403 otherwise, checked after the task is found, so a missing id is 404 for everyone). lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read. The same redaction applies to every task this API returns.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Success 200 {object} utils.APIResponse{data=models.Task} "The task"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - a non-admin asked for a task not assigned to them"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live task with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Task not found")
			utils.RespondNotFound(c, "Task not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to retrieve task")
		utils.RespondInternalError(c)
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

	service.RedactTaskLead(leadViewer(c), task)
	utils.LogHandlerResponse(logger, http.StatusOK, task)
	utils.RespondSuccess(c, http.StatusOK, task)
}

// List godoc
// @Summary List tasks
// @Description One page of tasks. Roles: every authenticated role. Admins see every task and may use search and sorting. Every other role is silently narrowed to the tasks assigned to them, and search, sort_by and sort_order are ignored for them, except with label_id, which is the one non-admin path that honours sort_by and sort_order. label_id filters to the tasks carrying that label and takes precedence over search (search is ignored when both are sent); an unparseable or non-positive label_id is ignored, and an unknown one yields an empty page rather than 404. Precedence for admins: label_id, then search, then sort_by, then the plain list. search matches title or description with SQL LIKE. An unknown sort_by is ignored (unsorted, ascending id) and any sort_order other than asc or desc falls back to asc. Pagination: page (1-based) overrides offset when greater than 0; offset defaults to 0; limit defaults to 20, non-positive or unparseable values fall back to the default and anything above 100 is clamped to 100. The non-admin path without label_id returns assigned_to as a zero-valued object. lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of tasks to skip" default(0)
// @Param limit query int false "Page size; clamped to 100" default(20)
// @Param sort_by query string false "Sort column (admin, or any role with label_id); unknown values are ignored" Enums(created_at, updated_at, title, status, priority, due_date)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Substring matched against title and description (admin only; ignored when label_id is given)"
// @Param label_id query int false "Only tasks carrying this label; non-admins still see only their own tasks"
// @Success 200 {object} utils.APIResponse{data=TaskListData,meta=utils.APIMeta} "One page of tasks; meta carries page, per_page, total and total_pages"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tasks [get]
func (h *TaskHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.List")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	// Support both page-based and offset-based pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	offset, limit := utils.ParseOffsetLimit(c)

	// Convert page to offset if page is provided
	if page > 0 {
		offset = (page - 1) * limit
	}

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

	// A label filter is applied before anything else, and for every role: it is
	// a narrowing of the same list, so it composes with the assignee scoping
	// instead of replacing it. A malformed or non-positive value is treated as
	// "no filter" rather than as an error, matching how sort_by is handled.
	var labelID uint
	if raw := c.Query("label_id"); raw != "" {
		if parsed, parseErr := strconv.ParseUint(raw, 10, 32); parseErr == nil && parsed > 0 {
			labelID = uint(parsed)
		}
	}

	var tasks []models.Task
	var total int64
	var err error

	// Admin can list all tasks, non-admin users can only list their own tasks
	if labelID != 0 && currentUserRole != string(models.RoleAdmin) {
		tasks, total, err = h.taskService.ListByLabelForAssignee(currentUserID, labelID, offset, limit, sortBy, sortOrder)
	} else if labelID != 0 {
		tasks, total, err = h.taskService.ListByLabel(labelID, offset, limit, sortBy, sortOrder)
	} else if currentUserRole != string(models.RoleAdmin) {
		tasks, total, err = h.taskService.GetByAssignee(currentUserID, offset, limit)
	} else if search != "" {
		tasks, total, err = h.taskService.Search(search, offset, limit, sortBy, sortOrder)
	} else if sortBy != "" {
		tasks, total, err = h.taskService.ListSorted(offset, limit, sortBy, sortOrder)
	} else {
		tasks, total, err = h.taskService.List(offset, limit)
	}

	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tasks")
		utils.RespondInternalError(c)
		return
	}
	service.RedactTaskLeads(leadViewer(c), tasks)

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	responseData := gin.H{"tasks": tasks, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

// ListMyTasks godoc
// @Summary List tasks assigned to the current user
// @Description One page of the tasks assigned to the caller, in ascending id order. Roles: every authenticated role; admins also see only their own tasks here. No sorting, search or label filter. Pagination as on GET /tasks (page overrides offset; limit default 20, clamped to 100). assigned_to is a zero-valued object on this endpoint (the assignee is not loaded). lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of tasks to skip" default(0)
// @Param limit query int false "Page size; clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=TaskListData,meta=utils.APIMeta} "One page of the caller's tasks"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tasks/my [get]
func (h *TaskHandler) ListMyTasks(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.ListMyTasks")

	currentUserID := c.GetUint("user_id")

	// Support both page-based and offset-based pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	offset, limit := utils.ParseOffsetLimit(c)

	// Convert page to offset if page is provided
	if page > 0 {
		offset = (page - 1) * limit
	}

	tasks, total, err := h.taskService.GetByAssignee(currentUserID, offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list my tasks")
		utils.RespondInternalError(c)
		return
	}
	service.RedactTaskLeads(leadViewer(c), tasks)

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	responseData := gin.H{"tasks": tasks, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

// upcomingTasksDefaultDays, upcomingTasksMaxDays and upcomingTasksMaxResults
// bound GET /tasks/upcoming. The day count is clamped rather than rejected, in
// keeping with the other list parameters here, and the result count is capped
// so the endpoint can never be turned into an unbounded table scan by widening
// the window.
const (
	upcomingTasksDefaultDays = 7
	upcomingTasksMaxDays     = 90
	upcomingTasksMaxResults  = 100
)

// GetUpcoming godoc
// @Summary List tasks due in the next N days
// @Description The open tasks whose due date falls between now and N days from now, as a bare array ordered by due date ascending (then id). Roles: every authenticated role; admins see every assignee's tasks, every other role only the tasks assigned to them. Completed and cancelled tasks and tasks without a due date are excluded; the window starts at the current instant, so overdue tasks are NOT included. An absent, unparseable or non-positive days value falls back to 7, and anything above 90 is clamped to 90 rather than rejected. At most 100 tasks are returned and there is no pagination. Labels are not loaded here (labels is omitted). lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read.
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Size of the look-ahead window in days; clamped to 1-90" minimum(1) maximum(90) default(7)
// @Success 200 {object} utils.APIResponse{data=[]models.Task} "Up to 100 upcoming tasks; an empty array when none"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tasks/upcoming [get]
func (h *TaskHandler) GetUpcoming(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TaskHandler.GetUpcoming")

	days := upcomingTasksDefaultDays
	if raw := c.Query("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days > upcomingTasksMaxDays {
		days = upcomingTasksMaxDays
	}

	from := time.Now()
	to := from.AddDate(0, 0, days)

	var tasks []models.Task
	var err error
	if c.GetString("user_role") == string(models.RoleAdmin) {
		tasks, err = h.taskService.GetDueWithin(from, to, upcomingTasksMaxResults)
	} else {
		tasks, err = h.taskService.GetDueWithinByAssignee(c.GetUint("user_id"), from, to, upcomingTasksMaxResults)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list upcoming tasks")
		utils.RespondInternalError(c)
		return
	}
	if tasks == nil {
		tasks = []models.Task{}
	}
	service.RedactTaskLeads(leadViewer(c), tasks)

	utils.LogHandlerResponse(logger, http.StatusOK, tasks)
	utils.RespondSuccess(c, http.StatusOK, tasks)
}

// Update godoc
// @Summary Update a task
// @Description Partially update a task: absent or empty fields are left unchanged (title, description, due_date and the links cannot be cleared this way). Roles: every authenticated role; admins may update any task, every other role only tasks assigned to them. Only admins may change assigned_to_id; sending the current assignee back is not a reassignment. A completed task cannot be moved to another status. A task cannot link both a lead and a customer. A lead_id that is sent must name a lead the caller may see (a hidden lead is 404 like a missing one), even when it equals the stored link, so sending lead ids cannot reveal a hidden link; a stored link is kept unchecked when both link fields are absent. Sending customer_id without lead_id replaces the association: the lead link is cleared and the customer link set after validation, whether or not the caller could see the old lead. label_ids, when present, replaces the whole label set (an empty array clears it); absent leaves the labels untouched; any id that matches no label rejects the whole request. The order of checks: id, body binding, task lookup, ownership, reassignment permission, then assignee, lead, customer, conflict, completed status, labels; any failure leaves the task unchanged. No automation event is produced. lead_id and lead are omitted whenever the caller may not see the linked lead (admins see every lead, sales users only the leads they own, every other role none), decided again on every read.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Param request body UpdateTaskRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Task} "The updated task"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), the new assignee is deactivated, the task would link both a lead and a customer, or a completed task would change status; VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; INVALID_REFERENCE when a label id matches no label"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - a non-admin tried to update a task not assigned to them, or to reassign a task"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - the task, the new assignee, the lead (missing or hidden from the caller) or the customer does not exist"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Task not found")
			utils.RespondNotFound(c, "Task not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to load task")
		utils.RespondInternalError(c)
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
		if req.LeadID == nil {
			// A customer-only request replaces the association, regardless of
			// whether the caller could see the previous lead. A conflict here
			// would disclose the presence of a redacted lead link.
			task.LeadID = nil
		}
	}

	if err := h.taskService.UpdateWithLabels(task, req.LabelIDs, req.LeadID != nil, currentUserID); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to update task")
		if errors.Is(err, apperrors.ErrAssigneeNotFound) ||
			errors.Is(err, apperrors.ErrLeadNotFound) ||
			errors.Is(err, apperrors.ErrCustomerNotFound) {
			utils.RespondNotFound(c, err.Error())
		} else if errors.Is(err, apperrors.ErrLabelNotFound) {
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
		} else if errors.Is(err, apperrors.ErrInactiveUser) ||
			errors.Is(err, apperrors.ErrTaskLeadCustomerConflict) ||
			errors.Is(err, apperrors.ErrCompletedTaskModify) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	service.RedactTaskLead(leadViewer(c), task)
	utils.LogHandlerResponse(logger, http.StatusOK, task)
	utils.RespondSuccess(c, http.StatusOK, task)
}

// Delete godoc
// @Summary Delete a task
// @Description Soft-delete a task. Roles: admin only; the role check runs before the id is parsed, so non-admins always receive 403. The task's label links stay in the join table but the task no longer appears anywhere (and no longer counts towards a label's task_count).
// @Tags tasks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Task ID"
// @Success 204 "Task deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live task with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Task not found")
			utils.RespondNotFound(c, "Task not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete task")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}
