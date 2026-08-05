package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type BulkHandler struct {
	bulkService service.BulkOperationService
}

func NewBulkHandler(bulkService service.BulkOperationService) *BulkHandler {
	return &BulkHandler{bulkService: bulkService}
}

// Generic bulk operation handlers

// BulkCreate handles POST /{resource}/bulk/create
func (h *BulkHandler) BulkCreate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkCreate")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasCreatePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk create")
		return
	}

	response, err := h.bulkService.ProcessBulkCreate(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk create failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkUpdate handles PUT /{resource}/bulk/update
func (h *BulkHandler) BulkUpdate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdate")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasUpdatePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk update")
		return
	}

	response, err := h.bulkService.ProcessBulkUpdate(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk update failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkDelete handles DELETE /{resource}/bulk/delete
func (h *BulkHandler) BulkDelete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkDelete")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasDeletePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk delete")
		return
	}

	response, err := h.bulkService.ProcessBulkDelete(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk delete failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkAction handles POST /{resource}/bulk/action
func (h *BulkHandler) BulkAction(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkAction")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type and action
	if !h.hasActionPermission(c, resourceType, req.Action) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk action")
		return
	}

	response, err := h.bulkService.ProcessBulkAction(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk action failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Bulk operation status and management

// GetBulkOperation handles GET /bulk/operations/{id}
func (h *BulkHandler) GetBulkOperation(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.GetBulkOperation")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid operation ID")
		return
	}

	operation, err := h.bulkService.GetBulkOperationWithItems(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Bulk operation not found")
		utils.RespondNotFound(c, "Bulk operation not found")
		return
	}

	// Check permissions - users can only view their own operations unless admin
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	if operation.UserID != currentUserID && currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "You can only view your own bulk operations")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, operation)
	utils.RespondSuccess(c, http.StatusOK, operation)
}

// ListBulkOperations handles GET /bulk/operations
func (h *BulkHandler) ListBulkOperations(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.ListBulkOperations")
	
	offset, limit := utils.ParseOffsetLimit(c)
	
	if limit > 100 {
		limit = 100
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	var operations []models.BulkOperation
	var total int64
	var err error

	// Admin can see all operations, others only their own
	if currentUserRole == string(models.RoleAdmin) {
		operations, total, err = h.bulkService.ListAllBulkOperations(offset, limit)
	} else {
		operations, total, err = h.bulkService.GetUserBulkOperations(currentUserID, offset, limit)
	}

	if err != nil {
		logger.WithError(err).Error("Failed to list bulk operations")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID: c.GetString("request_id"),
		Page:      (offset / limit) + 1,
		PerPage:   limit,
		Total:     total,
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"operations": operations})
	utils.RespondSuccessWithMeta(c, http.StatusOK, operations, meta)
}

// Resource-specific bulk handlers (for convenience and specific validation)

// User bulk operations
func (h *BulkHandler) BulkCreateUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkCreateUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk create users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk create users")
		return
	}
	
	var req models.BulkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkCreateUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk create users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkUpdateUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk update users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk update users")
		return
	}
	
	var req models.BulkUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkUpdateUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk update users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkDeleteUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkDeleteUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk delete users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk delete users")
		return
	}
	
	var req models.BulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkDeleteUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk delete users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkActionUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkActionUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can perform bulk actions on users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can perform bulk actions on users")
		return
	}
	
	var req models.BulkActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkActionUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk action users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Bulk status updates
//
// Three entity-specific endpoints, one per resource the UI offers a multi-select
// status change for. They are deliberately not the generic /bulk/:resource
// handlers above: the payloads are fixed by the frontend, the status has to be
// validated against the entity's own enum, and the operation is all-or-nothing
// rather than best-effort, so a partial-success response shape would be a lie.

// BulkLeadStatusRequest is the payload of POST /leads/bulk/status.
type BulkLeadStatusRequest struct {
	LeadIDs []uint            `json:"lead_ids" binding:"required,min=1,max=100,dive,gt=0"`
	Status  models.LeadStatus `json:"status" binding:"required,oneof=new contacted qualified unqualified converted"`
}

// BulkTicketStatusRequest is the payload of POST /tickets/bulk/status.
type BulkTicketStatusRequest struct {
	TicketIDs []uint              `json:"ticket_ids" binding:"required,min=1,max=100,dive,gt=0"`
	Status    models.TicketStatus `json:"status" binding:"required,oneof=open in_progress resolved closed"`
}

// BulkTaskStatusRequest is the payload of POST /tasks/bulk/status.
type BulkTaskStatusRequest struct {
	TaskIDs []uint            `json:"task_ids" binding:"required,min=1,max=100,dive,gt=0"`
	Status  models.TaskStatus `json:"status" binding:"required,oneof=pending in_progress completed cancelled"`
}

// BulkUpdateLeadStatus godoc
// @Summary Update the status of several leads at once
// @Description Sets the same status on up to 100 leads in a single all-or-nothing transaction. Requires the sales or admin role; sales users may only update leads they own. If any listed lead is missing or not owned by the caller, nothing is written and the offending IDs are named in the error details.
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkLeadStatusRequest true "Lead IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed lead was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Empty ID list, more than 100 IDs, or an invalid lead status"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - requires sales or admin role; sales users can only update leads they own, and the not-owned IDs are listed in the error details"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "One or more leads do not exist; the missing IDs are listed in the error details"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /leads/bulk/status [post]
func (h *BulkHandler) BulkUpdateLeadStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateLeadStatus")

	var req BulkLeadStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	result, err := h.bulkService.BulkSetLeadStatus(
		c.GetUint("user_id"), models.UserRole(c.GetString("user_role")), req.LeadIDs, req.Status)
	if err != nil {
		logger.WithError(err).Warn("Bulk lead status update rejected")
		respondBulkStatusError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, result)
	utils.RespondSuccess(c, http.StatusOK, result)
}

// BulkUpdateTicketStatus godoc
// @Summary Update the status of several tickets at once
// @Description Sets the same status on up to 100 tickets in a single all-or-nothing transaction. Admins may update any ticket, support users only tickets assigned to them; sales users are read-only on tickets and customers cannot update them. A closed ticket cannot be reopened. If any listed ticket is missing, not assigned to the caller, or closed, nothing is written and the offending IDs are named in the error details.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkTicketStatusRequest true "Ticket IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed ticket was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Empty ID list, more than 100 IDs, an invalid ticket status, or an attempt to reopen a closed ticket"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales and customer users cannot update tickets; support users can only update tickets assigned to them, and those IDs are listed in the error details"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "One or more tickets do not exist; the missing IDs are listed in the error details"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tickets/bulk/status [post]
func (h *BulkHandler) BulkUpdateTicketStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateTicketStatus")

	currentUserRole := models.UserRole(c.GetString("user_role"))

	// Mirrors the single-ticket update, which turns these roles away before it
	// looks at the body at all: an unauthorized caller learns nothing about
	// which payloads would have been well formed.
	if currentUserRole == models.RoleCustomer {
		utils.RespondForbidden(c, "Customers cannot update tickets")
		return
	}
	if currentUserRole == models.RoleSales {
		utils.RespondForbidden(c, "Sales users cannot update tickets")
		return
	}

	var req BulkTicketStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	result, err := h.bulkService.BulkSetTicketStatus(
		c.GetUint("user_id"), currentUserRole, req.TicketIDs, req.Status)
	if err != nil {
		logger.WithError(err).Warn("Bulk ticket status update rejected")
		respondBulkStatusError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, result)
	utils.RespondSuccess(c, http.StatusOK, result)
}

// BulkUpdateTaskStatus godoc
// @Summary Update the status of several tasks at once
// @Description Sets the same status on up to 100 tasks in a single all-or-nothing transaction. Admins may update any task, every other role only tasks assigned to them. The status of a completed task cannot be changed. If any listed task is missing, not assigned to the caller, or already completed, nothing is written and the offending IDs are named in the error details.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkTaskStatusRequest true "Task IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed task was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Empty ID list, more than 100 IDs, an invalid task status, or an attempt to change the status of a completed task"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - non-admin users can only update tasks assigned to them, and those IDs are listed in the error details"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "One or more tasks do not exist; the missing IDs are listed in the error details"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tasks/bulk/status [post]
func (h *BulkHandler) BulkUpdateTaskStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateTaskStatus")

	var req BulkTaskStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	result, err := h.bulkService.BulkSetTaskStatus(
		c.GetUint("user_id"), models.UserRole(c.GetString("user_role")), req.TaskIDs, req.Status)
	if err != nil {
		logger.WithError(err).Warn("Bulk task status update rejected")
		respondBulkStatusError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, result)
	utils.RespondSuccess(c, http.StatusOK, result)
}

// respondBulkStatusError maps a refused bulk status update onto the unified
// response shape. The classification is by sentinel, never by message text, and
// the details — which records caused the refusal — are carried through verbatim
// because naming them is the whole point of an all-or-nothing batch failing.
// Anything unrecognised is an internal error and its text stays in the log.
func respondBulkStatusError(c *gin.Context, err error) {
	var details interface{}
	message := "Bulk status update failed"
	if appErr, ok := apperrors.AsAppError(err); ok {
		details = appErr.Details
		message = appErr.Message
	}

	switch {
	case errors.Is(err, apperrors.ErrForbidden):
		utils.RespondError(c, http.StatusForbidden, utils.ErrCodeForbidden, message, details)
	case apperrors.IsNotFound(err):
		utils.RespondError(c, http.StatusNotFound, utils.ErrCodeNotFound, message, details)
	case errors.Is(err, apperrors.ErrCompletedTaskModify),
		errors.Is(err, apperrors.ErrClosedTicketReopen):
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeBadRequest, message, details)
	default:
		if appErr, ok := apperrors.AsAppError(err); ok && appErr.HTTPStatus == http.StatusBadRequest {
			utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeBadRequest, message, details)
			return
		}
		utils.RespondInternalError(c)
	}
}

// Helper methods

func (h *BulkHandler) isValidResourceType(resourceType string) bool {
	validTypes := []string{"users", "leads", "customers", "tasks", "tickets"}
	for _, validType := range validTypes {
		if resourceType == validType {
			return true
		}
	}
	return false
}

func (h *BulkHandler) hasCreatePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		return true // All authenticated users can create tasks and tickets
	default:
		return false
	}
}

func (h *BulkHandler) hasUpdatePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		return true // All authenticated users can update tasks and tickets
	default:
		return false
	}
}

func (h *BulkHandler) hasDeletePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin)
	case "tasks", "tickets":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales) || userRole == string(models.RoleSupport)
	default:
		return false
	}
}

func (h *BulkHandler) hasActionPermission(c *gin.Context, resourceType, action string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		// Most task/ticket actions can be performed by admin, sales, and support
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales) || userRole == string(models.RoleSupport)
	default:
		return false
	}
}