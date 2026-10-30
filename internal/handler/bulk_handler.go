package handler

import (
	"errors"
	"net/http"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type BulkHandler struct {
	bulkService service.BulkStatusService
}

func NewBulkHandler(bulkService service.BulkStatusService) *BulkHandler {
	return &BulkHandler{bulkService: bulkService}
}

// Bulk status updates
//
// Three entity-specific endpoints, one per resource the UI offers a multi-select
// status change for. The payloads are fixed by the frontend, the status has to be
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
		logger.WithError(utils.LogError(err)).Warn("Bulk lead status update rejected")
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
		logger.WithError(utils.LogError(err)).Warn("Bulk ticket status update rejected")
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
		logger.WithError(utils.LogError(err)).Warn("Bulk task status update rejected")
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
