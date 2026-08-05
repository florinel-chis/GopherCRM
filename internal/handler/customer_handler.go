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

type CreateCustomerRequest struct {
	FirstName  string `json:"first_name" binding:"required"`
	LastName   string `json:"last_name" binding:"required"`
	Email      string `json:"email" binding:"required,email"`
	Phone      string `json:"phone,omitempty"`
	Company    string `json:"company,omitempty"`
	Position   string `json:"position,omitempty"`
	Address    string `json:"address,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	Country    string `json:"country,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

type UpdateCustomerRequest struct {
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	Email      string `json:"email,omitempty" binding:"omitempty,email"`
	Phone      string `json:"phone,omitempty"`
	Company    string `json:"company,omitempty"`
	Position   string `json:"position,omitempty"`
	Address    string `json:"address,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	Country    string `json:"country,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// AssignCustomerRequest carries the staff account a customer is being handed to.
type AssignCustomerRequest struct {
	UserID uint `json:"user_id" binding:"required"`
}

// customerExportColumns is the CSV header, and the order every data row is
// written in. It is the file's contract with whatever spreadsheet or import
// script consumes the download, so columns are appended, never reordered or
// renamed in place.
var customerExportColumns = []string{
	"id", "first_name", "last_name", "email", "phone", "company",
	"address", "notes", "assigned_to_id", "created_at", "updated_at",
}

// Create godoc
// @Summary Create a new customer
// @Description Create a new customer (admin and sales roles only)
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateCustomerRequest true "Customer creation request"
// @Success 201 {object} utils.APIResponse{data=models.Customer} "Customer created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or Sales role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "Customer with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	if err := h.customerService.Create(customer); err != nil {
		logger.WithError(err).Error("Failed to create customer")
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "customer with this email already exists")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, customer)
	utils.RespondSuccess(c, http.StatusCreated, customer)
}

// List godoc
// @Summary List customers
// @Description List customers with pagination, optional search and sorting. Admin, sales and support roles see the same unfiltered set; the customer role is rejected outright and has no view of its own record here.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param sort_by query string false "Sort column; ignored unless one of the allowed values" Enums(created_at, updated_at, first_name, last_name, email, company)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Free-text search across customer fields; takes precedence over sort_by"
// @Success 200 {object} utils.APIResponse{data=object{customers=[]models.Customer,total=integer},meta=utils.APIMeta} "Customers retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, Sales or Support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Error("Failed to list customers")
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
// @Summary Get a customer by ID
// @Description Retrieve a single customer. Admin, sales and support roles may read any customer; the customer role is rejected and cannot read its own record through this endpoint.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "Customer retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, Sales or Support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(err).Error("Failed to retrieve customer")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

// Update godoc
// @Summary Update a customer
// @Description Update a customer (admin and sales roles only). Only non-empty fields in the request are applied; empty fields leave the stored value unchanged.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param request body UpdateCustomerRequest true "Customer update request"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "Customer updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID or request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or Sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "Customer with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(err).Error("Failed to look up customer for update")
		utils.RespondInternalError(c)
		return
	}

	// Apply updates
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

	if err := h.customerService.Update(customer); err != nil {
		logger.WithError(err).Error("Failed to update customer")
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "customer with this email already exists")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

// Delete godoc
// @Summary Erase a customer
// @Description Irreversibly erase a customer (admin role only). This is a GDPR Article 17 erasure, not a reversible archive: every personal field is overwritten in place — the email is replaced by an unrelated random address in the reserved .invalid domain — and the row is then soft-deleted so foreign keys from tickets and tasks still resolve. If the customer originated from a converted lead, that lead is erased in the same transaction. Use deactivation instead when the data must be recoverable.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Erasure failed"
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
			logger.WithError(err).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		logger.WithError(err).Error("Failed to delete customer")
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
// @Summary Export customers as CSV
// @Description Download every customer matching the optional filters as a CSV file. ADMIN ROLE ONLY, and deliberately narrower than the list endpoint, which sales and support can also read: a single request here egresses the personal data of the entire customer base in a form that leaves the application, so the GDPR data-minimisation principle puts it out of reach of the roles that only need to work one record at a time.
// @Description
// @Description The response is NOT the utils.APIResponse envelope. It is the raw CSV body — Content-Type text/csv; charset=utf-8, Content-Disposition attachment; filename=customers-export.csv — because the client saves it as a file. Errors before the file starts are still reported through the ordinary JSON envelope.
// @Description
// @Description Columns, in order: id, first_name, last_name, email, phone, company, address, notes, assigned_to_id, created_at, updated_at. Timestamps are RFC3339. An unassigned customer exports an empty assigned_to_id cell. Fields that a spreadsheet would treat as a formula are prefixed with an apostrophe.
// @Description
// @Description The export is not paginated: every matching row is included. Soft-deleted (erased) customers are excluded.
// @Tags customers
// @Produce text/csv
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param search query string false "Free-text search across first name, last name, email, company, phone and notes"
// @Param sort_by query string false "Sort column; ignored unless one of the allowed values" Enums(created_at, updated_at, first_name, last_name, email, company)
// @Param sort_order query string false "Sort direction; ignored unless sort_by is supplied" Enums(asc, desc) default(asc)
// @Success 200 {string} string "CSV file of customers (raw body, not the API envelope)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Error("Failed to export customers")
		utils.RespondInternalError(c)
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=customers-export.csv")
	c.Status(http.StatusOK)

	writer := csv.NewWriter(c.Writer)
	if err := writer.Write(customerExportColumns); err != nil {
		// The status line is already out; all that is left is to record it.
		logger.WithError(err).Error("Failed to write CSV header")
		return
	}

	for i := range customers {
		if err := writer.Write(customerExportRecord(&customers[i])); err != nil {
			logger.WithError(err).WithField("customer_id", customers[i].ID).Error("Failed to write CSV row")
			return
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		logger.WithError(err).Error("Failed to flush CSV export")
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
// @Summary Assign a customer to a user
// @Description Set the staff account that owns this customer relationship (admin and sales roles only; the route is guarded by middleware.RequireRole(admin, sales) and the handler repeats the check).
// @Description
// @Description The target account must exist, must be active, and must hold the admin or sales role: customer ownership is a sales function, so support accounts are rejected the same way a support-only ticket assignee is, and a customer-role account is rejected outright — handing it a book of other people's records would be a data-protection incident.
// @Description
// @Description A missing customer or a missing user is 404. A user that exists but is deactivated or holds the wrong role is 400, because the request identified a real account and was refused on its merits.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param request body AssignCustomerRequest true "Assignment request"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "Customer assigned successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID or request data, or the target user is deactivated or holds a role that cannot own customers"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or Sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer or user not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("Assignee not found")
			utils.RespondNotFound(c, "User not found")
		case apperrors.IsNotFound(err):
			logger.WithError(err).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
		case errors.Is(err, apperrors.ErrInactiveUser):
			logger.WithError(err).Warn("Assignee is inactive")
			utils.RespondBadRequest(c, "Cannot assign a customer to a deactivated user")
		case errors.Is(err, apperrors.ErrInvalidCustomerAssignee):
			logger.WithError(err).Warn("Assignee holds a role that cannot own customers")
			utils.RespondBadRequest(c, "Customers can only be assigned to sales or admin users")
		default:
			logger.WithError(err).Error("Failed to assign customer")
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}
