package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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

// currencyQueryPattern is the shape of a currency query filter: exactly three
// upper-case ASCII letters, as the service takes it.
var currencyQueryPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type QuoteHandler struct {
	quoteService service.QuoteService
}

func NewQuoteHandler(quoteService service.QuoteService) *QuoteHandler {
	return &QuoteHandler{quoteService: quoteService}
}

// QuoteRequest is the body of POST /quotes. At least one of deal_id and
// customer_id is required (checked by the service); with only deal_id the
// customer is taken from the deal.
type QuoteRequest struct {
	Title      string `json:"title" binding:"required"`
	DealID     *uint  `json:"deal_id,omitempty" binding:"omitempty,min=1"`
	CustomerID *uint  `json:"customer_id,omitempty" binding:"omitempty,min=1"`
	Currency   string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase"`
	ValidUntil string `json:"valid_until,omitempty"`
	Notes      string `json:"notes,omitempty"`
	OwnerID    *uint  `json:"owner_id,omitempty" binding:"omitempty,min=1"`
}

// NullableString is a JSON string field that tells an absent key (Set false),
// an explicit null (Set true, Value nil) and a value apart; PUT /quotes/:id
// uses it to clear valid_until with null while leaving it alone when absent.
type NullableString struct {
	Set   bool
	Value *string
}

// UnmarshalJSON is called for an explicit null as well as for a string, which
// is what makes the distinction possible.
func (n *NullableString) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.Value = &value
	return nil
}

// QuoteUpdateRequest is the body of PUT /quotes/:id: a partial update of a
// draft, every absent field keeps its stored value. currency may only change
// while the quote has no lines; owner_id only by an admin.
type QuoteUpdateRequest struct {
	Title      *string        `json:"title,omitempty"`
	ValidUntil NullableString `json:"valid_until,omitempty" swaggertype:"string" format:"date"`
	Notes      *string        `json:"notes,omitempty"`
	CustomerID *uint          `json:"customer_id,omitempty" binding:"omitempty,min=1"`
	Currency   *string        `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase"`
	OwnerID    *uint          `json:"owner_id,omitempty" binding:"omitempty,min=1"`
}

// QuoteLineRequest is the body of POST /quotes/:id/lines. The description and
// the unit price default to the product's.
type QuoteLineRequest struct {
	ProductID      uint    `json:"product_id" binding:"required,min=1"`
	Quantity       int64   `json:"quantity" binding:"required,min=1,max=1000000"`
	DiscountBP     int64   `json:"discount_bp" binding:"min=0,max=10000"`
	UnitPriceCents *int64  `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000"`
	Description    *string `json:"description,omitempty"`
}

// QuoteLineUpdateRequest is the body of PUT /quotes/:id/lines/:lineId: a
// partial update with the same bounds.
type QuoteLineUpdateRequest struct {
	Quantity       *int64  `json:"quantity,omitempty" binding:"omitempty,min=1,max=1000000"`
	DiscountBP     *int64  `json:"discount_bp,omitempty" binding:"omitempty,min=0,max=10000"`
	UnitPriceCents *int64  `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000"`
	Description    *string `json:"description,omitempty"`
	Position       *int64  `json:"position,omitempty" binding:"omitempty,min=1"`
}

// QuoteStatusRequest is the body of POST /quotes/:id/status.
type QuoteStatusRequest struct {
	Status models.QuoteStatus `json:"status" binding:"required,oneof=draft sent accepted rejected expired"`
}

// quoteNotesTooLongMessage answers notes that would not fit quotes.notes.
var quoteNotesTooLongMessage = fmt.Sprintf("notes are too long (at most %d bytes)", models.QuoteNotesMaxBytes)

// quoteLengthError names the first field whose value would not fit its column.
func quoteLengthError(title, notes *string) string {
	if title != nil && utf8.RuneCountInString(*title) > models.QuoteTitleMaxLength {
		return fmt.Sprintf("title is too long (at most %d characters)", models.QuoteTitleMaxLength)
	}
	if notes != nil && len(*notes) > models.QuoteNotesMaxBytes {
		return quoteNotesTooLongMessage
	}
	return ""
}

// lineDescriptionError refuses a description that would not fit its column.
func lineDescriptionError(description *string) string {
	if description != nil && utf8.RuneCountInString(*description) > models.QuoteLineDescriptionMaxLength {
		return fmt.Sprintf("description is too long (at most %d characters)", models.QuoteLineDescriptionMaxLength)
	}
	return ""
}

// Create godoc
// @Summary Create a quote
// @Description Create a draft quote (admin and sales roles). title is trimmed and required, 1 to 200 characters. At least one of deal_id and customer_id is required (400 VALIDATION_ERROR otherwise); with only deal_id the customer is taken from the deal, possibly none; a customer_id that differs from the deal's customer is a 400 VALIDATION_ERROR. An unknown deal, customer or owner is 400 INVALID_REFERENCE; a deal that is won or lost is 409 CONFLICT. A sales user may only quote the deals it owns and may only own the quote itself (403 otherwise); an admin may name any owner. currency is three upper-case letters and defaults to the deals.default_currency configuration (EUR as shipped). valid_until is YYYY-MM-DD. The quote starts as a draft with totals of 0 and no lines; its first history row (from_status null) is written in the same transaction.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body QuoteRequest true "Quote"
// @Success 201 {object} utils.APIResponse{data=models.Quote} "Quote created, with its number, owner, customer and deal summaries"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, a value longer than its column, a bad currency or date, missing links or a customer/deal mismatch (VALIDATION_ERROR), or an unknown deal_id, customer_id or owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales may only quote its own deals and own the quote itself"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The deal is won or lost"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes [post]
func (h *QuoteHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.Create")

	var req QuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := quoteLengthError(&req.Title, &req.Notes); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}

	quote := &models.Quote{
		Title:      req.Title,
		Currency:   req.Currency,
		Notes:      req.Notes,
		DealID:     req.DealID,
		CustomerID: req.CustomerID,
	}
	if req.ValidUntil != "" {
		date, err := models.ParseDealDate(req.ValidUntil)
		if err != nil {
			utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, "valid_until must be a date of the form YYYY-MM-DD", nil)
			return
		}
		quote.ValidUntil = &date
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := models.UserRole(c.GetString("user_role"))
	if req.OwnerID != nil {
		if currentUserRole != models.RoleAdmin && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign quotes to yourself")
			return
		}
		quote.OwnerID = *req.OwnerID
	} else {
		quote.OwnerID = currentUserID
	}

	if err := h.quoteService.Create(quote, currentUserID, currentUserRole); err != nil {
		h.respondError(c, logger, err)
		return
	}

	if created, err := h.quoteService.GetByID(quote.ID); err == nil {
		quote = created
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, quote)
	utils.RespondSuccess(c, http.StatusCreated, quote)
}

// List godoc
// @Summary List quotes
// @Description One page of quotes without their lines (admin and sales roles; sales users see only the quotes they own, whatever owner_id says — the owner filter is applied inside the query, so meta.total counts only those). Filters combine: status, deal_id, customer_id, owner_id (admin only). sort_by must be one of the listed columns — any other value is a 400, never interpolated — and defaults to created_at; sort_order defaults to desc.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param status query string false "Only quotes in this status" Enums(draft, sent, accepted, rejected, expired)
// @Param deal_id query int false "Only quotes of this deal"
// @Param customer_id query int false "Only quotes addressed to this customer"
// @Param owner_id query int false "Only quotes owned by this user (admin; sales is always narrowed to itself)"
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction; anything else falls back to desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Quote,meta=utils.APIMeta} "Quotes retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column, or status or an id filter is malformed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes [get]
func (h *QuoteHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	filter := repository.QuoteListFilter{
		SortBy:    c.Query("sort_by"),
		SortOrder: c.Query("sort_order"),
	}
	if _, _, err := utils.ValidateSort("quotes", filter.SortBy, filter.SortOrder); err != nil {
		utils.RespondBadRequest(c, "Invalid sort column")
		return
	}
	if status := c.Query("status"); status != "" {
		if !models.QuoteStatus(status).IsValid() {
			utils.RespondBadRequest(c, "Invalid status")
			return
		}
		filter.Status = models.QuoteStatus(status)
	}
	var ok bool
	if filter.DealID, ok = queryID(c, "deal_id"); !ok {
		return
	}
	if filter.CustomerID, ok = queryID(c, "customer_id"); !ok {
		return
	}
	if filter.OwnerID, ok = queryID(c, "owner_id"); !ok {
		return
	}
	// Sales users only ever see their own quotes, as on GET /deals.
	if scope := h.ownerScope(c); scope != nil {
		filter.OwnerID = scope
	}

	quotes, total, err := h.quoteService.List(offset, limit, filter)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, quotes)
	utils.RespondSuccessWithMeta(c, http.StatusOK, quotes, pageMeta(c, offset, limit, total))
}

// Get godoc
// @Summary Get a quote
// @Description One quote (admin and sales roles) with its lines ordered by position, its totals and summaries of its owner, customer and deal. Sales users can only view their own quotes (403 otherwise).
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "Quote retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales users can only view their own quotes"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id} [get]
func (h *QuoteHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.Get")

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, quote)
	utils.RespondSuccess(c, http.StatusOK, quote)
}

// Update godoc
// @Summary Update a draft quote
// @Description Partial update of a draft quote (admin and sales roles; sales users only their own): every absent field keeps its stored value. valid_until takes YYYY-MM-DD or null to clear. customer_id must name a live customer (400 INVALID_REFERENCE) and, when the quote has a deal with a customer, that customer (400 VALIDATION_ERROR). currency may only change while the quote has no lines (409 otherwise). owner_id may only be set by an admin. A quote that is not a draft is a 409 CONFLICT and nothing changes.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "Quote updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID or request data (VALIDATION_ERROR), or an unknown customer_id or owner_id (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only update their own quotes and may not reassign them"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The quote is not a draft, or the currency cannot change because the quote has lines"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id} [put]
func (h *QuoteHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.Update")

	var req QuoteUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := quoteLengthError(req.Title, req.Notes); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	currentUserID := c.GetUint("user_id")
	if req.OwnerID != nil {
		if c.GetString("user_role") != string(models.RoleAdmin) && *req.OwnerID != currentUserID {
			utils.RespondForbidden(c, "You can only assign quotes to yourself")
			return
		}
		quote.OwnerID = *req.OwnerID
	}
	if req.Title != nil {
		quote.Title = *req.Title
	}
	if req.Notes != nil {
		quote.Notes = *req.Notes
	}
	if req.CustomerID != nil {
		quote.CustomerID = req.CustomerID
	}
	if req.Currency != nil {
		quote.Currency = *req.Currency
	}
	if req.ValidUntil.Set {
		quote.ValidUntil = nil
		if req.ValidUntil.Value != nil && *req.ValidUntil.Value != "" {
			date, err := models.ParseDealDate(*req.ValidUntil.Value)
			if err != nil {
				utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, "valid_until must be a date of the form YYYY-MM-DD or null", nil)
				return
			}
			quote.ValidUntil = &date
		}
	}
	// The preloaded copies are not the caller's to keep: the service
	// re-validates the ids and the response is re-read below.
	quote.Owner, quote.Customer, quote.Deal, quote.Lines = nil, nil, nil, nil

	if err := h.quoteService.Update(quote); err != nil {
		h.respondError(c, logger, err)
		return
	}
	if refreshed, err := h.quoteService.GetByID(quote.ID); err == nil {
		quote = refreshed
	}

	utils.LogHandlerResponse(logger, http.StatusOK, quote)
	utils.RespondSuccess(c, http.StatusOK, quote)
}

// Delete godoc
// @Summary Delete a quote
// @Description Soft-delete a quote (admin and sales roles). A sales user may delete its own draft quotes only: another owner's quote is a 403 and a non-draft one a 409 CONFLICT. An admin may delete any quote. The lines and the history rows stay in their tables. Quotes hold no personal data, so this is not an erasure.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only delete their own quotes"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A sales user may only delete a draft"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id} [delete]
func (h *QuoteHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.Delete")

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}
	draftOnly := c.GetString("user_role") != string(models.RoleAdmin)
	if err := h.quoteService.Delete(quote.ID, draftOnly); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// AddLine godoc
// @Summary Add a line to a draft quote
// @Description Add a line priced from a product (admin and sales roles; sales users only their own quotes). The product must be live and active (400 INVALID_REFERENCE otherwise) and priced in the quote's currency (400 VALIDATION_ERROR). The product's name and price are copied onto the line unless description or unit_price_cents override them; a later change to the product never alters the line. quantity is 1 to 1000000, discount_bp 0 to 10000 (basis points: 1250 is 12.5%), unit_price_cents 0 to 1000000000000; quantity × unit_price_cents and the resulting quote total may not exceed 1000000000000, and a quote holds at most 100 lines (all 400 VALIDATION_ERROR, nothing written). One transaction: a locking read of the quote (FOR UPDATE on MySQL), the draft check, the insert, the totals recomputed from the lines and written WHERE status = 'draft'. A quote that is not a draft is a 409 CONFLICT.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteLineRequest true "Line"
// @Success 201 {object} utils.APIResponse{data=models.Quote} "The quote with its lines and recomputed totals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Bounds, the cap, the 100-line limit or a currency mismatch (VALIDATION_ERROR); an unknown, inactive or deleted product (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The quote is not a draft"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id}/lines [post]
func (h *QuoteHandler) AddLine(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.AddLine")

	var req QuoteLineRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := lineDescriptionError(req.Description); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	updated, err := h.quoteService.AddLine(quote.ID, models.QuoteLineInput{
		ProductID:      req.ProductID,
		Quantity:       req.Quantity,
		DiscountBP:     req.DiscountBP,
		UnitPriceCents: req.UnitPriceCents,
		Description:    req.Description,
	})
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, updated)
	utils.RespondSuccess(c, http.StatusCreated, updated)
}

// UpdateLine godoc
// @Summary Edit a line of a draft quote
// @Description Partial update of a line (admin and sales roles; sales users only their own quotes) with the same bounds and the same transaction shape as adding one. A line that is not on this quote is a 404. A quote that is not a draft is a 409 CONFLICT and nothing changes.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param lineId path int true "Line ID"
// @Param request body QuoteLineUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "The quote with its lines and recomputed totals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid id or request data (VALIDATION_ERROR)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found, or the line is not on this quote"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The quote is not a draft"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id}/lines/{lineId} [put]
func (h *QuoteHandler) UpdateLine(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.UpdateLine")

	var req QuoteLineUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := lineDescriptionError(req.Description); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}
	lineID, ok := lineIDParam(c)
	if !ok {
		return
	}
	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	updated, err := h.quoteService.UpdateLine(quote.ID, lineID, models.QuoteLinePatch{
		Quantity:       req.Quantity,
		DiscountBP:     req.DiscountBP,
		UnitPriceCents: req.UnitPriceCents,
		Description:    req.Description,
		Position:       req.Position,
	})
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, updated)
	utils.RespondSuccess(c, http.StatusOK, updated)
}

// DeleteLine godoc
// @Summary Remove a line from a draft quote
// @Description Delete a line (admin and sales roles; sales users only their own quotes) and recompute the totals in one transaction. A line that is not on this quote is a 404. A quote that is not a draft is a 409 CONFLICT and nothing changes.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param lineId path int true "Line ID"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "The quote with its remaining lines and recomputed totals"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid id"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found, or the line is not on this quote"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The quote is not a draft"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id}/lines/{lineId} [delete]
func (h *QuoteHandler) DeleteLine(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.DeleteLine")

	lineID, ok := lineIDParam(c)
	if !ok {
		return
	}
	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	updated, err := h.quoteService.DeleteLine(quote.ID, lineID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, updated)
	utils.RespondSuccess(c, http.StatusOK, updated)
}

// ChangeStatus godoc
// @Summary Move a quote to another status
// @Description Change the status (admin and sales roles; sales users only their own quotes). The allowed moves are draft→sent, sent→accepted, sent→rejected, sent→expired and sent→draft; any other pair, the same status again included, and a transition that lost its race are 400 INVALID_STATUS_TRANSITION and write nothing. One transaction: the quote is claimed by a conditional update (WHERE status = the stored status), then for draft→sent the line count is read with a locking read — zero lines rolls the claim back with 409 CONFLICT — and one history row is written. accepted is refused with 409 CONFLICT once valid_until lies in the past (local date). sent sets sent_at; accepted, rejected and expired set closed_at; sent→draft clears both. Accepting a quote does not change its deal.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteStatusRequest true "Target status"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "The quote after the move"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID or unknown status (VALIDATION_ERROR); a disallowed pair or a lost race (INVALID_STATUS_TRANSITION)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only move their own quotes"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "draft→sent without lines, or accepted after valid_until"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id}/status [post]
func (h *QuoteHandler) ChangeStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.ChangeStatus")

	var req QuoteStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}

	moved, err := h.quoteService.ChangeStatus(quote.ID, req.Status, c.GetUint("user_id"))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, moved)
	utils.RespondSuccess(c, http.StatusOK, moved)
}

// History godoc
// @Summary A quote's status history
// @Description The quote's status changes oldest first (admin and sales roles; sales users only their own quotes). The first row has from_status null and to_status draft. Each row carries changed_by (the acting user's id and name) and changed_at. data is the array.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=[]models.QuoteStatusChange} "History retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only view their own quotes"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /quotes/{id}/history [get]
func (h *QuoteHandler) History(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.History")

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}
	changes, err := h.quoteService.History(quote.ID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, changes)
	utils.RespondSuccess(c, http.StatusOK, changes)
}

// Email godoc
// @Summary Email a quote to its customer
// @Description Send the quote as a plaintext rendering (number, title, every line, totals, validity) to the linked customer's current address (admin and sales roles; sales users only their own quotes). The body is empty. Only a sent or accepted quote can be emailed (409 CONFLICT otherwise); a quote without a customer, or whose customer was erased, is a 409 CONFLICT and no mail is sent. The subject is fixed ("Quote Q-000042 from GopherCRM"). No transaction is held during the delivery; on success last_emailed_at is stamped by a conditional update that matches only while the quote is still sent or accepted and no revision to draft committed in between — otherwise 409 CONFLICT with last_emailed_at unchanged. A mailer failure is 502 MAIL_DELIVERY_FAILED.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=models.Quote} "The quote with last_emailed_at set"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid quote ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - sales users can only email their own quotes"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Quote not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The quote is not sent or accepted, has no live customer, or was revised during the send"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Failure 502 {object} utils.APIResponse{error=utils.APIError} "Mail delivery failed (MAIL_DELIVERY_FAILED)"
// @Router /quotes/{id}/email [post]
func (h *QuoteHandler) Email(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.Email")

	quote, ok := h.loadVisible(c, logger)
	if !ok {
		return
	}
	emailed, err := h.quoteService.Email(quote.ID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, emailed)
	utils.RespondSuccess(c, http.StatusOK, emailed)
}

// ListByDeal godoc
// @Summary List a deal's quotes
// @Description One page of the quotes of the deal (admin and sales roles; sales users see only the quotes they own, and another owner's deal is a 403). data is the array of quotes without lines, meta the pagination; sort_by and sort_order as on GET /quotes.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Quote,meta=utils.APIMeta} "Quotes retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid deal ID or sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required; sales only on its own deals"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Deal not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /deals/{id}/quotes [get]
func (h *QuoteHandler) ListByDeal(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.ListByDeal")

	id, ok := pathID(c, "Invalid deal ID")
	if !ok {
		return
	}
	offset, limit, sortBy, sortOrder, ok := listParams(c, "quotes")
	if !ok {
		return
	}

	quotes, total, err := h.quoteService.ListByDeal(id, h.ownerScope(c), offset, limit, sortBy, sortOrder)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Deal not found")
			utils.RespondNotFound(c, "Deal not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, quotes)
	utils.RespondSuccessWithMeta(c, http.StatusOK, quotes, pageMeta(c, offset, limit, total))
}

// ListByCustomer godoc
// @Summary List a customer's quotes
// @Description One page of the quotes addressed to the customer (admin and sales roles; sales users see only the quotes they own). data is the array of quotes without lines, meta the pagination; sort_by and sort_order as on GET /quotes.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Quote,meta=utils.APIMeta} "Quotes retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid customer ID or sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Customer not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /customers/{id}/quotes [get]
func (h *QuoteHandler) ListByCustomer(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuoteHandler.ListByCustomer")

	id, ok := pathID(c, "Invalid customer ID")
	if !ok {
		return
	}
	offset, limit, sortBy, sortOrder, ok := listParams(c, "quotes")
	if !ok {
		return
	}

	quotes, total, err := h.quoteService.ListByCustomer(id, h.ownerScope(c), offset, limit, sortBy, sortOrder)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			utils.RespondNotFound(c, "Customer not found")
			return
		}
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, quotes)
	utils.RespondSuccessWithMeta(c, http.StatusOK, quotes, pageMeta(c, offset, limit, total))
}

// listParams parses the pagination and the sort of a sub-list, answering 400
// itself on an unknown sort column.
func listParams(c *gin.Context, entity string) (offset, limit int, sortBy, sortOrder string, ok bool) {
	offset, limit = utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}
	sortBy, sortOrder = c.Query("sort_by"), c.Query("sort_order")
	if _, _, err := utils.ValidateSort(entity, sortBy, sortOrder); err != nil {
		utils.RespondBadRequest(c, "Invalid sort column")
		return 0, 0, "", "", false
	}
	return offset, limit, sortBy, sortOrder, true
}

// lineIDParam parses the :lineId segment.
func lineIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("lineId"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid line ID")
		return 0, false
	}
	return uint(id), true
}

// ownerScope is the owner filter a list gets from the caller's role: none for
// an admin, the caller itself for everybody else.
func (h *QuoteHandler) ownerScope(c *gin.Context) *uint {
	if c.GetString("user_role") == string(models.RoleAdmin) {
		return nil
	}
	userID := c.GetUint("user_id")
	return &userID
}

// loadVisible parses :id, loads the quote and applies the visibility rule of
// deals: a sales user may only see its own quotes, and somebody else's is a
// 403, not a 404. It has responded when ok is false.
func (h *QuoteHandler) loadVisible(c *gin.Context, logger *logrus.Entry) (*models.Quote, bool) {
	id, ok := pathID(c, "Invalid quote ID")
	if !ok {
		return nil, false
	}
	quote, err := h.quoteService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return nil, false
	}
	if c.GetString("user_role") != string(models.RoleAdmin) && quote.OwnerID != c.GetUint("user_id") {
		utils.RespondForbidden(c, "You can only view your own quotes")
		return nil, false
	}
	return quote, true
}

// respondError maps the quote sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *QuoteHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrDealNotFound),
		errors.Is(err, apperrors.ErrCustomerNotFound),
		errors.Is(err, apperrors.ErrAssigneeNotFound),
		errors.Is(err, apperrors.ErrProductNotFound):
		// A bad id in the body is a bad reference, not a missing resource at
		// the requested path.
		logger.WithError(utils.LogError(err)).Warn("Unknown quote reference")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrInvalidQuoteTransition):
		logger.WithError(utils.LogError(err)).Warn("Invalid quote transition")
		status := apperrors.New(apperrors.CodeInvalidStatusTransition, "").HTTPStatus
		utils.RespondError(c, status, apperrors.CodeInvalidStatusTransition, err.Error(), nil)
	case errors.Is(err, apperrors.ErrValidation), errors.Is(err, apperrors.ErrQuoteCurrencyMismatch):
		logger.WithError(utils.LogError(err)).Warn("Invalid quote")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case errors.Is(err, apperrors.ErrForbidden):
		logger.WithError(utils.LogError(err)).Warn("Quote access refused")
		utils.RespondForbidden(c, "You can only work on your own deals and quotes")
	case errors.Is(err, apperrors.ErrDealClosed),
		errors.Is(err, apperrors.ErrQuoteLocked),
		errors.Is(err, apperrors.ErrQuoteCurrencyLocked),
		errors.Is(err, apperrors.ErrQuoteHasNoLines),
		errors.Is(err, apperrors.ErrQuoteExpired),
		errors.Is(err, apperrors.ErrQuoteNotSent),
		errors.Is(err, apperrors.ErrQuoteNoRecipient),
		errors.Is(err, apperrors.ErrQuoteChangedDuringSend):
		logger.WithError(utils.LogError(err)).Warn("Quote conflict")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrMailDelivery):
		// The transport's own message is not echoed: it may quote the address.
		logger.WithError(utils.LogError(err)).Error("Quote email delivery failed")
		utils.RespondError(c, http.StatusBadGateway, utils.ErrCodeMailDeliveryFailed, "The quote email could not be delivered", nil)
	case errors.Is(err, apperrors.ErrQuoteLineNotFound):
		logger.WithError(utils.LogError(err)).Warn("Quote line not found")
		utils.RespondNotFound(c, "Quote line not found")
	case errors.Is(err, apperrors.ErrQuoteNotFound), apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Quote not found")
		utils.RespondNotFound(c, "Quote not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Quote operation failed")
		utils.RespondInternalError(c)
	}
}
