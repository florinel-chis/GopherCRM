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

type TicketHandler struct {
	ticketService   service.TicketService
	customerService service.CustomerService
}

func NewTicketHandler(ticketService service.TicketService, customerService ...service.CustomerService) *TicketHandler {
	h := &TicketHandler{ticketService: ticketService}
	if len(customerService) > 0 {
		h.customerService = customerService[0]
	}
	return h
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

// Create godoc
// @Summary Create a new ticket
// @Description Create a support ticket. Only support and admin users may create tickets; sales and customer users are rejected. When assigned_to_id is omitted the ticket is assigned to the caller. The assignee, if given, must exist and hold the support or admin role.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateTicketRequest true "Ticket creation request"
// @Success 201 {object} utils.APIResponse{data=models.Ticket} "Ticket created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, unknown assignee, or assignee is not a support/admin user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Support or Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tickets [post]
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
		if errors.Is(err, apperrors.ErrCustomerNotFound) {
			utils.RespondNotFound(c, "customer not found")
		} else if errors.Is(err, apperrors.ErrAssigneeNotFound) || errors.Is(err, apperrors.ErrInvalidAssigneeRole) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, ticket)
	utils.RespondSuccess(c, http.StatusCreated, ticket)
}

// List godoc
// @Summary List tickets
// @Description List all tickets with pagination, optional free-text search and sorting. Admin, sales and support users all see every ticket; customer users are rejected. The response data is an object with a "tickets" array and a "total" count, alongside pagination metadata.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param sort_by query string false "Sort column; ignored unless one of the allowed values" Enums(created_at, updated_at, title, status, priority)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Free-text search across ticket fields; takes precedence over the plain sorted listing"
// @Success 200 {object} utils.APIResponse{data=object,meta=utils.APIMeta} "Tickets retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - customers cannot list all tickets"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tickets [get]
func (h *TicketHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.List")

	currentUserRole := c.GetString("user_role")

	// Customer users cannot list all tickets
	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot list all tickets")
		return
	}

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
	}

	if !allowedSortColumns[sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	search := c.Query("search")

	var tickets []models.Ticket
	var total int64
	var err error

	if search != "" {
		tickets, total, err = h.ticketService.Search(search, offset, limit, sortBy, sortOrder)
	} else if sortBy != "" {
		tickets, total, err = h.ticketService.ListSorted(offset, limit, sortBy, sortOrder)
	} else {
		tickets, total, err = h.ticketService.List(offset, limit)
	}

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

// ListByCustomer godoc
// @Summary List tickets for a customer
// @Description List the tickets belonging to one customer. Admin, sales and support users may query any customer; a customer user may only query the customer record linked to their own account. The response data is an object with a "tickets" array and a "total" count, alongside pagination metadata.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param offset query int false "Result offset" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=object,meta=utils.APIMeta} "Tickets retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - customers can only view their own tickets"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /customers/{id}/tickets [get]
func (h *TicketHandler) ListByCustomer(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListByCustomer")
	
	customerID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	// Customer users may only list tickets belonging to their own customer record.
	currentUserID := c.GetUint("user_id")
	if c.GetString("user_role") == string(models.RoleCustomer) {
		if h.customerService == nil {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		customer, err := h.customerService.GetByUserID(currentUserID)
		if err != nil || customer == nil || customer.ID != uint(customerID) {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
	}

	offset, limit := utils.ParseOffsetLimit(c)

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

// ListMyTickets godoc
// @Summary List tickets assigned to the current user
// @Description List the tickets assigned to the authenticated user. Customer users are rejected because tickets are never assigned to them; sales users are allowed through but will always get an empty list, since only support and admin users can be assignees. The response data is an object with a "tickets" array and a "total" count, alongside pagination metadata.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Result offset" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=object,meta=utils.APIMeta} "Tickets retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - customers cannot have tickets assigned to them"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tickets/my [get]
func (h *TicketHandler) ListMyTickets(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListMyTickets")
	
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Only support and admin users can have tickets assigned
	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot have tickets assigned to them")
		return
	}
	
	offset, limit := utils.ParseOffsetLimit(c)

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

// Get godoc
// @Summary Get a ticket by ID
// @Description Retrieve a single ticket. Admin and sales users may read any ticket; support users may only read tickets assigned to them; customer users may only read tickets belonging to the customer record linked to their account.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "Ticket retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid ticket ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - the ticket is outside the caller's scope"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Ticket not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /tickets/{id} [get]
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
	
	// Customer users can only view tickets for their associated customer record
	if currentUserRole == string(models.RoleCustomer) {
		if h.customerService == nil {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		customer, err := h.customerService.GetByUserID(currentUserID)
		if err != nil || customer == nil {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		if ticket.CustomerID != customer.ID {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		// Customer is associated with this ticket - allow access
		utils.LogHandlerResponse(logger, http.StatusOK, ticket)
		utils.RespondSuccess(c, http.StatusOK, ticket)
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

// Update godoc
// @Summary Update a ticket
// @Description Update a ticket. Customer users are rejected outright; support users may only update tickets assigned to them; admin and sales users may update any ticket. Only non-empty fields are applied. A closed ticket cannot be moved back to another status, and a new assignee must exist and hold the support or admin role.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Param request body UpdateTicketRequest true "Ticket update request"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "Ticket updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid ticket ID, invalid request data, attempt to reopen a closed ticket, unknown assignee, or assignee is not a support/admin user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - customers cannot update tickets and support users only their own assignments"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Ticket not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /tickets/{id} [put]
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
		if errors.Is(err, apperrors.ErrClosedTicketReopen) ||
			errors.Is(err, apperrors.ErrAssigneeNotFound) ||
			errors.Is(err, apperrors.ErrInvalidAssigneeRole) {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, ticket)
	utils.RespondSuccess(c, http.StatusOK, ticket)
}

// Delete godoc
// @Summary Delete a ticket
// @Description Delete a ticket (admin role only). Any failure to delete an existing ticket is also reported as 404.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid ticket ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Ticket not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /tickets/{id} [delete]
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