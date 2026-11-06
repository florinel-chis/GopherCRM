package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// formDeliveryNotRetryableCode answers a retry the delivery's status,
// submission, form or recipient no longer allows (409).
const formDeliveryNotRetryableCode = "FORM_DELIVERY_NOT_RETRYABLE"

// ListDeliveries godoc
// @Summary List form mail deliveries that need attention
// @Description One page of form mail delivery records across every form, newest first (id descending). Each record is one mail of one submission: the double opt-in confirmation, the follow-up or one notification per notify_emails address. It carries ids, the kind, the status, an error code and attempt timestamps; never an address or message content.
// @Description Without status the list holds the rows that need attention: failed, log_only (logged by the development mailer because SMTP_HOST is not set, nothing delivered) and stuck (pending or sending for more than 15 minutes). With status it holds exactly the rows stored with that status. form_id and kind narrow either list.
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total and total_pages.
// @Description Errors: 400 VALIDATION_ERROR (message "Validation failed", error.details names form_id, status or kind) for a form_id that is not a positive integer or an unknown status or kind.
// @Description Roles: admin, sales, support.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param form_id query int false "Only deliveries of this form (positive integer; otherwise 400)" minimum(1)
// @Param status query string false "Only rows stored with this status instead of the attention set (unknown value: 400)" Enums(pending, sending, sent, failed, log_only, skipped)
// @Param kind query string false "Only this kind of mail (unknown value: 400)" Enums(confirmation, follow_up, notification)
// @Success 200 {object} utils.APIResponse{data=[]models.FormMailDelivery,meta=utils.APIMeta} "Deliveries retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - form_id is not a positive integer, or status or kind is unknown"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/deliveries [get]
func (h *FormHandler) ListDeliveries(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.ListDeliveries")

	offset, limit := utils.ParseOffsetLimit(c)
	filter := service.FormMailDeliveryFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Kind:   strings.TrimSpace(c.Query("kind")),
	}
	if raw := strings.TrimSpace(c.Query("form_id")); raw != "" {
		formID, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || formID == 0 {
			h.respondError(c, logger, service.FieldErrors{"form_id": "form_id must be a positive integer"}, "Delivery not found")
			return
		}
		filter.FormID = uint(formID)
	}

	var deliveries []models.FormMailDelivery
	var total int64
	var err error
	deliveries, total, err = h.formService.ListDeliveries(filter, offset, limit)
	if err != nil {
		h.respondError(c, logger, err, "Delivery not found")
		return
	}

	meta := formListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, deliveries)
	utils.RespondSuccessWithMeta(c, http.StatusOK, deliveries, meta)
}

// RetryDelivery godoc
// @Summary Retry a form mail delivery
// @Description Send one form mail again: a delivery whose status is failed, log_only or stuck (pending or sending for more than 15 minutes; see retryable). The message is rendered from the form as it is now, to the submitter's address for a confirmation or follow-up, or to the notification address the row was written for, which must still be in notify_emails. Retrying a confirmation re-arms the submission's single confirmation token with a new 48-hour link (the previous link stops working) and is refused once the submission is no longer pending or a newer submission of the same form and address exists. The call claims the row in one transaction, sends once after the commit and answers with the updated record; two concurrent retries of one row send once.
// @Description Errors: 400 BAD_REQUEST for an id that is not a positive integer ("Invalid delivery ID"); 404 NOT_FOUND for an unknown delivery or one whose submission is gone (purged or erased); 409 FORM_DELIVERY_NOT_RETRYABLE when the status does not allow a retry (sent, skipped, or pending or sending and not yet stuck), the form is deleted, a follow-up's form no longer has a follow-up subject or its submission is neither received nor confirmed, a notification's address is no longer in notify_emails, a confirmation's submission is no longer pending or has a newer submission, or another retry claimed the row first; 502 MAIL_DELIVERY_FAILED when the mailer refused or failed (the row is stamped failed; the transport's message is not echoed).
// @Description Roles: admin, sales.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Delivery ID"
// @Success 200 {object} utils.APIResponse{data=models.FormMailDelivery} "Delivery retried; status is sent, log_only or skipped"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not a positive integer ('Invalid delivery ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - delivery not found, or its submission is gone (message 'Delivery not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "FORM_DELIVERY_NOT_RETRYABLE - the delivery cannot be retried (see description)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Failure 502 {object} utils.APIResponse{error=utils.APIError} "MAIL_DELIVERY_FAILED - the mailer refused or failed to deliver ('The form mail could not be delivered')"
// @Router /forms/deliveries/{id}/retry [post]
func (h *FormHandler) RetryDelivery(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.RetryDelivery")

	id, ok := formPathID(c, "Invalid delivery ID")
	if !ok {
		return
	}

	delivery, err := h.formService.RetryDelivery(c.Request.Context(), id)
	if err != nil {
		h.respondError(c, logger, err, "Delivery not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, delivery)
	utils.RespondSuccess(c, http.StatusOK, delivery)
}
