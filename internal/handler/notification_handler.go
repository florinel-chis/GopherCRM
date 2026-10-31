package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	_ "github.com/florinel-chis/gophercrm/internal/models" // referenced by the swag annotations
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
//
// @Description Data payload of GET /notifications/unread-count.
type UnreadCountResponse struct {
	// Number of the caller's notifications that have not been marked read.
	Unread int64 `json:"unread" example:"3"`
}

// MarkAllReadResponse is the body of POST /notifications/read-all.
//
// @Description Data payload of POST /notifications/read-all.
type MarkAllReadResponse struct {
	// Number of notifications this call marked read (0 when none were unread).
	Marked int64 `json:"marked" example:"3"`
}

// List godoc
// @Summary List my notifications
// @Description One page of the caller's own notifications, newest first (created_at, then id, descending). Roles: admin, sales and support. Notifications are created only by the notify step of automation rules, and are hard-deleted when their recipient or the lead they refer to is erased. unread=true narrows to the unread ones; unread=false or absent lists all. Pagination: page (1-based) overrides offset when greater than 0; limit defaults to 20 and is clamped to 100.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of notifications to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Param unread query bool false "true lists only unread notifications; any value strconv.ParseBool accepts"
// @Success 200 {object} utils.APIResponse{data=[]models.Notification,meta=utils.APIMeta} "One page of notifications; meta carries page, per_page, total and total_pages"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - unread is not a boolean"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required (customers are refused by the route guard)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description The number of the caller's unread notifications. Roles: admin, sales and support.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=UnreadCountResponse} "Unread count"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required (customers are refused by the route guard)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Mark one of the caller's own notifications read (read_at set to now) and return it. Roles: admin, sales and support. A notification that belongs to another user is 404, exactly like a missing one. Marking an already read notification again is a no-op that keeps the original read_at. No request body.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Notification ID"
// @Success 200 {object} utils.APIResponse{data=models.Notification} "The notification, read"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required (customers are refused by the route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no notification with this id addressed to the caller"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Mark every unread notification of the caller read and return how many were marked. Roles: admin, sales and support. Nobody else's notifications are touched. No request body.
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=MarkAllReadResponse} "Count of notifications marked read"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin, sales or support role required (customers are refused by the route guard)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
