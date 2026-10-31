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

type LabelHandler struct {
	labelService service.LabelService
}

func NewLabelHandler(labelService service.LabelService) *LabelHandler {
	return &LabelHandler{labelService: labelService}
}

// CreateLabelRequest is the body of POST /labels. `hexcolor` plus `len=7`
// together admit exactly #RRGGBB — `hexcolor` alone would also accept the
// three-digit #RGB shorthand, which the colour column and the frontend swatch
// picker do not use.
//
// @Description Body of POST /labels.
type CreateLabelRequest struct {
	// Display name, 1 to 50 characters. Trimmed before it is stored; blank after trimming is 400. Must be unique case-insensitively (409).
	Name string `json:"name" binding:"required,max=50" example:"Follow-up"`
	// Colour as #RRGGBB: a hash and exactly six hex digits (the three-digit shorthand is refused).
	Color string `json:"color" binding:"required,hexcolor,len=7" example:"#FF8800"`
}

// UpdateLabelRequest is the body of PUT /labels/:id. Both fields are optional;
// an absent field leaves that part of the label alone.
//
// @Description Body of PUT /labels/{id}. Both fields are optional; an absent or empty field is left unchanged.
type UpdateLabelRequest struct {
	// New name, at most 50 characters, trimmed and unique case-insensitively among the other labels; absent or empty keeps the current name.
	Name string `json:"name,omitempty" binding:"omitempty,max=50" example:"Waiting on customer"`
	// New colour as #RRGGBB; absent or empty keeps the current colour.
	Color string `json:"color,omitempty" binding:"omitempty,hexcolor,len=7" example:"#3366CC"`
}

// List godoc
// @Summary List labels
// @Description Every label, ordered by name ascending, as a bare array (no pagination). Roles: every authenticated role. Each label carries task_count, the number of live (not soft-deleted) tasks using it, counted across all assignees whatever the caller's role.
// @Tags labels
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.Label} "All labels; an empty array when none"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /labels [get]
func (h *LabelHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LabelHandler.List")

	labels, err := h.labelService.List()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list labels")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, labels)
	utils.RespondSuccess(c, http.StatusOK, labels)
}

// Create godoc
// @Summary Create a label
// @Description Create a label. Roles: admin, sales and support (customers get 403 from the route guard). The name is trimmed before it is stored and must be unique case-insensitively, so Urgent and urgent cannot coexist. The colour must be a hash followed by six hex digits, e.g. #FF8800.
// @Tags labels
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateLabelRequest true "Label to create"
// @Success 201 {object} utils.APIResponse{data=models.Label} "Label created; task_count is 0"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; BAD_REQUEST when the name is blank after trimming or the colour is malformed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - a label with this name (case-insensitively) already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /labels [post]
func (h *LabelHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LabelHandler.Create")

	var req CreateLabelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	label := &models.Label{Name: req.Name, Color: req.Color}
	if err := h.labelService.Create(label); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, label)
	utils.RespondSuccess(c, http.StatusCreated, label)
}

// Update godoc
// @Summary Update a label
// @Description Rename or recolour a label. Roles: admin, sales and support. Only non-empty fields are applied; a label may keep its own name, so a pure recolour is never a duplicate. The checks run in this order: id, body binding, lookup (404), name and colour validation, uniqueness. The response carries the refreshed task_count. Tasks carrying the label show the change on their next read.
// @Tags labels
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Label ID"
// @Param request body UpdateLabelRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.Label} "The updated label"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here), the name is blank after trimming or the colour is malformed; VALIDATION_ERROR when a binding rule fails (error.details maps each failing field, by its Go struct field name such as Title or LabelIDs[0], to a message); BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no label with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - another label already has this name (case-insensitively)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /labels/{id} [put]
func (h *LabelHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LabelHandler.Update")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid label ID")
		return
	}

	var req UpdateLabelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	label, err := h.labelService.GetByID(uint(id))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	if req.Name != "" {
		label.Name = req.Name
	}
	if req.Color != "" {
		label.Color = req.Color
	}

	if err := h.labelService.Update(label); err != nil {
		h.respondError(c, logger, err)
		return
	}

	// Re-read so the response carries the task count the list endpoint would
	// report, rather than the one captured before the write.
	if refreshed, err := h.labelService.GetByID(uint(id)); err == nil {
		label = refreshed
	}

	utils.LogHandlerResponse(logger, http.StatusOK, label)
	utils.RespondSuccess(c, http.StatusOK, label)
}

// Delete godoc
// @Summary Delete a label
// @Description Delete a label and detach it from every task that carries it, in one transaction. Roles: admin only (route guard). The deletion is permanent: labels hold no personal data and are not soft-deleted, so the row is gone and its name can be reused at once. The tasks themselves are untouched.
// @Tags labels
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Label ID"
// @Success 204 "Label deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no label with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /labels/{id} [delete]
func (h *LabelHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LabelHandler.Delete")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid label ID")
		return
	}

	if err := h.labelService.Delete(uint(id)); err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// respondError maps the label sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *LabelHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrDuplicateLabelName):
		logger.WithError(utils.LogError(err)).Warn("Duplicate label name")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrInvalidLabelName), errors.Is(err, apperrors.ErrInvalidLabelColor):
		logger.WithError(utils.LogError(err)).Warn("Invalid label")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Label not found")
		utils.RespondNotFound(c, "Label not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Label operation failed")
		utils.RespondInternalError(c)
	}
}
