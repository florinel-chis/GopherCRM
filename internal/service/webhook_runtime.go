package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
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
// next_attempt_at <= now) with a plain SELECT of id and endpoint_id, and hands
// them to a bounded pool of workers (WebhookWorkers in production, set with
// WithWebhookWorkers; 1 without the option). The rows of one endpoint go to a
// single worker, sorted by ascending id (discovery orders by next_attempt_at,
// so a retried row can come back after newer ones), and that worker sends them
// one at a time: a slow endpoint holds only its own worker. Each row is
// claimed just before its send, by primary key with a conditional UPDATE
// (pending -> sending, attempts + 1, last_attempt_at, lease_until), so the
// lease covers only the row being sent; it is POSTed once through the
// public-only egress client and finished with a conditional UPDATE fenced on
// status sending and the claimed attempts. The id order holds within one
// runtime's pass only: a retry, or a second replica, can still reorder an
// endpoint's events.
// A failure below WebhookMaxAttempts returns the row to pending at now +
// min(WebhookRetryBase * 2^(attempts-1), WebhookRetryMax); the attempt that
// reaches the cap stores failed. Finished rows older than
// WebhookDeliveryRetention are pruned at most once per WebhookPruneInterval.
//
// Each endpoint has a circuit (#569). Every attempt whose request was sent
// (the endpoint was read, active and its secret opened) and failed adds one to
// webhook_endpoints.consecutive_failures; from the WebhookEndpointPauseAfter-th
// consecutive failure paused_until is set to the finish time +
// min(WebhookEndpointPauseBase * 2^(failures-WebhookEndpointPauseAfter),
// WebhookEndpointPauseMax). A 2xx answer resets both. Discovery leaves out
// every row of an endpoint whose pause has not ended, and a worker skips the
// rest of its group as soon as an attempt pauses the endpoint: those rows stay
// pending, unclaimed, with their attempts and next_attempt_at. Attempts that
// send nothing (a database failure reading the endpoint, endpoint_inactive,
// secret_unavailable, no client factory) leave the circuit alone, and so does
// the test ping. A manual redelivery of a paused endpoint's row waits for the
// pause, and a PUT on the endpoint does not clear it.
//
// Before discovery every pass checks the current secret of each active
// endpoint (#559): it reads their id and secret_sealed with a plain SELECT and
// opens each one the way a send would (webhookOpenCurrentSecret). The
// endpoints whose secret does not open, for example after the master secret
// changed, form the held set, and discovery leaves out their rows in the
// statement, before the limit. Held rows are never claimed or written: they
// stay pending with their attempts, next_attempt_at, error_code and
// last_attempt_at, and the circuit is not touched. The set is judged again on
// every pass, so the rows go out on the first pass after a restart with the
// right key or a rotation of the endpoint's secret. Entering the set logs one
// Error, leaving it one Info; SenderStatus reports paused while the last check
// held an endpoint. An endpoint whose secret stops opening between the check
// and its attempt is retried on the normal schedule with secret_unavailable
// (nothing sent, circuit untouched), and the next check holds it.
//
// Stop cancels the loop: no worker starts a further claim, and every attempt
// in flight runs to its end (its request is bounded by the request timeout and
// its finish write by webhookFinishTimeout). A pass joins its workers before
// it returns and the loop goroutine joins after the pass, so nothing is
// written after Stop returns nil.
//
// Log lines carry operation, endpoint_id, delivery_id, event_type, record_id,
// attempts, status, http_status, error_code, consecutive_failures and
// paused_until only: never the URL, its host,
// a secret (current or previous), the signature or a body. Transport errors, which quote the URL,
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
	// WebhookWorkers is the production size of the sender pool: at most this
	// many endpoints are sent to at the same time by one runtime.
	WebhookWorkers = 4
	// WebhookEndpointPauseAfter is how many consecutive failed sends pause an
	// endpoint for the first time (#569).
	WebhookEndpointPauseAfter = 3
	// WebhookEndpointPauseBase is the first pause; each further consecutive
	// failure doubles it.
	WebhookEndpointPauseBase = time.Minute
	// WebhookEndpointPauseMax caps the pause of an endpoint.
	WebhookEndpointPauseMax = time.Hour
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

// WebhookEndpointPause is the pause after the given number of consecutive
// failed sends of one endpoint (#569): zero below WebhookEndpointPauseAfter,
// then WebhookBackoff(failures-WebhookEndpointPauseAfter+1,
// WebhookEndpointPauseBase, WebhookEndpointPauseMax), so 1, 2, 4, 8, 16 and 32
// minutes, then an hour from the 9th failure on.
func WebhookEndpointPause(failures int) time.Duration {
	if failures < WebhookEndpointPauseAfter {
		return 0
	}
	return WebhookBackoff(failures-WebhookEndpointPauseAfter+1, WebhookEndpointPauseBase, WebhookEndpointPauseMax)
}

// webhookEndpointPausedUntil is the stored paused_until after the given
// number of consecutive failed sends, anchored at finishedAt; nil below
// WebhookEndpointPauseAfter.
func webhookEndpointPausedUntil(failures int, finishedAt time.Time) *time.Time {
	pause := WebhookEndpointPause(failures)
	if pause <= 0 {
		return nil
	}
	until := finishedAt.Add(pause)
	return &until
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

// WithWebhookWorkers sets the size of the sender pool; a non-positive value
// keeps the default of 1 (the sequential pass).
func WithWebhookWorkers(n int) WebhookRuntimeOption {
	return func(r *WebhookRuntime) {
		if n > 0 {
			r.workers = n
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
	workers   int

	// passMu serialises passes (the loop and direct RunOnce calls).
	passMu    sync.Mutex
	lastPrune time.Time

	// heldMu guards held and checked: the pass writes them, SenderStatus
	// reads them from the HTTP goroutines.
	heldMu  sync.Mutex
	held    map[uint]struct{}
	checked bool

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
		workers:  1,
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

// Workers is the size of the sender pool.
func (r *WebhookRuntime) Workers() int { return r.workers }

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
	// HeldEndpoints is the number of active endpoints whose current secret
	// did not open in this pass's check (#559); their rows were left out.
	HeldEndpoints int
}

// WebhookSenderStatus is what GET /health reports about the sender (#559).
type WebhookSenderStatus struct {
	// Paused is true when the last completed secret check held at least one
	// active endpoint; false before the first check.
	Paused bool
}

// SenderStatus reports the state of the last completed secret check. It is
// safe for concurrent use with a running pass.
func (r *WebhookRuntime) SenderStatus() WebhookSenderStatus {
	r.heldMu.Lock()
	defer r.heldMu.Unlock()
	return WebhookSenderStatus{Paused: r.checked && len(r.held) > 0}
}

// checkEndpointSecrets opens the current secret of every active endpoint
// (#559), replaces the held set with the endpoints whose secret does not open
// and returns their ids in id order. The set and its enter/leave log lines
// change under one lock, so two passes running together (the loop and a
// direct RunOnce) never log one change twice. A read error leaves the set as
// it was.
func (r *WebhookRuntime) checkEndpointSecrets(ctx context.Context) ([]uint, error) {
	endpoints, err := r.repo.ListActiveEndpointSecrets(ctx)
	if err != nil {
		return nil, err
	}
	next := make(map[uint]struct{})
	var held []uint
	for _, endpoint := range endpoints {
		if _, ok := webhookOpenCurrentSecret(r.box, endpoint.SecretSealed); !ok {
			next[endpoint.ID] = struct{}{}
			held = append(held, endpoint.ID)
		}
	}

	r.heldMu.Lock()
	defer r.heldMu.Unlock()
	for _, id := range held {
		if _, was := r.held[id]; !was {
			r.log().WithFields(logrus.Fields{
				"operation":   "webhook_secret_check",
				"endpoint_id": id,
				"error_code":  models.WebhookErrorSecretUnavailable,
			}).Error("Webhook deliveries held: endpoint signing secret cannot be opened")
		}
	}
	resumed := make([]uint, 0, len(r.held))
	for id := range r.held {
		if _, still := next[id]; !still {
			resumed = append(resumed, id)
		}
	}
	sort.Slice(resumed, func(a, b int) bool { return resumed[a] < resumed[b] })
	for _, id := range resumed {
		r.log().WithFields(logrus.Fields{"operation": "webhook_secret_check", "endpoint_id": id}).
			Info("Webhook deliveries resumed")
	}
	r.held = next
	r.checked = true
	return held, nil
}

// RunOnce runs one pass: lease recovery, the secret check of the active
// endpoints (#559), discovery without the held endpoints' rows, one attempt per due row
// through the worker pool, and the prune when it is due. It stops claiming
// once ctx is done and returns only after every worker has joined.
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

	held, err := r.checkEndpointSecrets(ctx)
	if err != nil {
		r.logPassError(ctx, "webhook_secret_check", err)
		return result, err
	}
	result.HeldEndpoints = len(held)

	due, err := r.repo.ListDueDeliveries(ctx, now, webhookDiscoveryBatch, held)
	if err != nil {
		r.logPassError(ctx, "webhook_discover", err)
		return result, err
	}
	sent := r.sendGroups(ctx, groupDueDeliveries(due))
	result.Attempted, result.Succeeded, result.Retried, result.Failed = sent.Attempted, sent.Succeeded, sent.Retried, sent.Failed

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

// groupDueDeliveries splits the discovered rows per endpoint, each group in
// ascending id order, the groups in the order their endpoint was first
// discovered.
func groupDueDeliveries(due []repository.WebhookDueDelivery) [][]uint {
	index := make(map[uint]int)
	var groups [][]uint
	for _, row := range due {
		i, ok := index[row.EndpointID]
		if !ok {
			i = len(groups)
			index[row.EndpointID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], row.ID)
	}
	for _, group := range groups {
		sort.Slice(group, func(a, b int) bool { return group[a] < group[b] })
	}
	return groups
}

// sendGroups runs the groups on at most r.workers goroutines (never more than
// there are groups); one worker works through a whole group, one row at a
// time, and checks ctx before every claim. An attempt that pauses its
// endpoint (#569) ends the group: the rest of its rows are neither claimed
// nor touched in this pass. It returns after every worker has
// joined, with the counters of every attempt.
func (r *WebhookRuntime) sendGroups(ctx context.Context, groups [][]uint) WebhookPassResult {
	workers := r.workers
	if workers > len(groups) {
		workers = len(groups)
	}
	queue := make(chan []uint, len(groups))
	for _, group := range groups {
		queue <- group
	}
	close(queue)

	results := make([]WebhookPassResult, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(own *WebhookPassResult) {
			defer wg.Done()
			for group := range queue {
				for _, id := range group {
					if ctx.Err() != nil {
						return
					}
					outcome, attempted, paused := r.deliver(ctx, id)
					if attempted {
						own.Attempted++
						switch outcome {
						case models.WebhookDeliverySucceeded:
							own.Succeeded++
						case models.WebhookDeliveryPending:
							own.Retried++
						case models.WebhookDeliveryFailed:
							own.Failed++
						}
					}
					if paused {
						break
					}
				}
			}
		}(&results[w])
	}
	wg.Wait()

	var total WebhookPassResult
	for _, part := range results {
		total.Attempted += part.Attempted
		total.Succeeded += part.Succeeded
		total.Retried += part.Retried
		total.Failed += part.Failed
	}
	return total
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
	// sent is true when the request went through sendWebhook: only such an
	// attempt counts in the endpoint circuit (#569).
	sent bool
	// failuresBefore is the endpoint's consecutive_failures as attempt read
	// it, the fence of the circuit write.
	failuresBefore int
}

// deliver claims one row and runs its attempt. It reports the stored status,
// whether an attempt was claimed, and whether the endpoint is now paused past
// r.now() (#569).
func (r *WebhookRuntime) deliver(ctx context.Context, id uint) (string, bool, bool) {
	claimAt := r.now()
	delivery, err := r.repo.ClaimDelivery(ctx, id, claimAt, claimAt.Add(WebhookSendLease))
	if err != nil {
		if !errors.Is(err, repository.ErrWebhookDeliveryNotClaimed) && ctx.Err() == nil {
			r.log().WithFields(logrus.Fields{"operation": "webhook_claim", "delivery_id": id}).
				WithError(utils.LogError(err)).Warn("Webhook delivery claim failed")
		}
		return "", false, false
	}

	// From here the attempt runs to its end even when Stop cancels ctx: the
	// request has its own timeout and the finish write its own bound.
	work := context.WithoutCancel(ctx)
	outcome := r.attempt(work, delivery)
	status := r.finish(work, delivery, outcome)
	if status == "" || !outcome.sent {
		// A lost fence or a failed finish records nothing, and neither does
		// an attempt that sent no request.
		return status, true, false
	}
	return status, true, r.recordEndpointHealth(work, delivery.EndpointID, outcome)
}

// recordEndpointHealth writes the endpoint circuit after a stored finish of a
// sent attempt (#569) and reports whether the endpoint is now paused past
// r.now(). A write error is one Warn and never fails the pass. A failure write
// that lost its fence (another replica's reset or failure landed first) logs
// nothing and reports false, so the group is not stopped on a pause that was
// never stored (#586).
func (r *WebhookRuntime) recordEndpointHealth(ctx context.Context, endpointID uint, outcome attemptOutcome) bool {
	writeCtx, cancel := context.WithTimeout(ctx, webhookFinishTimeout)
	defer cancel()
	if outcome.succeeded {
		if err := r.repo.RecordEndpointSuccess(writeCtx, endpointID); err != nil {
			r.logEndpointHealthError(endpointID, err)
		}
		return false
	}
	failures := outcome.failuresBefore + 1
	finishedAt := r.now()
	pausedUntil := webhookEndpointPausedUntil(failures, finishedAt)
	updated, err := r.repo.RecordEndpointFailure(writeCtx, endpointID, outcome.failuresBefore, pausedUntil)
	if err != nil {
		r.logEndpointHealthError(endpointID, err)
		return false
	}
	if !updated || pausedUntil == nil {
		return false
	}
	r.log().WithFields(logrus.Fields{
		"operation":            "webhook_endpoint_pause",
		"endpoint_id":          endpointID,
		"consecutive_failures": failures,
		"paused_until":         pausedUntil.UTC().Format(time.RFC3339Nano),
	}).Info("Webhook endpoint paused")
	return pausedUntil.After(r.now())
}

func (r *WebhookRuntime) logEndpointHealthError(endpointID uint, err error) {
	r.log().WithFields(logrus.Fields{"operation": "webhook_endpoint_health", "endpoint_id": endpointID}).
		WithError(utils.LogError(err)).Warn("Webhook endpoint health not recorded")
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
	// One clock reading decides the rotation window and stamps the request,
	// so a request is never signed with a secret whose window has closed.
	now := r.now()
	secrets, ok := webhookSigningSecrets(r.box, endpoint, now, r.log(), "webhook_deliver")
	if !ok {
		// The secret stopped opening after this pass's check: nothing is
		// sent, the circuit is left alone and the row is retried on the
		// normal schedule; the next check holds the endpoint (#559).
		return attemptOutcome{errorCode: models.WebhookErrorSecretUnavailable}
	}
	if endpoint.PreviousSecretExpiresAt != nil && !now.Before(*endpoint.PreviousSecretExpiresAt) {
		// The window is over: clear the previous secret, fenced on the expiry
		// in the statement so a rotation committed since the read survives.
		// A failure is housekeeping only and never blocks the send.
		if err := r.repo.ClearExpiredPreviousSecret(ctx, endpoint.ID, now); err != nil {
			r.log().WithFields(logrus.Fields{"operation": "webhook_clear_previous_secret", "endpoint_id": endpoint.ID}).
				WithError(utils.LogError(err)).Warn("Expired previous webhook secret not cleared")
		}
	}
	if r.clients == nil {
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	}
	outcome := sendWebhook(ctx, r.clients, endpoint.URL, r.timeout, func(reqCtx context.Context) (*http.Request, error) {
		return NewWebhookRequest(reqCtx, endpoint.URL, secrets, delivery, now)
	})
	outcome.failuresBefore = endpoint.ConsecutiveFailures
	return outcome
}

// webhookSigningSecrets returns the secrets a request to endpoint signed at
// now carries (#503): the opened current secret first, then the previous
// secret while its rotation window is open (now strictly before
// previous_secret_expires_at; at the expiry instant the window is over). ok is
// false when the current secret does not open or is not a whsec_ key
// (webhookOpenCurrentSecret, the same test the per-pass secret check makes, so
// the check holds such an endpoint's rows before they are claimed; an attempt
// that still meets it is retried with secret_unavailable). A previous secret that does not
// open is dropped with one Warn (operation, endpoint_id and
// error_code=previous_secret_unavailable only) and the request goes out with
// the current signature.
func webhookSigningSecrets(box *utils.SecretBox, endpoint *models.WebhookEndpoint, now time.Time, logger *logrus.Logger, operation string) ([]string, bool) {
	current, ok := webhookOpenCurrentSecret(box, endpoint.SecretSealed)
	if !ok {
		return nil, false
	}
	secrets := []string{current}
	if endpoint.PreviousSecretSealed == nil || endpoint.PreviousSecretExpiresAt == nil || !now.Before(*endpoint.PreviousSecretExpiresAt) {
		return secrets, true
	}
	previous, err := box.Open(*endpoint.PreviousSecretSealed)
	if err == nil && previous != "" {
		if _, err = webhookKey(previous); err == nil {
			return append(secrets, previous), true
		}
	}
	logger.WithFields(logrus.Fields{"operation": operation, "endpoint_id": endpoint.ID, "error_code": "previous_secret_unavailable"}).
		Warn("Previous webhook secret cannot be used; signing with the current secret only")
	return secrets, true
}

// webhookOpenCurrentSecret opens a sealed current secret: ok only when box
// opens it, it is not empty and it is a whsec_ key webhookKey accepts. The
// per-pass secret check and webhookSigningSecrets both use it (#559).
func webhookOpenCurrentSecret(box *utils.SecretBox, sealed string) (string, bool) {
	current, err := box.Open(sealed)
	if err != nil || current == "" {
		return "", false
	}
	if _, err := webhookKey(current); err != nil {
		return "", false
	}
	return current, true
}

// sendWebhook is the send path shared by deliveries and the test ping (which
// ignores outcome.sent and never touches the endpoint circuit): it
// gets the public-only client of url from clients (never nil), POSTs the
// request build returns once under a timeout bound (connection and TLS
// included), drains at most webhookResponseDrainBytes of the answer and
// classifies it. A redirect is never followed. The transport's own error,
// which quotes the URL, is classified and never returned.
func sendWebhook(ctx context.Context, clients WebhookClientFactory, url string, timeout time.Duration, build func(reqCtx context.Context) (*http.Request, error)) attemptOutcome {
	outcome := sendWebhookOnce(ctx, clients, url, timeout, build)
	outcome.sent = true
	return outcome
}

func sendWebhookOnce(ctx context.Context, clients WebhookClientFactory, url string, timeout time.Duration, build func(reqCtx context.Context) (*http.Request, error)) attemptOutcome {
	client, err := clients.PublicHTTPClient(url)
	if err != nil {
		return attemptOutcome{errorCode: models.WebhookErrorDestinationRefused}
	}
	defer client.CloseIdleConnections()

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := build(reqCtx)
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

// Stop cancels the loop and waits for the goroutine, every attempt in flight
// of the pool included, or for ctx to end. Once it returns nil the runtime writes
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
