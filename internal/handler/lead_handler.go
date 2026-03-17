package handler

import (
	"net/http"
	"strconv"
	"time"

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
	FirstName      string                    `json:"first_name" binding:"required"`
	LastName       string                    `json:"last_name" binding:"required"`
	Email          string                    `json:"email" binding:"required,email"`
	Phone          string                    `json:"phone,omitempty"`
	Company        string                    `json:"company,omitempty"`
	Position       string                    `json:"position,omitempty"`
	Source         string                    `json:"source,omitempty"`
	Status         models.LeadStatus         `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted"`
	Classification models.LeadClassification `json:"classification,omitempty" binding:"omitempty,oneof=unclassified test spam lead hot_lead"`
	ExternalID     string                    `json:"external_id,omitempty"`
	Notes          string                    `json:"notes,omitempty"`
	OwnerID        *uint                     `json:"owner_id,omitempty"`
	CreatedAt      *string                   `json:"created_at,omitempty"` // ISO8601 timestamp for import
}

type UpdateLeadRequest struct {
	FirstName      string                   `json:"first_name,omitempty"`
	LastName       string                   `json:"last_name,omitempty"`
	Email          string                   `json:"email,omitempty" binding:"omitempty,email"`
	Phone          string                   `json:"phone,omitempty"`
	Company        string                   `json:"company,omitempty"`
	Position       string                   `json:"position,omitempty"`
	Source         string                   `json:"source,omitempty"`
	Status         models.LeadStatus        `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted"`
	Classification models.LeadClassification `json:"classification,omitempty" binding:"omitempty,oneof=unclassified test spam lead hot_lead"`
	ExternalID     string                   `json:"external_id,omitempty"`
	Notes          string                   `json:"notes,omitempty"`
	OwnerID        *uint                    `json:"owner_id,omitempty"`
}

type ConvertLeadRequest struct {
	CompanyName string `json:"company_name,omitempty"`
	Website     string `json:"website,omitempty"`
	Address     string `json:"address,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// Create godoc
// @Summary Create a new lead
// @Description Create a new lead (sales and admin roles only)
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body CreateLeadRequest true "Lead creation request"
// @Success 201 {object} utils.APIResponse{data=models.Lead} "Lead created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Sales or Admin role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /leads [post]
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
		FirstName:      req.FirstName,
		LastName:       req.LastName,
		Email:          req.Email,
		Phone:          req.Phone,
		Company:        req.Company,
		Position:       req.Position,
		Source:         req.Source,
		Status:         req.Status,
		Classification: req.Classification,
		ExternalID:     req.ExternalID,
		Notes:          req.Notes,
	}

	// Set custom created_at for imports (preserves original submission date)
	if req.CreatedAt != nil && *req.CreatedAt != "" {
		// Try multiple timestamp formats
		formats := []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		var parsedTime time.Time
		var parseErr error
		for _, format := range formats {
			parsedTime, parseErr = time.Parse(format, *req.CreatedAt)
			if parseErr == nil {
				lead.CreatedAt = parsedTime
				break
			}
		}
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

	// Support both page-based and offset-based pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	classification := c.Query("classification")

	if limit > 100 {
		limit = 100
	}

	// Convert page to offset if page is provided
	if page > 0 {
		offset = (page - 1) * limit
	}

	// Parse and validate sort parameters
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	allowedSortColumns := map[string]bool{
		"created_at":     true,
		"updated_at":     true,
		"first_name":     true,
		"last_name":      true,
		"email":          true,
		"company":        true,
		"status":         true,
		"classification": true,
		"source":         true,
	}

	if !allowedSortColumns[sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	search := c.Query("search")

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
	} else if search != "" {
		// Search across lead fields
		leads, total, err = h.leadService.Search(search, offset, limit, sortBy, sortOrder)
	} else if classification != "" {
		// Filter by classification
		leads, total, err = h.leadService.GetByClassification(models.LeadClassification(classification), offset, limit)
	} else if sortBy != "" {
		// Sorted list
		leads, total, err = h.leadService.ListSorted(offset, limit, sortBy, sortOrder)
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
	if req.Classification != "" {
		updates["classification"] = req.Classification
	}
	if req.ExternalID != "" {
		updates["external_id"] = req.ExternalID
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

// ConvertToCustomer godoc
// @Summary Convert lead to customer
// @Description Convert a lead to a customer with additional customer information
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Lead ID"
// @Param request body ConvertLeadRequest true "Lead conversion request with customer details"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "Lead converted to customer successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Sales or Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Lead not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "Lead already converted"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /leads/{id}/convert [post]
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