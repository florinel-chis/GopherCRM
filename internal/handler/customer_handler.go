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

	allowedSortColumns := map[string]bool{
		"created_at": true,
		"updated_at": true,
		"first_name": true,
		"last_name":  true,
		"email":      true,
		"company":    true,
	}

	if !allowedSortColumns[sortBy] {
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