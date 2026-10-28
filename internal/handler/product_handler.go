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
	Name           string `json:"name" binding:"required"`
	SKU            string `json:"sku,omitempty"`
	Description    string `json:"description,omitempty"`
	UnitPriceCents *int64 `json:"unit_price_cents" binding:"required,min=0,max=1000000000000"`
	Currency       string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase"`
	IsActive       *bool  `json:"is_active,omitempty"`
}

// ProductUpdateRequest is the body of PUT /products/:id: a partial update, every
// absent field keeps its stored value.
type ProductUpdateRequest struct {
	Name           *string `json:"name,omitempty"`
	SKU            *string `json:"sku,omitempty"`
	Description    *string `json:"description,omitempty"`
	UnitPriceCents *int64  `json:"unit_price_cents,omitempty" binding:"omitempty,min=0,max=1000000000000"`
	Currency       *string `json:"currency,omitempty" binding:"omitempty,len=3,alpha,uppercase"`
	IsActive       *bool   `json:"is_active,omitempty"`
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
// @Description Create a catalogue product (admin role only). name is trimmed and required, 1 to 200 characters; sku is optional, at most 64 characters, indexed but not unique. unit_price_cents is an integer in minor units, 0 to 1000000000000 (10^12 cents); currency is three upper-case letters and defaults to the deals.default_currency configuration (EUR as shipped). is_active defaults to true. Quote lines copy the name and the price when they are added, so a later change here never alters an existing quote.
// @Tags products
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body ProductRequest true "Product"
// @Success 201 {object} utils.APIResponse{data=models.Product} "Product created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data (VALIDATION_ERROR): a missing name, a value longer than its column, a price outside the bound or a bad currency"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description One page of live products (admin and sales roles). offset and limit go through the shared pagination parser: limit defaults to 20, is capped at 100 and is never 0. search matches name and SKU (substring; case-insensitive on MySQL and MariaDB, case-sensitive on SQLite). is_active (true|false) and currency (three upper-case letters) narrow the result. sort_by must be one of the listed columns — any other value is a 400, never interpolated — and defaults to created_at; sort_order defaults to desc. Soft-deleted products never appear.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param search query string false "Substring search across name and SKU"
// @Param is_active query bool false "Only active (true) or inactive (false) products"
// @Param currency query string false "Only products priced in this currency (three upper-case letters)"
// @Param sort_by query string false "Sort column" Enums(id, name, sku, unit_price_cents, currency, is_active, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction; anything else falls back to desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.Product,meta=utils.APIMeta} "Products retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column, or is_active or currency is malformed (BAD_REQUEST)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description One live product (admin and sales roles). A soft-deleted product is a 404.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Success 200 {object} utils.APIResponse{data=models.Product} "Product retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid product ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Product not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Partial update of a product (admin role only): every absent field keeps its stored value, with the same bounds as on create. Existing quote lines are not touched; they copied the name and the price when they were added.
// @Tags products
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Param request body ProductUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Product} "Product updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid product ID or request data (VALIDATION_ERROR)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Product not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Soft-delete a product (admin role only). Quote lines that reference it keep their copied description and price. Products hold no personal data, so this is not an erasure.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid product ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Product not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
