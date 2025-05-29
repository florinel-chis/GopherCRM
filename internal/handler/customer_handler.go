package handler

import (
	"net/http"
	"strconv"

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
		if err.Error() == "customer with this email already exists" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, customer)
	utils.RespondSuccess(c, http.StatusCreated, customer)
}

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

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	customers, total, err := h.customerService.List(offset, limit)
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
		logger.WithError(err).Warn("Customer not found")
		utils.RespondNotFound(c, "Customer not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

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

	// Get existing customer
	customer, err := h.customerService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Customer not found")
		utils.RespondNotFound(c, "Customer not found")
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
		if err.Error() == "customer with this email already exists" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}

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
		logger.WithError(err).Error("Failed to delete customer")
		utils.RespondNotFound(c, "Customer not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}