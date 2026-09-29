package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type CompanyHandler struct {
	companyService service.CompanyService
}

func NewCompanyHandler(companyService service.CompanyService) *CompanyHandler {
	return &CompanyHandler{companyService: companyService}
}

// CompanyRequest is the body of POST /companies and PUT /companies/:id. PUT is
// a full replacement of the text fields: every field absent from the body is
// stored empty, which is how a domain or a note gets cleared. owner_id is the
// exception: nil leaves the owner alone, 0 clears it, any other value sets it.
type CompanyRequest struct {
	Name          string `json:"name" binding:"required"`
	Domain        string `json:"domain,omitempty"`
	Website       string `json:"website,omitempty"`
	Industry      string `json:"industry,omitempty"`
	EmployeeRange string `json:"employee_range,omitempty" binding:"omitempty,oneof=1-10 11-50 51-200 201-500 501-1000 1000+"`
	Phone         string `json:"phone,omitempty"`
	Address       string `json:"address,omitempty"`
	City          string `json:"city,omitempty"`
	State         string `json:"state,omitempty"`
	Country       string `json:"country,omitempty"`
	PostalCode    string `json:"postal_code,omitempty"`
	Notes         string `json:"notes,omitempty"`
	OwnerID       *uint  `json:"owner_id,omitempty"`
}

// companyNotesTooLongMessage answers notes that would not fit companies.notes.
var companyNotesTooLongMessage = fmt.Sprintf("Notes are too long (at most %d bytes)", models.CompanyNotesMaxBytes)

// lengthError names the first field whose value would not fit its column.
// The limits are the model's constants, which a test holds to the declared
// column widths, so the database never has to reject a value itself.
func (req *CompanyRequest) lengthError() string {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"name", req.Name, models.CompanyNameMaxLength},
		{"domain", req.Domain, models.CompanyDomainMaxLength},
		{"website", req.Website, models.CompanyWebsiteMaxLength},
		{"industry", req.Industry, models.CompanyIndustryMaxLength},
		{"employee_range", req.EmployeeRange, models.CompanyEmployeeRangeMaxLength},
		{"phone", req.Phone, models.CompanyPhoneMaxLength},
		{"address", req.Address, models.CompanyAddressMaxLength},
		{"city", req.City, models.CompanyCityMaxLength},
		{"state", req.State, models.CompanyStateMaxLength},
		{"country", req.Country, models.CompanyCountryMaxLength},
		{"postal_code", req.PostalCode, models.CompanyPostalCodeMaxLength},
	} {
		if utf8.RuneCountInString(field.value) > field.limit {
			return fmt.Sprintf("%s is too long (at most %d characters)", field.name, field.limit)
		}
	}
	if len(req.Notes) > models.CompanyNotesMaxBytes {
		return companyNotesTooLongMessage
	}
	return ""
}

// apply copies the text fields onto the company. The owner is handled by the
// caller, because who may set it depends on the caller's role.
func (req *CompanyRequest) apply(company *models.Company) {
	company.Name = req.Name
	company.Domain = req.Domain
	company.Website = req.Website
	company.Industry = req.Industry
	company.EmployeeRange = req.EmployeeRange
	company.Phone = req.Phone
	company.Address = req.Address
	company.City = req.City
	company.State = req.State
	company.Country = req.Country
	company.PostalCode = req.PostalCode
	company.Notes = req.Notes
}

// Create godoc
// @Summary Create a company
// @Description Create a company (admin and sales roles only). The name is trimmed and required, 1 to 200 characters. The domain is optional and normalised before it is stored — lower case, scheme, path and a leading "www." stripped — and must not be in use by another live company (409). The website, when present, must be an http or https URL. employee_range, when present, is one of 1-10, 11-50, 51-200, 201-500, 501-1000, 1000+. Every text field is bounded by its column width and notes by 65535 bytes; a longer value is a 400. owner_id names the account manager: sales users may only set it to themselves (403 otherwise) and get themselves when it is omitted; admins may set any live user, or omit it to leave the company unowned. An owner_id that matches no user is 400 INVALID_REFERENCE.
// @Tags companies
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CompanyRequest true "Company"
// @Success 201 {object} utils.APIResponse{data=models.Company} "Company created; owner is preloaded and customer_count/lead_count are 0"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, a value longer than its column, an invalid domain or website, or an unknown owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales may only own companies themselves"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A company with this domain already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies [post]
func (h *CompanyHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.Create")

	var req CompanyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := req.lengthError(); msg != "" {
		utils.RespondBadRequest(c, msg)
		return
	}

	company := &models.Company{}
	req.apply(company)

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	if req.OwnerID != nil && *req.OwnerID != 0 {
		// Only admins hand a company to somebody else; the owner rule of leads.
		if currentUserRole != string(models.RoleAdmin) && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign companies to yourself")
			return
		}
		company.OwnerID = req.OwnerID
	} else if currentUserRole != string(models.RoleAdmin) {
		company.OwnerID = &currentUserID
	}

	if err := h.companyService.Create(company); err != nil {
		h.respondError(c, logger, err)
		return
	}

	// Re-read so the response carries the preloaded owner the detail endpoint
	// would report, rather than the bare struct the service was handed.
	if created, err := h.companyService.GetByID(company.ID); err == nil {
		company = created
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, company)
	utils.RespondSuccess(c, http.StatusCreated, company)
}

// List godoc
// @Summary List companies
// @Description One page of companies (admin, sales and support roles). data is the array of companies, each with its owner and its live customer_count and lead_count; meta carries the pagination. search matches name, domain, industry and city (substring, case-insensitive on MySQL and MariaDB, case-sensitive on SQLite). sort_by must be one of the listed columns — any other value is a 400, never interpolated — and defaults to created_at; sort_order defaults to desc. page (1-based) overrides offset when supplied, as on the other lists.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param search query string false "Substring search across name, domain, industry and city"
// @Param sort_by query string false "Sort column" Enums(id, name, domain, industry, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction; anything else falls back to desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Company,meta=utils.APIMeta} "Companies retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies [get]
func (h *CompanyHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	sortBy := c.Query("sort_by")
	sortOrder := c.Query("sort_order")
	if _, _, err := utils.ValidateSort("companies", sortBy, sortOrder); err != nil {
		utils.RespondBadRequest(c, "Invalid sort column")
		return
	}

	companies, total, err := h.companyService.List(offset, limit, c.Query("search"), sortBy, sortOrder)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, companies)
	utils.RespondSuccessWithMeta(c, http.StatusOK, companies, pageMeta(c, offset, limit, total))
}

// Get godoc
// @Summary Get a company
// @Description One company (admin, sales and support roles) with its owner preloaded and its live customer_count and lead_count.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Success 200 {object} utils.APIResponse{data=models.Company} "Company retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies/{id} [get]
func (h *CompanyHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.Get")

	id, ok := companyID(c)
	if !ok {
		return
	}

	company, err := h.companyService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, company)
	utils.RespondSuccess(c, http.StatusOK, company)
}

// Update godoc
// @Summary Update a company
// @Description Replace a company's fields (admin and sales roles only). The body is the new state of every text field: a field left out is stored empty, which is how a domain or a note is cleared. The same validation as on create applies, and the company may keep its own domain without being reported as a duplicate. owner_id: absent leaves the owner unchanged, 0 clears it (admin only), another value sets it — sales users may only name themselves, admins any live user; an unknown user is 400 INVALID_REFERENCE.
// @Tags companies
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param request body CompanyRequest true "Company"
// @Success 200 {object} utils.APIResponse{data=models.Company} "Company updated; owner and counts refreshed"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID or request data, a value longer than its column, an invalid domain or website, or an unknown owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; only admins may reassign or clear the owner"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A company with this domain already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies/{id} [put]
func (h *CompanyHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.Update")

	id, ok := companyID(c)
	if !ok {
		return
	}

	var req CompanyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := req.lengthError(); msg != "" {
		utils.RespondBadRequest(c, msg)
		return
	}

	company, err := h.companyService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	if req.OwnerID != nil {
		switch {
		case *req.OwnerID == 0:
			if currentUserRole != string(models.RoleAdmin) {
				utils.RespondForbidden(c, "Only administrators can clear a company's owner")
				return
			}
			company.OwnerID = nil
		case currentUserRole != string(models.RoleAdmin) && *req.OwnerID != currentUserID:
			utils.RespondForbidden(c, "You can only assign companies to yourself")
			return
		default:
			company.OwnerID = req.OwnerID
		}
	}
	// The stored copy is not the caller's to keep: the service re-validates the
	// id, and the response is re-read below.
	company.Owner = nil
	req.apply(company)

	if err := h.companyService.Update(company); err != nil {
		h.respondError(c, logger, err)
		return
	}

	if refreshed, err := h.companyService.GetByID(id); err == nil {
		company = refreshed
	}

	utils.LogHandlerResponse(logger, http.StatusOK, company)
	utils.RespondSuccess(c, http.StatusOK, company)
}

// Delete godoc
// @Summary Delete a company
// @Description Soft-delete a company (admin role only). In the same transaction, company_id is set to NULL on every lead and customer that pointed at it, so nothing keeps a link to a company that is gone; the leads and customers themselves, and their free-text company field, are untouched. Companies hold no personal data, so this is not an erasure.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies/{id} [delete]
func (h *CompanyHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.Delete")

	id, ok := companyID(c)
	if !ok {
		return
	}

	if err := h.companyService.Delete(id); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// ListCustomers godoc
// @Summary List a company's customers
// @Description One page of the live customers linked to the company, newest first (admin, sales and support roles). data is the array of customers, meta the pagination.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Customer,meta=utils.APIMeta} "Customers retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies/{id}/customers [get]
func (h *CompanyHandler) ListCustomers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.ListCustomers")

	id, ok := companyID(c)
	if !ok {
		return
	}
	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	customers, total, err := h.companyService.ListCustomers(id, offset, limit)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customers)
	utils.RespondSuccessWithMeta(c, http.StatusOK, customers, pageMeta(c, offset, limit, total))
}

// ListLeads godoc
// @Summary List a company's leads
// @Description One page of the live leads linked to the company, newest first, with their owners (admin and sales roles only; support is refused as on the leads endpoints). Sales users see only the leads they own; admins see every lead of the company. data is the array of leads, meta the pagination.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Lead,meta=utils.APIMeta} "Leads retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /companies/{id}/leads [get]
func (h *CompanyHandler) ListLeads(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CompanyHandler.ListLeads")

	id, ok := companyID(c)
	if !ok {
		return
	}
	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	// Sales users only ever see their own leads, here as on GET /leads.
	var ownerID *uint
	if c.GetString("user_role") != string(models.RoleAdmin) {
		userID := c.GetUint("user_id")
		ownerID = &userID
	}

	leads, total, err := h.companyService.ListLeads(id, ownerID, offset, limit)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, leads)
	utils.RespondSuccessWithMeta(c, http.StatusOK, leads, pageMeta(c, offset, limit, total))
}

// companyID parses the :id segment, answering 400 itself on a bad value.
func companyID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid company ID")
		return 0, false
	}
	return uint(id), true
}

// pageMeta is the pagination block the other list endpoints return.
func pageMeta(c *gin.Context, offset, limit int, total int64) *utils.APIMeta {
	return &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
}

// respondError maps the company sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *CompanyHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrDuplicateCompanyDomain):
		logger.WithError(err).Warn("Duplicate company domain")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrAssigneeNotFound):
		// A bad owner id is a bad reference in the body, not a missing resource
		// at the requested path, so it is a 400 like an unknown label_id.
		logger.WithError(err).Warn("Unknown company owner")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(err).Warn("Invalid company")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("Company not found")
		utils.RespondNotFound(c, "Company not found")
	default:
		logger.WithError(err).Error("Company operation failed")
		utils.RespondInternalError(c)
	}
}
