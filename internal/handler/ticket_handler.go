package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
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
	// Due date and time as an RFC 3339 timestamp with any offset; stored in UTC, truncated to milliseconds. Omitted or null means no due date. Any other value (a date without a time, a number, a boolean, an object, an impossible date) is 400 INVALID_DUE_AT and nothing is written.
	DueAt json.RawMessage `json:"due_at,omitempty" swaggertype:"string" format:"date-time" example:"2026-11-20T17:00:00Z" extensions:"x-nullable"`
}

// @Description Body of PUT /tickets/{id}. Every field is optional; absent or empty fields are left unchanged, and a null due_at clears the due date.
type UpdateTicketRequest struct {
	// New title; absent or empty leaves it unchanged.
	Title string `json:"title,omitempty" example:"Printer offline after firmware update"`
	// New description; absent or empty leaves it unchanged.
	Description string `json:"description,omitempty" example:"The printer is visible again but prints blank pages."`
	// New status; absent or empty leaves it unchanged. A closed ticket cannot be moved to any other status (400); every other change is allowed.
	Status models.TicketStatus `json:"status,omitempty" binding:"omitempty,oneof=open in_progress resolved closed" example:"resolved"`
	// New priority; absent or empty leaves it unchanged.
	Priority models.TicketPriority `json:"priority,omitempty" binding:"omitempty,oneof=low medium high urgent" example:"urgent"`
	// New assignee, which must exist and hold the support or admin role (400 otherwise). Absent or null leaves it unchanged; the assignee cannot be removed.
	AssignedToID *uint `json:"assigned_to_id,omitempty" example:"5"`
	// Resolution text; absent or empty leaves it unchanged (it cannot be cleared).
	Resolution string `json:"resolution,omitempty" example:"Rolled the printer back to the previous firmware."`
	// New due date as an RFC 3339 timestamp with any offset; stored in UTC, truncated to milliseconds. Absent leaves it unchanged and null clears it. Any other value is 400 INVALID_DUE_AT and nothing is written, including the other fields of the body.
	DueAt json.RawMessage `json:"due_at,omitempty" swaggertype:"string" format:"date-time" example:"2026-11-20T17:00:00Z" extensions:"x-nullable"`
}

// The error code and message answered for a due_at that is not an RFC 3339
// timestamp; error.details.field names due_at.
const (
	invalidDueAtCode    = "INVALID_DUE_AT"
	invalidDueAtMessage = "due_at must be an RFC 3339 timestamp or null"
)

// parseDueAt decodes the raw due_at of a ticket body. present is false when
// the key was absent; a JSON null gives present with a nil value. A string
// holding an RFC 3339 timestamp is converted to UTC and truncated to
// milliseconds. Anything else is reported as not ok.
func parseDueAt(raw json.RawMessage) (present bool, value *time.Time, ok bool) {
	if raw == nil {
		return false, nil, true
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return true, nil, true
	}
	if !strings.HasPrefix(trimmed, `"`) {
		return true, nil, false
	}
	var text string
	if err := json.Unmarshal([]byte(trimmed), &text); err != nil {
		return true, nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return true, nil, false
	}
	parsed = parsed.UTC().Truncate(time.Millisecond)
	return true, &parsed, true
}

func respondInvalidDueAt(c *gin.Context) {
	utils.RespondError(c, http.StatusBadRequest, invalidDueAtCode, invalidDueAtMessage, gin.H{"field": "due_at"})
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
	// True to email this comment to the ticket's customer after it is saved. Only on a comment that is not an internal note (true together with is_internal is 400), and only for admin or the assigned support user. Defaults to false. The mail goes to the customer's current address and holds the ticket number, title and this comment's text; the 201 carries the outcome in delivery, whatever it is.
	EmailCustomer bool `json:"email_customer" example:"false"`
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
// @Description Create a support ticket. Needs tickets.create (the admin and support system roles hold it); without it the caller gets 403 before the body is read. The ticket always starts in status open, whatever the body says, and priority defaults to medium. When assigned_to_id is omitted the ticket is assigned to the caller; a given assignee must exist and hold assignment.ticket_assignee (the admin and support system roles hold it). The customer must exist. due_at is optional (an RFC 3339 timestamp, stored in UTC with millisecond precision); first_responded_at and overdue in the body are ignored, and a new ticket always has first_responded_at null. No notification or email is sent.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateTicketRequest true "Ticket to create"
// @Success 201 {object} utils.APIResponse{data=models.Ticket} "Ticket created; customer is a zero-valued object and assigned_to is omitted in this response"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; INVALID_DUE_AT (error.details.field is due_at) when due_at is neither null nor an RFC 3339 timestamp, nothing is created; BAD_REQUEST when the assignee does not exist or is not a support or admin user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.create ('Only support and admin users can create tickets', whatever the caller's role)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - the customer does not exist"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets [post]
func (h *TicketHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Create")

	currentUserID := c.GetUint("user_id")

	// Only support and admin users can create tickets
	if !hasPermission(c, permissions.TicketsCreate) {
		utils.RespondForbidden(c, "Only support and admin users can create tickets")
		return
	}

	var req CreateTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	_, dueAt, ok := parseDueAt(req.DueAt)
	if !ok {
		logger.Warn("Invalid due_at")
		respondInvalidDueAt(c)
		return
	}

	ticket := &models.Ticket{
		Title:        req.Title,
		Description:  req.Description,
		Priority:     req.Priority,
		CustomerID:   req.CustomerID,
		AssignedToID: req.AssignedToID,
		Status:       models.TicketStatusOpen,
		DueAt:        dueAt,
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
// @Description One page of tickets with their customer and assignee. Needs tickets.view: at scope all every ticket (the admin and sales system roles); at scope own only tickets assigned to the caller (the support system role; unassigned tickets and other people's are never listed), a scope applied in the query before offset and limit so the tickets, total and meta all reflect it and no parameter can widen it. A caller without tickets.view (a customer account, for one) gets 403, whatever the filters. Filters: status and priority each take exactly one value, matched case-sensitively; they combine with each other, with search and with sort_by/sort_order, are applied in the query before offset and limit (so tickets, total and meta reflect them), and at scope own only narrow the assigned tickets. An empty status or priority is ignored, a repeated parameter uses its first value, and an unknown or differently cased value is 400 VALIDATION_ERROR before anything is read. Precedence without a status or priority filter: at scope own, the assignee scope with optional search and sort; otherwise search (with optional sort), then sort_by, then the plain list. search is a substring match (SQL LIKE) on title, description and resolution. An unknown sort_by is ignored (ascending id) and any sort_order other than asc or desc falls back to asc. Pagination: page (1-based) overrides offset when greater than 0; limit defaults to 20 and is clamped to 100. Soft-deleted tickets are never listed; comments are not included.
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
// @Param status query string false "Only tickets in this status (exact, case-sensitive match); empty is ignored, an unknown value is 400" Enums(open, in_progress, resolved, closed)
// @Param priority query string false "Only tickets with this priority (exact, case-sensitive match); empty is ignored, an unknown value is 400" Enums(low, medium, high, urgent)
// @Success 200 {object} utils.APIResponse{data=TicketListData,meta=utils.APIMeta} "One page of tickets; meta carries page, per_page, total and total_pages"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - status or priority is not one of the listed values (error.details is keyed by the offending parameter name; the value is not echoed)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.view ('Customers cannot list all tickets', whatever the caller's role)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets [get]
func (h *TicketHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.List")

	// Customer users cannot list all tickets
	if !hasPermission(c, permissions.TicketsView) {
		utils.RespondForbidden(c, "Customers cannot list all tickets")
		return
	}

	// Optional status and priority filters (#218). c.Query takes the first
	// value of a repeated parameter, and an empty value counts as absent. The
	// match is exact and case-sensitive, and it is checked here, before the
	// query, because MySQL's default collation would compare case-insensitively.
	status := c.Query("status")
	priority := c.Query("priority")
	invalid := gin.H{}
	if status != "" && !models.TicketStatus(status).IsValid() {
		invalid["status"] = "must be one of open, in_progress, resolved, closed"
	}
	if priority != "" && !models.TicketPriority(priority).IsValid() {
		invalid["priority"] = "must be one of low, medium, high, urgent"
	}
	if len(invalid) > 0 {
		logger.WithField("invalid_parameters", len(invalid)).Warn("Invalid ticket list filter")
		utils.RespondValidationError(c, invalid)
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
	assignedOnly := hasPermissionOnOwnOnly(c, permissions.TicketsView)

	// Support users only ever see tickets assigned to them, as on the detail
	// route. The scope is part of the query, so the rows and the total both
	// reflect it before pagination, and no query parameter can widen it.
	// A status or priority filter goes through ListFiltered, with the same
	// assignee scope forced for support; without one the existing branches run
	// unchanged.
	if status != "" || priority != "" {
		filter := repository.TicketListFilter{Search: search, Status: status, Priority: priority}
		if assignedOnly {
			assigneeID := c.GetUint("user_id")
			filter.AssigneeID = &assigneeID
		}
		tickets, total, err = h.ticketService.ListFiltered(filter, offset, limit, sortBy, sortOrder)
	} else if assignedOnly {
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
// @Description One page of one customer's tickets, in ascending id order. Needs tickets.view or tickets.view_own_customer. At tickets.view scope all every one of the customer's tickets (the admin and sales system roles); at scope own any customer may be queried but only that customer's tickets assigned to the caller are returned (the support system role; applied in the query before offset and limit, so tickets, total and meta reflect it); a caller without tickets.view that holds tickets.view_own_customer (a customer account) may only query the customer record linked to its own account (403 otherwise). An unknown customer id simply yields an empty page. No page parameter, sorting or search; customer is a zero-valued object and assigned_to is omitted on this endpoint (neither is loaded).
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
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither tickets.view nor tickets.view_own_customer (Insufficient permissions); or a caller without tickets.view asked for another customer's tickets or has no linked customer record ('Customers can only view their own tickets')"
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
	if !hasPermission(c, permissions.TicketsView) {
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
	if hasPermissionOnOwnOnly(c, permissions.TicketsView) {
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
// @Description One page of the tickets assigned to the caller, in ascending id order. Needs tickets.view at any scope (the admin, sales and support system roles hold it; a customer account gets 403). A caller that cannot be made an assignee (one without assignment.ticket_assignee, such as the sales system role) normally gets an empty page; tickets assigned to the account before its role was changed still appear, because a role change does not clear assignments. No page parameter, sorting or search; customer is a zero-valued object and assigned_to is omitted on this endpoint (neither is loaded).
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Number of tickets to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=TicketListData,meta=utils.APIMeta} "One page of the caller's tickets"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.view ('Customers cannot have tickets assigned to them', whatever the caller's role)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/my [get]
func (h *TicketHandler) ListMyTickets(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.ListMyTickets")

	currentUserID := c.GetUint("user_id")

	// Only support and admin users can have tickets assigned
	if !hasPermission(c, permissions.TicketsView) {
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
// @Description One ticket with its customer, assignee and comments (oldest first, each with its author). Needs tickets.view or tickets.view_own_customer. At tickets.view scope all any ticket (the admin and sales system roles); at scope own only tickets assigned to the caller (the support system role); a caller without tickets.view that holds tickets.view_own_customer (a customer account) only tickets of the customer record linked to its account, and internal notes are removed from comments for it. Past the route guard access is checked after the lookup, so a missing id is 404 for every caller that holds one of the two keys.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "The ticket"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither tickets.view nor tickets.view_own_customer (Insufficient permissions); a caller at tickets.view scope own asked for a ticket not assigned to it ('You can only view tickets assigned to you'); a caller without tickets.view asked for a ticket of another customer or has no linked customer record ('Customers can only view their own tickets')"
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

	// Customer users can only view tickets for their associated customer record
	if !hasPermission(c, permissions.TicketsView) {
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
	if hasPermissionOnOwnOnly(c, permissions.TicketsView) &&
		(ticket.AssignedToID == nil || *ticket.AssignedToID != currentUserID) {
		utils.RespondForbidden(c, "You can only view tickets assigned to you")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, ticket)
	utils.RespondSuccess(c, http.StatusOK, ticket)
}

// Update godoc
// @Summary Update a ticket
// @Description Partially update a ticket: absent or empty fields are left unchanged, and nothing but due_at can be cleared. Needs tickets.update: at scope all any ticket (the admin system role), at scope own only tickets assigned to the caller (the support system role); a caller without it gets 403 before the id or body is read (the customer and sales system roles do not hold it, so sales is read-only on tickets). A closed ticket cannot be moved to another status (other fields of a closed ticket can still change). A new assignee must exist and hold assignment.ticket_assignee (the admin and support system roles hold it). A different assigned_to_id replaces the stored assignee, or sets one on an unassigned ticket, and the response names the new assignee; a caller at scope own who hands a ticket over can no longer read or update it. customer_id cannot be changed. due_at follows its own rule: absent leaves it unchanged, null clears it, an RFC 3339 timestamp sets it (stored in UTC with millisecond precision). first_responded_at and overdue in the body are ignored; first_responded_at is never changed by this route. The response carries the customer, assignee and comments (including internal notes). When the update changed at least one stored value of the ticket (title, description, status, priority, assigned_to_id, resolution or due_at), a ticket.updated webhook event (one delivery per active endpoint subscribed to it) is written in the same transaction; an update that sends only the stored values writes none.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Param request body UpdateTicketRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Ticket} "The updated ticket"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), a closed ticket would change status, or the new assignee does not exist or is not a support or admin user; INVALID_DUE_AT (error.details.field is due_at) when due_at is neither null nor an RFC 3339 timestamp, and then no field of the body is written; VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.update (a customer account gets 'Customers cannot update tickets', the sales system role 'Sales users cannot update tickets', every other caller 'Insufficient permissions'); or a caller at scope own asked for a ticket not assigned to it ('You can only update tickets assigned to you')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/{id} [put]
func (h *TicketHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Update")

	currentUserID := c.GetUint("user_id")

	// Customers cannot update tickets and sales users are read-only on them
	if !hasPermission(c, permissions.TicketsUpdate) {
		utils.RespondForbidden(c, refusalMessage(c, "Customers cannot update tickets",
			"Sales users cannot update tickets", "Insufficient permissions"))
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

	// due_at is decoded before anything is read or written: an invalid value
	// refuses the whole body.
	dueAtSent, dueAt, ok := parseDueAt(req.DueAt)
	if !ok {
		logger.Warn("Invalid due_at")
		respondInvalidDueAt(c)
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
	if hasPermissionOnOwnOnly(c, permissions.TicketsUpdate) &&
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
	// Absent keeps the stored due date; null clears it.
	if dueAtSent {
		ticket.DueAt = dueAt
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
// @Description Soft-delete a ticket. Needs tickets.delete (the admin system role holds it); the check runs before the id is parsed, so a caller without it always receives 403. Its comments are left in place but are no longer reachable through the API. The ticket's comment mail delivery records are hard-deleted in the same transaction.
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 204 "Ticket deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.delete ('Only administrators can delete tickets', whatever the caller's role)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/{id} [delete]
func (h *TicketHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.Delete")

	// Only admin users can delete tickets
	if !hasPermission(c, permissions.TicketsDelete) {
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
// @Description Every comment of a ticket, oldest first, each with its author, as a bare array (no pagination). Needs tickets.view or tickets.view_own_customer. At tickets.view scope all on any ticket (the admin and sales system roles); at scope own only on tickets assigned to the caller (the support system role); a caller without tickets.view that holds tickets.view_own_customer (a customer account) only on tickets of its own customer record, and then without internal notes. Past the route guard a missing ticket is 404 for every caller (checked before access).
// @Tags tickets
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Success 200 {object} utils.APIResponse{data=[]models.TicketComment} "The comments; an empty array when none"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither tickets.view nor tickets.view_own_customer (Insufficient permissions); a caller at tickets.view scope own asked for a ticket not assigned to it ('You can only view comments on tickets assigned to you'); a caller without tickets.view asked for a ticket of another customer or has no linked customer record ('Customers can only view their own tickets')"
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

	includeInternal := true
	if !hasPermission(c, permissions.TicketsView) {
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
	} else if hasPermissionOnOwnOnly(c, permissions.TicketsView) {
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
// @Description Add a comment or, with is_internal true, an internal note. Needs tickets.comment: at scope all on any ticket (the admin system role), at scope own only on tickets assigned to the caller (the support system role); a caller without it gets 403 before the id is read (the customer and sales system roles do not hold it). The order of checks is permission, id, ticket lookup, assignment, body. The text is taken from body, comment or content (the first that is not blank after trimming) and stored trimmed; the caller becomes the author. The first comment that is not an internal note sets the ticket's first_responded_at to its created_at, in the same transaction as the insert; later comments and internal notes leave it unchanged. Without email_customer (absent or false) no notification or email is sent and the response has no delivery key. With email_customer true (only on a comment that is not an internal note; with is_internal true it is 400 VALIDATION_ERROR and nothing is written), the comment, the first_responded_at stamp and a pending delivery record are written in one transaction; after the commit the ticket's customer is read live and one plaintext mail is sent to the customer's current address, with the subject Ticket T-000042 from followed by the product name (the general.company_name configuration value read at send time, or GopherCRM when it is unset, blank, unreadable or unusable in a header) and a body holding the ticket number, the title and this comment's text only. The delivery record is then stamped sent, failed (error_code mail_failed, timeout, invalid_recipient or lookup_failed), log_only (no SMTP_HOST, nothing delivered) or skipped (no_mailer, no_recipient, or customer_unavailable for an erased or missing customer) and returned as delivery in this response only; it holds ids, a status and an error code, never an address or content. The answer is 201 whatever the mail outcome, and there is no automatic retry.
// @Tags tickets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Ticket ID"
// @Param request body CreateTicketCommentRequest true "Comment to add"
// @Success 201 {object} utils.APIResponse{data=models.TicketComment} "The new comment with its author; with email_customer true it also carries delivery, the outcome of the mail"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), the body is not valid JSON, or body, comment and content are all blank; VALIDATION_ERROR (error.details.email_customer) when email_customer and is_internal are both true, nothing is written"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold tickets.comment (a customer account gets 'Customers cannot add comments', the sales system role 'Sales users cannot add comments', every other caller 'Only support and admin users can add comments'); or a caller at scope own asked for a ticket not assigned to it ('You can only comment on tickets assigned to you')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live ticket with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /tickets/{id}/comments [post]
func (h *TicketHandler) CreateComment(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "TicketHandler.CreateComment")

	currentUserID := c.GetUint("user_id")

	if !hasPermission(c, permissions.TicketsComment) {
		utils.RespondForbidden(c, refusalMessage(c, "Customers cannot add comments",
			"Sales users cannot add comments", "Only support and admin users can add comments"))
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

	if hasPermissionOnOwnOnly(c, permissions.TicketsComment) &&
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

	// email_customer is only for a reply the customer may read (#219).
	if req.EmailCustomer && req.IsInternal {
		logger.Warn("email_customer refused on an internal note")
		utils.RespondValidationError(c, gin.H{"email_customer": "cannot be true on an internal note"})
		return
	}

	comment := &models.TicketComment{
		TicketID:     uint(id),
		AuthorUserID: currentUserID,
		Body:         body,
		IsInternal:   req.IsInternal,
	}

	if req.EmailCustomer {
		err = h.ticketService.CreateCommentWithCustomerEmail(c.Request.Context(), comment)
	} else {
		err = h.ticketService.CreateComment(comment)
	}
	if err != nil {
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
