package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type DealHandler struct {
	dealService service.DealService
}

func NewDealHandler(dealService service.DealService) *DealHandler {
	return &DealHandler{dealService: dealService}
}

// DealRequest is the body of POST /deals and PUT /deals/:id. PUT is the new
// state of the text fields, the amount, the stage and the date: one absent
// from the body is stored empty or cleared. The links and the owner follow the
// scalar rule of the company link on leads: absent keeps, 0 clears (owner_id
// excepted, which cannot be cleared), another value sets. currency absent
// keeps the stored code on update and takes the configured default on create.
type DealRequest struct {
	// Deal title, required; trimmed, it must not be empty. At most 200
	// characters.
	Title string `json:"title" binding:"required" maxLength:"200" example:"Acme rollout 2027"`
	// Stage. Create: qualification when absent. Update: absent keeps the
	// stored stage; a different stage is a stage change with the rules of
	// POST /deals/{id}/stage.
	Stage models.DealStage `json:"stage,omitempty" binding:"omitempty,oneof=qualification proposal negotiation won lost" example:"proposal"`
	// Value in minor units (cents for EUR), 0 to 1000000000000. Absent means
	// 0, on update as well.
	AmountCents int64 `json:"amount_cents" binding:"min=0,max=1000000000000" example:"1250000"`
	// ISO 4217 code, exactly three upper-case ASCII letters. Create: the
	// deals.default_currency setting (EUR as shipped) when absent. Update:
	// absent keeps the stored code.
	Currency string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase" minLength:"3" maxLength:"3" example:"EUR"`
	// Win probability in percent, 0 to 100. When the deal enters a stage and
	// this is absent, the stage's default applies (10, 40, 70); won is
	// always 100 and lost always 0 whatever is sent. On an update that keeps
	// the stage, absent keeps the stored probability.
	Probability *int `json:"probability,omitempty" binding:"omitempty,min=0,max=100" example:"40"`
	// Calendar date YYYY-MM-DD (400 BAD_REQUEST for any other form). Absent
	// or empty clears it, on update as well.
	ExpectedCloseDate string `json:"expected_close_date,omitempty" format:"date" example:"2026-12-31"`
	// Reason for losing, at most 255 characters (trimmed when the stage
	// changes); stored only when the resulting stage is lost and cleared
	// otherwise. Absent on update clears it.
	LostReason string `json:"lost_reason,omitempty" maxLength:"255" example:"Budget cut"`
	// Where the deal came from, at most 100 characters. Absent on update
	// clears it.
	Source string `json:"source,omitempty" maxLength:"100" example:"referral"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes, not characters).
	// Absent on update clears them.
	Notes string `json:"notes,omitempty" example:"Decision expected after the board meeting."`
	// Company link: absent keeps it (none on create), 0 clears it, another
	// id must name a live company (400 INVALID_REFERENCE).
	CompanyID *uint `json:"company_id,omitempty" example:"12"`
	// Customer link: absent keeps it (none on create), 0 clears it, another
	// id must name a live customer (400 INVALID_REFERENCE).
	CustomerID *uint `json:"customer_id,omitempty" example:"31"`
	// Lead link: absent keeps it (none on create), 0 clears it, another id
	// must name a live lead the caller may see (admins any lead, sales users
	// the leads they own); a hidden lead is the same 400 INVALID_REFERENCE as
	// a missing one. A sent id is always checked, even when it equals the
	// stored link.
	LeadID *uint `json:"lead_id,omitempty" example:"55"`
	// Owner, at least 1. Create: the caller when absent. Update: absent keeps
	// the owner. A sales user may only send its own id (403 otherwise); an
	// admin may name any live user (400 INVALID_REFERENCE otherwise).
	OwnerID *uint `json:"owner_id,omitempty" binding:"omitempty,min=1" example:"7"`
}

// DealStageRequest is the body of POST /deals/:id/stage.
type DealStageRequest struct {
	// Target stage. Required.
	Stage models.DealStage `json:"stage" binding:"required,oneof=qualification proposal negotiation won lost" example:"won"`
	// Win probability in percent, 0 to 100; absent takes the new stage's
	// default (10, 40, 70). Ignored for won (always 100) and lost (always 0).
	Probability *int `json:"probability,omitempty" binding:"omitempty,min=0,max=100" example:"70"`
	// Reason for losing, at most 255 characters, trimmed. Used only when the
	// target stage is lost; absent keeps the reason already stored. Any other
	// target stage clears the reason.
	LostReason *string `json:"lost_reason,omitempty" maxLength:"255" example:"Chose a competitor"`
}

// dealNotesTooLongMessage answers notes that would not fit deals.notes.
var dealNotesTooLongMessage = fmt.Sprintf("Notes are too long (at most %d bytes)", models.DealNotesMaxBytes)

// lengthError names the first field whose value would not fit its column. The
// limits are the model's constants, which a test holds to the declared column
// widths, so the database never has to reject a value itself.
func (req *DealRequest) lengthError() string {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"title", req.Title, models.DealTitleMaxLength},
		{"lost_reason", req.LostReason, models.DealLostReasonMaxLength},
		{"source", req.Source, models.DealSourceMaxLength},
	} {
		if utf8.RuneCountInString(field.value) > field.limit {
			return fmt.Sprintf("%s is too long (at most %d characters)", field.name, field.limit)
		}
	}
	if len(req.Notes) > models.DealNotesMaxBytes {
		return dealNotesTooLongMessage
	}
	return ""
}

// apply copies the fields onto the deal. The owner is handled by the caller,
// because who may set it depends on the caller's role; probability travels
// separately because nil means "the stage's default". A link that is absent
// from the body is left as it is on the deal (none on a create, the stored
// link on an update); 0 clears it.
func (req *DealRequest) apply(deal *models.Deal) error {
	deal.Title = req.Title
	deal.Stage = req.Stage
	deal.AmountCents = req.AmountCents
	deal.Currency = req.Currency
	deal.LostReason = req.LostReason
	deal.Source = req.Source
	deal.Notes = req.Notes
	if req.CompanyID != nil {
		deal.CompanyID = optionalID(req.CompanyID)
	}
	if req.CustomerID != nil {
		deal.CustomerID = optionalID(req.CustomerID)
	}
	if req.LeadID != nil {
		deal.LeadID = optionalID(req.LeadID)
	}

	deal.ExpectedCloseDate = nil
	if req.ExpectedCloseDate != "" {
		date, err := models.ParseDealDate(req.ExpectedCloseDate)
		if err != nil {
			return fmt.Errorf("expected_close_date must be a date of the form YYYY-MM-DD")
		}
		deal.ExpectedCloseDate = &date
	}
	return nil
}

// optionalID turns a zero id into no link.
func optionalID(id *uint) *uint {
	if id == nil || *id == 0 {
		return nil
	}
	return id
}

// Create godoc
// @Summary Create a deal
// @Description Create a deal. Roles: admin and sales.
// @Description Validation, in order: binding (title required, stage from its set, amount_cents 0 to 10^12, currency three upper-case letters, probability 0 to 100, owner_id at least 1); title 200, lost_reason 255 and source 100 characters and notes 65535 bytes; expected_close_date YYYY-MM-DD; then the service (blank title after trimming) and the links: owner_id, company_id, customer_id and lead_id must name live rows, and lead_id a lead the caller may see (a hidden lead is reported like a missing one).
// @Description Defaults and stage rules: stage qualification; currency from the deals.default_currency setting (EUR as shipped; an unusable setting falls back to EUR); probability from the stage (qualification 10, proposal 40, negotiation 70), won always 100 and lost always 0; closed_at set when the stage is won or lost; lost_reason kept only when lost. owner_id defaults to the caller; a sales user may only name itself.
// @Description Side effects, in one transaction: the first stage-history row (from_stage null, to_stage the initial stage) and the deal_created automation event.
// @Description The response is the deal re-read with owner, company, customer and lead loaded; lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body DealRequest true "The deal to create"
// @Success 201 {object} utils.APIResponse{data=models.Deal} "Deal created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). BAD_REQUEST: a value longer than its column, a malformed expected_close_date, or a blank title. INVALID_REFERENCE: owner_id, company_id, customer_id or lead_id names no live row, or lead_id names a lead the caller may not see"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or a sales user named another owner ('You can only assign deals to yourself')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals [post]
func (h *DealHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Create")

	var req DealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := req.lengthError(); msg != "" {
		utils.RespondBadRequest(c, msg)
		return
	}

	deal := &models.Deal{}
	if err := req.apply(deal); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	if req.OwnerID != nil {
		// Only admins hand a deal to somebody else; the owner rule of leads.
		if currentUserRole != string(models.RoleAdmin) && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign deals to yourself")
			return
		}
		deal.OwnerID = *req.OwnerID
	} else {
		deal.OwnerID = currentUserID
	}

	if err := h.dealService.Create(deal, req.Probability, currentUserID); err != nil {
		h.respondError(c, logger, err)
		return
	}

	// Re-read so the response carries the preloaded associations the detail
	// endpoint would report, rather than the bare struct the service was
	// handed.
	if created, err := h.dealService.GetByID(deal.ID); err == nil {
		deal = created
	}
	service.RedactDealLead(leadViewer(c), deal)

	utils.LogHandlerResponse(logger, http.StatusCreated, deal)
	utils.RespondSuccess(c, http.StatusCreated, deal)
}

// List godoc
// @Summary List deals
// @Description One page of live deals with owner, company, customer and lead loaded. Roles: admin (every deal) and sales (only the deals it owns, whatever owner_id says; the filter is applied in the query, so meta.total counts only those). lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Description Filters combine (AND): search (substring of title or notes; case-insensitive on MySQL/MariaDB with their default collations, and on SQLite for ASCII letters only), stage, open=true (every stage but won and lost; open=false applies no filter), company_id, customer_id and owner_id. sort_by must be one of the listed columns (any other value is 400) and the id breaks ties in the same direction. Without sort_by the order is created_at descending and sort_order is ignored; with sort_by, a sort_order other than asc or desc becomes desc.
// @Description data is the array of deals; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param search query string false "Substring searched in title and notes"
// @Param stage query string false "Only deals in this stage; an unknown value is 400" Enums(qualification, proposal, negotiation, won, lost)
// @Param open query bool false "true: only deals whose stage is neither won nor lost; false: no filter. Anything strconv.ParseBool rejects is 400"
// @Param company_id query int false "Only deals linked to this company; must be a positive integer" minimum(1)
// @Param customer_id query int false "Only deals linked to this customer; must be a positive integer" minimum(1)
// @Param owner_id query int false "Only deals owned by this user; must be a positive integer. Honoured for admins; a sales caller is always narrowed to itself" minimum(1)
// @Param sort_by query string false "Sort column" Enums(id, title, stage, amount_cents, probability, expected_close_date, closed_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "One page of deals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: sort_by is not an allowed column ('Invalid sort column'), stage is unknown ('Invalid stage'), open is not a boolean, or company_id, customer_id or owner_id is not a positive integer ('<name> must be a positive integer')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals [get]
func (h *DealHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	filter := repository.DealListFilter{
		Search:    c.Query("search"),
		SortBy:    c.Query("sort_by"),
		SortOrder: c.Query("sort_order"),
	}
	if _, _, err := utils.ValidateSort("deals", filter.SortBy, filter.SortOrder); err != nil {
		utils.RespondBadRequest(c, "Invalid sort column")
		return
	}
	if stage := c.Query("stage"); stage != "" {
		if !models.DealStage(stage).IsValid() {
			utils.RespondBadRequest(c, "Invalid stage")
			return
		}
		filter.Stage = models.DealStage(stage)
	}
	if open := c.Query("open"); open != "" {
		value, err := strconv.ParseBool(open)
		if err != nil {
			utils.RespondBadRequest(c, "open must be true or false")
			return
		}
		filter.Open = value
	}
	var ok bool
	if filter.CompanyID, ok = queryID(c, "company_id"); !ok {
		return
	}
	if filter.CustomerID, ok = queryID(c, "customer_id"); !ok {
		return
	}
	if filter.OwnerID, ok = queryID(c, "owner_id"); !ok {
		return
	}
	// Sales users only ever see their own deals, as on GET /leads.
	if c.GetString("user_role") != string(models.RoleAdmin) {
		userID := c.GetUint("user_id")
		filter.OwnerID = &userID
	}

	deals, total, err := h.dealService.List(offset, limit, filter)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	service.RedactDealLeads(leadViewer(c), deals)

	utils.LogHandlerResponse(logger, http.StatusOK, deals)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deals, pageMeta(c, offset, limit, total))
}

// Pipeline godoc
// @Summary Deal pipeline by stage
// @Description The live deals aggregated per stage and currency. Roles: admin and sales; a sales user always gets its own deals only, whatever owner_id says.
// @Description data.stages lists all five stages in pipeline order (qualification, proposal, negotiation, won, lost), each present even when empty (count 0, totals []). count is the number of deals in the stage; totals holds one entry per currency, sorted by currency code; amounts in different currencies are never added together. amount_cents is the sum of the deals' amounts; weighted_cents is round_half_up(sum of amount_cents x probability / 100), integer arithmetic, rounded once on each stage-and-currency total. Soft-deleted deals are not counted. owner_id (admin) and company_id narrow the aggregate; an id that matches nothing yields empty stages.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param owner_id query int false "Only deals owned by this user; must be a positive integer. Honoured for admins; a sales caller is always narrowed to itself" minimum(1)
// @Param company_id query int false "Only deals linked to this company; must be a positive integer" minimum(1)
// @Success 200 {object} utils.APIResponse{data=models.DealPipeline} "The pipeline"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: owner_id or company_id is not a positive integer ('<name> must be a positive integer')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/pipeline [get]
func (h *DealHandler) Pipeline(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Pipeline")

	var filter repository.DealPipelineFilter
	var ok bool
	if filter.OwnerID, ok = queryID(c, "owner_id"); !ok {
		return
	}
	if filter.CompanyID, ok = queryID(c, "company_id"); !ok {
		return
	}

	// The service narrows a sales caller to itself, as the list does.
	pipeline, err := h.dealService.Pipeline(filter, c.GetUint("user_id"), models.UserRole(c.GetString("user_role")))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, pipeline)
	utils.RespondSuccess(c, http.StatusOK, pipeline)
}

// Get godoc
// @Summary Get a deal
// @Description One live deal with owner, company, customer and lead loaded. Roles: admin (any deal) and sales (only the deals it owns; another owner's deal is 403, not 404). lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read. The same rule applies to every deal this API returns.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "The deal"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid deal ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own deals': a sales user asked for a deal it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/{id} [get]
func (h *DealHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Get")

	deal, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}
	service.RedactDealLead(leadViewer(c), deal)

	utils.LogHandlerResponse(logger, http.StatusOK, deal)
	utils.RespondSuccess(c, http.StatusOK, deal)
}

// Update godoc
// @Summary Replace a deal
// @Description Replace a deal's fields. Roles: admin (any deal) and sales (only the deals it owns).
// @Description The body is the new state of title, amount_cents (absent means 0), expected_close_date, lost_reason, source and notes: a field left out is stored empty, 0 or cleared. stage, currency, owner_id absent keep the stored value. company_id, customer_id and lead_id absent keep the stored link, 0 clears it, another id sets it (a live row, else 400 INVALID_REFERENCE). Links are checked only when they change, so a deal keeps links to erased records and stays editable; a lead_id that is sent is always checked, even when unchanged, and must name a lead the caller may see; a stored lead link the caller may not see is kept unchecked when lead_id is absent. owner_id: a sales user may only send its own id; an admin any live user (checked only when it changes).
// @Description A stage different from the stored one follows the rules of POST /deals/{id}/stage (probability defaults to the new stage's unless sent, won 100, lost 0, closed_at set or cleared, lost_reason kept only on lost) and writes a stage-history row. The same stage writes no history row: closed_at stays, probability changes only when sent (still pinned for won and lost), and lost_reason survives only on a lost deal.
// @Description Side effects, in one transaction: the deal_updated automation event, plus deal_stage_changed and the history row when the stage changes.
// @Description The response is the deal re-read with its associations; lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param request body DealRequest true "The new state of the deal"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "The updated deal"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). BAD_REQUEST: id is not an unsigned integer, a value longer than its column, a malformed expected_close_date, or a blank title. INVALID_REFERENCE: a changed owner_id, company_id or customer_id, or a sent lead_id, names no live row (or a lead the caller may not see)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; FORBIDDEN 'You can only view your own deals': a sales user asked for a deal it does not own; or a sales user named another owner ('You can only assign deals to yourself')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/{id} [put]
func (h *DealHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Update")

	var req DealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := req.lengthError(); msg != "" {
		utils.RespondBadRequest(c, msg)
		return
	}

	deal, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	if req.OwnerID != nil {
		if currentUserRole != string(models.RoleAdmin) && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign deals to yourself")
			return
		}
		deal.OwnerID = *req.OwnerID
	}

	// The preloaded copies are not the caller's to keep: the service
	// re-validates the ids and the response is re-read below.
	deal.Owner, deal.Company, deal.Customer, deal.Lead = nil, nil, nil, nil
	if err := req.apply(deal); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	if err := h.dealService.Update(deal, req.Probability, req.LeadID != nil, currentUserID); err != nil {
		h.respondError(c, logger, err)
		return
	}

	if refreshed, err := h.dealService.GetByID(deal.ID); err == nil {
		deal = refreshed
	}
	service.RedactDealLead(leadViewer(c), deal)

	utils.LogHandlerResponse(logger, http.StatusOK, deal)
	utils.RespondSuccess(c, http.StatusOK, deal)
}

// ChangeStage godoc
// @Summary Move a deal to another stage
// @Description Move a deal to a stage. Roles: admin (any deal) and sales (only the deals it owns). probability defaults to the new stage's (qualification 10, proposal 40, negotiation 70) unless sent; won is always 100 and lost always 0. Entering won or lost sets closed_at to now (also on won to lost); entering an open stage clears it. lost_reason is stored (trimmed) when the new stage is lost, absent keeping the reason already stored, and cleared otherwise. Asking for the stage the deal is already in is a 200 that writes nothing (no history row, no event) and ignores probability and lost_reason.
// @Description Side effects, in one transaction: one stage-history row and the deal_stage_changed automation event (no deal_updated event).
// @Description The response is the deal re-read with its associations; lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param request body DealStageRequest true "Target stage"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "The deal after the move"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'). BAD_REQUEST: id is not an unsigned integer, or lost_reason over 255 characters (sent, or the stored one when moving to lost)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own deals': a sales user asked for a deal it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/{id}/stage [post]
func (h *DealHandler) ChangeStage(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.ChangeStage")

	var req DealStageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if req.LostReason != nil && utf8.RuneCountInString(*req.LostReason) > models.DealLostReasonMaxLength {
		utils.RespondBadRequest(c, fmt.Sprintf("lost_reason is too long (at most %d characters)", models.DealLostReasonMaxLength))
		return
	}

	deal, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	moved, err := h.dealService.ChangeStage(deal.ID, req.Stage, req.Probability, req.LostReason, c.GetUint("user_id"))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	service.RedactDealLead(leadViewer(c), moved)

	utils.LogHandlerResponse(logger, http.StatusOK, moved)
	utils.RespondSuccess(c, http.StatusOK, moved)
}

// History godoc
// @Summary A deal's stage history
// @Description The deal's stage changes, oldest first (changed_at, then id). Roles: admin (any deal) and sales (only the deals it owns). The first row has from_stage null and to_stage the stage the deal was created in. changed_by is the acting user's full record (including email), omitted when that account no longer exists. data is the array; there is no pagination.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 200 {object} utils.APIResponse{data=[]models.DealStageChange} "The stage history"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid deal ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own deals': a sales user asked for a deal it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/{id}/history [get]
func (h *DealHandler) History(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.History")

	deal, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	changes, err := h.dealService.History(deal.ID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, changes)
	utils.RespondSuccess(c, http.StatusOK, changes)
}

// Delete godoc
// @Summary Delete a deal
// @Description Soft-delete a deal. Role: admin. The stage history rows stay, and quotes keep their deal_id (their deal summary is then omitted). Deals hold no personal data, so this is not an erasure. No automation event is recorded.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid deal ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /deals/{id} [delete]
func (h *DealHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Delete")

	id, ok := pathID(c, "Invalid deal ID")
	if !ok {
		return
	}

	if err := h.dealService.Delete(id); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// ListByCompany godoc
// @Summary List a company's deals
// @Description One page of the live deals linked to the company, newest first (created_at descending, then id), with their associations. Roles: admin (every deal) and sales (only the deals it owns; filtered in the query, so meta.total counts only those). lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Description data is the array of deals; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "One page of deals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid company ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live company has this id ('Company not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /companies/{id}/deals [get]
func (h *DealHandler) ListByCompany(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.ListByCompany")

	id, ok := pathID(c, "Invalid company ID")
	if !ok {
		return
	}
	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	deals, total, err := h.dealService.ListByCompany(id, h.ownerScope(c), offset, limit)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Company not found")
			utils.RespondNotFound(c, "Company not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}
	service.RedactDealLeads(leadViewer(c), deals)

	utils.LogHandlerResponse(logger, http.StatusOK, deals)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deals, pageMeta(c, offset, limit, total))
}

// ListByCustomer godoc
// @Summary List a customer's deals
// @Description One page of the live deals linked to the customer, newest first (created_at descending, then id), with their associations. Roles: admin (every deal) and sales (only the deals it owns; filtered in the query, so meta.total counts only those). Support and customer roles are refused. lead_id and lead appear only when the caller may see the linked lead (admins every lead, sales users the leads they own), decided on every read.
// @Description data is the array of deals; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "One page of deals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid customer ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live (not erased) customer has this id ('Customer not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /customers/{id}/deals [get]
func (h *DealHandler) ListByCustomer(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.ListByCustomer")

	id, ok := pathID(c, "Invalid customer ID")
	if !ok {
		return
	}
	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	deals, total, err := h.dealService.ListByCustomer(id, h.ownerScope(c), offset, limit)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}
	service.RedactDealLeads(leadViewer(c), deals)

	utils.LogHandlerResponse(logger, http.StatusOK, deals)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deals, pageMeta(c, offset, limit, total))
}

// ownerScope is the owner filter a list gets from the caller's role: none for
// an admin, the caller itself for everybody else.
func (h *DealHandler) ownerScope(c *gin.Context) *uint {
	if c.GetString("user_role") == string(models.RoleAdmin) {
		return nil
	}
	userID := c.GetUint("user_id")
	return &userID
}

// loadVisible parses :id, loads the deal and applies the visibility rule of
// leads: a sales user may only see its own deals, and somebody else's is a
// 403, not a 404 — the same answer GET /leads/:id gives. It has responded
// when ok is false.
func (h *DealHandler) loadVisible(c *gin.Context, logger *logrus.Entry) (*models.Deal, bool) {
	id, ok := pathID(c, "Invalid deal ID")
	if !ok {
		return nil, false
	}

	deal, err := h.dealService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return nil, false
	}

	if c.GetString("user_role") != string(models.RoleAdmin) && deal.OwnerID != c.GetUint("user_id") {
		utils.RespondForbidden(c, "You can only view your own deals")
		return nil, false
	}
	return deal, true
}

// pathID parses the :id segment, answering 400 with message itself on a bad
// value.
func pathID(c *gin.Context, message string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, message)
		return 0, false
	}
	return uint(id), true
}

// queryID parses an optional unsigned id from the query string. A missing
// parameter is nil; a malformed one is answered with 400 and ok false.
func queryID(c *gin.Context, name string) (*uint, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, fmt.Sprintf("%s must be a positive integer", name))
		return nil, false
	}
	value := uint(id)
	return &value, true
}

// respondError maps the deal sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *DealHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrAssigneeNotFound),
		errors.Is(err, apperrors.ErrCompanyNotFound),
		errors.Is(err, apperrors.ErrCustomerNotFound),
		errors.Is(err, apperrors.ErrLeadNotFound):
		// A bad id in the body is a bad reference, not a missing resource at
		// the requested path, so it is a 400 like an unknown label_id.
		logger.WithError(utils.LogError(err)).Warn("Unknown deal reference")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrForbidden):
		logger.WithError(utils.LogError(err)).Warn("Deal access refused")
		utils.RespondForbidden(c, "Deals are available to the admin and sales roles only")
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Invalid deal")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Deal not found")
		utils.RespondNotFound(c, "Deal not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Deal operation failed")
		utils.RespondInternalError(c)
	}
}
