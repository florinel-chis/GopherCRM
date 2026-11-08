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

// Customer external-id mappings (#212): POST and DELETE
// /customers/{id}/external-ids, and the lookup mode of GET /customers. No log
// line of these paths carries a source, scope or external_id value; the
// validation answers name the field and the rule, never the value.

// WithExternalIDs hands the handler the external-id service. Without it the
// mapping routes and the lookup mode of GET /customers answer 500.
func (h *CustomerHandler) WithExternalIDs(externalIDs service.CustomerExternalIDService) *CustomerHandler {
	h.externalIDs = externalIDs
	return h
}

// CreateCustomerExternalIDRequest is the body of POST /customers/{id}/external-ids.
//
// @Description Body of POST /customers/{id}/external-ids. Each value must be valid UTF-8 without control characters and without leading or trailing whitespace; it is stored and compared exactly (case-sensitive). The service, not the binder, enforces the required fields.
type CreateCustomerExternalIDRequest struct {
	// System that issued the identifier; required, 1-64 characters.
	Source string `json:"source" validate:"required" minLength:"1" maxLength:"64" example:"shopify"`
	// Namespace inside the source, for example a storefront site; optional,
	// 0-128 characters, absent or empty means ''.
	Scope string `json:"scope,omitempty" maxLength:"128" example:"store-eu"`
	// The identifier in that source and scope; required, 1-255 characters.
	ExternalID string `json:"external_id" validate:"required" minLength:"1" maxLength:"255" example:"gid-1001"`
}

// respondExternalIDFieldError answers a refused field with 400
// VALIDATION_ERROR naming the field and the rule, never the value.
func respondExternalIDFieldError(c *gin.Context, fieldErr *service.CustomerExternalIDFieldError) {
	utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, fieldErr.Error(),
		gin.H{"field": fieldErr.Field, "reason": fieldErr.Reason})
}

// externalIDWriterRole repeats the route guard: admin and sales, the roles
// that may update a customer.
func externalIDWriterRole(c *gin.Context) bool {
	role := c.GetString("user_role")
	return role == string(models.RoleAdmin) || role == string(models.RoleSales)
}

// CreateExternalID godoc
// @Summary Map an external identity to a customer
// @Description Admin and sales (route guard, repeated in the handler); support and customer get 403. Maps the (source, scope, external_id) triple to the live customer in one transaction that locks the customer row and inserts the mapping (#212). The triple is unique across all customers and compared exactly (case-sensitive): a triple already mapped, to this customer or another one, is 409. source is 1-64, scope 0-128 (absent means ”) and external_id 1-255 characters; each must be valid UTF-8 without control characters or leading or trailing whitespace. A missing or erased customer is 404 and nothing is written. Erasing the customer later deletes its mappings, after which the triple can be mapped again. There is no update: delete and re-create instead.
// @Tags customers
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param request body CreateCustomerExternalIDRequest true "The external identity"
// @Success 201 {object} utils.APIResponse{data=models.CustomerExternalID} "Mapping created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID, or a body that does not bind (Invalid request format). VALIDATION_ERROR: a refused field; details.field names it (source, scope or external_id) and details.reason the rule, never the value"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is neither admin nor sales (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live customer with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: the triple is already mapped to a customer"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the mapping could not be stored"
// @Router /customers/{id}/external-ids [post]
func (h *CustomerHandler) CreateExternalID(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.CreateExternalID")

	if !externalIDWriterRole(c) {
		utils.RespondForbidden(c, "Insufficient permissions to update customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}

	var req CreateCustomerExternalIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondBadRequest(c, "Invalid request format")
		return
	}

	if h.externalIDs == nil {
		logger.Error("Customer external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	mapping, err := h.externalIDs.AddExternalID(c.Request.Context(), uint(id), req.Source, req.Scope, req.ExternalID)
	if err != nil {
		logger = logger.WithField("customer_id", id)
		var fieldErr *service.CustomerExternalIDFieldError
		switch {
		case errors.As(err, &fieldErr):
			logger.WithError(utils.LogError(err)).Warn("Customer external id refused by validation")
			respondExternalIDFieldError(c, fieldErr)
		case errors.Is(err, apperrors.ErrNotFound):
			logger.Warn("Customer external id refused: customer not found")
			utils.RespondNotFound(c, "Customer not found")
		case errors.Is(err, apperrors.ErrCustomerExternalIDTaken):
			logger.Warn("Customer external id refused: already mapped")
			utils.RespondConflict(c, "external id is already mapped to a customer")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to add customer external id")
			utils.RespondInternalError(c)
		}
		return
	}

	logger.WithFields(logrus.Fields{"customer_id": id, "mapping_id": mapping.ID}).Info("Customer external id created")
	utils.LogHandlerResponse(logger, http.StatusCreated, nil)
	utils.RespondSuccess(c, http.StatusCreated, mapping)
}

// DeleteExternalID godoc
// @Summary Delete an external identity mapping of a customer
// @Description Admin and sales (route guard, repeated in the handler); support and customer get 403. Hard-deletes the mapping when it belongs to the customer at the path (#212); the triple can then be mapped again. A mapping id that does not exist or belongs to another customer is 404 and nothing is deleted.
// @Tags customers
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Customer ID"
// @Param mappingId path int true "Mapping ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid customer ID or Invalid mapping ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is neither admin nor sales (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the customer has no mapping with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the mapping could not be deleted"
// @Router /customers/{id}/external-ids/{mappingId} [delete]
func (h *CustomerHandler) DeleteExternalID(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "CustomerHandler.DeleteExternalID")

	if !externalIDWriterRole(c) {
		utils.RespondForbidden(c, "Insufficient permissions to update customers")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, "Invalid customer ID")
		return
	}
	mappingID, err := strconv.ParseUint(c.Param("mappingId"), 10, 32)
	if err != nil || mappingID == 0 {
		utils.RespondBadRequest(c, "Invalid mapping ID")
		return
	}

	if h.externalIDs == nil {
		logger.Error("Customer external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	logger = logger.WithFields(logrus.Fields{"customer_id": id, "mapping_id": mappingID})
	if err := h.externalIDs.RemoveExternalID(c.Request.Context(), uint(id), uint(mappingID)); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.Warn("Customer external id not found")
			utils.RespondNotFound(c, "External id mapping not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete customer external id")
		utils.RespondInternalError(c)
		return
	}

	logger.Info("Customer external id deleted")
	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// isExternalIDLookup reports whether GET /customers carries any of the
// lookup parameters, even an empty one.
func (h *CustomerHandler) isExternalIDLookup(c *gin.Context) bool {
	_, hasSource := c.GetQuery("source")
	_, hasScope := c.GetQuery("scope")
	_, hasExternalID := c.GetQuery("external_id")
	return hasSource || hasScope || hasExternalID
}

// listByExternalID answers the lookup mode of GET /customers with the list
// shape holding at most one live customer. The parameters are validated here,
// before any service call; search, sort_by, page and offset are ignored.
func (h *CustomerHandler) listByExternalID(c *gin.Context, logger *logrus.Entry) {
	source := c.Query("source")
	scope := c.Query("scope")
	externalID := c.Query("external_id")
	_, limit := utils.ParseOffsetLimit(c)

	if err := service.ValidateCustomerExternalIdentity(source, scope, externalID); err != nil {
		var fieldErr *service.CustomerExternalIDFieldError
		if errors.As(err, &fieldErr) {
			logger.WithError(utils.LogError(err)).Warn("Customer external id lookup refused by validation")
			respondExternalIDFieldError(c, fieldErr)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Customer external id lookup validation failed")
		utils.RespondInternalError(c)
		return
	}

	if h.externalIDs == nil {
		logger.Error("Customer external ids are not wired")
		utils.RespondInternalError(c)
		return
	}

	customer, err := h.externalIDs.LookupCustomer(c.Request.Context(), source, scope, externalID)
	if err != nil {
		var fieldErr *service.CustomerExternalIDFieldError
		if errors.As(err, &fieldErr) {
			logger.WithError(utils.LogError(err)).Warn("Customer external id lookup refused by validation")
			respondExternalIDFieldError(c, fieldErr)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer by external id")
		utils.RespondInternalError(c)
		return
	}

	customers := []models.Customer{}
	if customer != nil {
		customers = append(customers, *customer)
	}
	total := int64(len(customers))
	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
	logger.WithField("count", total).Debug("Customer external id lookup answered")
	responseData := gin.H{"customers": customers, "total": total}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccessWithMeta(c, http.StatusOK, responseData, meta)
}
