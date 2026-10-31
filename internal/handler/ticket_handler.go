package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// @Description Body of POST /tickets.
type CreateTicketRequest struct {
	// Short summary. Required (non-empty). Not length-checked by the API; the column is 255 characters wide.
	Title string `json:"title" binding:"required" example:"Printer offline after firmware update"`
	// Full description of the problem. Required (non-empty).
	Description string `json:"description" binding:"required" example:"Since this morning the office printer no longer appears on the network."`
	// Priority; medium when omitted or empty.
	Priority models.TicketPriority `json:"priority,omitempty" binding:"omitempty,oneof=low medium high urgent" example:"high"`
	// ID of the customer the ticket is for. Required (non-zero); an unknown customer is 404.
	CustomerID uint `json:"customer_id" binding:"required" example:"3"`
	// ID of the support or admin user to assign. Omitted or null assigns the caller. An unknown user or a user of another role is 400.
	AssignedToID *uint `json:"assigned_to_id,omitempty" example:"5"`
}

// @Description Body of PUT /tickets/{id}. Every field is optional; absent or empty fields are left unchanged.
type UpdateTicketRequest struct {
	// New title; absent or empty leaves it unchanged.
	Title string `json:"title,omitempty" example:"Printer offline after firmware update"`
	// New description; absent or empty leaves it unchanged.
	Description string `json:"description,omitempty" example:"The printer is visible again but prints blank pages."`
	// New status; absent or empty leaves it unchanged. A closed ticket cannot be moved to any other status (400); every other change is allowed.
	Status models.TicketStatus `json:"status,omitempty" binding:"omitempty,oneof=open in_progress resolved closed" example:"resolved"`
	// New priority; absent or empty leaves it unchanged.
	Priority models.TicketPriority `json:"priority,omitempty" binding:"omitempty,oneof=low medium high urgent" example:"urgent"`
	// New assignee, which must exist and hold the support or admin role (400 otherwise). Absent or null leaves it unchanged; the assignee cannot be removed. On a ticket that already has an assignee the new value is currently not stored (see the operation description).
	AssignedToID *uint `json:"assigned_to_id,omitempty" example:"5"`
	// Resolution text; absent or empty leaves it unchanged (it cannot be cleared).
	Resolution string `json:"resolution,omitempty" example:"Rolled the printer back to the previous firmware."`
}

// CreateTicketCommentRequest is the body of POST /tickets/:id/comments. The
// text may come in body, comment or content; the first one that is not blank
// after trimming wins.
//
// @Description Body of POST /tickets/{id}/comments. The text may come in body, comment or content.
type CreateTicketCommentRequest struct {
	// The comment text. The first of body, comment and content that is not blank after trimming is stored (trimmed); all three blank is 400.
	Body string `json:"body" example:"Rolled back the firmware; waiting for the customer to confirm."`
	// Alias of body, used only when body is blank.
	Comment string `json:"comment" example:""`
	// Alias of body, used only when body and comment are blank.
	Content string `json:"content" example:""`
	// True for an internal note, visible to staff only and never returned to customer callers. Defaults to false.
	IsInternal bool `json:"is_internal" example:"false"`
}

// TicketListData is the data payload of the ticket list endpoints. It is a
// documentation-only type: the handlers build the same shape inline.
//
// @Description Data payload of the ticket list endpoints: one page of tickets and the total before pagination.
type TicketListData struct {
	// The page of tickets. Unsorted pages come in ascending id order; a sorted page is ordered by the sort column, then id in the same direction.
	Tickets []models.Ticket `json:"tickets"`
	// Number of tickets in the caller's scope matching the filters, before pagination; the same value as meta.total.
	Total int64 `json:"total" example:"17"`
}

func (r CreateTicketCommentRequest) GetBody() string {
	if strings.TrimSpace(r.Body) != "" {
		return strings.TrimSpace(r.Body)
	}
	if strings.TrimSpace(r.Comment) != "" {
		return strings.TrimSpace(r.Comment)
	}
	return strings.TrimSpace(r.Content)
}

// Create godoc
// @Summary Create a new ticket
// @Description Create a support ticket. Roles: support and admin (sales and customers get 403 before the body is read). The ticket always starts in status open, whatever the body says, and priority defaults to medium. When assigned_to_id is omitted the ticket is assigned to the caller; a given assignee must exist and hold the support or admin role. The customer must exist. No notification or email is sent.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateTicketRequest true "Ticket to create"
// @Success 201 {object} utils.APIResponse{data=models.Ticket} "Ticket created; customer is a zero-valued object and assigned_to is omitted in this response"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; BAD_REQUEST when the assignee does not exist or is not a support or admin user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - support or admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - the customer does not exist"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		logger.WithError(utils.LogError(err)).Error("Failed to create ticket")
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
// @Description One page of tickets with their customer and assignee. Roles: admin and sales see every ticket; support sees only tickets assigned to them (unassigned tickets and other people's are never listed), a scope applied in the query before offset and limit so the tickets, total and meta all reflect it and no parameter can widen it; customers get 403. Precedence: for support, the assignee scope with optional search and sort; otherwise search (with optional sort), then sort_by, then the plain list. search is a substring match (SQL LIKE) on title, description and resolution. An unknown sort_by is ignored (ascending id) and any sort_order other than asc or desc falls back to asc. Pagination: page (1-based) overrides offset when greater than 0; limit defaults to 20 and is clamped to 100. Soft-deleted tickets are never listed; comments are not included.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of tickets to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Param sort_by query string false "Sort column; unknown values are ignored" Enums(created_at, updated_at, title, status, priority)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Substring matched against title, description and resolution; for support it only narrows the assigned tickets"
// @Success 200 {object} utils.APIResponse{data=TicketListData,meta=utils.APIMeta} "One page of tickets; meta carries page, per_page, total and total_pages"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - customers cannot list tickets"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

	// Support users only ever see tickets assigned to them, as on the detail
	// route. The scope is part of the query, so the rows and the total both
	// reflect it before pagination, and no query parameter can widen it.
	if currentUserRole == string(models.RoleSupport) {
		tickets, total, err = h.ticketService.ListAssigned(c.GetUint("user_id"), search, offset, limit, sortBy, sortOrder)
	} else if search != "" {
		tickets, total, err = h.ticketService.Search(search, offset, limit, sortBy, sortOrder)
	} else if sortBy != "" {
		tickets, total, err = h.ticketService.ListSorted(offset, limit, sortBy, sortOrder)
	} else {
		tickets, total, err = h.ticketService.List(offset, limit)
	}

	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tickets")
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
// @Description One page of one customer's tickets, in ascending id order. Roles: admin and sales get all of the customer's tickets; support may query any customer but gets only that customer's tickets assigned to them (applied in the query before offset and limit, so tickets, total and meta reflect it); a customer user may only query the customer record linked to their own account (403 otherwise). An unknown customer id simply yields an empty page. No page parameter, sorting or search; customer is a zero-valued object and assigned_to is omitted on this endpoint (neither is loaded).
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param offset query int false "Number of tickets to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=TicketListData,meta=utils.APIMeta} "One page of the customer's tickets"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST (Invalid customer ID) - the id is not an unsigned integer; 0 is not rejected and, for staff, lists nothing"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - a customer user asked for another customer's tickets, or has no linked customer record"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

	var tickets []models.Ticket
	var total int64
	// Support users get the customer's tickets that are assigned to them: the
	// intersection drives both the rows and the total.
	if c.GetString("user_role") == string(models.RoleSupport) {
		tickets, total, err = h.ticketService.GetByCustomerAndAssignee(uint(customerID), currentUserID, offset, limit)
	} else {
		tickets, total, err = h.ticketService.GetByCustomer(uint(customerID), offset, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list tickets by customer")
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
// @Description One page of the tickets assigned to the caller, in ascending id order. Roles: admin, sales and support (customers get 403). A sales user normally gets an empty page, since only support and admin users can be made assignees; tickets assigned to the account before its role was changed to sales still appear, because a role change does not clear assignments. No page parameter, sorting or search; customer is a zero-valued object and assigned_to is omitted on this endpoint (neither is loaded).
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Number of tickets to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=TicketListData,meta=utils.APIMeta} "One page of the caller's tickets"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - customers cannot have tickets assigned to them"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		logger.WithError(utils.LogError(err)).Error("Failed to list my tickets")
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
// @Description One ticket with its customer, assignee and comments (oldest first, each with its author). Roles: admin and sales may read any ticket; support only tickets assigned to them; a customer user only tickets of the customer record linked to their account, and internal notes are removed from comments for them. Access is checked after the lookup, so a missing id is 404 for every role.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "The ticket"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - the ticket is outside the caller's scope (support: not assigned to them; customer: not their customer's ticket or no linked customer record)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			utils.RespondNotFound(c, "Ticket not found")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to get ticket")
			utils.RespondInternalError(c)
		}
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
		// Customer is associated with this ticket - filter internal comments
		if len(ticket.Comments) > 0 {
			filtered := make([]models.TicketComment, 0, len(ticket.Comments))
			for _, comment := range ticket.Comments {
				if !comment.IsInternal {
					filtered = append(filtered, comment)
				}
			}
			ticket.Comments = filtered
		}
		// Allow access
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
// @Description Partially update a ticket: absent or empty fields are left unchanged, and nothing can be cleared. Roles: admin may update any ticket, support only tickets assigned to them; customers and sales get 403 before the id or body is read (sales is read-only on tickets). A closed ticket cannot be moved to another status (other fields of a closed ticket can still change). A new assignee must exist and hold the support or admin role. Known limitation: on a ticket that already has an assignee, a different assigned_to_id is validated but not stored; the previous assignee is kept and returned (a ticket without an assignee can be assigned). customer_id cannot be changed. The response carries the customer, assignee and comments (including internal notes).
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Param request body UpdateTicketRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "The updated ticket"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), a closed ticket would change status, or the new assignee does not exist or is not a support or admin user; VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - customers and sales cannot update tickets; support only tickets assigned to them"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

	// Sales users are read-only on tickets
	if currentUserRole == string(models.RoleSales) {
		utils.RespondForbidden(c, "Sales users cannot update tickets")
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			utils.RespondNotFound(c, "Ticket not found")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to get ticket for update")
			utils.RespondInternalError(c)
		}
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
		logger.WithError(utils.LogError(err)).Error("Failed to update ticket")
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
// @Description Soft-delete a ticket. Roles: admin only; the role check runs before the id is parsed, so non-admins always receive 403. Its comments are left in place but are no longer reachable through the API.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 204 "Ticket deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			utils.RespondNotFound(c, "Ticket not found")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to delete ticket")
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// ListComments godoc
// @Summary List comments for a ticket
// @Description Every comment of a ticket, oldest first, each with its author, as a bare array (no pagination). Roles: admin and sales on any ticket; support only on tickets assigned to them; a customer user only on tickets of their own customer record, and then without internal notes. A missing ticket is 404 for every role (checked before access).
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 200 {object} utils.APIResponse{data=[]models.TicketComment} "The comments; an empty array when none"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - the ticket is outside the caller's scope"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/{id}/comments [get]
func (h *TicketHandler) ListComments(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListComments")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid ticket ID")
		return
	}

	ticket, err := h.ticketService.GetByID(uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			utils.RespondNotFound(c, "Ticket not found")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to get ticket for listing comments")
			utils.RespondInternalError(c)
		}
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	includeInternal := true
	if currentUserRole == string(models.RoleCustomer) {
		if h.customerService == nil {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		customer, err := h.customerService.GetByUserID(currentUserID)
		if err != nil || customer == nil || ticket.CustomerID != customer.ID {
			utils.RespondForbidden(c, "Customers can only view their own tickets")
			return
		}
		includeInternal = false
	} else if currentUserRole == string(models.RoleSupport) {
		if ticket.AssignedToID == nil || *ticket.AssignedToID != currentUserID {
			utils.RespondForbidden(c, "You can only view comments on tickets assigned to you")
			return
		}
	}

	comments, err := h.ticketService.ListComments(uint(id), includeInternal)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list ticket comments")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, comments)
	utils.RespondSuccess(c, http.StatusOK, comments)
}

// CreateComment godoc
// @Summary Create a comment on a ticket
// @Description Add a comment or, with is_internal true, an internal note. Roles: admin on any ticket, support only on tickets assigned to them; customers and sales get 403 before the id is read. The order of checks is role, id, ticket lookup, assignment, body. The text is taken from body, comment or content (the first that is not blank after trimming) and stored trimmed; the caller becomes the author. No notification or email is sent.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Param request body CreateTicketCommentRequest true "Comment to add"
// @Success 201 {object} utils.APIResponse{data=models.TicketComment} "The new comment with its author"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), the body is not valid JSON, or body, comment and content are all blank"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - only admin and the assigned support user can comment"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/{id}/comments [post]
func (h *TicketHandler) CreateComment(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.CreateComment")

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	if currentUserRole == string(models.RoleCustomer) {
		utils.RespondForbidden(c, "Customers cannot add comments")
		return
	}
	if currentUserRole == string(models.RoleSales) {
		utils.RespondForbidden(c, "Sales users cannot add comments")
		return
	}
	if currentUserRole != string(models.RoleAdmin) && currentUserRole != string(models.RoleSupport) {
		utils.RespondForbidden(c, "Only support and admin users can add comments")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid ticket ID")
		return
	}

	ticket, err := h.ticketService.GetByID(uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Ticket not found")
			utils.RespondNotFound(c, "Ticket not found")
		} else {
			logger.WithError(utils.LogError(err)).Error("Failed to get ticket for creating comment")
			utils.RespondInternalError(c)
		}
		return
	}

	if currentUserRole == string(models.RoleSupport) &&
		(ticket.AssignedToID == nil || *ticket.AssignedToID != currentUserID) {
		utils.RespondForbidden(c, "You can only comment on tickets assigned to you")
		return
	}

	var req CreateTicketCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	body := req.GetBody()
	if body == "" {
		utils.RespondBadRequest(c, "Comment body is required")
		return
	}

	comment := &models.TicketComment{
		TicketID:     uint(id),
		AuthorUserID: currentUserID,
		Body:         body,
		IsInternal:   req.IsInternal,
	}

	if err := h.ticketService.CreateComment(comment); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create comment")
		if apperrors.IsNotFound(err) {
			utils.RespondNotFound(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, comment)
	utils.RespondSuccess(c, http.StatusCreated, comment)
}
