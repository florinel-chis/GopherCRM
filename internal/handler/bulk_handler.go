package handler

import (
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type BulkHandler struct {
	bulkService service.BulkOperationService
}

func NewBulkHandler(bulkService service.BulkOperationService) *BulkHandler {
	return &BulkHandler{bulkService: bulkService}
}

// Generic bulk operation handlers

// BulkCreate handles POST /{resource}/bulk/create
func (h *BulkHandler) BulkCreate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkCreate")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasCreatePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk create")
		return
	}

	response, err := h.bulkService.ProcessBulkCreate(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk create failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkUpdate handles PUT /{resource}/bulk/update
func (h *BulkHandler) BulkUpdate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdate")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasUpdatePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk update")
		return
	}

	response, err := h.bulkService.ProcessBulkUpdate(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk update failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkDelete handles DELETE /{resource}/bulk/delete
func (h *BulkHandler) BulkDelete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkDelete")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type
	if !h.hasDeletePermission(c, resourceType) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk delete")
		return
	}

	response, err := h.bulkService.ProcessBulkDelete(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk delete failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// BulkAction handles POST /{resource}/bulk/action
func (h *BulkHandler) BulkAction(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkAction")
	
	resourceType := c.Param("resource")
	userID := c.GetUint("user_id")
	
	var req models.BulkActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Validate resource type
	if !h.isValidResourceType(resourceType) {
		utils.RespondBadRequest(c, "Invalid resource type")
		return
	}

	// Check permissions for the resource type and action
	if !h.hasActionPermission(c, resourceType, req.Action) {
		utils.RespondForbidden(c, "Insufficient permissions for bulk action")
		return
	}

	response, err := h.bulkService.ProcessBulkAction(userID, resourceType, &req)
	if err != nil {
		logger.WithError(err).Error("Bulk action failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Bulk operation status and management

// GetBulkOperation handles GET /bulk/operations/{id}
func (h *BulkHandler) GetBulkOperation(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.GetBulkOperation")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid operation ID")
		return
	}

	operation, err := h.bulkService.GetBulkOperationWithItems(uint(id))
	if err != nil {
		logger.WithError(err).Warn("Bulk operation not found")
		utils.RespondNotFound(c, "Bulk operation not found")
		return
	}

	// Check permissions - users can only view their own operations unless admin
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	if operation.UserID != currentUserID && currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "You can only view your own bulk operations")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, operation)
	utils.RespondSuccess(c, http.StatusOK, operation)
}

// ListBulkOperations handles GET /bulk/operations
func (h *BulkHandler) ListBulkOperations(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.ListBulkOperations")
	
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	
	if limit > 100 {
		limit = 100
	}

	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")

	var operations []models.BulkOperation
	var err error

	// Admin can see all operations, others only their own
	if currentUserRole == string(models.RoleAdmin) {
		// For admin, we would need to add a method to list all operations
		// For now, list user operations
		operations, err = h.bulkService.GetUserBulkOperations(currentUserID, offset, limit)
	} else {
		operations, err = h.bulkService.GetUserBulkOperations(currentUserID, offset, limit)
	}

	if err != nil {
		logger.WithError(err).Error("Failed to list bulk operations")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID: c.GetString("request_id"),
		Page:      (offset / limit) + 1,
		PerPage:   limit,
		Total:     int64(len(operations)), // This should be actual count from service
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"operations": operations})
	utils.RespondSuccessWithMeta(c, http.StatusOK, operations, meta)
}

// Resource-specific bulk handlers (for convenience and specific validation)

// User bulk operations
func (h *BulkHandler) BulkCreateUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkCreateUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk create users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk create users")
		return
	}
	
	var req models.BulkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkCreateUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk create users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkUpdateUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkUpdateUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk update users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk update users")
		return
	}
	
	var req models.BulkUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkUpdateUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk update users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkDeleteUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkDeleteUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can bulk delete users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can bulk delete users")
		return
	}
	
	var req models.BulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkDeleteUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk delete users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

func (h *BulkHandler) BulkActionUsers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "BulkHandler.BulkActionUsers")
	
	userID := c.GetUint("user_id")
	userRole := c.GetString("user_role")
	
	// Only admins can perform bulk actions on users
	if userRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can perform bulk actions on users")
		return
	}
	
	var req models.BulkActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	response, err := h.bulkService.BulkActionUsers(&req, userID)
	if err != nil {
		logger.WithError(err).Error("Bulk action users failed")
		apperrors.HandleError(c, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Helper methods

func (h *BulkHandler) isValidResourceType(resourceType string) bool {
	validTypes := []string{"users", "leads", "customers", "tasks", "tickets"}
	for _, validType := range validTypes {
		if resourceType == validType {
			return true
		}
	}
	return false
}

func (h *BulkHandler) hasCreatePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		return true // All authenticated users can create tasks and tickets
	default:
		return false
	}
}

func (h *BulkHandler) hasUpdatePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		return true // All authenticated users can update tasks and tickets
	default:
		return false
	}
}

func (h *BulkHandler) hasDeletePermission(c *gin.Context, resourceType string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin)
	case "tasks", "tickets":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales) || userRole == string(models.RoleSupport)
	default:
		return false
	}
}

func (h *BulkHandler) hasActionPermission(c *gin.Context, resourceType, action string) bool {
	userRole := c.GetString("user_role")
	
	switch resourceType {
	case "users":
		return userRole == string(models.RoleAdmin)
	case "leads":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "customers":
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales)
	case "tasks", "tickets":
		// Most task/ticket actions can be performed by admin, sales, and support
		return userRole == string(models.RoleAdmin) || userRole == string(models.RoleSales) || userRole == string(models.RoleSupport)
	default:
		return false
	}
}