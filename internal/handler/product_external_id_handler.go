package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Product external-id mappings (#239): POST and DELETE
// /products/{id}/external-ids, and the lookup mode of GET /products. No log
// line of these paths carries a source, scope or external_id value; the
// validation answers name the field and the rule, never the value.

// WithExternalIDs hands the handler the external-id service. Without it the
// mapping routes and the lookup mode of GET /products answer 500.
func (h *ProductHandler) WithExternalIDs(externalIDs service.ProductExternalIDService) *ProductHandler {
	h.externalIDs = externalIDs
	return h
}

// CreateProductExternalIDRequest is the body of POST /products/{id}/external-ids.
//
// @Description Body of POST /products/{id}/external-ids. Each value must be valid UTF-8 without control characters and without leading or trailing whitespace; it is stored and compared exactly (case-sensitive). The service, not the binder, enforces the required fields.
type CreateProductExternalIDRequest struct {
	// System that issued the identifier; required, 1-64 characters.
	Source string `json:"source" validate:"required" minLength:"1" maxLength:"64" example:"erp"`
	// Namespace inside the source, for example a company code; optional,
	// 0-128 characters, absent or empty means ''.
	Scope string `json:"scope,omitempty" maxLength:"128" example:"plant-01"`
	// The identifier in that source and scope; required, 1-255 characters.
	ExternalID string `json:"external_id" validate:"required" minLength:"1" maxLength:"255" example:"MAT-100042"`
}

// productExternalIDWriterRole repeats the route guard: admin only, as every
// product write is.
func productExternalIDWriterRole(c *gin.Context) bool {
	return c.GetString("user_role") == string(models.RoleAdmin)
}

// CreateExternalID godoc
// @Summary Map an external identity to a product
// @Description Admin only (route guard, repeated in the handler); sales gets 403, support and customer get 403 on every product route. Maps the (source, scope, external_id) triple to the live product, active or not, in one transaction that locks the product row and inserts the mapping (#239). The triple is unique across all products and compared exactly (case-sensitive): a triple already mapped, to this product or another one, is 409. source is 1-64, scope 0-128 (absent means the empty string) and external_id 1-255 characters; each must be valid UTF-8 without control characters or leading or trailing whitespace. A missing or deleted product is 404 and nothing is written. Deleting the product later hard-deletes its mappings, after which the triple can be mapped again. products.sku is unrelated and stays non-unique. There is no update: delete and re-create instead.
// @Tags products
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Param request body CreateProductExternalIDRequest true "The external identity"
// @Success 201 {object} utils.APIResponse{data=models.ProductExternalID} "Mapping created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid product ID, or a body that does not bind (Invalid request format). VALIDATION_ERROR: a refused field; details.field names it (source, scope or external_id) and details.reason the rule, never the value"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live product with this id ('Product not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the triple is already mapped to a product"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the mapping could not be stored"
// @Router /products/{id}/external-ids [post]
func (h *ProductHandler) CreateExternalID(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.CreateExternalID")

	if !productExternalIDWriterRole(c) {
		utils.RespondForbidden(c, "Insufficient permissions")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, "Invalid product ID")
		return
	}

	var req CreateProductExternalIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondBadRequest(c, "Invalid request format")
		return
	}

	if h.externalIDs == nil {
		logger.Error("Product external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	mapping, err := h.externalIDs.AddExternalID(c.Request.Context(), uint(id), req.Source, req.Scope, req.ExternalID)
	if err != nil {
		logger = logger.WithField("product_id", id)
		var fieldErr *service.CustomerExternalIDFieldError
		switch {
		case errors.As(err, &fieldErr):
			logger.WithError(utils.LogError(err)).Warn("Product external id refused by validation")
			respondExternalIDFieldError(c, fieldErr)
		case errors.Is(err, apperrors.ErrNotFound), errors.Is(err, apperrors.ErrProductNotFound):
			logger.Warn("Product external id refused: product not found")
			utils.RespondNotFound(c, "Product not found")
		case errors.Is(err, apperrors.ErrProductExternalIDTaken):
			logger.Warn("Product external id refused: already mapped")
			utils.RespondConflict(c, "external id is already mapped to a product")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to add product external id")
			utils.RespondInternalError(c)
		}
		return
	}

	logger.WithFields(logrus.Fields{"product_id": id, "mapping_id": mapping.ID}).Info("Product external id created")
	utils.LogHandlerResponse(logger, http.StatusCreated, nil)
	utils.RespondSuccess(c, http.StatusCreated, mapping)
}

// DeleteExternalID godoc
// @Summary Delete an external identity mapping of a product
// @Description Admin only (route guard, repeated in the handler); sales gets 403, support and customer get 403 on every product route. Hard-deletes the mapping when it belongs to the product at the path (#239); the triple can then be mapped again. A mapping id that does not exist or belongs to another product is 404 and nothing is deleted.
// @Tags products
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Product ID"
// @Param mappingId path int true "Mapping ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid product ID or Invalid mapping ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the product has no mapping with this id ('External id mapping not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the mapping could not be deleted"
// @Router /products/{id}/external-ids/{mappingId} [delete]
func (h *ProductHandler) DeleteExternalID(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "ProductHandler.DeleteExternalID")

	if !productExternalIDWriterRole(c) {
		utils.RespondForbidden(c, "Insufficient permissions")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, "Invalid product ID")
		return
	}
	mappingID, err := strconv.ParseUint(c.Param("mappingId"), 10, 32)
	if err != nil || mappingID == 0 {
		utils.RespondBadRequest(c, "Invalid mapping ID")
		return
	}

	if h.externalIDs == nil {
		logger.Error("Product external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	logger = logger.WithFields(logrus.Fields{"product_id": id, "mapping_id": mappingID})
	if err := h.externalIDs.RemoveExternalID(c.Request.Context(), uint(id), uint(mappingID)); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.Warn("Product external id not found")
			utils.RespondNotFound(c, "External id mapping not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete product external id")
		utils.RespondInternalError(c)
		return
	}

	logger.Info("Product external id deleted")
	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// isProductExternalIDLookup reports whether GET /products carries any of the
// lookup parameters, even an empty one.
func isProductExternalIDLookup(c *gin.Context) bool {
	_, hasSource := c.GetQuery("source")
	_, hasScope := c.GetQuery("scope")
	_, hasExternalID := c.GetQuery("external_id")
	return hasSource || hasScope || hasExternalID
}

// listByExternalID answers the lookup mode of GET /products with the list
// shape (data an array) holding at most one live product. The parameters are
// validated here, before any service call; search, is_active, currency,
// sort_by, sort_order, page and offset are ignored.
func (h *ProductHandler) listByExternalID(c *gin.Context, logger *logrus.Entry) {
	source := c.Query("source")
	scope := c.Query("scope")
	externalID := c.Query("external_id")
	_, limit := utils.ParseOffsetLimit(c)

	if err := service.ValidateProductExternalIdentity(source, scope, externalID); err != nil {
		var fieldErr *service.CustomerExternalIDFieldError
		if errors.As(err, &fieldErr) {
			logger.WithError(utils.LogError(err)).Warn("Product external id lookup refused by validation")
			respondExternalIDFieldError(c, fieldErr)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Product external id lookup validation failed")
		utils.RespondInternalError(c)
		return
	}

	if h.externalIDs == nil {
		logger.Error("Product external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	product, err := h.externalIDs.LookupProduct(c.Request.Context(), source, scope, externalID)
	if err != nil {
		var fieldErr *service.CustomerExternalIDFieldError
		if errors.As(err, &fieldErr) {
			logger.WithError(utils.LogError(err)).Warn("Product external id lookup refused by validation")
			respondExternalIDFieldError(c, fieldErr)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up product by external id")
		utils.RespondInternalError(c)
		return
	}

	products := []models.Product{}
	if product != nil {
		products = append(products, *product)
	}
	total := int64(len(products))
	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
	logger.WithField("count", total).Debug("Product external id lookup answered")
	utils.LogHandlerResponse(logger, http.StatusOK, products)
	utils.RespondSuccessWithMeta(c, http.StatusOK, products, meta)
}
