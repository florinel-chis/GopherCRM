package webhook

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

	"github.com/florinel-chis/gophercrm/internal/egress"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// The webhook endpoint registry (#345). Admin only (the route guard). An
// endpoint's signing secret is whsec_ plus the base64 of 32 random bytes,
// stored sealed with utils.SecretBox under WebhookSecretContext and returned
// once, in the create or the rotate-secret response. Log lines carry the operation and the
// endpoint id (the test ping also its outcome, error code and HTTP status; a
// redelivery the delivery id and error code) only: never the URL, its host, the secret or the sealed value.

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

// ErrWebhookDeliveryNotRedeliverable is returned by RedeliverDelivery when
// the delivery is pending or sending: it is already queued or being sent.
var ErrWebhookDeliveryNotRedeliverable = errors.New("webhook delivery is pending or being sent")

// ErrWebhookEndpointUnavailable is returned by RedeliverDelivery when the
// delivery's endpoint is deleted or inactive.
var ErrWebhookEndpointUnavailable = errors.New("webhook endpoint is deleted or inactive")

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
	WebhookURLRule = "must be an https URL of at most 2048 characters, without credentials, query string or fragment, whose host is not a private, loopback, link-local or metadata address"
)

// WebhookEventTypesRule is the refusal message of event_types, built from the
// webhook catalogue.
var WebhookEventTypesRule = SubscribeRule()

// ValidateWebhookURL checks an endpoint URL before anything is stored: https,
// a host, no userinfo, query or fragment, at most 2048 characters, and an IP
// literal host must be public. A host name is judged again on every
// connection by the egress guard.
func ValidateWebhookURL(raw string) error {
	if raw == "" || utf8.RuneCountInString(raw) > models.WebhookEndpointURLMaxLength {
		return &WebhookValidationError{Field: "url", Message: WebhookURLRule}
	}
	if egress.CheckPublicURL(raw) != nil {
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
		if !IsEventType(value) {
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

// WebhookService is the admin registry of outbound webhook endpoints and the
// delivery log (#345). Invalid input is a *WebhookValidationError (it unwraps
// to apperrors.ErrValidation), an unknown id apperrors.ErrNotFound, and a
// service built without its repository or secret box ErrWebhooksUnavailable
// (TestEndpoint also without its ping client factory). RedeliverDelivery
// answers ErrWebhookDeliveryNotRedeliverable for a pending or sending row and
// ErrWebhookEndpointUnavailable for a deleted or inactive endpoint.
type WebhookService interface {
	// CreateEndpoint validates the URL and event types, generates the signing
	// secret, stores it sealed and returns it this once.
	CreateEndpoint(ctx context.Context, input WebhookEndpointInput) (*models.WebhookEndpointCreated, error)
	GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error)
	ListEndpoints(ctx context.Context, offset, limit int, sortBy, sortOrder string) ([]models.WebhookEndpoint, int64, error)
	// UpdateEndpoint writes the fields the patch carries; it never changes
	// the secret (RotateEndpointSecret does).
	UpdateEndpoint(ctx context.Context, id uint, patch WebhookEndpointPatch) (*models.WebhookEndpoint, error)
	// RotateEndpointSecret replaces the signing secret and returns the new
	// one this once; the replaced secret keeps signing next to it for
	// WebhookSecretOverlap (#503).
	RotateEndpointSecret(ctx context.Context, id uint) (*models.WebhookEndpointSecretRotated, error)
	// DeleteEndpoint hard-deletes the endpoint and its delivery rows in one
	// transaction, endpoint row locked first.
	DeleteEndpoint(ctx context.Context, id uint) error
	// ListDeliveries returns one page of the delivery log, newest first.
	ListDeliveries(ctx context.Context, filter repository.WebhookDeliveryFilter, offset, limit int) ([]models.WebhookDelivery, int64, error)
	// TestEndpoint sends one signed webhook.ping request to the endpoint,
	// active or not, synchronously and without writing anything. A refused,
	// failed or unsigned attempt is a failed result, not an error; a service
	// built without WithWebhookPingClients returns ErrWebhooksUnavailable.
	TestEndpoint(ctx context.Context, id uint) (*models.WebhookPingResult, error)
	// RedeliverDelivery puts a succeeded or failed delivery back into the
	// queue (pending, attempts 0, due now, same event_id) with one
	// conditional UPDATE and returns the requeued row.
	RedeliverDelivery(ctx context.Context, id uint) (*models.WebhookDelivery, error)
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
// same egress guard deliveries use ((*egress.Guard).PublicHTTPClient).
// Without it TestEndpoint returns ErrWebhooksUnavailable.
func WithWebhookPingClients(clients WebhookClientFactory) WebhookServiceOption {
	return func(s *webhookService) { s.pingClients = clients }
}

// WithWebhookPingTimeout bounds one test ping by d instead of
// WebhookRequestTimeout. A non-positive d keeps the default.
func WithWebhookPingTimeout(d time.Duration) WebhookServiceOption {
	return func(s *webhookService) {
		if d > 0 {
			s.pingTimeout = d
		}
	}
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

// NewWebhookSecret returns whsec_ plus the base64 of 32 random bytes.
func NewWebhookSecret() (string, error) {
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

	secret, err := NewWebhookSecret()
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
	if apperrors.IsNotFound(err) {
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
		if !apperrors.IsNotFound(err) {
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
		if !apperrors.IsNotFound(err) {
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
	if filter.EventType != "" && !IsEventType(filter.EventType) {
		return nil, 0, &WebhookValidationError{Field: "event_type", Message: FilterRule()}
	}
	return s.repo.ListDeliveries(ctx, filter, offset, limit)
}

// TestEndpoint sends one signed webhook.ping request to the endpoint,
// synchronously, through the shared send path of deliveries (egress guard,
// signing, WebhookRequestTimeout, no redirect followed). It works on an
// inactive endpoint, is never retried and writes nothing: the endpoint read
// is its only database statement. Inside a rotation window it carries both
// signatures, and it never clears an expired previous secret. An unusable
// current secret is a failed outcome
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
	eventID, err := NewEventID()
	if err != nil {
		return nil, err
	}

	// One clock reading decides the rotation window and stamps the ping. The
	// ping never clears an expired previous secret: it writes nothing.
	var outcome attemptOutcome
	sentAt := time.Now()
	secrets, ok := webhookSigningSecrets(s.box, endpoint, sentAt, utils.Logger, "webhook_endpoint_test")
	if !ok {
		outcome = attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable}
	} else {
		outcome = sendWebhook(ctx, s.pingClients, endpoint.URL, s.pingTimeout, func(reqCtx context.Context) (*http.Request, error) {
			body, err := webhookPingBody(eventID, sentAt)
			if err != nil {
				return nil, err
			}
			return newSignedWebhookRequest(reqCtx, endpoint.URL, secrets, eventID, body, sentAt)
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

// WebhookSecretOverlap is how long the secret a rotation replaces still signs
// requests next to the new one (#503).
const WebhookSecretOverlap = 24 * time.Hour

// RotateEndpointSecret generates a new signing secret, stores it sealed and
// keeps the replaced sealed secret as the previous one until now +
// WebhookSecretOverlap, in one UPDATE by primary key (#503). It works on an
// inactive endpoint and never opens the current or the previous secret, so a
// secret sealed under a rotated API_KEY_SECRET can be replaced. It logs the
// operation and the endpoint id only.
func (s *webhookService) RotateEndpointSecret(ctx context.Context, id uint) (*models.WebhookEndpointSecretRotated, error) {
	logger := webhookLog("webhook_endpoint_rotate_secret").WithField("endpoint_id", id)
	if err := s.available(); err != nil {
		return nil, err
	}
	secret, err := NewWebhookSecret()
	if err != nil {
		logger.WithField("error_code", "secret_generation_failed").Error("Webhook secret could not be generated")
		return nil, err
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		logger.WithField("error_code", "secret_seal_failed").Error("Webhook secret could not be sealed")
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.repo.RotateEndpointSecret(ctx, id, sealed, now.Add(WebhookSecretOverlap), now); err != nil {
		if !apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Error("Failed to rotate webhook endpoint secret")
		}
		return nil, s.notFound(id, err)
	}
	logger.Info("Webhook endpoint secret rotated")
	endpoint, err := s.GetEndpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return &models.WebhookEndpointSecretRotated{WebhookEndpoint: *endpoint, Secret: secret}, nil
}

// RedeliverDelivery puts a finished (succeeded or failed) delivery back into
// the queue (#502): the runtime sends it again under the same event_id with
// the full retry budget. The endpoint must exist and be active; that check is
// a read before the write, not part of its predicate, so an endpoint
// deactivated in between still gets the row requeued (the runtime then fails
// it endpoint_inactive). The record the delivery names is never read. It logs
// the operation, the delivery id, the endpoint id and the error code only.
func (s *webhookService) RedeliverDelivery(ctx context.Context, id uint) (*models.WebhookDelivery, error) {
	const operation = "webhook_delivery_redeliver"
	if err := s.available(); err != nil {
		return nil, err
	}
	deliveryNotFound := func() error {
		return fmt.Errorf("webhook delivery %d not found: %w", id, apperrors.ErrNotFound)
	}
	delivery, err := s.repo.GetDelivery(ctx, id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, deliveryNotFound()
		}
		return nil, err
	}
	logger := utils.Logger.WithFields(logrus.Fields{
		"operation":   operation,
		"delivery_id": delivery.ID,
		"endpoint_id": delivery.EndpointID,
	})
	endpoint, err := s.repo.GetEndpoint(ctx, delivery.EndpointID)
	if err != nil && !apperrors.IsNotFound(err) {
		return nil, err
	}
	if err != nil || !endpoint.IsActive {
		logger.WithField("error_code", "endpoint_unavailable").Warn("Webhook delivery not redelivered")
		return nil, ErrWebhookEndpointUnavailable
	}
	if err := s.repo.RequeueDelivery(ctx, id, time.Now().UTC()); err != nil {
		if !errors.Is(err, repository.ErrWebhookDeliveryNotRequeued) {
			logger.WithError(utils.LogError(err)).Error("Failed to redeliver webhook delivery")
			return nil, err
		}
		if _, readErr := s.repo.GetDelivery(ctx, id); readErr != nil {
			if apperrors.IsNotFound(readErr) {
				return nil, deliveryNotFound()
			}
			return nil, readErr
		}
		logger.WithField("error_code", "not_redeliverable").Warn("Webhook delivery not redelivered")
		return nil, ErrWebhookDeliveryNotRedeliverable
	}
	logger.WithField("error_code", "").Info("Webhook delivery queued for redelivery")
	requeued, err := s.repo.GetDelivery(ctx, id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, deliveryNotFound()
		}
		return nil, err
	}
	return requeued, nil
}
