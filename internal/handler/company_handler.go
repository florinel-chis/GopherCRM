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
	// Company name, required; trimmed, it must not be empty. At most 200
	// characters.
	Name string `json:"name" binding:"required" maxLength:"200" example:"Acme Corporation"`
	// Domain or URL; stored normalised (lower case, scheme, path, query,
	// fragment, a leading "www." and a trailing dot removed). What remains
	// may hold only letters, digits, dots and hyphens (400 otherwise) and
	// must not be used by another live company (409). At most 255
	// characters as sent. Absent or empty clears it on update.
	Domain string `json:"domain,omitempty" maxLength:"255" example:"https://www.acme.com/about"`
	// Website: an http or https URL with a host, trimmed (400 otherwise). At
	// most 255 characters. Absent or empty clears it on update.
	Website string `json:"website,omitempty" maxLength:"255" example:"https://www.acme.com"`
	// Industry, at most 100 characters.
	Industry string `json:"industry,omitempty" maxLength:"100" example:"Manufacturing"`
	// Head-count band; absent or empty stores no band.
	EmployeeRange string `json:"employee_range,omitempty" binding:"omitempty,oneof=1-10 11-50 51-200 201-500 501-1000 1000+" example:"51-200"`
	// Phone number, at most 50 characters.
	Phone string `json:"phone,omitempty" maxLength:"50" example:"+1 555 0100"`
	// Street address, at most 255 characters.
	Address string `json:"address,omitempty" maxLength:"255" example:"1 Industrial Way"`
	// City, at most 100 characters.
	City string `json:"city,omitempty" maxLength:"100" example:"Springfield"`
	// State or region, at most 100 characters.
	State string `json:"state,omitempty" maxLength:"100" example:"IL"`
	// Country, at most 100 characters.
	Country string `json:"country,omitempty" maxLength:"100" example:"USA"`
	// Postal code, at most 20 characters.
	PostalCode string `json:"postal_code,omitempty" maxLength:"20" example:"62701"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes, not characters).
	Notes string `json:"notes,omitempty" example:"Key account; renewal in Q3."`
	// Account manager. Create: a sales user may send only its own id and
	// gets itself when it is absent or 0; an admin may name any user, or
	// leave the company unowned. Update: absent keeps the owner, 0 clears it
	// (admin only), another id sets it (sales: its own id only). An id that
	// names no live user is 400 INVALID_REFERENCE.
	OwnerID *uint `json:"owner_id,omitempty" example:"7"`
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
// @Description Create a company. Roles: admin and sales.
// @Description Validation, in order: binding (name required, employee_range from its set), column widths (name 200, domain 255, website 255, industry 100, employee_range 20, phone 50, address 255, city, state and country 100, postal_code 20 characters; notes 65535 bytes), then the service: the trimmed name must not be empty, the domain is normalised and must be a bare host, the website must be an http or https URL, the owner must be a live user, and the domain must be free among live companies.
// @Description Owner: a sales user may only name itself and becomes the owner when owner_id is absent or 0; an admin may name any live user, or leave the company unowned by omitting it. The owner may be any live account; its role is not checked.
// @Description The response is the stored company re-read with its owner and counts (all 0 for a new company).
// @Tags companies
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CompanyRequest true "The company to create"
// @Success 201 {object} utils.APIResponse{data=models.Company} "Company created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). BAD_REQUEST: a value longer than its column ('<field> is too long (at most N characters)' or the notes byte limit), a blank name, a domain that is not a host, or a website that is not an http(s) URL. INVALID_REFERENCE: owner_id names no live user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or a sales user named another owner ('You can only assign companies to yourself')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another live company already uses the normalised domain"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One page of live companies, each with its owner and its customer_count, lead_count and deal_count (counted over all live records, whoever owns them). Roles: admin, sales and support.
// @Description search is a substring match on name, domain, industry and city (case-insensitive on MySQL/MariaDB with their default collations, and on SQLite for ASCII letters only). sort_by must be one of the listed columns (any other value is 400) and the id breaks ties in the same direction. Without sort_by the order is created_at descending and sort_order is ignored; with sort_by, a sort_order other than asc or desc (case-insensitive) becomes desc.
// @Description data is the array of companies; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param search query string false "Substring searched in name, domain, industry and city"
// @Param sort_by query string false "Sort column" Enums(id, name, domain, industry, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Company,meta=utils.APIMeta} "One page of companies"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: sort_by is not an allowed column ('Invalid sort column')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin, sales or support"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One live company with its owner and its customer_count, lead_count and deal_count. Roles: admin, sales and support (every company is visible to all three).
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Success 200 {object} utils.APIResponse{data=models.Company} "The company"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin, sales or support"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Summary Replace a company
// @Description Replace a company's fields. Roles: admin and sales; a sales user may update any company, not only the ones it owns. The body is the new state of every text field: a field left out is stored empty, which is how a domain or a note is cleared. The validation of create applies; the company may keep its own domain. owner_id: absent keeps the owner, 0 clears it (admin only), another id sets it (sales: only its own id). The owner, also a kept one, must still be a live user: a company whose stored owner account was deleted is refused with 400 INVALID_REFERENCE until owner_id is changed.
// @Description The response is the company re-read with its owner and counts.
// @Tags companies
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param request body CompanyRequest true "The new state of the company"
// @Success 200 {object} utils.APIResponse{data=models.Company} "The updated company"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID'), a value longer than its column, a blank name, a domain that is not a host, or a website that is not an http(s) URL. VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). INVALID_REFERENCE: the owner (sent or kept) names no live user"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; a sales user named another owner ('You can only assign companies to yourself') or sent owner_id 0 ('Only administrators can clear a company's owner')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another live company already uses the normalised domain"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Soft-delete a company. Role: admin. In the same transaction company_id is set to NULL on every lead, customer and deal that pointed at it; those records and their free-text company field are otherwise untouched. Companies hold no personal data, so this is not an erasure. A deleted company's domain becomes free for a new company.
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One page of the live customers linked to the company, newest first (created_at descending, then id). Roles: admin, sales and support; every role sees every linked customer.
// @Description data is the array of customers; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Customer,meta=utils.APIMeta} "One page of customers"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin, sales or support"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One page of the live leads linked to the company, newest first (created_at descending, then id), each with its owner loaded. Roles: admin (every lead of the company) and sales (only the leads it owns; the filter is applied in the query, so meta.total counts only those). Support is refused, as on the leads endpoints.
// @Description data is the array of leads; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags companies
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Lead,meta=utils.APIMeta} "One page of leads"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
		logger.WithError(utils.LogError(err)).Warn("Duplicate company domain")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrAssigneeNotFound):
		// A bad owner id is a bad reference in the body, not a missing resource
		// at the requested path, so it is a 400 like an unknown label_id.
		logger.WithError(utils.LogError(err)).Warn("Unknown company owner")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Invalid company")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Company not found")
		utils.RespondNotFound(c, "Company not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Company operation failed")
		utils.RespondInternalError(c)
	}
}
