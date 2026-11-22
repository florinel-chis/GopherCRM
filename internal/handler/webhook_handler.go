package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Outbound webhooks (#345): the admin-only endpoint registry and delivery log.
// Every input is validated here, before the service is called; a refusal is
// 400 VALIDATION_ERROR with error.details keyed by the JSON field or query
// parameter, and never echoes the value. Log lines carry the operation, the
// endpoint id and the error code only: never the URL, its host or the secret.

// WebhookEndpointCreateRequest registers an endpoint.
type WebhookEndpointCreateRequest struct {
	// Target URL: https, no credentials, query string or fragment, at most 2048 characters; an IP-literal host must be public, and a host name must resolve to public addresses only when a delivery connects.
	URL string `json:"url" validate:"required" maxLength:"2048" example:"https://hooks.example.com/gophercrm"`
	// Event types to subscribe to: 1 to 9 of lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated and ticket.deleted; duplicates collapse.
	EventTypes []string `json:"event_types" validate:"required" enums:"lead.created,lead.updated,lead.deleted,customer.created,customer.updated,customer.deleted,ticket.created,ticket.updated,ticket.deleted" example:"lead.created,ticket.created"`
	// Whether events are enqueued for the endpoint; absent means true.
	IsActive *bool `json:"is_active" example:"true"`
}

// WebhookEndpointUpdateRequest changes an endpoint. An absent field keeps the
// stored value; at least one must be present. It never changes the secret:
// POST /webhooks/endpoints/{id}/rotate-secret does.
type WebhookEndpointUpdateRequest struct {
	// New target URL, under the same rules as on create.
	URL *string `json:"url" maxLength:"2048" example:"https://hooks.example.com/gophercrm"`
	// New subscribed subset: 1 to 9 of lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated and ticket.deleted.
	EventTypes []string `json:"event_types" enums:"lead.created,lead.updated,lead.deleted,customer.created,customer.updated,customer.deleted,ticket.created,ticket.updated,ticket.deleted" example:"lead.created"`
	// Activate or deactivate the endpoint. Pending deliveries of an inactive endpoint fail with endpoint_inactive when they are claimed.
	IsActive *bool `json:"is_active" example:"false"`
}

// WebhookHandler serves /webhooks.
type WebhookHandler struct {
	webhookService service.WebhookService
}

// NewWebhookHandler builds the handler.
func NewWebhookHandler(webhookService service.WebhookService) *WebhookHandler {
	return &WebhookHandler{webhookService: webhookService}
}

const (
	webhookIDRule     = "must be a positive integer"
	webhookBodyRule   = "must carry url, event_types or is_active"
	webhookSortByRule = "must be id, created_at or updated_at"
	webhookStatusRule = "must be pending, sending, succeeded or failed"
)

// webhookEventTypeRule is the refusal message of the event_type filter, built
// from the webhook catalogue.
var webhookEventTypeRule = webhook.FilterRule()

// parseWebhookID reads a positive integer path or query value.
func parseWebhookID(raw string) (uint, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || id == 0 {
		return 0, false
	}
	return uint(id), true
}

// webhookHandlerLog is the logger of one webhook operation. It deliberately
// does not start from the request logger: the lines carry the operation, the
// endpoint id and the error code only.
func webhookHandlerLog(operation string) *logrus.Entry {
	return utils.Logger.WithField("operation", operation)
}

// respondWebhookError maps a service error; the cause is never echoed.
func respondWebhookError(c *gin.Context, logger *logrus.Entry, err error) {
	var validation *service.WebhookValidationError
	switch {
	case errors.As(err, &validation):
		logger.WithField("error_code", utils.ErrCodeValidation).Warn("Webhook request refused")
		utils.RespondValidationError(c, validation.Details())
	case errors.Is(err, apperrors.ErrNotFound):
		utils.RespondNotFound(c, "Webhook endpoint not found")
	default:
		logger.WithField("error_code", utils.ErrCodeInternal).WithError(utils.LogError(err)).Error("Webhook request failed")
		utils.RespondInternalError(c)
	}
}

// ListEndpoints godoc
// @Summary List webhook endpoints
// @Description One page of the registered outbound webhook endpoints. The signing secret is never part of the answer.
// @Description Ordered by sort_by (id, created_at or updated_at) and then by id in the same direction, or by id ascending without sort_by. Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default).
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param sort_by query string false "Sort column (another value: 400)" Enums(id, created_at, updated_at)
// @Param sort_order query string false "Sort direction; anything but asc sorts descending" Enums(asc, desc)
// @Success 200 {object} utils.APIResponse{data=[]models.WebhookEndpoint,meta=utils.APIMeta} "Endpoints retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - sort_by is not id, created_at or updated_at (error.details.sort_by)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints [get]
func (h *WebhookHandler) ListEndpoints(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_list")
	offset, limit := utils.ParseOffsetLimit(c)
	sortBy := strings.TrimSpace(c.Query("sort_by"))
	sortOrder := strings.TrimSpace(c.Query("sort_order"))
	if sortBy != "" && !utils.AllowedSortColumns["webhook_endpoints"][sortBy] {
		utils.RespondValidationError(c, map[string]string{"sort_by": webhookSortByRule})
		return
	}
	endpoints, total, err := h.webhookService.ListEndpoints(c.Request.Context(), offset, limit, sortBy, sortOrder)
	if err != nil {
		respondWebhookError(c, logger, err)
		return
	}
	utils.RespondSuccessWithMeta(c, http.StatusOK, endpoints, formListMeta(c, offset, limit, total))
}

// CreateEndpoint godoc
// @Summary Register a webhook endpoint
// @Description Registers an outbound webhook endpoint and generates its signing secret (whsec_ followed by the base64 of 32 random bytes). The secret is returned once, in this response: it is stored encrypted and no other route shows it; only POST /webhooks/endpoints/{id}/rotate-secret replaces it and returns the new one, once.
// @Description From then on every lead, customer or ticket created (through the API, a lead conversion or a form) and every lead, customer or ticket changed by PUT /leads/{id}, POST /leads/{id}/assign, POST /leads/{id}/convert (lead.updated next to customer.created), PUT /customers/{id}, PUT /tickets/{id}, the lead and ticket bulk status endpoints or the automation assign-owner step on a lead or ticket (only when a stored value actually changed) enqueues one delivery per active endpoint subscribed to its event type, in the writing transaction. A ticket deleted by DELETE /tickets/{id}, and every lead or customer erased (DELETE /leads/{id}, DELETE /customers/{id}, DELETE /users/{id} and their cascades), enqueues the deleted event for every active endpoint subscribed to it, whether or not that endpoint received an earlier event for the record. Deliveries are POSTed after the commit with a thin JSON body {id, type, occurred_at, record_id} and the Standard Webhooks headers webhook-id, webhook-timestamp and webhook-signature (v1, HMAC-SHA256 over id.timestamp.body). A non-2xx answer, a redirect (never followed), a timeout (10 s) or a refused destination is retried with exponential backoff (1 minute doubling, at most 6 hours) up to 10 attempts.
// @Description The URL must be https without credentials, query string or fragment, at most 2048 characters; an IP-literal host must be public, and a host name that resolves to a private, loopback, link-local or metadata address is refused at every delivery (destination_refused).
// @Description Roles: admin.
// @Tags webhooks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body WebhookEndpointCreateRequest true "Endpoint to register"
// @Success 201 {object} utils.APIResponse{data=models.WebhookEndpointCreated} "Endpoint registered; data.secret is shown only here"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - an invalid url or event_types, or an empty body (error.details keyed by url, event_types or body; the value is not echoed); BAD_REQUEST - malformed JSON ('Invalid request format')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints [post]
func (h *WebhookHandler) CreateEndpoint(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_create")
	var req WebhookEndpointCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			utils.RespondValidationError(c, map[string]string{"body": webhookBodyRule})
			return
		}
		utils.RespondBadRequest(c, "Invalid request format")
		return
	}
	details := map[string]string{}
	url := strings.TrimSpace(req.URL)
	var validation *service.WebhookValidationError
	if err := service.ValidateWebhookURL(url); errors.As(err, &validation) {
		details[validation.Field] = validation.Message
	}
	if _, err := service.ValidateWebhookEventTypes(req.EventTypes); errors.As(err, &validation) {
		details[validation.Field] = validation.Message
	}
	if len(details) > 0 {
		logger.WithField("error_code", utils.ErrCodeValidation).Warn("Webhook endpoint refused")
		utils.RespondValidationError(c, details)
		return
	}

	created, err := h.webhookService.CreateEndpoint(c.Request.Context(), service.WebhookEndpointInput{
		URL: url, EventTypes: req.EventTypes, IsActive: req.IsActive,
	})
	if err != nil {
		respondWebhookError(c, logger, err)
		return
	}
	logger.WithField("endpoint_id", created.ID).Debug("Webhook endpoint registered")
	utils.RespondSuccess(c, http.StatusCreated, created)
}

// GetEndpoint godoc
// @Summary Get a webhook endpoint
// @Description One registered webhook endpoint, without its signing secret.
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Endpoint ID"
// @Success 200 {object} utils.APIResponse{data=models.WebhookEndpoint} "Endpoint retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - the id is not a positive integer (error.details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no endpoint with that id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints/{id} [get]
func (h *WebhookHandler) GetEndpoint(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_get")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	endpoint, err := h.webhookService.GetEndpoint(c.Request.Context(), id)
	if err != nil {
		respondWebhookError(c, logger.WithField("endpoint_id", id), err)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, endpoint)
}

// TestEndpoint godoc
// @Summary Send a test ping to a webhook endpoint
// @Description Sends one signed webhook.ping request to the endpoint, synchronously, and answers with its outcome. The body is {id, type, occurred_at} with type webhook.ping and no record_id; the headers are the Standard Webhooks headers of a delivery (webhook-id equal to the body's id, webhook-timestamp, webhook-signature v1 under the endpoint's secret).
// @Description The ping uses the same egress guard as deliveries (a host that resolves to a private, loopback, link-local or metadata address is refused: destination_refused), the same 10 s timeout, and never follows a redirect. It works on an inactive endpoint, is not written to the delivery log, is not retried and leaves the endpoint unchanged.
// @Description A failed ping is still a 200 with outcome failed: error_code is http_status (a non-2xx answer), redirect, timeout, connection_failed, destination_refused or secret_unavailable (the stored secret cannot be opened; nothing is sent). http_status is null when no answer was received.
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Endpoint ID"
// @Success 200 {object} utils.APIResponse{data=models.WebhookPingResult} "Ping sent (or refused); see outcome"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - the id is not a positive integer (error.details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no endpoint with that id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure, or the ping is not wired; the cause is never echoed"
// @Router /webhooks/endpoints/{id}/test [post]
func (h *WebhookHandler) TestEndpoint(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_test")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	result, err := h.webhookService.TestEndpoint(c.Request.Context(), id)
	if err != nil {
		respondWebhookError(c, logger.WithField("endpoint_id", id), err)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, result)
}

// UpdateEndpoint godoc
// @Summary Update a webhook endpoint
// @Description Changes the URL, the subscribed event types or the active flag of an endpoint. An absent field keeps the stored value; a body with none of the three is refused. The signing secret is not changed (POST /webhooks/endpoints/{id}/rotate-secret replaces it) and is not returned.
// @Description Deliveries already enqueued keep their endpoint: those of an endpoint set inactive fail with endpoint_inactive when they are claimed, and those of an endpoint whose URL changed are sent to the new URL.
// @Description Roles: admin.
// @Tags webhooks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Endpoint ID"
// @Param request body WebhookEndpointUpdateRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.WebhookEndpoint} "Endpoint updated"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - a bad id, an invalid url or event_types, or a body with none of the fields (error.details keyed by id, url, event_types or body; the value is not echoed); BAD_REQUEST - malformed JSON ('Invalid request format')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no endpoint with that id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints/{id} [put]
func (h *WebhookHandler) UpdateEndpoint(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_update")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	var req WebhookEndpointUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			utils.RespondValidationError(c, map[string]string{"body": webhookBodyRule})
			return
		}
		utils.RespondBadRequest(c, "Invalid request format")
		return
	}
	patch := service.WebhookEndpointPatch{EventTypes: req.EventTypes, IsActive: req.IsActive}
	if req.URL != nil {
		url := strings.TrimSpace(*req.URL)
		patch.URL = &url
	}
	if patch.Empty() {
		utils.RespondValidationError(c, map[string]string{"body": webhookBodyRule})
		return
	}
	details := map[string]string{}
	var validation *service.WebhookValidationError
	if patch.URL != nil {
		if err := service.ValidateWebhookURL(*patch.URL); errors.As(err, &validation) {
			details[validation.Field] = validation.Message
		}
	}
	if patch.EventTypes != nil {
		if _, err := service.ValidateWebhookEventTypes(patch.EventTypes); errors.As(err, &validation) {
			details[validation.Field] = validation.Message
		}
	}
	if len(details) > 0 {
		logger.WithFields(logrus.Fields{"endpoint_id": id, "error_code": utils.ErrCodeValidation}).Warn("Webhook endpoint update refused")
		utils.RespondValidationError(c, details)
		return
	}

	endpoint, err := h.webhookService.UpdateEndpoint(c.Request.Context(), id, patch)
	if err != nil {
		respondWebhookError(c, logger.WithField("endpoint_id", id), err)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, endpoint)
}

// DeleteEndpoint godoc
// @Summary Delete a webhook endpoint
// @Description Permanently deletes the endpoint and every delivery row of it (pending ones included, which are then never sent), in one transaction.
// @Description Roles: admin.
// @Tags webhooks
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Endpoint ID"
// @Success 204 "Endpoint and its deliveries deleted"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - the id is not a positive integer (error.details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no endpoint with that id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints/{id} [delete]
func (h *WebhookHandler) DeleteEndpoint(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_delete")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	if err := h.webhookService.DeleteEndpoint(c.Request.Context(), id); err != nil {
		respondWebhookError(c, logger.WithField("endpoint_id", id), err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RotateSecret godoc
// @Summary Rotate the signing secret of a webhook endpoint
// @Description Generates a new signing secret (whsec_ followed by the base64 of 32 random bytes) for the endpoint and returns it in this response only: it is stored encrypted and no other route shows it. The replaced secret is kept, encrypted, as the previous secret until previous_secret_expires_at, 24 hours from now.
// @Description While that window is open every delivery, retry, redelivery and test ping carries two space-separated signatures in webhook-signature (v1,<new> v1,<previous>, the new one first), so a consumer can switch to the new secret without missing a request. From previous_secret_expires_at on only the new signature is sent, and the next delivery clears the previous secret. A second rotation inside the window replaces the previous secret: at most two secrets ever sign.
// @Description It works on an inactive endpoint and never reads the stored secrets, so an endpoint whose secret cannot be opened any more (API_KEY_SECRET rotated) can be given a new one. No request body is read.
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Endpoint ID"
// @Success 200 {object} utils.APIResponse{data=models.WebhookEndpointSecretRotated} "Secret rotated; the new secret is shown once"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - the id is not a positive integer (error.details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no endpoint with that id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/endpoints/{id}/rotate-secret [post]
func (h *WebhookHandler) RotateSecret(c *gin.Context) {
	logger := webhookHandlerLog("webhook_endpoint_rotate_secret")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	rotated, err := h.webhookService.RotateEndpointSecret(c.Request.Context(), id)
	if err != nil {
		respondWebhookError(c, logger.WithField("endpoint_id", id), err)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, rotated)
}

// ListDeliveries godoc
// @Summary List webhook deliveries
// @Description One page of the outbound webhook delivery log, newest first (id descending). Each row is one event for one endpoint: ids, the event type, the status, the attempt count, the last HTTP status, the error code and the attempt times; never a URL, a secret or a payload. Succeeded and failed rows are pruned 30 days after their last change.
// @Description Filters: endpoint_id, status and event_type; an empty value is ignored. Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default).
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param endpoint_id query int false "Only deliveries of this endpoint (positive integer; otherwise 400)" minimum(1)
// @Param status query string false "Only rows with this status (unknown value: 400)" Enums(pending, sending, succeeded, failed)
// @Param event_type query string false "Only rows of this event type (unknown value: 400)" Enums(lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated, ticket.deleted)
// @Success 200 {object} utils.APIResponse{data=[]models.WebhookDelivery,meta=utils.APIMeta} "Deliveries retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - endpoint_id is not a positive integer, or status or event_type is unknown (error.details keyed by the parameter; the value is not echoed)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/deliveries [get]
func (h *WebhookHandler) ListDeliveries(c *gin.Context) {
	logger := webhookHandlerLog("webhook_delivery_list")
	offset, limit := utils.ParseOffsetLimit(c)
	filter := repository.WebhookDeliveryFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		EventType: strings.TrimSpace(c.Query("event_type")),
	}
	details := map[string]string{}
	if raw := strings.TrimSpace(c.Query("endpoint_id")); raw != "" {
		id, ok := parseWebhookID(raw)
		if !ok {
			details["endpoint_id"] = webhookIDRule
		}
		filter.EndpointID = id
	}
	if filter.Status != "" && !models.IsWebhookDeliveryStatus(filter.Status) {
		details["status"] = webhookStatusRule
	}
	if filter.EventType != "" && !webhook.IsEventType(filter.EventType) {
		details["event_type"] = webhookEventTypeRule
	}
	if len(details) > 0 {
		utils.RespondValidationError(c, details)
		return
	}
	deliveries, total, err := h.webhookService.ListDeliveries(c.Request.Context(), filter, offset, limit)
	if err != nil {
		respondWebhookError(c, logger, err)
		return
	}
	utils.RespondSuccessWithMeta(c, http.StatusOK, deliveries, formListMeta(c, offset, limit, total))
}

// RedeliverDelivery godoc
// @Summary Redeliver a logged webhook delivery
// @Description Puts a succeeded or failed delivery back into the queue: the same row becomes pending with attempts 0 and is due at once; last_http_status and error_code are cleared, last_attempt_at keeps its value. The runtime then sends it again with the same event id (webhook-id) and the full retry budget (10 attempts). No new row is written and the record the event names is not read.
// @Description A pending or sending delivery (already queued or being sent) is refused with 409, and so is a delivery whose endpoint is deleted or inactive. The endpoint is checked before the write; an endpoint deactivated in between still gets the row requeued, and the runtime then fails it with endpoint_inactive.
// @Description Roles: admin.
// @Tags webhooks
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Delivery ID"
// @Success 200 {object} utils.APIResponse{data=models.WebhookDelivery} "Delivery queued again (status pending)"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - the id is not a positive integer (error.details.id)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no delivery with that id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - the delivery is pending or being sent, or its endpoint is deleted or inactive"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /webhooks/deliveries/{id}/redeliver [post]
func (h *WebhookHandler) RedeliverDelivery(c *gin.Context) {
	logger := webhookHandlerLog("webhook_delivery_redeliver")
	id, ok := parseWebhookID(c.Param("id"))
	if !ok {
		utils.RespondValidationError(c, map[string]string{"id": webhookIDRule})
		return
	}
	delivery, err := h.webhookService.RedeliverDelivery(c.Request.Context(), id)
	switch {
	case err == nil:
		utils.RespondSuccess(c, http.StatusOK, delivery)
	case errors.Is(err, apperrors.ErrNotFound):
		utils.RespondNotFound(c, "Webhook delivery not found")
	case errors.Is(err, service.ErrWebhookDeliveryNotRedeliverable):
		utils.RespondConflict(c, "Webhook delivery is pending or being sent")
	case errors.Is(err, service.ErrWebhookEndpointUnavailable):
		utils.RespondConflict(c, "Webhook endpoint is deleted or inactive")
	default:
		respondWebhookError(c, logger.WithField("delivery_id", id), err)
	}
}
