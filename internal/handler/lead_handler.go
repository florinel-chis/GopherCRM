package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
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

// leadNotesTooLongMessage answers notes that would not fit leads.notes; the
// database would otherwise reject the write with a server error.
var leadNotesTooLongMessage = fmt.Sprintf("Notes are too long (at most %d bytes)", models.LeadNotesMaxBytes)

// CreateLeadRequest is the body of POST /leads. An email or a phone number is
// required (checked by the handler, not the binding tags).
type CreateLeadRequest struct {
	// Given name. Required, any non-empty string (100-character column, not
	// checked by the API).
	FirstName string `json:"first_name" binding:"required" example:"Ada"`
	// Family name. Required, any non-empty string (100-character column, not
	// checked by the API).
	LastName string `json:"last_name" binding:"required" example:"Lovelace"`
	// Email address, validated when present. Either email or a non-blank
	// phone is required.
	Email string `json:"email" binding:"omitempty,email" format:"email" example:"ada@example.com"`
	// Phone number, free text. Either a non-blank phone or email is required.
	Phone string `json:"phone,omitempty" example:"+44 20 7946 0000"`
	// Free-text employer name, independent of company_id.
	Company string `json:"company,omitempty" example:"Analytical Engines Ltd"`
	// Job title.
	Position string `json:"position,omitempty" example:"CTO"`
	// Where the lead came from.
	Source string `json:"source,omitempty" example:"website"`
	// Initial status; new when omitted. converted passes the binding but is
	// refused with 400 VALIDATION_ERROR (details.field "status"), so it is
	// not listed among the accepted values: only POST /leads/{id}/convert
	// converts a lead.
	Status models.LeadStatus `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted" example:"new" swaggertype:"string" enums:"new,contacted,qualified,unqualified"`
	// Classification; unclassified when omitted.
	Classification models.LeadClassification `json:"classification,omitempty" binding:"omitempty,oneof=unclassified test spam lead hot_lead" example:"unclassified"`
	// Identifier in an external system, for imports.
	ExternalID string `json:"external_id,omitempty" example:"crm-legacy-1042"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes; a longer value is
	// 400 BAD_REQUEST).
	Notes string `json:"notes,omitempty" example:"Met at the trade fair."`
	// Owner of the lead. Required for admins (400 without it); a sales user
	// may omit it (the lead is then its own) or send its own id, any other id
	// is 403. Not checked against the users table by the handler.
	OwnerID *uint `json:"owner_id,omitempty" example:"7"`

	// CompanyID links the lead to a company record; 0 or absent means none.
	// It is independent of the free-text Company field above.

	// Company record to link; 0 or absent means no link. An id that names
	// no live company is 400 INVALID_REFERENCE.
	CompanyID *uint `json:"company_id,omitempty" example:"12"`
	// Original creation time for imports. Accepted layouts: RFC 3339,
	// 2006-01-02T15:04:05Z, "2006-01-02 15:04:05" (UTC) and 2006-01-02. A
	// value in no accepted layout is ignored silently and the current time
	// is used.
	CreatedAt *string `json:"created_at,omitempty" example:"2025-03-14T09:26:53Z"` // ISO8601 timestamp for import
}

// UpdateLeadRequest is the body of PUT /leads/:id. Every field is optional,
// and an empty string is the same as an absent field: a text field cannot be
// cleared through this endpoint.
type UpdateLeadRequest struct {
	// New given name; empty or absent keeps the stored one.
	FirstName string `json:"first_name,omitempty" example:"Ada"`
	// New family name; empty or absent keeps the stored one.
	LastName string `json:"last_name,omitempty" example:"King"`
	// New email address, validated when present; empty or absent keeps the
	// stored one. The email-or-phone rule of create is not re-checked.
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"ada.king@example.com"`
	// New phone number; empty or absent keeps the stored one.
	Phone string `json:"phone,omitempty" example:"+44 20 7946 0001"`
	// New free-text company name; empty or absent keeps the stored one.
	Company string `json:"company,omitempty" example:"Analytical Engines Ltd"`
	// New job title; empty or absent keeps the stored one.
	Position string `json:"position,omitempty" example:"CEO"`
	// New source; empty or absent keeps the stored one.
	Source string `json:"source,omitempty" example:"referral"`
	// New status; empty or absent keeps the stored one. converted is refused
	// with 400 VALIDATION_ERROR unless the lead is already converted.
	Status models.LeadStatus `json:"status,omitempty" binding:"omitempty,oneof=new contacted qualified unqualified converted" example:"qualified"`
	// New classification; empty or absent keeps the stored one.
	Classification models.LeadClassification `json:"classification,omitempty" binding:"omitempty,oneof=unclassified test spam lead hot_lead" example:"hot_lead"`
	// New external id; empty or absent keeps the stored one.
	ExternalID string `json:"external_id,omitempty" example:"crm-legacy-1042"`
	// New notes, replacing the stored text; at most 65535 bytes (400
	// BAD_REQUEST above). Empty or absent keeps the stored notes.
	Notes string `json:"notes,omitempty" example:"Demo booked for Tuesday."`
	// New owner. Admin only: any owner_id from another role, even the
	// caller's own id, is 403.
	OwnerID *uint `json:"owner_id,omitempty" example:"9"`

	// CompanyID: absent leaves the link alone, 0 clears it, any other value
	// must name a live company.

	// Company link: absent keeps it, 0 clears it, any other value must name
	// a live company (400 INVALID_REFERENCE otherwise).
	CompanyID *uint `json:"company_id,omitempty" example:"12"`
}

// LeadListData is the data object of GET /leads. It documents the response
// shape for the API specification only; the handler builds it as a map.
type LeadListData struct {
	// The page of leads.
	Leads []models.Lead `json:"leads"`
	// Number of leads matching the request over all pages (also in
	// meta.total).
	Total int64 `json:"total" example:"57"`
}

// respondLeadRuleError answers a lead write refused by a conversion rule — a
// conversion setting, or converted asked for outside the conversion — with
// 400 VALIDATION_ERROR, the rule's message and the field it is about.
func respondLeadRuleError(c *gin.Context, ruleErr *service.LeadConversionRuleError) {
	utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, ruleErr.Message, ruleErr.Details())
}

type ConvertLeadRequest struct {
	// Company name for the new customer; empty or absent takes the lead's
	// free-text company.
	CompanyName string `json:"company_name,omitempty" example:"Analytical Engines Ltd"`
	// Website stored on the new customer: trimmed, at most 255 characters,
	// an http or https URL with a host (400 BAD_REQUEST otherwise). May be
	// empty.
	Website string `json:"website,omitempty" example:"https://engines.example.com"`
	// Postal address stored on the new customer.
	Address string `json:"address,omitempty" example:"12 St James's Square, London"`

	// Notes are stored on the new customer. They are required (non-blank)
	// when the leads.conversion.require_notes setting is on.

	// Notes stored on the new customer. Required (not blank) when the
	// leads.conversion.require_notes setting is on: 400 VALIDATION_ERROR
	// with details.field "notes".
	Notes string `json:"notes,omitempty" example:"Converted after the pilot."`
}

// Create godoc
// @Summary Create a lead
// @Description Create a lead. Roles: admin and sales (the /leads group guard).
// @Description Checks, in order: the binding rules (first_name and last_name required, email well-formed, status and classification from their sets); notes at most 65535 bytes; status converted is refused (only POST /leads/{id}/convert converts); an email or a non-blank phone is required. Owner: an admin must send owner_id; a sales user may omit it (the lead becomes its own) or send its own id. company_id links a company record independently of the free-text company field: 0 or absent means no link, an id that names no live company is 400 INVALID_REFERENCE. created_at may backdate an imported lead; a value in no accepted layout is ignored and the current time is used.
// @Description Side effect: the lead_created automation event is recorded in the same transaction as the insert.
// @Description The response is the stored lead; owner is not loaded here (all its fields are zero) and company_record is omitted.
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateLeadRequest true "The lead to create"
// @Success 201 {object} utils.APIResponse{data=models.Lead} "Lead created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). BAD_REQUEST: notes over 65535 bytes, neither email nor phone ('Provide an email address or a phone number'), or an admin without owner_id ('Owner ID is required for admin users'). VALIDATION_ERROR: status converted, with error.details {'field': 'status'}. INVALID_REFERENCE: company_id names no live company"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales, or a sales user sent another user's owner_id ('You can only assign leads to yourself')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure, including a database rejection (for example an owner_id that names no user, or a value longer than its column on MySQL/MariaDB)"
// @Router /leads [post]
func (h *LeadHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Create")

	var req CreateLeadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if len(req.Notes) > models.LeadNotesMaxBytes {
		utils.RespondBadRequest(c, leadNotesTooLongMessage)
		return
	}
	// A lead becomes converted only through the conversion endpoint.
	if req.Status == models.LeadStatusConverted {
		respondLeadRuleError(c, service.ConvertedStatusRefusal())
		return
	}

	// A lead needs some way to be reached, but either channel will do.
	if req.Email == "" && strings.TrimSpace(req.Phone) == "" {
		utils.RespondBadRequest(c, "Provide an email address or a phone number")
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
	if req.CompanyID != nil && *req.CompanyID != 0 {
		lead.CompanyID = req.CompanyID
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
		logger.WithError(utils.LogError(err)).Error("Failed to create lead")
		if errors.Is(err, apperrors.ErrCompanyNotFound) {
			// A bad company id is a bad reference in the body, not a missing
			// resource at the requested path, so it is a 400 like label_ids.
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
			return
		}
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, lead)
	utils.RespondSuccess(c, http.StatusCreated, lead)
}

// List godoc
// @Summary List leads
// @Description One page of leads. Roles: admin and sales.
// @Description Without external_id, a sales user always gets the leads it owns, in ascending id order, and search, classification, sort_by and sort_order are ignored; the owner field is not loaded on that path (all its fields are zero).
// @Description Without external_id, for an admin the parameters do not combine, the first one present wins: search (substring match on first_name, last_name, email, company, phone and notes; case-insensitive on MySQL/MariaDB with their default collations, and on SQLite for ASCII letters only; honours the sort), then classification (exact match, any value accepted, an unknown one simply matches nothing; ascending id order; owner not loaded), then sort_by, else every lead in ascending id order. An unknown sort_by is ignored silently (ascending id order) and a sort_order other than asc or desc becomes asc. Soft-deleted (erased) leads never appear.
// @Description external_id, when present and not empty, takes precedence over everything above for every role, including search, classification, sort_by, sort_order, page, offset and limit: the lookup is an SQL equality on the column, byte-exact on SQLite, while on MySQL/MariaDB the column collation decides (utf8mb4_unicode_ci in the migrations: case-insensitive, trailing spaces ignored); the value is not trimmed. external_id is not unique: the result holds every matching live lead the caller may view (admin any lead, sales only the leads it owns, every other role none), in ascending id order, and total is their number (meta.page 1, meta.per_page the parsed limit, which does not cut the list). A lead the caller may not see is left out, never 403 or 404, so a hidden lead reads like an unknown value. At most the first 100 matching leads by id are read before the visibility filter, so with more than 100 leads sharing one value later ones (including a sales user's own) are not returned. The owner is loaded on that path, company_record is not.
// @Description data is an object {leads, total}; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags leads
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param search query string false "Admin only: substring searched in first_name, last_name, email, company, phone and notes"
// @Param classification query string false "Admin only, ignored when search is present: exact classification to match" Enums(unclassified, test, spam, lead, hot_lead)
// @Param sort_by query string false "Admin only, ignored when classification wins: sort column; any other value is ignored (no error)" Enums(created_at, updated_at, first_name, last_name, email, company, status, classification, source)
// @Param sort_order query string false "Sort direction for sort_by; any other value becomes asc" Enums(asc, desc) default(asc)
// @Param external_id query string false "Exact external_id to look up (at most 255 characters); when present and not empty it overrides every other parameter and returns the matching leads the caller may view" maxlength(255)
// @Success 200 {object} utils.APIResponse{data=LeadListData,meta=utils.APIMeta} "One page of leads and the total"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: external_id is longer than 255 characters ('external_id is too long (at most 255 characters)', error.details {'field': 'external_id'})"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /leads [get]
func (h *LeadHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.List")

	// Support both page-based and offset-based pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	offset, limit := utils.ParseOffsetLimit(c)

	// An exact external_id lookup replaces every other filter, the sort and
	// the paging.
	if externalID := c.Query("external_id"); externalID != "" {
		h.listByExternalID(c, externalID, limit)
		return
	}
	classification := c.Query("classification")

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

	// Role-based filtering: only admin and sales users can see all leads
	// Other roles can only see their own leads
	isAdminOrSales := currentUserRole == string(models.RoleAdmin) || currentUserRole == string(models.RoleSales)

	if currentUserRole == string(models.RoleSales) {
		// For sales users, only show their own leads
		leads, total, err = h.leadService.GetByOwner(currentUserID, offset, limit)
	} else if !isAdminOrSales {
		// Non-admin, non-sales users can only see their own leads
		leads, total, err = h.leadService.GetByOwner(currentUserID, offset, limit)
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
		logger.WithError(utils.LogError(err)).Error("Failed to list leads")
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

// listByExternalID answers GET /leads?external_id= with the usual list shape
// holding the live leads whose external_id matches that the caller may see.
// Visibility follows service.CanViewLead row by row; a lead the caller may not
// see is simply left out (never 403 or 404), so external ids cannot be probed.
func (h *LeadHandler) listByExternalID(c *gin.Context, externalID string, limit int) {
	logger := utils.LogHandlerStart(c, "LeadHandler.listByExternalID")

	if utf8.RuneCountInString(externalID) > service.LeadExternalIDMaxLength {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation,
			fmt.Sprintf("external_id is too long (at most %d characters)", service.LeadExternalIDMaxLength),
			gin.H{"field": "external_id"})
		return
	}

	matches, err := h.leadService.ListByExternalID(externalID)
	if err != nil && !apperrors.IsNotFound(err) {
		logger.WithError(utils.LogError(err)).Error("Failed to look up leads by external id")
		utils.RespondInternalError(c)
		return
	}
	viewer := leadViewer(c)
	leads := []models.Lead{}
	for i := range matches {
		if service.CanViewLead(viewer, &matches[i]) {
			leads = append(leads, matches[i])
		}
	}

	total := int64(len(leads))
	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
	responseData := gin.H{"leads": leads, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}

// Get godoc
// @Summary Get a lead
// @Description One lead with its owner and linked company (company_record) loaded. Roles: admin (any lead) and sales (only the leads it owns; another owner's lead is 403, not 404).
// @Tags leads
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Lead ID"
// @Success 200 {object} utils.APIResponse{data=models.Lead} "The lead"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid lead ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales, or a sales user asked for another owner's lead ('You can only view your own leads')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live lead has this id, or the lookup failed ('Lead not found'; every lookup error is reported as 404)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Router /leads/{id} [get]
func (h *LeadHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LeadHandler.Get")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid lead ID")
		return
	}

	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Lead not found")
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

// Update godoc
// @Summary Update a lead
// @Description Partial update. Roles: admin (any lead) and sales (only the leads it owns). Every field is optional and an empty string counts as absent, so a text field cannot be cleared here; the email-or-phone rule of create is not re-checked. Only an admin may send owner_id (any other role gets 403, even for its own id); the new owner is not checked against the users table by the handler. company_id: absent keeps the link, 0 clears it, another value must name a live company (400 INVALID_REFERENCE). status converted is refused (400 VALIDATION_ERROR) unless the lead is already converted. The lead is re-read under a row lock in the writing transaction, so the update never overwrites a concurrent conversion with a stale copy.
// @Description Side effect: lead_updated is recorded in the same transaction, plus lead_status_changed (with the previous and the new status) when the status changes.
// @Description The response is the updated lead; owner is not loaded here (all its fields are zero) and company_record is omitted.
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Lead ID"
// @Param request body UpdateLeadRequest true "Fields to change; empty strings and absent fields are left unchanged"
// @Success 200 {object} utils.APIResponse{data=models.Lead} "The updated lead"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid lead ID') or notes over 65535 bytes. VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). VALIDATION_ERROR: status converted for a lead that is not converted, with error.details {'field': 'status'}. INVALID_REFERENCE: company_id names no live company"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; a sales user asked for another owner's lead ('You can only update your own leads'); a non-admin sent owner_id ('Only administrators can reassign leads')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live lead has this id ('Lead not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /leads/{id} [put]
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
	if len(req.Notes) > models.LeadNotesMaxBytes {
		utils.RespondBadRequest(c, leadNotesTooLongMessage)
		return
	}

	// Check if lead exists and user has permission
	lead, err := h.leadService.GetByID(uint(id))
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Lead not found")
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

	// The scalar counterpart of the label_ids rule: absent keeps, 0 clears.
	if req.CompanyID != nil {
		if *req.CompanyID == 0 {
			updates["company_id"] = (*uint)(nil)
		} else {
			updates["company_id"] = req.CompanyID
		}
	}

	updatedLead, err := h.leadService.Update(uint(id), updates)
	if err != nil {
		var ruleErr *service.LeadConversionRuleError
		if errors.As(err, &ruleErr) {
			logger.WithError(utils.LogError(err)).Warn("Lead update refused")
			respondLeadRuleError(c, ruleErr)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to update lead")
		if errors.Is(err, apperrors.ErrCompanyNotFound) {
			utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
			return
		}
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, updatedLead)
	utils.RespondSuccess(c, http.StatusOK, updatedLead)
}

// Delete godoc
// @Summary Delete (erase) a lead
// @Description GDPR erasure, irreversible. Roles: admin (any lead) and sales (only the leads it owns, and only leads that were never converted). The lead's personal fields are overwritten in place (the email becomes a random address in the reserved .invalid domain; names, phone, company, position, external id and notes are cleared), linked form submissions are scrubbed, and the row is then soft-deleted, all in one transaction. A converted lead is the same person as the customer it became, so erasing it also erases that customer, the customer's own customer-role login (credentials included) and every other lead converted into it; staff accounts, owners and assignees are never erased. company_id and deal links stay. A sales user asking to delete a converted lead gets 403 and nothing changes.
// @Tags leads
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Lead ID"
// @Success 204 "Erased; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid lead ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; a sales user asked for another owner's lead ('You can only delete your own leads'), or for a converted lead ('Only an admin can delete a converted lead: it also erases the customer it became')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live lead has this id ('Lead not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the erasure failed (nothing is written when it failed before the commit; a failed COMMIT is ambiguous and the erasure may have been applied) or the lead vanished between the lookup and the erasure"
// @Router /leads/{id} [delete]
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
		logger.WithError(utils.LogError(err)).Warn("Lead not found")
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

	if err := h.leadService.Delete(uint(id), models.UserRole(currentUserRole)); err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			utils.RespondForbidden(c, "Only an admin can delete a converted lead: it also erases the customer it became")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete lead")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// ConvertToCustomer godoc
// @Summary Convert a lead into a customer
// @Description Create a customer from the lead and mark the lead converted, in one transaction. Roles: admin (any lead) and sales (only the leads it owns).
// @Description The customer gets the lead's first_name, last_name, email and phone; company is company_name or, when that is empty, the lead's free-text company; website (trimmed, at most 255 characters, an http or https URL with a host), address and notes come from the body. The lead's company_id is copied when it still names a live company (a link to a deleted company is dropped and the conversion succeeds). With leads.conversion.auto_assign_owner on, the lead's owner becomes the customer's assigned_to_id (never user_id).
// @Description Refusals, before anything is written: a lead already converted (status converted or a customer_id set) is 400 BAD_REQUEST; the lead's status must be one of leads.conversion.allowed_statuses (re-checked under a row lock) and notes must not be blank when leads.conversion.require_notes is on, both 400 VALIDATION_ERROR; a lead without an email is 400 BAD_REQUEST (every customer needs one); an email that already belongs to a customer is 409. A failure to read the conversion settings is a 500. Deadlocks are retried up to three times.
// @Description Side effect: lead_updated and lead_status_changed (to converted, origin conversion) are recorded in the same transaction. No customer_created event exists.
// @Tags leads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Lead ID"
// @Param request body ConvertLeadRequest true "Customer details that the lead does not carry; send {} for none"
// @Success 200 {object} utils.APIResponse{data=models.Customer} "The new customer"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer, the body is not valid JSON ('Invalid request format', an empty body included), an invalid website, the lead is already converted ('lead already converted'), or it has no email address. VALIDATION_ERROR: a conversion setting refused it; error.details is {'field': 'status', 'status': <lead status>, 'allowed_statuses': [...]} or {'field': 'notes'}"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales, or a sales user asked for another owner's lead ('You can only convert your own leads')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live lead has this id, or it was erased during the conversion ('Lead not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: a customer with the lead's email address already exists ('customer with this email already exists')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the settings could not be read, the transaction failed, or the client went away while the lead was read"
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

	// Check if lead exists and user has permission. The read is bound to the
	// request, so a client that has gone away stops waiting for a connection.
	lead, err := h.leadService.GetByIDContext(c.Request.Context(), uint(id))
	if err != nil {
		if ctxErr := c.Request.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			// The client went away while the read waited; nobody reads
			// this answer, and the lead's existence was never checked.
			logger.WithError(utils.LogError(err)).Warn("Lead conversion abandoned by the request")
			utils.RespondInternalError(c)
			return
		}
		logger.WithError(utils.LogError(err)).Warn("Lead not found")
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
		Website:   req.Website,
		FirstName: lead.FirstName,
		LastName:  lead.LastName,
		Email:     lead.Email,
		Phone:     lead.Phone,
		Company:   req.CompanyName,
		Address:   req.Address,
		Notes:     req.Notes,
	}

	customer, err := h.leadService.ConvertToCustomer(c.Request.Context(), uint(id), customerData)
	if err != nil {
		// A refusal the caller can act on (4xx) is a warning; only a
		// failure the server owns (500) is logged as an error.
		warn := logger.WithError(utils.LogError(err)).Warn
		var ruleErr *service.LeadConversionRuleError
		switch {
		case errors.As(err, &ruleErr):
			warn("Failed to convert lead")
			respondLeadRuleError(c, ruleErr)
		case errors.Is(err, apperrors.ErrValidation):
			warn("Failed to convert lead")
			utils.RespondBadRequest(c, err.Error())
		case errors.Is(err, apperrors.ErrLeadConverted):
			warn("Failed to convert lead")
			utils.RespondBadRequest(c, "lead already converted")
		case errors.Is(err, apperrors.ErrLeadMissingEmail):
			warn("Failed to convert lead")
			utils.RespondBadRequest(c, "This lead has no email address. Add one before converting it: every customer needs an email.")
		case errors.Is(err, apperrors.ErrDuplicateEmail):
			warn("Failed to convert lead")
			utils.RespondConflict(c, "customer with this email already exists")
		case apperrors.IsNotFound(err):
			// Erased between the lookup above and the conversion.
			warn("Failed to convert lead")
			utils.RespondNotFound(c, "Lead not found")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to convert lead")
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, customer)
	utils.RespondSuccess(c, http.StatusOK, customer)
}
