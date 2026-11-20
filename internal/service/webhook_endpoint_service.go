package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// The webhook endpoint registry (#345). Admin only (the route guard). An
// endpoint's signing secret is whsec_ plus the base64 of 32 random bytes,
// stored sealed with utils.SecretBox under WebhookSecretContext and returned
// once, in the create response. Log lines carry the operation and the
// endpoint id (the test ping also its outcome, error code and HTTP status)
// only: never the URL, its host, the secret or the sealed value.

// WebhookSecretContext is the SecretBox context of endpoint secrets; the
// master secret is API_KEY_SECRET (falling back to JWT_SECRET).
const WebhookSecretContext = "webhook-endpoint-secret"

// webhookSecretBytes is the size of the random key.
const webhookSecretBytes = 32

// webhookEventTypesMaxItems bounds the raw event_types array before
// deduplication.
const webhookEventTypesMaxItems = 20

// ErrWebhooksUnavailable is returned by a service built without its
// repository or secret box. It is a plain error: the handler answers 500.
var ErrWebhooksUnavailable = errors.New("webhooks are not configured")

// WebhookValidationError is one refused input, keyed by the JSON field name.
// The message never quotes the value.
type WebhookValidationError struct {
	Field   string
	Message string
}

func (e *WebhookValidationError) Error() string { return e.Field + ": " + e.Message }

// Unwrap makes the refusal match apperrors.ErrValidation.
func (e *WebhookValidationError) Unwrap() error { return apperrors.ErrValidation }

// Details is the error.details object of the 400 answer.
func (e *WebhookValidationError) Details() map[string]string {
	return map[string]string{e.Field: e.Message}
}

// Messages of the refusals; none quotes the value.
const (
	WebhookURLRule        = "must be an https URL of at most 2048 characters, without credentials, query string or fragment, whose host is not a private, loopback, link-local or metadata address"
	WebhookEventTypesRule = "must list 1 to 9 of lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated and ticket.deleted"
)

// ValidateWebhookURL checks an endpoint URL before anything is stored: https,
// a host, no userinfo, query or fragment, at most 2048 characters, and an IP
// literal host must be public. A host name is judged again on every
// connection by the egress guard.
func ValidateWebhookURL(raw string) error {
	if raw == "" || utf8.RuneCountInString(raw) > models.WebhookEndpointURLMaxLength {
		return &WebhookValidationError{Field: "url", Message: WebhookURLRule}
	}
	if aeo.CheckPublicURL(raw) != nil {
		return &WebhookValidationError{Field: "url", Message: WebhookURLRule}
	}
	return nil
}

// ValidateWebhookEventTypes checks and canonicalises the subscribed subset.
func ValidateWebhookEventTypes(values []string) (models.WebhookEventTypes, error) {
	if len(values) == 0 || len(values) > webhookEventTypesMaxItems {
		return nil, &WebhookValidationError{Field: "event_types", Message: WebhookEventTypesRule}
	}
	for _, value := range values {
		if !models.IsWebhookEventType(value) {
			return nil, &WebhookValidationError{Field: "event_types", Message: WebhookEventTypesRule}
		}
	}
	return models.CanonicalWebhookEventTypes(values), nil
}

// WebhookEndpointInput is a create request. IsActive nil means true.
type WebhookEndpointInput struct {
	URL        string
	EventTypes []string
	IsActive   *bool
}

// WebhookEndpointPatch is an update request; a nil field keeps the stored
// value.
type WebhookEndpointPatch struct {
	URL        *string
	EventTypes []string
	IsActive   *bool
}

// Empty reports whether the patch carries no field.
func (p WebhookEndpointPatch) Empty() bool {
	return p.URL == nil && p.EventTypes == nil && p.IsActive == nil
}

type webhookService struct {
	repo      repository.WebhookRepository
	txManager *utils.TransactionManager
	box       *utils.SecretBox
	// pingClients hands out the public-only client of a test ping; nil
	// refuses the ping with ErrWebhooksUnavailable.
	pingClients WebhookClientFactory
	// pingTimeout bounds one ping (WebhookRequestTimeout).
	pingTimeout time.Duration
}

// WebhookServiceOption configures NewWebhookService.
type WebhookServiceOption func(*webhookService)

// WithWebhookPingClients wires the test ping (TestEndpoint) to clients, the
// same egress guard deliveries use ((*aeo.EgressGuard).PublicHTTPClient).
// Without it TestEndpoint returns ErrWebhooksUnavailable.
func WithWebhookPingClients(clients WebhookClientFactory) WebhookServiceOption {
	return func(s *webhookService) { s.pingClients = clients }
}

// NewWebhookService builds the endpoint registry. box seals the secrets
// (utils.NewSecretBox(API_KEY_SECRET, WebhookSecretContext)); txManager runs
// the delete.
func NewWebhookService(repo repository.WebhookRepository, txManager *utils.TransactionManager, box *utils.SecretBox, opts ...WebhookServiceOption) WebhookService {
	s := &webhookService{repo: repo, txManager: txManager, box: box, pingTimeout: WebhookRequestTimeout}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func webhookLog(operation string) *logrus.Entry {
	return utils.Logger.WithField("operation", operation)
}

// newWebhookSecret returns whsec_ plus the base64 of 32 random bytes.
func newWebhookSecret() (string, error) {
	raw := make([]byte, webhookSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("webhook secret: %w", err)
	}
	return WebhookSecretPrefix + base64.StdEncoding.EncodeToString(raw), nil
}

func (s *webhookService) available() error {
	if s.repo == nil || s.box == nil {
		return ErrWebhooksUnavailable
	}
	return nil
}

func (s *webhookService) CreateEndpoint(ctx context.Context, input WebhookEndpointInput) (*models.WebhookEndpointCreated, error) {
	logger := webhookLog("webhook_endpoint_create")
	if err := s.available(); err != nil {
		logger.WithField("error_code", "unavailable").Error("Webhook endpoint registry is not wired")
		return nil, err
	}
	url := strings.TrimSpace(input.URL)
	if err := ValidateWebhookURL(url); err != nil {
		return nil, err
	}
	types, err := ValidateWebhookEventTypes(input.EventTypes)
	if err != nil {
		return nil, err
	}
	active := true
	if input.IsActive != nil {
		active = *input.IsActive
	}

	secret, err := newWebhookSecret()
	if err != nil {
		logger.WithField("error_code", "secret_generation_failed").Error("Webhook secret could not be generated")
		return nil, err
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		logger.WithField("error_code", "secret_seal_failed").Error("Webhook secret could not be sealed")
		return nil, err
	}
	endpoint := &models.WebhookEndpoint{URL: url, EventTypes: types, SecretSealed: sealed, IsActive: active}
	if err := s.repo.CreateEndpoint(ctx, endpoint); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create webhook endpoint")
		return nil, err
	}
	logger.WithField("endpoint_id", endpoint.ID).Info("Webhook endpoint created")
	return &models.WebhookEndpointCreated{WebhookEndpoint: *endpoint, Secret: secret}, nil
}

func (s *webhookService) notFound(id uint, err error) error {
	if isNotFound(err) {
		return fmt.Errorf("webhook endpoint %d not found: %w", id, apperrors.ErrNotFound)
	}
	return err
}

func (s *webhookService) GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	endpoint, err := s.repo.GetEndpoint(ctx, id)
	if err != nil {
		return nil, s.notFound(id, err)
	}
	return endpoint, nil
}

func (s *webhookService) ListEndpoints(ctx context.Context, offset, limit int, sortBy, sortOrder string) ([]models.WebhookEndpoint, int64, error) {
	if err := s.available(); err != nil {
		return nil, 0, err
	}
	return s.repo.ListEndpoints(ctx, offset, limit, sortBy, sortOrder)
}

func (s *webhookService) UpdateEndpoint(ctx context.Context, id uint, patch WebhookEndpointPatch) (*models.WebhookEndpoint, error) {
	logger := webhookLog("webhook_endpoint_update").WithField("endpoint_id", id)
	if err := s.available(); err != nil {
		return nil, err
	}
	if patch.Empty() {
		return nil, &WebhookValidationError{Field: "body", Message: "must carry url, event_types or is_active"}
	}
	columns := map[string]interface{}{}
	if patch.URL != nil {
		url := strings.TrimSpace(*patch.URL)
		if err := ValidateWebhookURL(url); err != nil {
			return nil, err
		}
		columns["url"] = url
	}
	if patch.EventTypes != nil {
		types, err := ValidateWebhookEventTypes(patch.EventTypes)
		if err != nil {
			return nil, err
		}
		columns["event_types"] = types
	}
	if patch.IsActive != nil {
		// An explicit column write, so false is stored.
		columns["is_active"] = *patch.IsActive
	}
	if err := s.repo.UpdateEndpoint(ctx, id, columns); err != nil {
		if !isNotFound(err) {
			logger.WithError(utils.LogError(err)).Error("Failed to update webhook endpoint")
		}
		return nil, s.notFound(id, err)
	}
	logger.Info("Webhook endpoint updated")
	return s.GetEndpoint(ctx, id)
}

func (s *webhookService) DeleteEndpoint(ctx context.Context, id uint) error {
	logger := webhookLog("webhook_endpoint_delete").WithField("endpoint_id", id)
	if err := s.available(); err != nil {
		return err
	}
	var err error
	if s.txManager != nil {
		err = s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
			tx, ok := utils.GetTxFromContext(ctx)
			if !ok {
				return utils.ErrNoTransaction
			}
			return s.repo.WithTx(tx).DeleteEndpoint(ctx, id)
		})
	} else {
		err = s.repo.DeleteEndpoint(ctx, id)
	}
	if err != nil {
		if !isNotFound(err) {
			logger.WithError(utils.LogError(err)).Error("Failed to delete webhook endpoint")
		}
		return s.notFound(id, err)
	}
	logger.Info("Webhook endpoint deleted")
	return nil
}

func (s *webhookService) ListDeliveries(ctx context.Context, filter repository.WebhookDeliveryFilter, offset, limit int) ([]models.WebhookDelivery, int64, error) {
	if err := s.available(); err != nil {
		return nil, 0, err
	}
	if filter.Status != "" && !models.IsWebhookDeliveryStatus(filter.Status) {
		return nil, 0, &WebhookValidationError{Field: "status", Message: "must be pending, sending, succeeded or failed"}
	}
	if filter.EventType != "" && !models.IsWebhookEventType(filter.EventType) {
		return nil, 0, &WebhookValidationError{Field: "event_type", Message: "must be lead.created, lead.updated, customer.created, customer.updated, ticket.created or ticket.updated"}
	}
	return s.repo.ListDeliveries(ctx, filter, offset, limit)
}

// TestEndpoint sends one signed webhook.ping request to the endpoint,
// synchronously, through the shared send path of deliveries (egress guard,
// signing, WebhookRequestTimeout, no redirect followed). It works on an
// inactive endpoint, is never retried and writes nothing: the endpoint read
// is its only database statement. An unusable secret is a failed outcome
// (secret_unavailable) without any network work. It logs one line carrying
// the operation, the endpoint id, the outcome, the error code and the HTTP
// status only.
func (s *webhookService) TestEndpoint(ctx context.Context, id uint) (*models.WebhookPingResult, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	if s.pingClients == nil {
		webhookLog("webhook_endpoint_test").WithField("error_code", "unavailable").Error("Webhook test ping is not wired")
		return nil, ErrWebhooksUnavailable
	}
	endpoint, err := s.repo.GetEndpoint(ctx, id)
	if err != nil {
		return nil, s.notFound(id, err)
	}
	eventID, err := newWebhookEventID()
	if err != nil {
		return nil, err
	}

	var outcome attemptOutcome
	secret, openErr := s.box.Open(endpoint.SecretSealed)
	if openErr != nil || secret == "" {
		outcome = attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable}
	} else if _, keyErr := webhookKey(secret); keyErr != nil {
		outcome = attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable}
	} else {
		outcome = sendWebhook(ctx, s.pingClients, endpoint.URL, s.pingTimeout, func(reqCtx context.Context) (*http.Request, error) {
			sentAt := time.Now()
			body, err := webhookPingBody(eventID, sentAt)
			if err != nil {
				return nil, err
			}
			return newSignedWebhookRequest(reqCtx, endpoint.URL, secret, eventID, body, sentAt)
		})
	}

	result := &models.WebhookPingResult{EventID: eventID, Outcome: models.WebhookPingSucceeded, HTTPStatus: outcome.httpStatus}
	if !outcome.succeeded {
		result.Outcome = models.WebhookPingFailed
		code := outcome.errorCode
		result.ErrorCode = &code
	}
	fields := logrus.Fields{
		"operation":   "webhook_endpoint_test",
		"endpoint_id": endpoint.ID,
		"outcome":     result.Outcome,
		"error_code":  outcome.errorCode,
	}
	if outcome.succeeded {
		fields["error_code"] = ""
	}
	if outcome.httpStatus != nil {
		fields["http_status"] = *outcome.httpStatus
	}
	utils.Logger.WithFields(fields).Info("Webhook endpoint test ping sent")
	return result, nil
}
