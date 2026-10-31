package handler

import (
	"encoding/csv"
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

type CustomerHandler struct {
	customerService service.CustomerService
}

func NewCustomerHandler(customerService service.CustomerService) *CustomerHandler {
	return &CustomerHandler{customerService: customerService}
}

// @Description Body of POST /customers. Text fields other than the names and email are optional free text.
type CreateCustomerRequest struct {
	// Optional web site: an absolute http or https URL with a host, at most 255
	// characters after trimming.
	Website string `json:"website,omitempty" maxLength:"255" example:"https://acme.example"`
	// Given name; required, non-empty.
	FirstName string `json:"first_name" binding:"required" example:"Ana"`
	// Family name; required, non-empty.
	LastName string `json:"last_name" binding:"required" example:"Popescu"`
	// Email address; required, valid, and not used by any customer, erased
	// ones included (409).
	Email string `json:"email" binding:"required,email" format:"email" example:"ana.popescu@example.com"`
	// Phone number, free text.
	Phone string `json:"phone,omitempty" example:"+40 721 000 000"`
	// Free-text company name; independent of company_id.
	Company string `json:"company,omitempty" example:"Acme SRL"`
	// Job title.
	Position string `json:"position,omitempty" example:"Purchasing manager"`
	// Street address.
	Address string `json:"address,omitempty" example:"Strada Exemplu 1"`
	// City.
	City string `json:"city,omitempty" example:"Cluj-Napoca"`
	// State, county or region.
	State string `json:"state,omitempty" example:"Cluj"`
	// Country, free text.
	Country string `json:"country,omitempty" example:"Romania"`
	// Postal code.
	PostalCode string `json:"postal_code,omitempty" example:"400000"`
	// Free-text notes.
	Notes string `json:"notes,omitempty" example:"Met at the trade fair."`
	// CompanyID links the customer to a company record; 0 or absent means
	// none. It is independent of the free-text Company field above.

	// Optional link to a live company record; 0 or absent means no link. An id
	// matching no live company is 400 INVALID_REFERENCE.
	CompanyID *uint `json:"company_id,omitempty" example:"3"`
}

// @Description Body of PUT /customers/{id}. Every field is optional. A text field that is omitted or empty leaves the stored value unchanged (it cannot be cleared this way), except website, where an empty string clears it.
type UpdateCustomerRequest struct {
	// Website: absent leaves the value alone; an empty string clears it.

	// Web site: omitted or null leaves it unchanged, "" clears it, anything
	// else must be an absolute http or https URL with a host, at most 255
	// characters after trimming.
	Website *string `json:"website,omitempty" maxLength:"255" example:"https://acme.example"`
	// New given name; empty leaves it unchanged.
	FirstName string `json:"first_name,omitempty" example:"Ana"`
	// New family name; empty leaves it unchanged.
	LastName string `json:"last_name,omitempty" example:"Popescu"`
	// New email address; must be valid and not used by another customer (409).
	// Empty leaves it unchanged.
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"ana.popescu@example.com"`
	// New phone number; empty leaves it unchanged.
	Phone string `json:"phone,omitempty" example:"+40 721 000 000"`
	// New free-text company name; empty leaves it unchanged.
	Company string `json:"company,omitempty" example:"Acme SRL"`
	// New job title; empty leaves it unchanged.
	Position string `json:"position,omitempty" example:"Purchasing manager"`
	// New street address; empty leaves it unchanged.
	Address string `json:"address,omitempty" example:"Strada Exemplu 1"`
	// New city; empty leaves it unchanged.
	City string `json:"city,omitempty" example:"Cluj-Napoca"`
	// New state, county or region; empty leaves it unchanged.
	State string `json:"state,omitempty" example:"Cluj"`
	// New country; empty leaves it unchanged.
	Country string `json:"country,omitempty" example:"Romania"`
	// New postal code; empty leaves it unchanged.
	PostalCode string `json:"postal_code,omitempty" example:"400000"`
	// New notes, replacing the old ones; empty leaves them unchanged.
	Notes string `json:"notes,omitempty" example:"Prefers email contact."`
	// CompanyID: absent leaves the link alone, 0 clears it, any other value
	// must name a live company.

	// Company link: omitted or null leaves it unchanged, 0 clears it, any
	// other value must name a live company (400 INVALID_REFERENCE otherwise).
	CompanyID *uint `json:"company_id,omitempty" example:"3"`
}

// AssignCustomerRequest carries the staff account a customer is being handed to.
//
// @Description Body of POST /customers/{id}/assign.
type AssignCustomerRequest struct {
	// ID of the new account manager: an existing, active admin or sales user.
	// Required and non-zero.
	UserID uint `json:"user_id" binding:"required" minimum:"1" example:"7"`
}

// CustomerListResponse documents the data payload of GET /customers for the
// OpenAPI spec; the handler builds it as a gin.H.
//
// @Description A page of customers.
type CustomerListResponse struct {
	// The customers of this page, never null.
	Customers []models.Customer `json:"customers"`
	// Total number of customers matching the query, across all pages (also
	// in meta.total, where it is omitted when 0).
	Total int64 `json:"total" example:"57"`
}

// customerExportColumns is the CSV header, and the order every data row is
// written in. It is the file's contract with whatever spreadsheet or import
// script consumes the download, so columns are appended, never reordered or
// renamed in place.
var customerExportColumns = []string{
	"id", "first_name", "last_name", "email", "phone", "company",
	"address", "notes", "assigned_to_id", "created_at", "updated_at",
	"website",
}

// Create godoc
// @Summary Create a customer
// @Description Admin and sales (route guard, repeated in the handler). Creates a customer without an account manager or login link. website is trimmed and must be empty or an absolute http or https URL with a host, at most 255 characters. company_id optionally links a company record, independently of the free-text company field; 0 or absent means no link. No mail is sent and no automation event is recorded.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateCustomerRequest true "New customer"
// @Success 201 {object} utils.APIResponse{data=models.Customer} "Customer created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: first_name, last_name or email missing, or email invalid (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), or an invalid website (the message says why). INVALID_REFERENCE: company_id names no live company"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is neither admin nor sales (Insufficient permissions)"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: a customer already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customer could not be stored"
// @Router /customers [post]
func (h *CustomerHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Create")

	currentUserRole := c.GetString("user_role")

	// Only admin and sales users can create customers
	if currentUserRole != string(models.RoleAdmin) && currentUserRole != string(models.RoleSales) {
		utils.RespondForbidden(c, "Insufficient permissions to create customers")
		return
	}

	var req CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	customer := &models.Customer{
		Website:    req.Website,
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		Email:      req.Email,
		Phone:      req.Phone,
		Company:    req.Company,
		Position:   req.Position,
		Address:    req.Address,
		City:       req.City,
		State:      req.State,
		Country:    req.Country,
		PostalCode: req.PostalCode,
		Notes:      req.Notes,
	}
	if req.CompanyID != nil && *req.CompanyID != 0 {
		customer.CompanyID = req.CompanyID
	}

	if err := h.customerService.Create(customer); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create customer")
		switch {
		case errors.Is(err, apperrors.ErrValidation):
			utils.RespondBadRequest(c, err.Error())
		case errors.Is(err, apperrors.ErrDuplicateEmail):
			utils.RespondConflict(c, "customer with this email already exists")
		case errors.Is(err, apperrors.ErrCompanyNotFound):
			// A bad company id is a bad reference in the body, not a missing
			// resource at the requested path, so it is a 400 like label_ids.
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
		default:
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, customer)
	utils.RespondSuccess(c, http.StatusCreated, customer)
}

// List godoc
// @Summary List customers
// @Description Admin, sales and support (checked in the handler; customer gets 403). All three roles see the same unfiltered set of live customers; there is no ownership narrowing. data holds customers and total; meta repeats the pagination (page, per_page, total, total_pages, each omitted when 0). Without sort_by the rows come in ascending id order; with an allowed sort_by they are ordered by that column, then id, in the requested direction. search matches a substring (SQL LIKE) of first_name, last_name, email, company, phone or notes and combines with the sort. The embedded user (the customer's login) is loaded only for the default listing without search and sort_by.
// @Description Pagination: limit defaults to 20 and is capped at 100 (a missing, non-numeric or non-positive value gives 20); offset defaults to 0; page, when a positive integer, overrides offset with (page - 1) * limit. An unknown sort_by is ignored rather than rejected.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; a positive value overrides offset"
// @Param offset query int false "Number of rows to skip, ignored when page is given" default(0)
// @Param limit query int false "Page size; capped at 100, non-positive or invalid values give 20" default(20)
// @Param sort_by query string false "Sort column; any other value is ignored (ascending id order)" Enums(created_at, updated_at, first_name, last_name, email, company)
// @Param sort_order query string false "Sort direction; any other value falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Substring matched against first_name, last_name, email, company, phone and notes"
// @Success 200 {object} utils.APIResponse{data=CustomerListResponse,meta=utils.APIMeta} "A page of customers"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions to list customers)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customers could not be read"
// @Router /customers [get]
func (h *CustomerHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.List")

	currentUserRole := c.GetString("user_role")

	// Only admin, sales, and support users can list customers
	if currentUserRole != string(models.RoleAdmin) &&
		currentUserRole != string(models.RoleSales) &&
		currentUserRole != string(models.RoleSupport) {
		utils.RespondForbidden(c, "Insufficient permissions to list customers")
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

	if !customerSortColumns[sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	search := c.Query("search")

	var customers []models.Customer
	var total int64
	var err error

	if search != "" {
		customers, total, err = h.customerService.Search(search, offset, limit, sortBy, sortOrder)
	} else if sortBy != "" {
		customers, total, err = h.customerService.ListSorted(offset, limit, sortBy, sortOrder)
	} else {
		customers, total, err = h.customerService.List(offset, limit)
	}

	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list customers")
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

	responseData := gin.H{"customers": customers, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

// Get godoc
// @Summary Get a customer
// @Description Admin, sales and support (checked in the handler) may read any live customer; the customer role gets 403, also for its own record. The response embeds the customer's login (user) and the linked company (company_record) when they exist.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "The customer"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions to view customers)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live customer with this id (erased customers included)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customer could not be read"
// @Router /customers/{id} [get]
func (h *CustomerHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Get")

	currentUserRole := c.GetString("user_role")

	// Only admin, sales, and support users can get customers
	if currentUserRole != string(models.RoleAdmin) &&
		currentUserRole != string(models.RoleSales) &&
		currentUserRole != string(models.RoleSupport) {
		utils.RespondForbidden(c, "Insufficient permissions to view customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	customer, err := h.customerService.GetByID(uint(id))
	if err != nil {
		// Only a genuine miss is a 404. A failed lookup answered with 404 tells
		// the client the customer does not exist, and it stops retrying data
		// that is still there.
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to retrieve customer")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

// Update godoc
// @Summary Update a customer
// @Description Admin and sales (checked in the handler; support and customer get 403). Any live customer may be updated; there is no ownership check. Text fields apply only when non-empty, so they cannot be cleared here; website is the exception: omitted or null leaves it unchanged, an empty string clears it, otherwise it is trimmed and must be an absolute http or https URL with a host, at most 255 characters. company_id: omitted or null leaves the link unchanged, 0 clears it, any other value must name a live company. The account manager is changed through POST /customers/{id}/assign. The whole row is written back.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param request body UpdateCustomerRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "The updated customer"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID, malformed JSON (Invalid request format), or an invalid website (the message says why). VALIDATION_ERROR: email is not an email address (details maps Email to a message). INVALID_REFERENCE: company_id names no live company"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is neither admin nor sales (Insufficient permissions to update customers)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live customer with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another customer already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customer could not be read or stored"
// @Router /customers/{id} [put]
func (h *CustomerHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Update")

	currentUserRole := c.GetString("user_role")

	// Only admin and sales users can update customers
	if currentUserRole != string(models.RoleAdmin) && currentUserRole != string(models.RoleSales) {
		utils.RespondForbidden(c, "Insufficient permissions to update customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	var req UpdateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Get existing customer. As in Get, only a genuine miss is a 404 - a failed
	// lookup must not be reported as a customer that does not exist.
	customer, err := h.customerService.GetByID(uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer for update")
		utils.RespondInternalError(c)
		return
	}

	// Apply updates
	if req.Website != nil {
		customer.Website = *req.Website
	}
	if req.FirstName != "" {
		customer.FirstName = req.FirstName
	}
	if req.LastName != "" {
		customer.LastName = req.LastName
	}
	if req.Email != "" {
		customer.Email = req.Email
	}
	if req.Phone != "" {
		customer.Phone = req.Phone
	}
	if req.Company != "" {
		customer.Company = req.Company
	}
	if req.Position != "" {
		customer.Position = req.Position
	}
	if req.Address != "" {
		customer.Address = req.Address
	}
	if req.City != "" {
		customer.City = req.City
	}
	if req.State != "" {
		customer.State = req.State
	}
	if req.Country != "" {
		customer.Country = req.Country
	}
	if req.PostalCode != "" {
		customer.PostalCode = req.PostalCode
	}
	if req.Notes != "" {
		customer.Notes = req.Notes
	}
	// The scalar counterpart of the label_ids rule: absent keeps, 0 clears.
	if req.CompanyID != nil {
		if *req.CompanyID == 0 {
			customer.CompanyID = nil
		} else {
			customer.CompanyID = req.CompanyID
		}
		customer.CompanyRecord = nil
	}

	if err := h.customerService.Update(customer); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to update customer")
		switch {
		case errors.Is(err, apperrors.ErrValidation):
			utils.RespondBadRequest(c, err.Error())
		case errors.Is(err, apperrors.ErrDuplicateEmail):
			utils.RespondConflict(c, "customer with this email already exists")
		case errors.Is(err, apperrors.ErrCompanyNotFound):
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
		default:
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

// Delete godoc
// @Summary Erase a customer (irreversible)
// @Description Admin only (route guard, repeated in the handler). A GDPR Article 17 erasure, not an archive: in one transaction every personal field (names, email, phone, company, website, position, postal address, notes) is overwritten in place, the email becoming an unrelated random address in the reserved .invalid domain, and the row is soft-deleted so references from tickets, tasks and deals still resolve. The same person is erased everywhere in that transaction: every lead converted into the customer (with its linked form submissions scrubbed) and, when user_id names a customer-role account, that login (credentials purged) and its other customer records. A staff account in user_id, the account manager (assigned_to_id), company_id and deals are left as they are. Any failure before the commit rolls the whole erasure back. A failed COMMIT is the exception: its outcome is ambiguous (the erasure may have been applied), so read the record again before retrying.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Success 204 "Erased; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live customer with this id (an already erased customer included)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the erasure failed; before the commit it was rolled back and nothing was erased, while a failed COMMIT is ambiguous (the erasure may have been applied)"
// @Router /customers/{id} [delete]
func (h *CustomerHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Delete")

	currentUserRole := c.GetString("user_role")

	// Only admin users can delete customers
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can delete customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	if err := h.customerService.Delete(uint(id)); err != nil {
		// Only the not-found sentinel means "there was nobody to erase". Any
		// other error is a failed erasure and must surface as a 500, so an
		// operator never records a completed erasure that did not happen.
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete customer")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// customerSortColumns is the sort allowlist shared by the list and export
// endpoints. It is the SQL-injection guard: anything outside it is dropped
// before it reaches the repository, never interpolated.
var customerSortColumns = map[string]bool{
	"created_at": true,
	"updated_at": true,
	"first_name": true,
	"last_name":  true,
	"email":      true,
	"company":    true,
}

// Export godoc
// @Summary Export customers as CSV (admin)
// @Description Admin only (route guard, repeated in the handler), deliberately narrower than the list, which sales and support can read: one request egresses the personal data of the whole customer base, so data minimisation keeps it from the roles that work one record at a time.
// @Description The success body is NOT the JSON envelope: it is the raw CSV, Content-Type text/csv; charset=utf-8 and Content-Disposition attachment; filename=customers-export.csv. Errors raised before the file starts (401, 403, 429, 500) still use the JSON envelope. Every row is read before the first byte is written, so a database failure is a JSON 500, never a truncated file.
// @Description Columns, in this order: id, first_name, last_name, email, phone, company, address, notes, assigned_to_id, created_at, updated_at, website. Timestamps are RFC 3339; an unassigned customer has an empty assigned_to_id. A text cell starting with =, @, a tab or a carriage return, or with + or - followed by anything other than a plain number, is prefixed with an apostrophe so spreadsheets do not run it as a formula.
// @Description Not paginated: every live customer matching search is included; erased customers never are. Without sort_by the rows come in ascending id order; with an allowed sort_by they are ordered by that column, then id, in the requested direction.
// @Tags customers
// @Produce text/csv
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param search query string false "Substring matched against first_name, last_name, email, company, phone and notes"
// @Param sort_by query string false "Sort column; any other value is ignored (ascending id order)" Enums(created_at, updated_at, first_name, last_name, email, company)
// @Param sort_order query string false "Sort direction, used only with sort_by; any other value falls back to asc" Enums(asc, desc) default(asc)
// @Success 200 {string} string "The CSV file (raw body, not the JSON envelope)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customers could not be read"
// @Router /customers/export [get]
func (h *CustomerHandler) Export(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Export")

	// Belt and braces with the route-level RequireRole(admin) guard. A bulk PII
	// download is the last endpoint that should depend on a single check.
	if c.GetString("user_role") != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can export customers")
		return
	}

	search := c.Query("search")
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	if !customerSortColumns[sortBy] {
		sortBy = ""
	}
	if sortBy == "" {
		// A direction without a column means nothing; do not pass one down.
		sortOrder = ""
	} else if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	// The rows are fetched in full before a single byte is written. Streaming
	// straight from the database would mean a failure halfway through arriving
	// after a 200 and half a file, which the client would save as a complete
	// export.
	customers, err := h.customerService.ExportAll(search, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to export customers")
		utils.RespondInternalError(c)
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=customers-export.csv")
	c.Status(http.StatusOK)

	writer := csv.NewWriter(c.Writer)
	if err := writer.Write(customerExportColumns); err != nil {
		// The status line is already out; all that is left is to record it.
		logger.WithError(utils.LogError(err)).Error("Failed to write CSV header")
		return
	}

	for i := range customers {
		if err := writer.Write(customerExportRecord(&customers[i])); err != nil {
			logger.WithError(utils.LogError(err)).WithField("customer_id", customers[i].ID).Error("Failed to write CSV row")
			return
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to flush CSV export")
		return
	}

	logger.WithField("count", len(customers)).Info("Customer export written")
}

// customerExportRecord renders one customer as a CSV row, in the column order
// pinned by customerExportColumns.
func customerExportRecord(customer *models.Customer) []string {
	assignedTo := ""
	if customer.AssignedToID != nil {
		assignedTo = strconv.FormatUint(uint64(*customer.AssignedToID), 10)
	}

	return []string{
		strconv.FormatUint(uint64(customer.ID), 10),
		csvSafeField(customer.FirstName),
		csvSafeField(customer.LastName),
		csvSafeField(customer.Email),
		csvSafeField(customer.Phone),
		csvSafeField(customer.Company),
		csvSafeField(customer.Address),
		csvSafeField(customer.Notes),
		assignedTo,
		customer.CreatedAt.Format(time.RFC3339),
		customer.UpdatedAt.Format(time.RFC3339),
		csvSafeField(customer.Website),
	}
}

// csvSafeField blunts spreadsheet formula injection. A cell that opens with =,
// @, a tab or a carriage return is executed as a formula by Excel and by
// LibreOffice, so text that arrived from outside (notes carried over from a
// converted lead, a company name) could run when an administrator opens the
// download. Prefixing an apostrophe makes the spreadsheet treat it as literal
// text.
//
// A leading + or - is treated separately: those overwhelmingly begin phone
// numbers and negative figures, and mangling every international dialling code
// would be a worse outcome than the risk. They are escaped only when what
// follows is not a plain number.
func csvSafeField(value string) string {
	if value == "" {
		return value
	}

	switch value[0] {
	case '=', '@', '\t', '\r':
		return "'" + value
	case '+', '-':
		if !isNumericLike(value[1:]) {
			return "'" + value
		}
	}
	return value
}

// isNumericLike reports whether the rest of a +/- prefixed value looks like a
// phone number or a plain figure rather than a formula.
func isNumericLike(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r == ' ', r == '-', r == '(', r == ')', r == '.', r == '+', r == ',':
		default:
			return false
		}
	}
	return true
}

// Assign godoc
// @Summary Assign a customer to an account manager
// @Description Admin and sales (route guard, repeated in the handler); sales may assign any customer, not only its own. Sets assigned_to_id to user_id and returns the customer (with its login and company embedded; assigned_to itself is not embedded). The target must exist, be active and hold the admin or sales role: support and customer accounts are refused, since customer ownership is a sales function and handing a customer account a book of other people's records would be a data-protection incident.
// @Description A missing customer or user is 404; an existing user that is deactivated or holds the wrong role is 400, because the request named a real account and was refused on its merits.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param request body AssignCustomerRequest true "The new account manager"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "The customer with its new account manager"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID, malformed JSON (Invalid request format), Cannot assign a customer to a deactivated user, or Customers can only be assigned to sales or admin users. VALIDATION_ERROR: user_id missing or 0 (details maps UserID to a message)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is neither admin nor sales (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: Customer not found, or User not found (no live user with user_id)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the customer could not be read or stored"
// @Router /customers/{id}/assign [post]
func (h *CustomerHandler) Assign(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.Assign")

	currentUserRole := c.GetString("user_role")

	// Only admin and sales users can assign customers
	if currentUserRole != string(models.RoleAdmin) && currentUserRole != string(models.RoleSales) {
		utils.RespondForbidden(c, "Insufficient permissions to assign customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	var req AssignCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	customer, err := h.customerService.Assign(uint(id), req.UserID)
	if err != nil {
		switch {
		// The assignee sentinel is checked first: it is a distinct miss from the
		// customer's, and the two answer with different messages.
		case errors.Is(err, apperrors.ErrAssigneeNotFound):
			logger.WithError(utils.LogError(err)).Warn("Assignee not found")
			utils.RespondNotFound(c, "User not found")
		case apperrors.IsNotFound(err):
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
		case errors.Is(err, apperrors.ErrInactiveUser):
			logger.WithError(utils.LogError(err)).Warn("Assignee is inactive")
			utils.RespondBadRequest(c, "Cannot assign a customer to a deactivated user")
		case errors.Is(err, apperrors.ErrInvalidCustomerAssignee):
			logger.WithError(utils.LogError(err)).Warn("Assignee holds a role that cannot own customers")
			utils.RespondBadRequest(c, "Customers can only be assigned to sales or admin users")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to assign customer")
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}
