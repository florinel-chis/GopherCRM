package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// NotificationHandler serves a staff user's own in-app notifications.
type NotificationHandler struct {
	notificationService service.NotificationService
}

func NewNotificationHandler(notificationService service.NotificationService) *NotificationHandler {
	return &NotificationHandler{notificationService: notificationService}
}

// UnreadCountResponse is the body of GET /notifications/unread-count.
type UnreadCountResponse struct {
	Unread int64 `json:"unread"`
}

// MarkAllReadResponse is the body of POST /notifications/read-all.
type MarkAllReadResponse struct {
	Marked int64 `json:"marked"`
}

// List godoc
// @Summary List my notifications
// @Description One page of the caller's own notifications, newest first (admin, sales and support; customers get 403). unread=true narrows to the unread ones. Pagination goes through the shared parser.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param unread query bool false "Only unread notifications"
// @Success 200 {object} utils.APIResponse{data=[]models.Notification,meta=utils.APIMeta} "Notifications retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "unread is not a boolean"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - staff role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /notifications [get]
func (h *NotificationHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "NotificationHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	if page, _ := strconv.Atoi(c.DefaultQuery("page", "0")); page > 0 {
		offset = (page - 1) * limit
	}
	unreadOnly := false
	if raw := c.Query("unread"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			utils.RespondBadRequest(c, "unread must be true or false")
			return
		}
		unreadOnly = value
	}

	notifications, total, err := h.notificationService.List(c.GetUint("user_id"), unreadOnly, offset, limit)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, notifications)
	utils.RespondSuccessWithMeta(c, http.StatusOK, notifications, pageMeta(c, offset, limit, total))
}

// UnreadCount godoc
// @Summary Count my unread notifications
// @Description The caller's unread notification count (admin, sales and support; customers get 403).
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=UnreadCountResponse} "Unread count"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - staff role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /notifications/unread-count [get]
func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "NotificationHandler.UnreadCount")
	count, err := h.notificationService.UnreadCount(c.GetUint("user_id"))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, UnreadCountResponse{Unread: count})
}

// MarkRead godoc
// @Summary Mark a notification read
// @Description Mark the caller's own notification read and return it; one that belongs to another user, or does not exist, is 404. Marking an already read notification again is a no-op.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Notification ID"
// @Success 200 {object} utils.APIResponse{data=models.Notification} "Notification marked read"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid notification ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - staff role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Notification not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /notifications/{id}/read [post]
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "NotificationHandler.MarkRead")
	id, ok := pathID(c, "Invalid notification ID")
	if !ok {
		return
	}
	notification, err := h.notificationService.MarkRead(c.GetUint("user_id"), id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, notification)
	utils.RespondSuccess(c, http.StatusOK, notification)
}

// MarkAllRead godoc
// @Summary Mark all my notifications read
// @Description Mark every unread notification of the caller read and return how many were marked. Nobody else's notifications are touched.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=MarkAllReadResponse} "Count of notifications marked read"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - staff role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /notifications/read-all [post]
func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "NotificationHandler.MarkAllRead")
	marked, err := h.notificationService.MarkAllRead(c.GetUint("user_id"))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, MarkAllReadResponse{Marked: marked})
}

func (h *NotificationHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrNotificationNotFound), apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Notification not found")
		utils.RespondNotFound(c, "Notification not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Notification request failed")
		utils.RespondInternalError(c)
	}
}
