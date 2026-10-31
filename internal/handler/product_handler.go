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

type ProductHandler struct {
	productService service.ProductService
}

func NewProductHandler(productService service.ProductService) *ProductHandler {
	return &ProductHandler{productService: productService}
}

// ProductRequest is the body of POST /products. unit_price_cents is a pointer
// so that an explicit 0 (a free item) passes `required`.
type ProductRequest struct {
	// Product name, required; trimmed, it must not be empty. At most 200
	// characters.
	Name string `json:"name" binding:"required" maxLength:"200" example:"Annual support plan"`
	// Optional stock-keeping code, trimmed, at most 64 characters; not
	// unique.
	SKU string `json:"sku,omitempty" maxLength:"64" example:"SUP-ANNUAL"`
	// Free-text description, at most 65535 bytes (UTF-8 bytes, not
	// characters).
	Description string `json:"description,omitempty" example:"Business-hours support for twelve months."`
	// Price of one unit in minor units, 0 to 1000000000000. Required; 0 (a
	// free item) is accepted.
	UnitPriceCents *int64 `json:"unit_price_cents" binding:"required,min=0,max=1000000000000" example:"120000"`
	// ISO 4217 code, exactly three upper-case ASCII letters; the
	// deals.default_currency setting (EUR as shipped) when absent.
	Currency string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase" minLength:"3" maxLength:"3" example:"EUR"`
	// Whether quote lines may use the product; true when absent.
	IsActive *bool `json:"is_active,omitempty" example:"true"`
}

// ProductUpdateRequest is the body of PUT /products/:id: a partial update, every
// absent field keeps its stored value.
type ProductUpdateRequest struct {
	// New name; trimmed, it must not be empty. At most 200 characters.
	Name *string `json:"name,omitempty" maxLength:"200" example:"Annual support plan (premium)"`
	// New SKU, trimmed, at most 64 characters; an empty string clears it.
	SKU *string `json:"sku,omitempty" maxLength:"64" example:"SUP-ANNUAL-P"`
	// New description, at most 65535 bytes; an empty string clears it.
	Description *string `json:"description,omitempty" example:"Around-the-clock support for twelve months."`
	// New unit price in minor units, 0 to 1000000000000.
	UnitPriceCents *int64 `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000" example:"180000"`
	// New ISO 4217 code, exactly three upper-case ASCII letters. Existing
	// quote lines keep their prices.
	Currency *string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase" minLength:"3" maxLength:"3" example:"EUR"`
	// New active flag; false stops the product from being added to quote
	// lines.
	IsActive *bool `json:"is_active,omitempty" example:"false"`
}

// productLengthError names the first field whose value would not fit its
// column. The limits are the model's constants, which a test holds to the
// declared column widths.
func productLengthError(name, sku *string, description *string) string {
	if name != nil && utf8.RuneCountInString(*name) > models.ProductNameMaxLength {
		return fmt.Sprintf("name is too long (at most %d characters)", models.ProductNameMaxLength)
	}
	if sku != nil && utf8.RuneCountInString(*sku) > models.ProductSKUMaxLength {
		return fmt.Sprintf("sku is too long (at most %d characters)", models.ProductSKUMaxLength)
	}
	if description != nil && len(*description) > models.ProductDescriptionMaxBytes {
		return fmt.Sprintf("description is too long (at most %d bytes)", models.ProductDescriptionMaxBytes)
	}
	return ""
}

// Create godoc
// @Summary Create a product
// @Description Create a catalogue product. Role: admin. name is required (trimmed, 1 to 200 characters); sku is optional (trimmed, at most 64 characters, indexed but not unique); description at most 65535 bytes. unit_price_cents is required, an integer in minor units from 0 to 10^12. currency is three upper-case letters and defaults to the deals.default_currency setting (EUR as shipped; an unusable setting falls back to EUR). is_active defaults to true. Quote lines copy the name and the price when they are added, so a later change here never alters an existing quote.
// @Tags products
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body ProductRequest true "The product to create"
// @Success 201 {object} utils.APIResponse{data=models.Product} "Product created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name, for example UnitPriceCents, to a message), a value is longer than its column (no details), or the trimmed name is empty (no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /products [post]
func (h *ProductHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.Create")

	var req ProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := productLengthError(&req.Name, &req.SKU, &req.Description); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}

	product := &models.Product{
		Name:           req.Name,
		SKU:            req.SKU,
		Description:    req.Description,
		UnitPriceCents: *req.UnitPriceCents,
		Currency:       req.Currency,
		IsActive:       true,
	}
	if req.IsActive != nil {
		product.IsActive = *req.IsActive
	}

	if err := h.productService.Create(product); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, product)
	utils.RespondSuccess(c, http.StatusCreated, product)
}

// List godoc
// @Summary List products
// @Description One page of live products. Roles: admin and sales. Filters combine (AND): search (substring of name or sku; case-insensitive on MySQL/MariaDB with their default collations, and on SQLite for ASCII letters only), is_active and currency. sort_by must be one of the listed columns (any other value is 400) and the id breaks ties in the same direction. Without sort_by the order is created_at descending and sort_order is ignored; with sort_by, a sort_order other than asc or desc becomes desc. Soft-deleted products never appear.
// @Description data is the array of products; meta carries request_id, page (offset / limit + 1, rounded down), per_page and, when not zero, total and total_pages (both are omitted for an empty result).
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; when greater than 0 it overrides offset (offset = (page-1) x limit). Non-numeric values are ignored"
// @Param offset query int false "Number of rows to skip; negative or non-numeric values fall back to 0" default(0)
// @Param limit query int false "Page size; 0, negative or non-numeric values fall back to 20, values above 100 are clamped to 100" default(20)
// @Param search query string false "Substring searched in name and sku"
// @Param is_active query bool false "Only active (true) or inactive (false) products; anything strconv.ParseBool rejects is 400"
// @Param currency query string false "Only products priced in this currency: exactly three upper-case ASCII letters, else 400"
// @Param sort_by query string false "Sort column" Enums(id, name, sku, unit_price_cents, currency, is_active, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction, applied only with sort_by; any other value becomes desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Product,meta=utils.APIMeta} "One page of products"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: sort_by is not an allowed column ('Invalid sort column'), is_active is not a boolean ('is_active must be true or false'), or currency is not three upper-case letters ('currency must be three upper-case letters')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /products [get]
func (h *ProductHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}

	filter := repository.ProductListFilter{
		Search:    c.Query("search"),
		SortBy:    c.Query("sort_by"),
		SortOrder: c.Query("sort_order"),
	}
	if _, _, err := utils.ValidateSort("products", filter.SortBy, filter.SortOrder); err != nil {
		utils.RespondBadRequest(c, "Invalid sort column")
		return
	}
	if raw := c.Query("is_active"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			utils.RespondBadRequest(c, "is_active must be true or false")
			return
		}
		filter.IsActive = &value
	}
	if currency := c.Query("currency"); currency != "" {
		if !currencyQueryPattern.MatchString(currency) {
			utils.RespondBadRequest(c, "currency must be three upper-case letters")
			return
		}
		filter.Currency = currency
	}

	products, total, err := h.productService.List(offset, limit, filter)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, products)
	utils.RespondSuccessWithMeta(c, http.StatusOK, products, pageMeta(c, offset, limit, total))
}

// Get godoc
// @Summary Get a product
// @Description One live product, active or not. Roles: admin and sales. A soft-deleted product is a 404.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Success 200 {object} utils.APIResponse{data=models.Product} "The product"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid product ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live product has this id ('Product not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /products/{id} [get]
func (h *ProductHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.Get")

	id, ok := pathID(c, "Invalid product ID")
	if !ok {
		return
	}
	product, err := h.productService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, product)
	utils.RespondSuccess(c, http.StatusOK, product)
}

// Update godoc
// @Summary Update a product
// @Description Partial update of a product. Role: admin. Every absent field keeps its stored value; the merged product is validated with the rules of create (a name that trims to empty is refused; sku and description may be cleared with an empty string). Existing quote lines are not touched: they copied the name and the price when they were added.
// @Tags products
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Param request body ProductUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Product} "The updated product"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid product ID'). VALIDATION_ERROR: a binding rule failed (message 'Validation failed', error.details maps the Go field name, for example UnitPriceCents, to a message), a value is longer than its column (no details), or the trimmed name is empty (no details). BAD_REQUEST: the body is not valid JSON for the schema ('Invalid request format')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live product has this id ('Product not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /products/{id} [put]
func (h *ProductHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.Update")

	id, ok := pathID(c, "Invalid product ID")
	if !ok {
		return
	}
	var req ProductUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	if msg := productLengthError(req.Name, req.SKU, req.Description); msg != "" {
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, msg, nil)
		return
	}

	product, err := h.productService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	if req.Name != nil {
		product.Name = *req.Name
	}
	if req.SKU != nil {
		product.SKU = *req.SKU
	}
	if req.Description != nil {
		product.Description = *req.Description
	}
	if req.UnitPriceCents != nil {
		product.UnitPriceCents = *req.UnitPriceCents
	}
	if req.Currency != nil {
		product.Currency = *req.Currency
	}
	if req.IsActive != nil {
		product.IsActive = *req.IsActive
	}

	if err := h.productService.Update(product); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, product)
	utils.RespondSuccess(c, http.StatusOK, product)
}

// Delete godoc
// @Summary Delete a product
// @Description Soft-delete a product. Role: admin. Quote lines that reference it keep their product_id and their copied description and price; it can no longer be added to new lines. Products hold no personal data, so this is not an erasure.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: id is not an unsigned integer ('Invalid product ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: the Authorization header is missing or is neither Bearer <jwt> nor ApiKey <key> ('Missing or invalid authorization header'), or the credential is invalid, expired or belongs to a deactivated account ('Invalid credentials')"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN 'Insufficient permissions': the caller is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live product has this id ('Product not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: more than 120 requests per minute (burst 30) from this client IP, one bucket shared by every authenticated endpoint; error.details.retry_after is '60s' and no Retry-After or X-RateLimit header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /products/{id} [delete]
func (h *ProductHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.Delete")

	id, ok := pathID(c, "Invalid product ID")
	if !ok {
		return
	}
	if err := h.productService.Delete(id); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// respondError maps the product sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *ProductHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Invalid product")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case errors.Is(err, apperrors.ErrProductNotFound), apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Product not found")
		utils.RespondNotFound(c, "Product not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Product operation failed")
		utils.RespondInternalError(c)
	}
}
