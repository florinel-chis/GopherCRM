package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// The webhook delivery runtime (#345).
//
// It delivers committed outbox rows only: every WebhookPollInterval it
// recovers expired leases, discovers due rows (status pending and
// next_attempt_at <= now) with a plain SELECT, claims each by primary key with
// a conditional UPDATE (pending -> sending, attempts + 1, last_attempt_at,
// lease_until), POSTs once through the public-only egress client, and finishes
// with a conditional UPDATE fenced on status sending and the claimed attempts.
// A failure below WebhookMaxAttempts returns the row to pending at now +
// min(WebhookRetryBase * 2^(attempts-1), WebhookRetryMax); the attempt that
// reaches the cap stores failed. Finished rows older than
// WebhookDeliveryRetention are pruned at most once per WebhookPruneInterval.
//
// Stop cancels the loop: no further claim starts, and an attempt already
// claimed runs to its end (its request is bounded by the request timeout and
// its finish write by webhookFinishTimeout) before the goroutine joins, so
// nothing is written after Stop returns nil.
//
// Log lines carry operation, endpoint_id, delivery_id, event_type, record_id,
// attempts, status, http_status and error_code only: never the URL, its host,
// the secret, the signature or a body. Transport errors, which quote the URL,
// are classified and never logged.

const (
	// WebhookPollInterval is the time between two delivery passes.
	WebhookPollInterval = 5 * time.Second
	// WebhookRequestTimeout bounds one POST, connection and TLS included.
	WebhookRequestTimeout = 10 * time.Second
	// WebhookSendLease is how long a claim holds a row before another pass may
	// return it to pending.
	WebhookSendLease = 2 * time.Minute
	// WebhookRetryBase is the delay after the first failed attempt; each
	// further failure doubles it.
	WebhookRetryBase = time.Minute
	// WebhookRetryMax caps the delay between two attempts.
	WebhookRetryMax = 6 * time.Hour
	// WebhookDeliveryRetention is the age (updated_at) after which succeeded
	// and failed rows are pruned.
	WebhookDeliveryRetention = 30 * 24 * time.Hour
	// WebhookPruneInterval is the least time between two prunes.
	WebhookPruneInterval = time.Hour
)

const (
	// webhookDiscoveryBatch bounds the rows one pass discovers.
	webhookDiscoveryBatch = 100
	// webhookFinishTimeout bounds the finish write of one attempt.
	webhookFinishTimeout = 5 * time.Second
	// webhookResponseDrainBytes is how much of a response body is read (and
	// discarded) before the connection is closed.
	webhookResponseDrainBytes = 4 << 10
)

// WebhookClientFactory hands out the HTTP client of one endpoint URL.
// (*aeo.EgressGuard).PublicHTTPClient is the production implementation.
type WebhookClientFactory interface {
	PublicHTTPClient(url string) (*http.Client, error)
}

// WebhookBackoff is the delay after the given number of failed attempts:
// min(base * 2^(attempts-1), max).
func WebhookBackoff(attempts int, base, ceiling time.Duration) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := base
	for i := 1; i < attempts; i++ {
		if delay >= ceiling/2 {
			return ceiling
		}
		delay *= 2
	}
	if delay > ceiling {
		return ceiling
	}
	return delay
}

// WebhookRuntimeOption configures NewWebhookRuntime.
type WebhookRuntimeOption func(*WebhookRuntime)

// WithWebhookClock replaces the wall clock.
func WithWebhookClock(now func() time.Time) WebhookRuntimeOption {
	return func(r *WebhookRuntime) {
		if now != nil {
			r.now = now
		}
	}
}

// WithWebhookTicker replaces the poll ticker.
func WithWebhookTicker(newTicker func(interval time.Duration) (<-chan time.Time, func())) WebhookRuntimeOption {
	return func(r *WebhookRuntime) {
		if newTicker != nil {
			r.newTicker = newTicker
		}
	}
}

// WithWebhookPollInterval replaces WebhookPollInterval; a non-positive value
// keeps it.
func WithWebhookPollInterval(interval time.Duration) WebhookRuntimeOption {
	return func(r *WebhookRuntime) {
		if interval > 0 {
			r.interval = interval
		}
	}
}

// WithWebhookRequestTimeout replaces WebhookRequestTimeout; a non-positive
// value keeps it.
func WithWebhookRequestTimeout(timeout time.Duration) WebhookRuntimeOption {
	return func(r *WebhookRuntime) {
		if timeout > 0 {
			r.timeout = timeout
		}
	}
}

// WithWebhookLogger replaces utils.Logger.
func WithWebhookLogger(logger *logrus.Logger) WebhookRuntimeOption {
	return func(r *WebhookRuntime) { r.logger = logger }
}

// WebhookRuntime owns the delivery goroutine. A runtime runs once: Start after
// Stop does nothing.
type WebhookRuntime struct {
	repo    repository.WebhookRepository
	clients WebhookClientFactory
	box     *utils.SecretBox
	logger  *logrus.Logger

	now       func() time.Time
	newTicker func(time.Duration) (<-chan time.Time, func())
	interval  time.Duration
	timeout   time.Duration

	// passMu serialises passes (the loop and direct RunOnce calls).
	passMu    sync.Mutex
	lastPrune time.Time

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	exited  chan struct{}
}

// NewWebhookRuntime builds the runtime over repo. clients hands out the
// public-only HTTP client of an endpoint URL; box opens the sealed secrets
// (context WebhookSecretContext).
func NewWebhookRuntime(repo repository.WebhookRepository, clients WebhookClientFactory, box *utils.SecretBox, opts ...WebhookRuntimeOption) *WebhookRuntime {
	r := &WebhookRuntime{
		repo:     repo,
		clients:  clients,
		box:      box,
		now:      time.Now,
		interval: WebhookPollInterval,
		timeout:  WebhookRequestTimeout,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Interval is the time between two passes.
func (r *WebhookRuntime) Interval() time.Duration { return r.interval }

// RequestTimeout bounds one POST.
func (r *WebhookRuntime) RequestTimeout() time.Duration { return r.timeout }

func (r *WebhookRuntime) log() *logrus.Logger {
	if r.logger != nil {
		return r.logger
	}
	return utils.Logger
}

// WebhookPassResult counts what one pass did.
type WebhookPassResult struct {
	Attempted int
	Succeeded int
	Retried   int
	Failed    int
	Recovered int64
	Pruned    int64
}

// RunOnce runs one pass: lease recovery, discovery, one attempt per due row,
// and the prune when it is due. It stops claiming once ctx is done.
func (r *WebhookRuntime) RunOnce(ctx context.Context) (WebhookPassResult, error) {
	r.passMu.Lock()
	defer r.passMu.Unlock()
	var result WebhookPassResult

	now := r.now()
	recovered, leaseFailed, err := r.repo.RecoverExpiredLeases(ctx, now, models.WebhookMaxAttempts)
	result.Recovered = recovered + leaseFailed
	if err != nil {
		r.logPassError(ctx, "webhook_recover_leases", err)
		return result, err
	}
	if recovered > 0 || leaseFailed > 0 {
		r.log().WithFields(logrus.Fields{"operation": "webhook_recover_leases", "error_code": models.WebhookErrorLeaseExpired}).
			Info("Webhook deliveries with an expired lease were recovered")
	}

	ids, err := r.repo.ListDueDeliveryIDs(ctx, now, webhookDiscoveryBatch)
	if err != nil {
		r.logPassError(ctx, "webhook_discover", err)
		return result, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		outcome, attempted := r.deliver(ctx, id)
		if !attempted {
			continue
		}
		result.Attempted++
		switch outcome {
		case models.WebhookDeliverySucceeded:
			result.Succeeded++
		case models.WebhookDeliveryPending:
			result.Retried++
		case models.WebhookDeliveryFailed:
			result.Failed++
		}
	}

	if ctx.Err() == nil && (r.lastPrune.IsZero() || !r.now().Before(r.lastPrune.Add(WebhookPruneInterval))) {
		pruneAt := r.now()
		pruned, err := r.repo.PruneFinished(ctx, pruneAt.Add(-WebhookDeliveryRetention))
		result.Pruned = pruned
		if err != nil {
			r.logPassError(ctx, "webhook_prune", err)
			return result, err
		}
		r.lastPrune = pruneAt
		if pruned > 0 {
			r.log().WithField("operation", "webhook_prune").Info("Finished webhook deliveries pruned")
		}
	}
	return result, nil
}

func (r *WebhookRuntime) logPassError(ctx context.Context, operation string, err error) {
	entry := r.log().WithField("operation", operation)
	if ctx.Err() != nil {
		entry.Info("Webhook pass interrupted")
		return
	}
	entry.WithError(utils.LogError(err)).Warn("Webhook pass failed")
}

// attemptOutcome is what one POST produced.
type attemptOutcome struct {
	succeeded  bool
	httpStatus *int
	errorCode  string
	// terminal fails the row whatever its attempt count.
	terminal bool
}

// deliver claims one row and runs its attempt. It reports the stored status
// and whether an attempt was claimed.
func (r *WebhookRuntime) deliver(ctx context.Context, id uint) (string, bool) {
	claimAt := r.now()
	delivery, err := r.repo.ClaimDelivery(ctx, id, claimAt, claimAt.Add(WebhookSendLease))
	if err != nil {
		if !errors.Is(err, repository.ErrWebhookDeliveryNotClaimed) && ctx.Err() == nil {
			r.log().WithFields(logrus.Fields{"operation": "webhook_claim", "delivery_id": id}).
				WithError(utils.LogError(err)).Warn("Webhook delivery claim failed")
		}
		return "", false
	}

	// From here the attempt runs to its end even when Stop cancels ctx: the
	// request has its own timeout and the finish write its own bound.
	work := context.WithoutCancel(ctx)
	outcome := r.attempt(work, delivery)
	return r.finish(work, delivery, outcome), true
}

func (r *WebhookRuntime) attempt(ctx context.Context, delivery *models.WebhookDelivery) attemptOutcome {
	endpoint, err := r.repo.GetEndpoint(ctx, delivery.EndpointID)
	if err != nil && !isNotFound(err) {
		// A database failure is not a verdict on the endpoint: nothing is
		// sent and the attempt is retried like a failed connection.
		return attemptOutcome{errorCode: models.WebhookErrorConnectionFailed}
	}
	if err != nil || endpoint == nil || !endpoint.IsActive {
		return attemptOutcome{errorCode: models.WebhookErrorEndpointInactive, terminal: true}
	}
	secret, err := r.box.Open(endpoint.SecretSealed)
	if err != nil || secret == "" {
		return attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable, terminal: true}
	}
	if _, err := webhookKey(secret); err != nil {
		return attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable, terminal: true}
	}
	if r.clients == nil {
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	}
	client, err := r.clients.PublicHTTPClient(endpoint.URL)
	if err != nil {
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	}
	defer client.CloseIdleConnections()

	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	req, err := NewWebhookRequest(reqCtx, endpoint.URL, secret, delivery, r.now())
	if err != nil {
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	}
	resp, err := client.Do(req)
	if err != nil {
		return classifyWebhookTransportError(reqCtx, err)
	}
	_, _ = io.CopyN(io.Discard, resp.Body, webhookResponseDrainBytes)
	_ = resp.Body.Close()
	status := resp.StatusCode
	if status >= 200 && status < 300 {
		return attemptOutcome{succeeded: true, httpStatus: &status}
	}
	if status >= 300 && status < 400 {
		return attemptOutcome{httpStatus: &status, errorCode: models.WebhookErrorRedirect}
	}
	return attemptOutcome{httpStatus: &status, errorCode: models.WebhookErrorHTTPStatus}
}

// classifyWebhookTransportError maps a failed request to an error code. The
// error itself, which quotes the URL, is never kept.
func classifyWebhookTransportError(reqCtx context.Context, err error) attemptOutcome {
	if status, ok := aeo.RedirectStatus(err); ok {
		return attemptOutcome{httpStatus: &status, errorCode: models.WebhookErrorRedirect}
	}
	switch {
	case errors.Is(err, aeo.ErrDestinationNotPermitted), errors.Is(err, aeo.ErrEndpointNotPermitted):
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	case errors.Is(err, aeo.ErrRedirectNotFollowed):
		return attemptOutcome{errorCode: models.WebhookErrorRedirect}
	case errors.Is(reqCtx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return attemptOutcome{errorCode: models.WebhookErrorTimeout}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return attemptOutcome{errorCode: models.WebhookErrorTimeout}
	}
	return attemptOutcome{errorCode: models.WebhookErrorConnectionFailed}
}

// finish stores the outcome of a claimed attempt and returns the stored
// status ("" when the fenced write matched nothing or failed).
func (r *WebhookRuntime) finish(ctx context.Context, delivery *models.WebhookDelivery, outcome attemptOutcome) string {
	now := r.now()
	result := repository.WebhookDeliveryOutcome{LastHTTPStatus: outcome.httpStatus, ErrorCode: outcome.errorCode, Now: now}
	switch {
	case outcome.succeeded:
		result.Status = models.WebhookDeliverySucceeded
		result.ErrorCode = ""
	case outcome.terminal || delivery.Attempts >= models.WebhookMaxAttempts:
		result.Status = models.WebhookDeliveryFailed
	default:
		result.Status = models.WebhookDeliveryPending
		next := now.Add(WebhookBackoff(delivery.Attempts, WebhookRetryBase, WebhookRetryMax))
		result.NextAttemptAt = &next
	}

	fields := logrus.Fields{
		"operation":   "webhook_deliver",
		"endpoint_id": delivery.EndpointID,
		"delivery_id": delivery.ID,
		"event_type":  delivery.EventType,
		"record_id":   delivery.RecordID,
		"attempts":    delivery.Attempts,
		"status":      result.Status,
		"error_code":  result.ErrorCode,
	}
	if outcome.httpStatus != nil {
		fields["http_status"] = *outcome.httpStatus
	}

	writeCtx, cancel := context.WithTimeout(ctx, webhookFinishTimeout)
	defer cancel()
	if err := r.repo.FinishDelivery(writeCtx, delivery.ID, delivery.Attempts, result); err != nil {
		r.log().WithFields(fields).WithError(utils.LogError(err)).Warn("Webhook delivery outcome not recorded")
		return ""
	}
	entry := r.log().WithFields(fields)
	switch result.Status {
	case models.WebhookDeliverySucceeded:
		entry.Info("Webhook delivered")
	case models.WebhookDeliveryFailed:
		entry.Warn("Webhook delivery failed")
	default:
		entry.Info("Webhook delivery will be retried")
	}
	return result.Status
}

// Start runs one pass at once on a goroutine derived from ctx, then one on
// every tick. Starting twice, or after Stop, is a no-op.
func (r *WebhookRuntime) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	exited := make(chan struct{})
	r.exited = exited
	ticks, stopTicker := r.newTicker(r.interval)
	go r.loop(loopCtx, ticks, stopTicker, exited)
	r.log().WithField("operation", "webhook_runtime").Info("Webhook runtime started")
}

func (r *WebhookRuntime) loop(ctx context.Context, ticks <-chan time.Time, stopTicker func(), exited chan<- struct{}) {
	defer close(exited)
	defer stopTicker()
	_, _ = r.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		if ctx.Err() != nil {
			return
		}
		_, _ = r.RunOnce(ctx)
	}
}

// Stop cancels the loop and waits for the goroutine, an attempt in flight
// included, or for ctx to end. Once it returns nil the runtime writes
// nothing. A repeated Stop after the join returns nil even on an ended ctx.
func (r *WebhookRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.stopped = true
	cancel := r.cancel
	exited := r.exited
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if exited == nil {
		return nil
	}
	select {
	case <-exited:
		return nil
	default:
	}
	select {
	case <-exited:
		r.log().WithField("operation", "webhook_runtime").Info("Webhook runtime stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("webhook runtime did not stop before the deadline: %w", ctx.Err())
	}
}
