package handler

import (
	"net/http"
	"strconv"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type LeadHandler struct {
	leadService service.LeadService
}

func NewLeadHandler(leadService service.LeadService) *LeadHandler {
	return &LeadHandler{leadService: leadService}
}

type CreateLeadRequest struct {
	FirstName string           `json:"first_name" binding:"required"`
	LastName  string           `json:"last_name" binding:"required"`
	Email     string           `json:"email" binding:"required,email"`
	Phone     string           `json:"phone,omitempty"`
	Company   string           `json:"company,omitempty"`
	Position  string           `json:"position,omitempty"`
	Source    string           `json:"source,omitempty"`
	Status    models.LeadStatus `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted"`
	Notes     string           `json:"notes,omitempty"`
	OwnerID   *uint            `json:"owner_id,omitempty"`
}

type UpdateLeadRequest struct {
	FirstName string           `json:"first_name,omitempty"`
	LastName  string           `json:"last_name,omitempty"`
	Email     string           `json:"email,omitempty" binding:"omitempty,email"`
	Phone     string           `json:"phone,omitempty"`
	Company   string           `json:"company,omitempty"`
	Position  string           `json:"position,omitempty"`
	Source    string           `json:"source,omitempty"`
	Status    models.LeadStatus `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted"`
	Notes     string           `json:"notes,omitempty"`
	OwnerID   *uint            `json:"owner_id,omitempty"`
}

type ConvertLeadRequest struct {
	CompanyName string `json:"company_name,omitempty"`
	Website     string `json:"website,omitempty"`
	Address     string `json:"address,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

func (h *LeadHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Create")
	
	var req CreateLeadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	lead := &models.Lead{
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Email:     req.Email,
		Phone:     req.Phone,
		Company:   req.Company,
		Position:  req.Position,
		Source:    req.Source,
		Status:    req.Status,
		Notes:     req.Notes,
	}

	// Set owner - if not specified, assign to current user (for sales), or require for admin
	if req.OwnerID != nil {
		// Only admins can assign leads to other users
		if currentUserRole != string(models.RoleAdmin) && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign leads to yourself")
			return
		}
		lead.OwnerID = *req.OwnerID
	} else {
		// Default to current user for sales, require explicit assignment for admin
		if currentUserRole == string(models.RoleAdmin) {
			utils.RespondBadRequest(c, "Owner ID is required for admin users")
			return
		}
		lead.OwnerID = currentUserID
	}

	if err := h.leadService.Create(lead); err != nil {
		logger.WithError(err).Error("Failed to create lead")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, lead)
	utils.RespondSuccess(c, http.StatusCreated, lead)
}

func (h *LeadHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.List")
	
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	
	if limit > 100 {
		limit = 100
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	var leads []models.Lead
	var total int64
	var err error

	// Role-based filtering: sales users can only see their own leads
	if currentUserRole == string(models.RoleSales) {
		// For sales users, only show their own leads
		leads, err = h.leadService.GetByOwner(currentUserID, offset, limit)
		// Note: We'll need to add a CountByOwner method for accurate pagination
		total = int64(len(leads)) // Temporary approximation
	} else {
		// Admin users can see all leads
		leads, total, err = h.leadService.List(offset, limit)
	}

	if err != nil {
		logger.WithError(err).Error("Failed to list leads")
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

	responseData := gin.H{"leads": leads, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

func (h *LeadHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Get")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid lead ID")
		return
	}

	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Lead not found")
		utils.RespondNotFound(c, "Lead not found")
		return
	}

	// Permission check: sales users can only view their own leads
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	if currentUserRole == string(models.RoleSales) && lead.OwnerID != currentUserID {
		utils.RespondForbidden(c, "You can only view your own leads")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, lead)
	utils.RespondSuccess(c, http.StatusOK, lead)
}

func (h *LeadHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Update")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid lead ID")
		return
	}

	var req UpdateLeadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Check if lead exists and user has permission
	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Lead not found")
		utils.RespondNotFound(c, "Lead not found")
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Permission check: sales users can only update their own leads
	if currentUserRole == string(models.RoleSales) && lead.OwnerID != currentUserID {
		utils.RespondForbidden(c, "You can only update your own leads")
		return
	}

	// Build updates map
	updates := make(map[string]interface{})
	if req.FirstName != "" {
		updates["first_name"] = req.FirstName
	}
	if req.LastName != "" {
		updates["last_name"] = req.LastName
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.Phone != "" {
		updates["phone"] = req.Phone
	}
	if req.Company != "" {
		updates["company"] = req.Company
	}
	if req.Position != "" {
		updates["position"] = req.Position
	}
	if req.Source != "" {
		updates["source"] = req.Source
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	if req.Notes != "" {
		updates["notes"] = req.Notes
	}
	
	// Only admins can reassign leads
	if req.OwnerID != nil {
		if currentUserRole != string(models.RoleAdmin) {
			utils.RespondForbidden(c, "Only administrators can reassign leads")
			return
		}
		updates["owner_id"] = *req.OwnerID
	}

	updatedLead, err := h.leadService.Update(uint(id), updates)
	if err != nil {
		logger.WithError(err).Error("Failed to update lead")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, updatedLead)
	utils.RespondSuccess(c, http.StatusOK, updatedLead)
}

func (h *LeadHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Delete")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid lead ID")
		return
	}

	// Check if lead exists and user has permission
	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Lead not found")
		utils.RespondNotFound(c, "Lead not found")
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Permission check: only admins or lead owners can delete
	if currentUserRole != string(models.RoleAdmin) && lead.OwnerID != currentUserID {
		utils.RespondForbidden(c, "You can only delete your own leads")
		return
	}

	if err := h.leadService.Delete(uint(id)); err != nil {
		logger.WithError(err).Error("Failed to delete lead")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

func (h *LeadHandler) ConvertToCustomer(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.ConvertToCustomer")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid lead ID")
		return
	}

	var req ConvertLeadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Check if lead exists and user has permission
	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Lead not found")
		utils.RespondNotFound(c, "Lead not found")
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	// Permission check: only admins or lead owners can convert
	if currentUserRole != string(models.RoleAdmin) && lead.OwnerID != currentUserID {
		utils.RespondForbidden(c, "You can only convert your own leads")
		return
	}

	// Create customer data from request and lead
	customerData := &models.Customer{
		FirstName: lead.FirstName,
		LastName:  lead.LastName,
		Email:     lead.Email,
		Phone:     lead.Phone,
		Company:   req.CompanyName,
		Address:   req.Address,
		Notes:     req.Notes,
	}

	customer, err := h.leadService.ConvertToCustomer(uint(id), customerData)
	if err != nil {
		logger.WithError(err).Error("Failed to convert lead")
		if err.Error() == "lead already converted" {
			utils.RespondBadRequest(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}