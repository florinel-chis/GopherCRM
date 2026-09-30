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
	Title             string           `json:"title" binding:"required"`
	Stage             models.DealStage `json:"stage,omitempty" binding:"omitempty,oneof=qualification proposal negotiation won lost"`
	AmountCents       int64            `json:"amount_cents" binding:"min=0"`
	Currency          string           `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase"`
	Probability       *int             `json:"probability,omitempty" binding:"omitempty,min=0,max=100"`
	ExpectedCloseDate string           `json:"expected_close_date,omitempty"`
	LostReason        string           `json:"lost_reason,omitempty"`
	Source            string           `json:"source,omitempty"`
	Notes             string           `json:"notes,omitempty"`
	CompanyID         *uint            `json:"company_id,omitempty"`
	CustomerID        *uint            `json:"customer_id,omitempty"`
	LeadID            *uint            `json:"lead_id,omitempty"`
	OwnerID           *uint            `json:"owner_id,omitempty" binding:"omitempty,min=1"`
}

// DealStageRequest is the body of POST /deals/:id/stage.
type DealStageRequest struct {
	Stage       models.DealStage `json:"stage" binding:"required,oneof=qualification proposal negotiation won lost"`
	Probability *int             `json:"probability,omitempty" binding:"omitempty,min=0,max=100"`
	LostReason  *string          `json:"lost_reason,omitempty"`
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
// @Description Create a deal (admin and sales roles only). title is trimmed and required, 1 to 200 characters. stage defaults to qualification. amount_cents is an integer in minor units, 0 or more; currency is three upper-case letters and defaults to the deals.default_currency configuration (EUR as shipped) when omitted. probability is 0 to 100 and defaults per stage (qualification 10, proposal 40, negotiation 70); won is always 100 and lost always 0. expected_close_date is YYYY-MM-DD. lost_reason is stored only when the stage is lost. company_id, customer_id and lead_id are optional and must name live rows (400 INVALID_REFERENCE otherwise; 0 means none). owner_id defaults to the caller; sales users may only name themselves (403 otherwise), admins any live user. A first history row (from_stage null) is written in the same transaction; when the stage is won or lost, closed_at is set.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body DealRequest true "Deal"
// @Success 201 {object} utils.APIResponse{data=models.Deal} "Deal created, with owner, company, customer and lead preloaded"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, a value longer than its column, a bad currency, probability or date, or an unknown company_id, customer_id, lead_id or owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales may only own deals themselves"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	utils.LogHandlerResponse(logger, http.StatusCreated, deal)
	utils.RespondSuccess(c, http.StatusCreated, deal)
}

// List godoc
// @Summary List deals
// @Description One page of deals (admin and sales roles; sales users see only the deals they own, whatever owner_id says). data is the array of deals, each with its owner, company, customer and lead; meta carries the pagination. search matches title and notes (substring; case-insensitive on MySQL and MariaDB, case-sensitive on SQLite). Filters combine: stage, company_id, customer_id, owner_id (admin only) and open=true, which selects every stage but won and lost. sort_by must be one of the listed columns — any other value is a 400, never interpolated — and defaults to created_at; sort_order defaults to desc. page (1-based) overrides offset when supplied.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param search query string false "Substring search across title and notes"
// @Param stage query string false "Only deals in this stage" Enums(qualification, proposal, negotiation, won, lost)
// @Param open query bool false "true: only deals whose stage is neither won nor lost"
// @Param company_id query int false "Only deals linked to this company"
// @Param customer_id query int false "Only deals linked to this customer"
// @Param owner_id query int false "Only deals owned by this user (admin; sales is always narrowed to itself)"
// @Param sort_by query string false "Sort column" Enums(id, title, stage, amount_cents, probability, expected_close_date, closed_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction; anything else falls back to desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "Deals retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column, or stage, open or an id filter is malformed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	utils.LogHandlerResponse(logger, http.StatusOK, deals)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deals, pageMeta(c, offset, limit, total))
}

// Pipeline godoc
// @Summary Deal pipeline by stage
// @Description The live deals aggregated per stage (admin and sales roles; sales users always get their own deals only, whatever owner_id says, as on GET /deals). data.stages lists all five stages in pipeline order — qualification, proposal, negotiation, won, lost — each present even when empty (count 0, totals []). count is the number of deals in the stage; totals holds one entry per currency, sorted by currency code, and amounts in different currencies are never added together. amount_cents is the sum of the deals' amounts; weighted_cents is round_half_up(sum of amount_cents × probability / 100), computed with integer arithmetic and rounded once on each stage-and-currency total, not per deal. Soft-deleted deals are not counted. owner_id (admin only) and company_id narrow the aggregate; a malformed or zero id is a 400, as on the list, and an id that matches nothing yields empty stages.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param owner_id query int false "Only deals owned by this user (admin; sales is always narrowed to itself)"
// @Param company_id query int false "Only deals linked to this company"
// @Success 200 {object} utils.APIResponse{data=models.DealPipeline} "Pipeline retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "owner_id or company_id is not a positive integer"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description One deal (admin and sales roles) with its owner, company, customer and lead preloaded. Sales users can only view their own deals (403 otherwise).
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "Deal retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales users can only view their own deals"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /deals/{id} [get]
func (h *DealHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DealHandler.Get")

	deal, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, deal)
	utils.RespondSuccess(c, http.StatusOK, deal)
}

// Update godoc
// @Summary Update a deal
// @Description Replace a deal's fields (admin and sales roles; sales users only their own deals). The body is the new state of the text fields, amount, stage and expected_close_date: one left out is stored empty or cleared. The links follow the rule of company_id on leads: company_id, customer_id and lead_id absent keep the stored link, 0 clears it, another value sets it (a live row, else 400 INVALID_REFERENCE). Links are checked only when the request sets them; a deal keeps links to erased records and stays editable. owner_id absent keeps the owner (sales may only name themselves, admins any live user); currency absent keeps the stored code. A stage different from the stored one goes through the same rules as POST /deals/{id}/stage — probability defaults to the new stage's unless sent, won is 100 and lost 0, closed_at set or cleared, lost_reason kept only on lost — and writes a history row in the same transaction. The same stage writes no history row; probability is then applied only when sent.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param request body DealRequest true "Deal"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "Deal updated; associations refreshed"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID or request data, a value longer than its column, a bad currency, probability or date, or an unknown company_id, customer_id, lead_id or owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only update their own deals and may not reassign them"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	if err := h.dealService.Update(deal, req.Probability, currentUserID); err != nil {
		h.respondError(c, logger, err)
		return
	}

	if refreshed, err := h.dealService.GetByID(deal.ID); err == nil {
		deal = refreshed
	}

	utils.LogHandlerResponse(logger, http.StatusOK, deal)
	utils.RespondSuccess(c, http.StatusOK, deal)
}

// ChangeStage godoc
// @Summary Move a deal to another stage
// @Description Move a deal to a stage (admin and sales roles; sales users only their own deals). The move is recorded as a history row in the same transaction. probability defaults to the new stage's (qualification 10, proposal 40, negotiation 70) unless sent; won is always 100 and lost always 0. Entering won or lost sets closed_at; entering an open stage clears it. lost_reason is stored when the new stage is lost (absent keeps the previous reason, if any) and cleared otherwise. Asking for the stage the deal is already in is a 200 that changes nothing and writes no history row.
// @Tags deals
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param request body DealStageRequest true "Target stage"
// @Success 200 {object} utils.APIResponse{data=models.Deal} "Deal after the move"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID, unknown stage, probability outside 0-100 or lost_reason over 255 characters"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only move their own deals"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	utils.LogHandlerResponse(logger, http.StatusOK, moved)
	utils.RespondSuccess(c, http.StatusOK, moved)
}

// History godoc
// @Summary A deal's stage history
// @Description The deal's stage changes oldest first (admin and sales roles; sales users only their own deals). The first row has from_stage null and to_stage the stage the deal was created in. Each row carries changed_by (the acting user's id, name and email) and changed_at. data is the array.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 200 {object} utils.APIResponse{data=[]models.DealStageChange} "History retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only view their own deals"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Soft-delete a deal (admin role only). The stage history rows stay. Deals hold no personal data, so this is not an erasure.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description One page of the live deals linked to the company, newest first, with their associations (admin and sales roles; sales users see only the deals they own). data is the array of deals, meta the pagination.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Company ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "Deals retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid company ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Company not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("Company not found")
			utils.RespondNotFound(c, "Company not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, deals)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deals, pageMeta(c, offset, limit, total))
}

// ListByCustomer godoc
// @Summary List a customer's deals
// @Description One page of the live deals linked to the customer, newest first, with their associations (admin and sales roles; sales users see only the deals they own). data is the array of deals, meta the pagination.
// @Tags deals
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.Deal,meta=utils.APIMeta} "Deals retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
			logger.WithError(err).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}

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
		logger.WithError(err).Warn("Unknown deal reference")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrForbidden):
		logger.WithError(err).Warn("Deal access refused")
		utils.RespondForbidden(c, "Deals are available to the admin and sales roles only")
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(err).Warn("Invalid deal")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("Deal not found")
		utils.RespondNotFound(c, "Deal not found")
	default:
		logger.WithError(err).Error("Deal operation failed")
		utils.RespondInternalError(c)
	}
}
