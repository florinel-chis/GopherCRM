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
//
// @Description Body of POST /leads/bulk/status.
type BulkLeadStatusRequest struct {
	// IDs of the leads to update, 1 to 100, each positive. A repeated id is updated once.
	LeadIDs []uint `json:"lead_ids" binding:"required,min=1,max=100,dive,gt=0" example:"12,15,19"`
	// The status to set on every listed lead. converted passes the binding but is always refused (400 VALIDATION_ERROR, details.field status), so it is not listed among the accepted values: conversion goes through POST /leads/{id}/convert.
	Status models.LeadStatus `json:"status" binding:"required,oneof=new contacted qualified unqualified converted" example:"contacted" swaggertype:"string" enums:"new,contacted,qualified,unqualified"`
}

// BulkTicketStatusRequest is the payload of POST /tickets/bulk/status.
//
// @Description Body of POST /tickets/bulk/status.
type BulkTicketStatusRequest struct {
	// IDs of the tickets to update, 1 to 100, each positive. A repeated id is updated once.
	TicketIDs []uint `json:"ticket_ids" binding:"required,min=1,max=100,dive,gt=0" example:"18,21"`
	// The status to set on every listed ticket. A closed ticket cannot be moved to another status.
	Status models.TicketStatus `json:"status" binding:"required,oneof=open in_progress resolved closed" example:"resolved"`
}

// BulkTaskStatusRequest is the payload of POST /tasks/bulk/status.
//
// @Description Body of POST /tasks/bulk/status.
type BulkTaskStatusRequest struct {
	// IDs of the tasks to update, 1 to 100, each positive. A repeated id is updated once.
	TaskIDs []uint `json:"task_ids" binding:"required,min=1,max=100,dive,gt=0" example:"4,9"`
	// The status to set on every listed task. A completed task cannot be moved to another status.
	Status models.TaskStatus `json:"status" binding:"required,oneof=pending in_progress completed cancelled" example:"completed"`
}

// BulkUpdateLeadStatus godoc
// @Summary Update the status of several leads at once
// @Description Set the same status on up to 100 leads in one all-or-nothing transaction. Roles: admin and sales (route guard; support and customers get 403 before the body is read); sales users may only update leads they own. Repeated ids are collapsed. If any listed lead is missing (404, error.details.missing_ids) or, for sales, not owned by the caller (403, error.details.forbidden_ids), nothing is written; missing ids are reported before ownership. status converted is refused with 400 VALIDATION_ERROR and error.details {field: status}: a lead becomes converted only through POST /leads/{id}/convert. Side effects: every updated lead records a lead_updated automation event, plus lead_status_changed when its status actually changed (origin bulk), in the same transaction, from which the automation worker later starts runs of the enabled rules with a matching trigger; and, once the id, status and role checks have passed, an audit row is written to the bulk operation log and marked completed or failed.
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkLeadStatusRequest true "Lead IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed lead was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when the id list is missing, empty, longer than 100 or holds a 0, or the status is not an allowed value (error.details maps the Go field name, e.g. LeadIDs or LeadIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON; VALIDATION_ERROR with details.field status for status converted"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin or sales role required; or a sales user listed leads it does not own (error.details.forbidden_ids)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - one or more leads do not exist (error.details.missing_ids)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /leads/bulk/status [post]
func (h *BulkHandler) BulkUpdateLeadStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateLeadStatus")

	var req BulkLeadStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	result, err := h.bulkService.BulkSetLeadStatus(c.Request.Context(),
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
// @Description Set the same status on up to 100 tickets in one all-or-nothing transaction. Roles: admin may update any ticket, support only tickets assigned to them; sales (read-only on tickets) and customers get 403 before the body is read. Repeated ids are collapsed. If any listed ticket is missing (404, error.details.missing_ids), not assigned to a support caller (403, error.details.forbidden_ids) or closed while the target status is not closed (400, error.details.closed_ids), nothing is written; the checks report in that order. No automation event is recorded; once the id, status and role checks have passed, an audit row is written to the bulk operation log and marked completed or failed.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkTicketStatusRequest true "Ticket IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed ticket was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when the id list is missing, empty, longer than 100 or holds a 0, or the status is not an allowed value (error.details maps the Go field name, e.g. TicketIDs or TicketIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON; BAD_REQUEST when a listed ticket is closed and the target status is not (error.details.closed_ids)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - sales and customer users cannot update tickets; a support user listed tickets not assigned to them (error.details.forbidden_ids)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - one or more tickets do not exist (error.details.missing_ids)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

	result, err := h.bulkService.BulkSetTicketStatus(c.Request.Context(),
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
// @Description Set the same status on up to 100 tasks in one all-or-nothing transaction. Roles: every authenticated role; admins may update any task, every other role only tasks assigned to them. Repeated ids are collapsed. If any listed task is missing (404, error.details.missing_ids), not assigned to a non-admin caller (403, error.details.forbidden_ids) or completed while the target status is not completed (400, error.details.completed_ids), nothing is written; the checks report in that order. No automation event is recorded; once the id, status and role checks have passed, an audit row is written to the bulk operation log and marked completed or failed.
// @Tags tasks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body BulkTaskStatusRequest true "Task IDs and the status to set"
// @Success 200 {object} utils.APIResponse{data=models.BulkStatusUpdateResult} "Every listed task was updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when the id list is missing, empty, longer than 100 or holds a 0, or the status is not an allowed value (error.details maps the Go field name, e.g. TaskIDs or TaskIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON; BAD_REQUEST when a listed task is completed and the target status is not (error.details.completed_ids)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - a non-admin listed tasks not assigned to them (error.details.forbidden_ids)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - one or more tasks do not exist (error.details.missing_ids)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tasks/bulk/status [post]
func (h *BulkHandler) BulkUpdateTaskStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateTaskStatus")

	var req BulkTaskStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	result, err := h.bulkService.BulkSetTaskStatus(c.Request.Context(),
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

	var ruleErr *service.LeadConversionRuleError
	switch {
	case errors.As(err, &ruleErr):
		respondLeadRuleError(c, ruleErr)
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
