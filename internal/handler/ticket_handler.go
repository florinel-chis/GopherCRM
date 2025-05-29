package handler

import (
	"net/http"
	"strconv"

	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/service"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type TicketHandler struct {
	ticketService service.TicketService
}

func NewTicketHandler(ticketService service.TicketService) *TicketHandler {
	return &TicketHandler{ticketService: ticketService}
}

type CreateTicketRequest struct {
	Title        string                  `json:"title" binding:"required"`
	Description  string                  `json:"description" binding:"required"`
	Priority     models.TicketPriority   `json:"priority,omitempty" binding:"omitempty,oneof=low medium high urgent"`
	CustomerID   uint                    `json:"customer_id" binding:"required"`
	AssignedToID *uint                   `json:"assigned_to_id,omitempty"`
}

type UpdateTicketRequest struct {
	Title        string                  `json:"title,omitempty"`
	Description  string                  `json:"description,omitempty"`
	Status       models.TicketStatus     `json:"status,omitempty" binding:"omitempty,oneof=open in_progress resolved closed"`
	Priority     models.TicketPriority   `json:"priority,omitempty" binding:"omitempty,oneof=low medium high urgent"`
	AssignedToID *uint                   `json:"assigned_to_id,omitempty"`
	Resolution   string                  `json:"resolution,omitempty"`
}

func (h *TicketHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Create")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Only support and admin users can create tickets
	if currentUserRole != string(models.RoleSupport) && currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only support and admin users can create tickets")
		return
	}

	var req CreateTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	ticket := &models.Ticket{
		Title:        req.Title,
		Description:  req.Description,
		Priority:     req.Priority,
		CustomerID:   req.CustomerID,
		AssignedToID: req.AssignedToID,
		Status:       models.TicketStatusOpen,
	}

	// If no assignee specified, assign to current user
	if ticket.AssignedToID == nil {
		ticket.AssignedToID = &currentUserID
	}

	if err := h.ticketService.Create(ticket); err != nil {
		logger.WithError(err).Error("Failed to create ticket")
		if err.Error() == "customer not found" {
			utils.RespondNotFound(c, err.Error())
		} else if err.Error() == "assignee not found" || err.Error() == "tickets can only be assigned to support or admin users" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, ticket)
	utils.RespondSuccess(c, http.StatusCreated, ticket)
}

func (h *TicketHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.List")
	
	currentUserRole := c.GetString("user_role")
	
	// Customer users cannot list all tickets
	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot list all tickets")
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	tickets, total, err := h.ticketService.List(offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list tickets")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	responseData := gin.H{"tickets": tickets, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *TicketHandler) ListByCustomer(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListByCustomer")
	
	customerID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	tickets, total, err := h.ticketService.GetByCustomer(uint(customerID), offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list tickets by customer")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	responseData := gin.H{"tickets": tickets, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *TicketHandler) ListMyTickets(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListMyTickets")
	
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Only support and admin users can have tickets assigned
	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot have tickets assigned to them")
		return
	}
	
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	tickets, total, err := h.ticketService.GetByAssignee(currentUserID, offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list my tickets")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	responseData := gin.H{"tickets": tickets, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *TicketHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Get")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid ticket ID")
		return
	}

	ticket, err := h.ticketService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Ticket not found")
		utils.RespondNotFound(c, "Ticket not found")
		return
	}

	// Check permissions
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Customer users can only view their own tickets
	if currentUserRole == string(models.RoleCustomer) {
		// In a real implementation, we'd check if the current user is the customer
		// For now, we'll reject all customer access
		utils.RespondForbidden(c, "Customers can only view their own tickets")
		return
	}
	
	// Support users can only view assigned tickets
	if currentUserRole == string(models.RoleSupport) && 
		(ticket.AssignedToID == nil || *ticket.AssignedToID != currentUserID) {
		utils.RespondForbidden(c, "You can only view tickets assigned to you")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, ticket)
	utils.RespondSuccess(c, http.StatusOK, ticket)
}

func (h *TicketHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Update")
	
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Customers cannot update tickets
	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot update tickets")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid ticket ID")
		return
	}

	var req UpdateTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Get existing ticket
	ticket, err := h.ticketService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Ticket not found")
		utils.RespondNotFound(c, "Ticket not found")
		return
	}

	// Check permissions
	if currentUserRole == string(models.RoleSupport) && 
		(ticket.AssignedToID == nil || *ticket.AssignedToID != currentUserID) {
		utils.RespondForbidden(c, "You can only update tickets assigned to you")
		return
	}

	// Apply updates
	if req.Title != "" {
		ticket.Title = req.Title
	}
	if req.Description != "" {
		ticket.Description = req.Description
	}
	if req.Status != "" {
		ticket.Status = req.Status
	}
	if req.Priority != "" {
		ticket.Priority = req.Priority
	}
	if req.AssignedToID != nil {
		ticket.AssignedToID = req.AssignedToID
	}
	if req.Resolution != "" {
		ticket.Resolution = req.Resolution
	}

	if err := h.ticketService.Update(ticket); err != nil {
		logger.WithError(err).Error("Failed to update ticket")
		if err.Error() == "cannot reopen closed ticket" ||
		   err.Error() == "assignee not found" ||
		   err.Error() == "tickets can only be assigned to support or admin users" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, ticket)
	utils.RespondSuccess(c, http.StatusOK, ticket)
}

func (h *TicketHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Delete")
	
	currentUserRole := c.GetString("user_role")
	
	// Only admin users can delete tickets
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can delete tickets")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid ticket ID")
		return
	}

	if err := h.ticketService.Delete(uint(id)); err != nil {
		logger.WithError(err).Error("Failed to delete ticket")
		utils.RespondNotFound(c, "Ticket not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}