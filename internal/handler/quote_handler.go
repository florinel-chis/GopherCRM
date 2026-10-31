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
	// Quote title, required; trimmed, it must not be empty. At most 200
	// characters.
	Title string `json:"title" binding:"required" maxLength:"200" example:"Acme rollout - phase 1"`
	// Deal the quote grows from (at least 1). Must name a live deal that is
	// neither won nor lost; a sales user must own it. With no customer_id
	// the deal's customer is used (possibly none).
	DealID *uint `json:"deal_id,omitempty" binding:"omitempty,min=1" example:"88"`
	// Customer the quote is addressed to (at least 1); must name a live
	// customer and, when deal_id is sent and the deal has a customer, that
	// customer. deal_id or customer_id is required.
	CustomerID *uint `json:"customer_id,omitempty" binding:"omitempty,min=1" example:"31"`
	// ISO 4217 code, exactly three upper-case ASCII letters; the
	// deals.default_currency setting (EUR as shipped) when absent. Every line
	// must be priced in it.
	Currency string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase" minLength:"3" maxLength:"3" example:"EUR"`
	// Last day of validity, a calendar date YYYY-MM-DD (400 VALIDATION_ERROR
	// for any other form). Absent or empty means none.
	ValidUntil string `json:"valid_until,omitempty" format:"date" example:"2026-12-31"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes, not characters).
	Notes string `json:"notes,omitempty" example:"Prices exclude VAT."`
	// Owner (at least 1); the caller when absent. A sales user may only send
	// its own id (403 otherwise); an admin may name any live user.
	OwnerID *uint `json:"owner_id,omitempty" binding:"omitempty,min=1" example:"7"`
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
	// New title; trimmed, it must not be empty. At most 200 characters.
	Title *string `json:"title,omitempty" maxLength:"200" example:"Acme rollout - phase 1 (revised)"`
	// New last day of validity: a date YYYY-MM-DD sets it, null or an empty
	// string clears it, absent keeps it.
	ValidUntil NullableString `json:"valid_until,omitempty" swaggertype:"string" format:"date" extensions:"x-nullable" example:"2027-01-31"`
	// New notes, at most 65535 bytes; an empty string clears them.
	Notes *string `json:"notes,omitempty" example:"Prices exclude VAT. Valid for one site."`
	// New customer (at least 1; it cannot be cleared). Must name a live
	// customer and, when the quote's deal has a customer, that customer.
	CustomerID *uint `json:"customer_id,omitempty" binding:"omitempty,min=1" example:"31"`
	// New ISO 4217 code, exactly three upper-case ASCII letters; refused with
	// 409 while the quote has lines.
	Currency *string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase" minLength:"3" maxLength:"3" example:"USD"`
	// New owner (at least 1). A sales user may only send its own id; an
	// admin may name any live user.
	OwnerID *uint `json:"owner_id,omitempty" binding:"omitempty,min=1" example:"9"`
}

// QuoteLineRequest is the body of POST /quotes/:id/lines. The description and
// the unit price default to the product's.
type QuoteLineRequest struct {
	// Product to price the line from: live, active and in the quote's
	// currency. Required, at least 1.
	ProductID uint `json:"product_id" binding:"required,min=1" example:"9"`
	// Number of units, 1 to 1000000. Required.
	Quantity int64 `json:"quantity" binding:"required,min=1,max=1000000" example:"2"`
	// Per-line discount in basis points, 0 to 10000 (1250 = 12.5 percent); 0
	// when absent.
	DiscountBP int64 `json:"discount_bp" binding:"min=0,max=10000" example:"1250"`
	// Unit price override in minor units, 0 to 1000000000000; the product's
	// price when absent.
	UnitPriceCents *int64 `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000" example:"115000"`
	// Line text override, at most 255 characters, trimmed; the product's
	// name when absent or blank.
	Description *string `json:"description,omitempty" maxLength:"255" example:"Annual support plan, two sites"`
}

// QuoteLineUpdateRequest is the body of PUT /quotes/:id/lines/:lineId: a
// partial update with the same bounds.
type QuoteLineUpdateRequest struct {
	// New number of units, 1 to 1000000.
	Quantity *int64 `json:"quantity,omitempty" binding:"omitempty,min=1,max=1000000" example:"3"`
	// New discount in basis points, 0 to 10000.
	DiscountBP *int64 `json:"discount_bp,omitempty" binding:"omitempty,min=0,max=10000" example:"0"`
	// New unit price in minor units, 0 to 1000000000000.
	UnitPriceCents *int64 `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000" example:"120000"`
	// New line text, at most 255 characters, trimmed; a blank value keeps
	// the stored text.
	Description *string `json:"description,omitempty" maxLength:"255" example:"Annual support plan"`
	// New 1-based display position, at least 1. Other lines are not
	// renumbered, so positions may repeat.
	Position *int64 `json:"position,omitempty" binding:"omitempty,min=1" example:"2"`
}

// QuoteStatusRequest is the body of POST /quotes/:id/status.
type QuoteStatusRequest struct {
	// Target status. Required. Allowed moves: draft to sent; sent to
	// accepted, rejected, expired or back to draft.
	Status models.QuoteStatus `json:"status" binding:"required,oneof=draft sent accepted rejected expired" example:"sent"`
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
// @Description Create a draft quote. Roles: admin and sales.
// @Description Rules: title required (trimmed, 1 to 200 characters); notes at most 65535 bytes; valid_until YYYY-MM-DD; currency three upper-case letters, defaulting to the deals.default_currency setting (EUR as shipped). At least one of deal_id and customer_id is required. With deal_id: the deal must be live (else 400 INVALID_REFERENCE), owned by the caller when the caller is sales (403), and neither won nor lost (409); without customer_id the deal's customer is used, possibly none (such a quote cannot be emailed); a customer_id different from the deal's customer is 400 VALIDATION_ERROR. customer_id must name a live customer. owner_id defaults to the caller; a sales user may only name itself; the owner must be a live user.
// @Description The quote starts as a draft with no lines and totals of 0, whatever else was sent; its first status-history row (from_status null, to_status draft) is written in the same transaction. No automation event is recorded for quotes.
// @Description The response is the quote re-read with its number, lines and the customer, deal and owner summaries.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body QuoteRequest true "The quote to create"
// @Success 201 {object} utils.APIResponse{data=models.QuoteJSON} "Quote created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name to a message), or a rule checked by the handler or the service failed (the message names it, no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'): title over 200 characters or blank, notes over 65535 bytes, a malformed valid_until, neither deal_id nor customer_id, or a customer_id that is not the deal's customer. INVALID_REFERENCE: deal_id, customer_id or owner_id names no live row"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; a sales user named another owner ('You can only assign quotes to yourself') or quoted a deal it does not own ('You can only work on your own deals and quotes')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the deal is won or lost"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One page of live quotes with their number and customer, deal and owner summaries; lines are not loaded (lines is an empty array). Roles: admin (every quote) and sales (only the quotes it owns, whatever owner_id says; filtered in the query, so meta.total counts only those).
// @Description Filters combine (AND): status, deal_id, customer_id and owner_id. sort_by must be one of the listed columns (any other value is 400) and the id breaks ties in the same direction. Without sort_by the order is created_at descending and sort_order is ignored; with sort_by, a sort_order other than asc or desc becomes desc.
// @Description data is the array of quotes; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param status query string false "Only quotes in this status; an unknown value is 400" Enums(draft, sent, accepted, rejected, expired)
// @Param deal_id query int false "Only quotes of this deal; must be a positive integer" minimum(1)
// @Param customer_id query int false "Only quotes addressed to this customer; must be a positive integer" minimum(1)
// @Param owner_id query int false "Only quotes owned by this user; must be a positive integer. Honoured for admins; a sales caller is always narrowed to itself" minimum(1)
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.QuoteJSON,meta=utils.APIMeta} "One page of quotes"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: sort_by is not an allowed column ('Invalid sort column'), status is unknown ('Invalid status'), or deal_id, customer_id or owner_id is not a positive integer ('<name> must be a positive integer')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One live quote with its lines (ordered by position, then id), totals, number and the customer, deal and owner summaries. Roles: admin (any quote) and sales (only the quotes it owns; another owner's quote is 403, not 404).
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The quote"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Partial update of a draft quote. Roles: admin (any quote) and sales (only the quotes it owns). Every absent field keeps its stored value; deal_id is not editable. valid_until takes YYYY-MM-DD, or null or an empty string to clear it. customer_id, when it changes, must name a live customer (400 INVALID_REFERENCE) and, when the quote's deal has a customer, be that customer (400 VALIDATION_ERROR). currency may change only while the quote has no lines (409 otherwise). owner_id: a sales user may only name itself; an admin any live user (checked when it changes). The stored quote is re-read under a row lock and written only while it is still a draft.
// @Description The response is the quote re-read with its lines and summaries.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The updated quote"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID'). VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name to a message), or a rule checked by the handler or the service failed (the message names it, no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'): title over 200 characters or blank, notes over 65535 bytes, a malformed valid_until, or a customer that is not the deal's customer. INVALID_REFERENCE: a changed customer_id or owner_id names no live row"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own; or a sales user named another owner ('You can only assign quotes to yourself')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the quote is not a draft (also when a status change committed while the request ran); nothing is written; or the currency cannot change because the quote has lines"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Soft-delete a quote. Roles: admin (any quote, any status) and sales (only its own quotes, and only drafts). The lines and the history rows stay in their tables. Quotes hold no personal data, so this is not an erasure.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: a sales user asked to delete a quote that is not a draft"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Add a line priced from a product. Roles: admin (any quote) and sales (only the quotes it owns). The product must be live and active (400 INVALID_REFERENCE otherwise) and priced in the quote's currency (400 VALIDATION_ERROR). The product's name and price are copied onto the line unless description (when not blank) or unit_price_cents override them; a later change to the product never alters the line. The new line's position is the highest position plus one. quantity is 1 to 1000000, discount_bp 0 to 10000 (basis points: 1250 is 12.5 percent), unit_price_cents 0 to 10^12; quantity x unit_price_cents and the resulting quote total may not exceed 10^12, and a quote holds at most 100 lines (all 400 VALIDATION_ERROR, nothing written).
// @Description One transaction: a locking read of the quote (SELECT ... FOR UPDATE on MySQL/MariaDB), the draft check, the insert, then the totals recomputed from the lines and written only while the quote is still a draft.
// @Description The response is the whole quote with its lines and recomputed totals.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteLineRequest true "The line to add"
// @Success 201 {object} utils.APIResponse{data=models.QuoteJSON} "The quote with the new line"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID'). VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name to a message), or a rule checked by the handler or the service failed (the message names it, no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'): a bound, the 10^12 gross or total cap, the 100-line limit, a description over 255 characters, or a product in another currency. INVALID_REFERENCE: the product does not exist, is deleted or is inactive"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the quote is not a draft (also when a status change committed while the request ran); nothing is written"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Partial update of a line. Roles: admin (any quote) and sales (only the quotes it owns). Absent fields keep their values; a blank description keeps the stored text. The bounds of adding a line apply to the merged line, and position must be at least 1 (other lines are not renumbered). Same transaction shape as adding a line, with the totals recomputed.
// @Description The response is the whole quote with its lines and recomputed totals.
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param lineId path int true "Line ID"
// @Param request body QuoteLineUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The quote with the edited line"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id or lineId is not an unsigned integer ('Invalid quote ID', 'Invalid line ID'). VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name to a message), or a rule checked by the handler or the service failed (the message names it, no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format'): a bound, the 10^12 gross or total cap, or a description over 255 characters"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found'), or the line is not on this quote ('Quote line not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the quote is not a draft (also when a status change committed while the request ran); nothing is written"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Hard-delete a line and recompute the totals in one transaction (locking read, draft check, delete, conditional totals write). Roles: admin (any quote) and sales (only the quotes it owns). The remaining lines keep their positions.
// @Description The response is the whole quote with its remaining lines and recomputed totals.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param lineId path int true "Line ID"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The quote without the line"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id or lineId is not an unsigned integer ('Invalid quote ID', 'Invalid line ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found'), or the line is not on this quote ('Quote line not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the quote is not a draft (also when a status change committed while the request ran); nothing is written"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Change the status. Roles: admin (any quote) and sales (only the quotes it owns). The allowed moves are draft to sent, sent to accepted, sent to rejected, sent to expired and sent back to draft; accepted, rejected and expired are final. Any other pair, the same status again included, is 400 INVALID_STATUS_TRANSITION and writes nothing.
// @Description One transaction: a locking read of the quote, the transition check, the acceptance check (accepted is 409 once valid_until lies before today's date in the server's zone), a conditional claim (WHERE status = the stored status; a lost race is 400 INVALID_STATUS_TRANSITION), for draft to sent a locked count of the lines (none rolls everything back with 409), and one status-history row.
// @Description Timestamps: sent sets sent_at and clears closed_at; accepted, rejected and expired set closed_at; sent to draft clears both. Accepting a quote does not change its deal, and no email is sent (see POST /quotes/{id}/email).
// @Tags quotes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Param request body QuoteStatusRequest true "Target status"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The quote after the move"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID'). VALIDATION_ERROR (message 'Validation failed'): a binding rule failed; error.details maps the Go field name (for example FirstName) to a message. BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format') (status missing or not one of the five). INVALID_STATUS_TRANSITION: the move is not allowed from the current status, or another transition won the race"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: draft to sent on a quote without lines, or accepted after valid_until"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description The quote's status changes, oldest first (changed_at, then id). Roles: admin (any quote) and sales (only the quotes it owns). The first row has from_status null and to_status draft. changed_by is a summary of the acting user (id and names, no email), omitted when that account no longer exists. data is the array; there is no pagination.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=[]models.QuoteStatusChangeJSON} "The status history"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description Send the quote as plain text (number, title, currency, validity, every line, subtotal, discount, total and notes) to the linked customer's current email address. Roles: admin (any quote) and sales (only the quotes it owns). No request body. The subject is 'Quote Q-000042 from GopherCRM' for quote 42.
// @Description Only a sent or accepted quote can be emailed; a quote without a customer, whose customer was erased, or whose customer has no email address is refused; all 409 and no mail is sent. No transaction is held during the delivery; afterwards last_emailed_at is stamped by a conditional update that matches only while the quote is still sent or accepted and no revision back to draft committed since the request started (even one followed by a new send); otherwise 409 with last_emailed_at unchanged (the mail has then already gone out). With SMTP_HOST unset the server only logs the message and the request succeeds. A delivery failure is 502 MAIL_DELIVERY_FAILED. The status does not change.
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Quote ID"
// @Success 200 {object} utils.APIResponse{data=models.QuoteJSON} "The quote with last_emailed_at set"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid quote ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or FORBIDDEN 'You can only view your own quotes': a sales user addressed a quote it does not own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live quote has this id ('Quote not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the quote is not sent or accepted, has no customer, its customer was erased or has no email address, or it was taken back to draft (or left sent and accepted) during the send"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Failure 502 {object} utils.APIResponse{error=utils.APIError} "MAIL_DELIVERY_FAILED: the mailer refused or failed to deliver ('The quote email could not be delivered'; the transport's message is not echoed)"
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
// @Description One page of the live quotes of the deal, without lines. Roles: admin (every quote) and sales (only on a deal it owns, and only the quotes it owns; another owner's deal is 403). sort_by and sort_order behave as on GET /quotes.
// @Description data is the array of quotes; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Deal ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.QuoteJSON,meta=utils.APIMeta} "One page of quotes"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid deal ID') or sort_by is not an allowed column ('Invalid sort column')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales; or a sales user asked about a deal it does not own ('You can only work on your own deals and quotes')"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live deal has this id ('Deal not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
// @Description One page of the live quotes addressed to the customer, without lines. Roles: admin (every quote) and sales (only the quotes it owns; filtered in the query, so meta.total counts only those). Support and customer roles are refused. sort_by and sort_order behave as on GET /quotes.
// @Description data is the array of quotes; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags quotes
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param sort_by query string false "Sort column" Enums(id, title, status, total_cents, currency, valid_until, sent_at, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.QuoteJSON,meta=utils.APIMeta} "One page of quotes"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid customer ID') or sort_by is not an allowed column ('Invalid sort column')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live (not erased) customer has this id ('Customer not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
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
