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
type CreateLabelRequest struct {
	Name  string `json:"name" binding:"required,max=50"`
	Color string `json:"color" binding:"required,hexcolor,len=7"`
}

// UpdateLabelRequest is the body of PUT /labels/:id. Both fields are optional;
// an absent field leaves that part of the label alone.
type UpdateLabelRequest struct {
	Name  string `json:"name,omitempty" binding:"omitempty,max=50"`
	Color string `json:"color,omitempty" binding:"omitempty,hexcolor,len=7"`
}

// List godoc
// @Summary List labels
// @Description Every label, ordered by name ascending, as a bare array. Each label carries task_count, the number of live (not soft-deleted) tasks currently using it. Available to every authenticated role.
// @Tags labels
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.Label} "Labels retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /labels [get]
func (h *LabelHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "LabelHandler.List")

	labels, err := h.labelService.List()
	if err != nil {
		logger.WithError(err).Error("Failed to list labels")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, labels)
	utils.RespondSuccess(c, http.StatusOK, labels)
}

// Create godoc
// @Summary Create a label
// @Description Create a label (admin, sales and support roles only). The name is trimmed before it is stored and must be unique case-insensitively, so "Urgent" and "urgent" cannot coexist. The colour must be a six-digit hex value with a leading hash, e.g. #FF8800.
// @Tags labels
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateLabelRequest true "Label creation request"
// @Success 201 {object} utils.APIResponse{data=models.Label} "Label created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, blank name or malformed colour"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A label with this name already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Rename or recolour a label (admin, sales and support roles only). Only the fields present in the request body are applied; a label may keep its own name, so a pure recolour is never reported as a duplicate. The response carries the refreshed task_count.
// @Tags labels
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Label ID"
// @Param request body UpdateLabelRequest true "Label update request"
// @Success 200 {object} utils.APIResponse{data=models.Label} "Label updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid label ID, invalid request data, blank name or malformed colour"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Label not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A label with this name already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Delete a label (admin role only) and detach it from every task that carries it, in one transaction. The deletion is permanent: labels hold no personal data and are not soft-deleted, so the row is gone rather than hidden. The tasks themselves are untouched.
// @Tags labels
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Label ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid label ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Label not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Warn("Duplicate label name")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrInvalidLabelName), errors.Is(err, apperrors.ErrInvalidLabelColor):
		logger.WithError(err).Warn("Invalid label")
		utils.RespondBadRequest(c, err.Error())
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("Label not found")
		utils.RespondNotFound(c, "Label not found")
	default:
		logger.WithError(err).Error("Label operation failed")
		utils.RespondInternalError(c)
	}
}
