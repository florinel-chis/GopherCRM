package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-3, AC-4, AC-6, AC-10, AC-11 (#345): the delivery runtime over the real
// repository on SQLite, an HTTPS consumer reached through an injected egress
// guard, and an injected clock where timing matters.

func webhookRuntimeEnv(t *testing.T) *webhookEnv {
	t.Helper()
	env := newWebhookEnv(t, openTicketIntakeDB(t, false), nil)
	env.wire(env.repo)
	return env
}

// requestTimestamp reads the webhook-timestamp of a received request.
func requestTimestamp(t *testing.T, request consumerRequest) time.Time {
	t.Helper()
	seconds, err := strconv.ParseInt(request.Header.Get("webhook-timestamp"), 10, 64)
	require.NoError(t, err)
	return time.Unix(seconds, 0)
}

func TestWebhookDeliveryAfterCommit(t *testing.T) {
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	endpoint := env.endpoint(consumer.url("/hook"), true, models.WebhookEventLeadCreated, models.WebhookEventCustomerCreated, models.WebhookEventTicketCreated)
	rt := env.runtime(consumer.guard(nil))
	ctx := context.Background()

	t.Run("a rolled-back create sends nothing", func(t *testing.T) {
		env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))
		require.ErrorIs(t, env.leads.Create(env.newLead()), injectedFailure)
		env.wire(env.repo)
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.Attempted)
		assert.Empty(t, consumer.received())
	})

	t.Run("a committed row is POSTed once on the first pass, signed", func(t *testing.T) {
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		rows := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
		require.Len(t, rows, 1)

		before := time.Now()
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Succeeded)

		received := consumer.received()
		require.Len(t, received, 1)
		request := received[0]
		assert.Equal(t, "/hook", request.Path)
		assert.Equal(t, rows[0].EventID, request.Header.Get("webhook-id"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, "GopherCRM-Webhooks/1", request.Header.Get("User-Agent"))
		assert.Equal(t, `{"id":"`+rows[0].EventID+`","type":"lead.created","occurred_at":"`+
			rows[0].OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z")+`","record_id":`+strconv.FormatUint(uint64(lead.ID), 10)+`}`,
			string(request.Body))
		require.NoError(t, verifyStandardWebhook(endpoint.Secret, request.Header, request.Body, time.Now(), 5*time.Minute))
		assert.False(t, requestTimestamp(t, request).Before(before.Truncate(time.Second)))

		stored := env.delivery(rows[0].ID)
		assert.Equal(t, models.WebhookDeliverySucceeded, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
		require.NotNil(t, stored.LastHTTPStatus)
		assert.Equal(t, http.StatusOK, *stored.LastHTTPStatus)
		assert.Nil(t, stored.NextAttemptAt)
		assert.Nil(t, stored.LeaseUntil)
		assert.Equal(t, "", stored.ErrorCode)
		require.NotNil(t, stored.LastAttemptAt)

		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.Attempted, "a second pass sends nothing")
		assert.Len(t, consumer.received(), 1)
	})

	t.Run("customer and ticket events are delivered the same way", func(t *testing.T) {
		customer := env.newCustomer()
		require.NoError(t, env.customers.Create(context.Background(), customer))
		ticket := env.newTicket(customer.ID)
		require.NoError(t, env.tickets.Create(ticket))
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, result.Succeeded)
		received := consumer.received()
		require.Len(t, received, 3)
		types := map[string]bool{}
		for _, request := range received[1:] {
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(request.Body, &body))
			assert.Len(t, body, 4, "id, type, occurred_at and record_id only")
			types[body["type"].(string)] = true
			require.NoError(t, verifyStandardWebhook(endpoint.Secret, request.Header, request.Body, time.Now(), 5*time.Minute))
		}
		assert.True(t, types[models.WebhookEventCustomerCreated])
		assert.True(t, types[models.WebhookEventTicketCreated])
	})

	t.Run("the started runtime delivers on the first poll after the commit", func(t *testing.T) {
		ticks := make(chan time.Time, 1)
		started := env.runtime(consumer.guard(nil), WithWebhookTicker(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
		started.Start(ctx)
		t.Cleanup(func() { _ = started.Stop(context.Background()) })
		before := len(consumer.received())

		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		ticks <- time.Now()
		deadline := time.Now().Add(10 * time.Second)
		for len(consumer.received()) == before {
			require.True(t, time.Now().Before(deadline), "the row is delivered on the next pass")
			time.Sleep(10 * time.Millisecond)
		}
		require.NoError(t, started.Stop(context.Background()))
		assert.Equal(t, models.WebhookDeliverySucceeded, env.deliveries(models.WebhookEventLeadCreated, lead.ID)[0].Status)
	})
}

func TestWebhookRetryBackoffAndCap(t *testing.T) {
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	consumer.setHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	endpoint := env.endpoint(consumer.url("/hook"), true, models.WebhookEventLeadCreated)
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	clock := newWebhookClock(start)
	rt := env.runtime(consumer.guard(nil), WithWebhookClock(clock.Now))
	ctx := context.Background()

	assert.Equal(t, 10, models.WebhookMaxAttempts)
	assert.Equal(t, time.Minute, WebhookRetryBase)
	assert.Equal(t, 6*time.Hour, WebhookRetryMax)
	expected := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, 64 * time.Minute, 128 * time.Minute, 256 * time.Minute}
	for i, delay := range expected {
		assert.Equal(t, delay, WebhookBackoff(i+1, WebhookRetryBase, WebhookRetryMax), "after attempt %d", i+1)
	}
	assert.Equal(t, 6*time.Hour, WebhookBackoff(10, WebhookRetryBase, WebhookRetryMax), "the ceiling")
	assert.Equal(t, 6*time.Hour, WebhookBackoff(64, WebhookRetryBase, WebhookRetryMax))

	lead := env.newLead()
	require.NoError(t, env.leads.Create(lead))
	rows := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
	require.Len(t, rows, 1)
	id := rows[0].ID

	for attempt := 1; attempt <= models.WebhookMaxAttempts; attempt++ {
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, result.Attempted, "attempt %d", attempt)
		require.Len(t, consumer.received(), attempt, "exactly one request per attempt")

		stored := env.delivery(id)
		assert.Equal(t, attempt, stored.Attempts)
		assert.Equal(t, models.WebhookErrorHTTPStatus, stored.ErrorCode)
		require.NotNil(t, stored.LastHTTPStatus)
		assert.Equal(t, http.StatusServiceUnavailable, *stored.LastHTTPStatus)
		assert.Nil(t, stored.LeaseUntil)
		if attempt < models.WebhookMaxAttempts {
			assert.Equal(t, models.WebhookDeliveryPending, stored.Status, "attempt %d returns the row to pending", attempt)
			require.NotNil(t, stored.NextAttemptAt)
			want := clock.Now().Add(expected[attempt-1])
			assert.WithinDuration(t, want, *stored.NextAttemptAt, time.Millisecond, "attempt %d", attempt)

			// Not due a moment before; due exactly at next_attempt_at.
			clock.Set(want.Add(-time.Second))
			result, err = rt.RunOnce(ctx)
			require.NoError(t, err)
			assert.Zero(t, result.Attempted, "not due before next_attempt_at")
			clock.Set(want)
		} else {
			assert.Equal(t, models.WebhookDeliveryFailed, stored.Status, "the attempt that reaches the cap fails the row")
			assert.Nil(t, stored.NextAttemptAt)
		}
	}

	clock.Advance(30 * 24 * time.Hour / 2)
	result, err := rt.RunOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, result.Attempted, "a failed row is never retried")
	received := consumer.received()
	require.Len(t, received, models.WebhookMaxAttempts, "exactly 10 requests")

	// Every attempt was signed afresh with its own timestamp.
	seen := map[string]bool{}
	for i, request := range received {
		signature := request.Header.Get("webhook-signature")
		assert.False(t, seen[signature], "attempt %d has a fresh signature", i+1)
		seen[signature] = true
		require.NoError(t, verifyStandardWebhook(endpoint.Secret, request.Header, request.Body, requestTimestamp(t, request), time.Second))
		assert.Equal(t, received[0].Body, request.Body, "the same event body")
	}

	t.Run("3xx records redirect and the Location is not followed", func(t *testing.T) {
		redirecting := newWebhookConsumer(t)
		redirecting.setHandler(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/hook" {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		require.NoError(t, env.endpoints.DeleteEndpoint(ctx, endpoint.ID))
		env.endpoint(redirecting.url("/hook"), true, models.WebhookEventLeadCreated)
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		redirectRuntime := env.runtime(redirecting.guard(nil), WithWebhookClock(clock.Now))
		result, err := redirectRuntime.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Retried)
		received := redirecting.received()
		require.Len(t, received, 1, "the redirect target receives no request")
		assert.Equal(t, "/hook", received[0].Path)
		stored := env.deliveries(models.WebhookEventLeadCreated, lead.ID)[0]
		assert.Equal(t, models.WebhookDeliveryPending, stored.Status)
		assert.Equal(t, models.WebhookErrorRedirect, stored.ErrorCode)
		require.NotNil(t, stored.LastHTTPStatus)
		assert.Equal(t, http.StatusFound, *stored.LastHTTPStatus)
	})
}

func TestWebhookUndeliverableEndpoint(t *testing.T) {
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	rt := env.runtime(consumer.guard(nil))
	ctx := context.Background()

	inactive := env.endpoint(consumer.url("/inactive"), true, models.WebhookEventLeadCreated)
	deleted := env.endpoint(consumer.url("/deleted"), true, models.WebhookEventLeadCreated)
	unreadable := env.endpoint(consumer.url("/unreadable"), true, models.WebhookEventLeadCreated)
	foreign := env.endpoint(consumer.url("/foreign"), true, models.WebhookEventLeadCreated)

	lead := env.newLead()
	require.NoError(t, env.leads.Create(lead))
	rows := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
	require.Len(t, rows, 4)

	isActive := false
	_, err := env.endpoints.UpdateEndpoint(ctx, inactive.ID, WebhookEndpointPatch{IsActive: &isActive})
	require.NoError(t, err)
	// A deleted endpoint whose rows survive (the delete removes them; this
	// row is detached by hand to model an endpoint missing at claim time).
	require.NoError(t, env.db.Exec("DELETE FROM webhook_endpoints WHERE id = ?", deleted.ID).Error)
	require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Where("id = ?", unreadable.ID).Update("secret_sealed", "enc:v1:not-a-real-ciphertext").Error)
	otherBox := utils.NewSecretBox(webhookTestMasterSecret, "configuration-secret")
	sealedElsewhere, err := otherBox.Seal("whsec_c2VjcmV0LWtleS1vZi1hbm90aGVyLWNvbnRleHQ=")
	require.NoError(t, err)
	require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Where("id = ?", foreign.ID).Update("secret_sealed", sealedElsewhere).Error)

	before := map[uint]models.WebhookDelivery{}
	for _, row := range rows {
		before[row.EndpointID] = row
	}

	result, err := rt.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Attempted, "only the inactive and the deleted endpoint's rows are attempted")
	assert.Equal(t, 2, result.Failed)
	assert.Equal(t, 2, result.HeldEndpoints, "the unreadable and the foreign endpoint are held (#559)")
	assert.Empty(t, consumer.received(), "nothing is sent")
	assert.Zero(t, consumer.dials.Load(), "nothing is dialled")

	for _, row := range env.deliveries(models.WebhookEventLeadCreated, lead.ID) {
		switch row.EndpointID {
		case inactive.ID, deleted.ID:
			assert.Equal(t, models.WebhookDeliveryFailed, row.Status, "endpoint %d", row.EndpointID)
			assert.Equal(t, models.WebhookErrorEndpointInactive, row.ErrorCode, "endpoint %d", row.EndpointID)
			assert.Equal(t, 1, row.Attempts)
			assert.Nil(t, row.NextAttemptAt, "never retried")
		case unreadable.ID, foreign.ID:
			// Held: never claimed, so the row is exactly as it was enqueued.
			was := before[row.EndpointID]
			assert.Equal(t, models.WebhookDeliveryPending, row.Status, "endpoint %d", row.EndpointID)
			assert.Equal(t, 0, row.Attempts, "endpoint %d", row.EndpointID)
			assert.Empty(t, row.ErrorCode, "endpoint %d", row.EndpointID)
			assert.Nil(t, row.LastAttemptAt, "endpoint %d", row.EndpointID)
			require.NotNil(t, row.NextAttemptAt)
			require.NotNil(t, was.NextAttemptAt)
			assert.True(t, row.NextAttemptAt.Equal(*was.NextAttemptAt), "endpoint %d", row.EndpointID)
		default:
			t.Fatalf("unexpected endpoint %d", row.EndpointID)
		}
		assert.Nil(t, row.LastHTTPStatus)
	}
	result, err = rt.RunOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, result.Attempted)
	assert.Equal(t, 2, result.HeldEndpoints)
	assert.True(t, rt.SenderStatus().Paused)
}

// deadlineRecordingWebhookRepository wraps the real repository and records,
// for GetEndpoint and ClearExpiredPreviousSecret, the time of each call and
// the deadline its context carried, then delegates.
type deadlineRecordingWebhookRepository struct {
	repository.WebhookRepository
	mu    sync.Mutex
	reads map[string][]recordedDeadline
}

type recordedDeadline struct {
	calledAt time.Time
	deadline time.Time
	ok       bool
}

func (r *deadlineRecordingWebhookRepository) record(name string, ctx context.Context) {
	// The timestamp is taken before the deadline is read, so deadline <=
	// calledAt + bound holds for any bound set before the call.
	calledAt := time.Now()
	deadline, ok := ctx.Deadline()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads == nil {
		r.reads = map[string][]recordedDeadline{}
	}
	r.reads[name] = append(r.reads[name], recordedDeadline{calledAt: calledAt, deadline: deadline, ok: ok})
}

func (r *deadlineRecordingWebhookRepository) calls(name string) []recordedDeadline {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedDeadline(nil), r.reads[name]...)
}

func (r *deadlineRecordingWebhookRepository) GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error) {
	r.record("GetEndpoint", ctx)
	return r.WebhookRepository.GetEndpoint(ctx, id)
}

func (r *deadlineRecordingWebhookRepository) ClearExpiredPreviousSecret(ctx context.Context, id uint, now time.Time) error {
	r.record("ClearExpiredPreviousSecret", ctx)
	return r.WebhookRepository.ClearExpiredPreviousSecret(ctx, id, now)
}

func TestWebhookRuntimeLifecycle(t *testing.T) {
	assert.Equal(t, 5*time.Second, WebhookPollInterval)
	assert.Equal(t, 10*time.Second, WebhookRequestTimeout)
	assert.Equal(t, 2*time.Minute, WebhookSendLease)
	assert.Equal(t, 30*24*time.Hour, WebhookDeliveryRetention)
	assert.Equal(t, time.Hour, WebhookPruneInterval)

	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	env.endpoint(consumer.url("/hook"), true, models.WebhookEventLeadCreated)

	t.Run("defaults and the injected interval", func(t *testing.T) {
		assert.Equal(t, WebhookPollInterval, env.runtime(nil).Interval())
		assert.Equal(t, WebhookRequestTimeout, env.runtime(nil).RequestTimeout())
		assert.Equal(t, 250*time.Millisecond, env.runtime(nil, WithWebhookPollInterval(250*time.Millisecond)).Interval())
		assert.NoError(t, env.runtime(nil).Stop(context.Background()), "a runtime never started stops at once")
		assert.Equal(t, 4, WebhookWorkers)
		assert.Equal(t, 1, env.runtime(nil).Workers(), "without the option the pass is sequential")
		assert.Equal(t, 3, env.runtime(nil, WithWebhookWorkers(3)).Workers())
		assert.Equal(t, 1, env.runtime(nil, WithWebhookWorkers(0)).Workers(), "a non-positive size keeps the default")
		assert.Equal(t, 1, env.runtime(nil, WithWebhookWorkers(-2)).Workers())
	})

	t.Run("the injected interval drives the passes", func(t *testing.T) {
		rt := env.runtime(consumer.guard(nil), WithWebhookPollInterval(20*time.Millisecond))
		rt.Start(context.Background())
		before := len(consumer.received())
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		deadline := time.Now().Add(10 * time.Second)
		for len(consumer.received()) == before {
			require.True(t, time.Now().Before(deadline), "a pass runs every interval")
			time.Sleep(10 * time.Millisecond)
		}
		require.NoError(t, rt.Stop(context.Background()))
	})

	t.Run("Stop drains an attempt in flight and nothing is written afterwards", func(t *testing.T) {
		baseline := runtime.NumGoroutine()
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var once sync.Once
		releaseAll := func() { once.Do(func() { close(release) }) }
		t.Cleanup(releaseAll)
		consumer.setHandler(func(w http.ResponseWriter, _ *http.Request) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			w.WriteHeader(http.StatusNoContent)
		})
		defer consumer.setHandler(nil)

		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		ticks := make(chan time.Time, 1)
		rt := env.runtime(consumer.guard(nil), WithWebhookTicker(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
		rt.Start(context.Background())
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the first pass never reached the consumer")
		}

		short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := rt.Stop(short)
		require.Error(t, err, "Stop does not return nil while an attempt is in flight")
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		releaseAll()
		require.NoError(t, rt.Stop(context.Background()))
		row := env.deliveries(models.WebhookEventLeadCreated, lead.ID)[0]
		assert.Equal(t, models.WebhookDeliverySucceeded, row.Status, "the drained attempt recorded its outcome")
		require.NotNil(t, row.LastHTTPStatus)
		assert.Equal(t, http.StatusNoContent, *row.LastHTTPStatus)

		later := env.newLead()
		require.NoError(t, env.leads.Create(later))
		select {
		case ticks <- time.Now():
		default:
		}
		time.Sleep(200 * time.Millisecond)
		pending := env.deliveries(models.WebhookEventLeadCreated, later.ID)[0]
		assert.Equal(t, models.WebhookDeliveryPending, pending.Status, "nothing is written after Stop returned nil")
		assert.Equal(t, 0, pending.Attempts)

		expired, cancelExpired := context.WithCancel(context.Background())
		cancelExpired()
		assert.NoError(t, rt.Stop(expired), "a repeated Stop returns nil even on an ended context")
		rt.Start(context.Background())
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 0, env.deliveries(models.WebhookEventLeadCreated, later.ID)[0].Attempts, "Start after Stop does nothing")

		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > baseline {
			if time.Now().After(deadline) {
				t.Fatalf("a stopped runtime holds no goroutine: %d running, %d before", runtime.NumGoroutine(), baseline)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("the attempt's database reads carry a deadline", func(t *testing.T) {
		env := webhookRuntimeEnv(t)
		reads := newWebhookConsumer(t)
		created := env.endpoint(reads.url("/hook"), true, models.WebhookEventLeadCreated)
		_, err := env.endpoints.RotateEndpointSecret(context.Background(), created.ID)
		require.NoError(t, err)
		// Close the rotation window, so the attempt clears the previous secret.
		closed := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Where("id = ?", created.ID).
			Update("previous_secret_expires_at", closed).Error)
		row := seedPoolDelivery(t, env, created.ID, env.seedLead().ID, time.Now().UTC().Add(-time.Minute))

		wrapper := &deadlineRecordingWebhookRepository{WebhookRepository: env.repo}
		rt := NewWebhookRuntime(wrapper, reads.guard(nil), env.box)
		result, err := rt.RunOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, result.Succeeded)

		for _, name := range []string{"GetEndpoint", "ClearExpiredPreviousSecret"} {
			calls := wrapper.calls(name)
			require.NotEmpty(t, calls, "%s is called", name)
			for _, call := range calls {
				require.True(t, call.ok, "%s runs with a deadline", name)
				assert.True(t, call.deadline.After(call.calledAt), "%s: the deadline lies after the call", name)
				assert.False(t, call.deadline.After(call.calledAt.Add(webhookFinishTimeout)),
					"%s: the deadline is at most webhookFinishTimeout after the call", name)
			}
		}
		var stored models.WebhookEndpoint
		require.NoError(t, env.db.Where("id = ?", created.ID).Take(&stored).Error)
		assert.Nil(t, stored.PreviousSecretSealed, "the expired previous secret is cleared")
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(row.ID).Status, "the request is not cut short by the read bound")
	})

	t.Run("Stop drains every attempt in flight of a pool", func(t *testing.T) {
		env := webhookRuntimeEnv(t)
		pool := newWebhookConsumer(t)
		now := time.Now().UTC().Truncate(time.Second)
		endpointA := env.endpoint(pool.url("/a"), true, models.WebhookEventLeadCreated)
		endpointB := env.endpoint(pool.url("/b"), true, models.WebhookEventLeadCreated)
		recordID := env.seedLead().ID
		a1 := seedPoolDelivery(t, env, endpointA.ID, recordID, now.Add(-2*time.Minute))
		a2 := seedPoolDelivery(t, env, endpointA.ID, recordID, now.Add(-time.Minute))
		b1 := seedPoolDelivery(t, env, endpointB.ID, recordID, now.Add(-2*time.Minute))
		require.Less(t, a1.ID, a2.ID)

		baseline := runtime.NumGoroutine()
		entered := make(chan string, 4)
		release := make(chan struct{})
		var once sync.Once
		releaseAll := func() { once.Do(func() { close(release) }) }
		t.Cleanup(releaseAll)
		pool.setHandler(func(w http.ResponseWriter, r *http.Request) {
			entered <- r.URL.Path
			<-release
			w.WriteHeader(http.StatusNoContent)
		})

		ticks := make(chan time.Time, 1)
		rt := env.runtime(pool.guard(nil), WithWebhookWorkers(2), WithWebhookClock(func() time.Time { return now }),
			WithWebhookTicker(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
		rt.Start(context.Background())
		seen := map[string]bool{}
		wait := time.After(5 * time.Second)
		for len(seen) < 2 {
			select {
			case path := <-entered:
				seen[path] = true
			case <-wait:
				releaseAll()
				t.Fatalf("both endpoints were not in flight at once: %v", seen)
			}
		}
		assert.Equal(t, map[string]bool{"/a": true, "/b": true}, seen)

		short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := rt.Stop(short)
		require.Error(t, err, "Stop does not return nil while attempts are in flight")
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		releaseAll()
		bounded, cancelBounded := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelBounded()
		require.NoError(t, rt.Stop(bounded))
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(a1.ID).Status, "A's attempt in flight was drained")
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(b1.ID).Status, "B's attempt in flight was drained")
		queued := env.delivery(a2.ID)
		assert.Equal(t, models.WebhookDeliveryPending, queued.Status, "no worker claims after Stop cancels")
		assert.Equal(t, 0, queued.Attempts)

		later := seedPoolDelivery(t, env, endpointB.ID, recordID, now.Add(-time.Minute))
		select {
		case ticks <- time.Now():
		default:
		}
		time.Sleep(200 * time.Millisecond)
		after := env.delivery(later.ID)
		assert.Equal(t, models.WebhookDeliveryPending, after.Status, "nothing is written after Stop returned nil")
		assert.Equal(t, 0, after.Attempts)

		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > baseline {
			if time.Now().After(deadline) {
				t.Fatalf("a stopped pool holds no goroutine: %d running, %d before", runtime.NumGoroutine(), baseline)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

func TestWebhookEgressRefusal(t *testing.T) {
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	hosts := map[string]string{
		"loopback.example.net": "127.0.0.1",
		"private.example.net":  "10.0.0.5",
		"metadata.example.net": "169.254.169.254",
		"ipv6.example.net":     "::1",
	}
	extra := map[string][]netip.Addr{}
	for host, addr := range hosts {
		extra[host] = []netip.Addr{netip.MustParseAddr(addr)}
		env.endpoint("https://"+host+"/hook", true, models.WebhookEventLeadCreated)
	}
	start := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	clock := newWebhookClock(start)
	rt := env.runtime(consumer.guard(extra), WithWebhookClock(clock.Now))

	lead := env.newLead()
	require.NoError(t, env.leads.Create(lead))
	result, err := rt.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, len(hosts), result.Attempted)
	assert.Zero(t, consumer.dials.Load(), "a refused destination is never dialled")
	assert.Empty(t, consumer.received())

	rows := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
	require.Len(t, rows, len(hosts))
	for _, row := range rows {
		assert.Equal(t, models.WebhookDeliveryPending, row.Status, "retried later")
		assert.Equal(t, models.WebhookErrorDestinationRefused, row.ErrorCode)
		assert.Nil(t, row.LastHTTPStatus)
		require.NotNil(t, row.NextAttemptAt)
		assert.WithinDuration(t, start.Add(WebhookRetryBase), *row.NextAttemptAt, time.Millisecond)
	}
}

func TestWebhookRequestTimeout(t *testing.T) {
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		w.WriteHeader(http.StatusOK)
	})
	env.endpoint(consumer.url("/slow"), true, models.WebhookEventLeadCreated)
	const timeout = 300 * time.Millisecond
	rt := env.runtime(consumer.guard(nil), WithWebhookRequestTimeout(timeout))
	assert.Equal(t, timeout, rt.RequestTimeout())

	lead := env.newLead()
	require.NoError(t, env.leads.Create(lead))
	started := time.Now()
	result, err := rt.RunOnce(context.Background())
	elapsed := time.Since(started)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Retried)
	assert.GreaterOrEqual(t, elapsed, timeout)
	assert.Less(t, elapsed, timeout+5*time.Second, "the attempt ends within the bound")

	row := env.deliveries(models.WebhookEventLeadCreated, lead.ID)[0]
	assert.Equal(t, models.WebhookDeliveryPending, row.Status)
	assert.Equal(t, models.WebhookErrorTimeout, row.ErrorCode)
	assert.Nil(t, row.LastHTTPStatus)
	assert.Equal(t, 1, row.Attempts)
}

func TestWebhookDeliveryLogAndPrune(t *testing.T) {
	env := webhookRuntimeEnv(t)
	ctx := context.Background()
	first := env.endpoint("https://example.com/one", true, models.WebhookEventLeadCreated, models.WebhookEventTicketCreated)
	second := env.endpoint("https://example.com/two", true, models.WebhookEventLeadCreated)

	lead := env.newLead()
	require.NoError(t, env.leads.Create(lead))
	customer := env.seedCustomer()
	ticket := env.newTicket(customer.ID)
	require.NoError(t, env.tickets.Create(ticket))
	firstRows := env.deliveries(models.WebhookEventLeadCreated, lead.ID)
	require.Len(t, firstRows, 2)
	require.NoError(t, env.db.Model(&models.WebhookDelivery{}).Where("id = ?", firstRows[0].ID).
		Updates(map[string]interface{}{"status": models.WebhookDeliveryFailed, "next_attempt_at": nil, "attempts": 10, "error_code": models.WebhookErrorHTTPStatus}).Error)

	t.Run("newest first, paginated and filtered", func(t *testing.T) {
		rows, total, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{}, 0, 100)
		require.NoError(t, err)
		assert.EqualValues(t, 3, total)
		require.Len(t, rows, 3)
		assert.Greater(t, rows[0].ID, rows[1].ID)
		assert.Greater(t, rows[1].ID, rows[2].ID)

		page, _, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{}, 1, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, rows[1].ID, page[0].ID)

		byEndpoint, total, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{EndpointID: second.ID}, 0, 100)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		assert.Equal(t, second.ID, byEndpoint[0].EndpointID)
		failed, total, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{Status: models.WebhookDeliveryFailed}, 0, 100)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		assert.Equal(t, firstRows[0].ID, failed[0].ID)
		tickets, total, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{EventType: models.WebhookEventTicketCreated}, 0, 100)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		assert.Equal(t, ticket.ID, tickets[0].RecordID)
		assert.Equal(t, first.ID, tickets[0].EndpointID)

		_, _, err = env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{Status: "done"}, 0, 10)
		assert.ErrorAs(t, err, new(*WebhookValidationError))
		_, _, err = env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{EventType: "lead.archived"}, 0, 10)
		assert.ErrorAs(t, err, new(*WebhookValidationError))

		raw, err := json.Marshal(rows)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "lease_until")
		var decoded []map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		for _, key := range []string{"id", "endpoint_id", "event_id", "event_type", "record_id", "occurred_at", "status", "attempts",
			"next_attempt_at", "last_attempt_at", "last_http_status", "error_code", "created_at", "updated_at"} {
			assert.Contains(t, decoded[0], key)
		}
		assert.Len(t, decoded[0], 14)
	})

	t.Run("finished rows older than the retention are pruned at most hourly", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		clock := newWebhookClock(now)
		rt := env.runtime(nil, WithWebhookClock(clock.Now))
		seed := func(status string, updated time.Time) uint {
			row := &models.WebhookDelivery{EndpointID: first.ID, EventID: "evt_prune_" + strconv.FormatUint(webhookTestSeq.Add(1), 10),
				EventType: models.WebhookEventLeadCreated, RecordID: 1, OccurredAt: updated, Status: status, CreatedAt: updated, UpdatedAt: updated}
			if status == models.WebhookDeliveryPending {
				due := now.Add(48 * time.Hour)
				row.NextAttemptAt = &due
			}
			require.NoError(t, env.db.Create(row).Error)
			return row.ID
		}
		exists := func(id uint) bool { return env.count(&models.WebhookDelivery{}, "id = ?", id) == 1 }
		old := now.Add(-WebhookDeliveryRetention - time.Hour)
		oldSucceeded := seed(models.WebhookDeliverySucceeded, old)
		oldFailed := seed(models.WebhookDeliveryFailed, old)
		oldPending := seed(models.WebhookDeliveryPending, old)
		// The clock advances 1 h 10 min below; keep recent younger than the
		// retention for the whole subtest.
		recent := seed(models.WebhookDeliverySucceeded, now.Add(-WebhookDeliveryRetention+3*time.Hour))

		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 2, result.Pruned)
		assert.False(t, exists(oldSucceeded))
		assert.False(t, exists(oldFailed))
		assert.True(t, exists(oldPending), "pending rows are never pruned")
		assert.True(t, exists(recent), "rows younger than 30 days stay")

		another := seed(models.WebhookDeliveryFailed, old)
		clock.Advance(10 * time.Minute)
		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.Pruned, "no second prune within the hour")
		assert.True(t, exists(another))

		clock.Advance(WebhookPruneInterval)
		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 1, result.Pruned)
		assert.False(t, exists(another))
		assert.True(t, exists(oldPending))
		assert.True(t, exists(recent), "rows younger than 30 days stay")
	})
}

// TestWebhookLogsOmitSecrets captures every log line of the endpoint service,
// the outbox, the runtime and the handler-facing service calls.
func TestWebhookLogsOmitSecrets(t *testing.T) {
	env := webhookRuntimeEnv(t)
	var buffer lockedBuffer
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	utils.Logger.SetOutput(&buffer)
	t.Cleanup(func() { utils.Logger = previous })
	ctx := context.Background()

	consumer := newWebhookConsumer(t)
	const responseCanary = "RESPONSE-BODY-CANARY-5e1d"
	var status sync.Map
	status.Store("code", http.StatusOK)
	consumer.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		code, _ := status.Load("code")
		w.WriteHeader(code.(int))
		_, _ = w.Write([]byte(responseCanary))
	})
	good := env.endpoint(consumer.url("/private-path-canary"), true, models.WebhookEventLeadCreated)
	refused := env.endpoint("https://refused-host-canary.example.net/hook", true, models.WebhookEventLeadCreated)
	rt := env.runtime(consumer.guard(map[string][]netip.Addr{"refused-host-canary.example.net": {netip.MustParseAddr("10.0.0.9")}}))

	lead := &models.Lead{FirstName: "Personal-First-Canary", LastName: "Personal-Last-Canary", Email: "personal.canary@webhooks.test",
		OwnerID: env.owner.ID, Status: models.LeadStatusNew}
	require.NoError(t, env.leads.Create(lead))
	_, err := rt.RunOnce(ctx)
	require.NoError(t, err)

	status.Store("code", http.StatusInternalServerError)
	second := &models.Lead{FirstName: "Personal-First-Canary", LastName: "Personal-Last-Canary", Email: "personal.canary2@webhooks.test",
		OwnerID: env.owner.ID, Status: models.LeadStatusNew}
	require.NoError(t, env.leads.Create(second))
	_, err = rt.RunOnce(ctx)
	require.NoError(t, err)

	_, err = env.endpoints.UpdateEndpoint(ctx, good.ID, WebhookEndpointPatch{URL: strPtr(consumer.url("/changed-path-canary"))})
	require.NoError(t, err)
	_, err = env.endpoints.CreateEndpoint(ctx, WebhookEndpointInput{URL: "http://insecure-canary.example.net/x", EventTypes: []string{"lead.created"}})
	require.Error(t, err)
	require.NoError(t, env.endpoints.DeleteEndpoint(ctx, refused.ID))
	require.Error(t, env.endpoints.DeleteEndpoint(ctx, refused.ID))

	var sealed []string
	require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Pluck("secret_sealed", &sealed).Error)
	received := consumer.received()
	require.NotEmpty(t, received)

	logs := buffer.String()
	require.Contains(t, logs, `"operation":"webhook_deliver"`, "the runtime logged")
	require.Contains(t, logs, `"operation":"webhook_endpoint_create"`, "the endpoint service logged")
	forbidden := []string{
		"example.com", "example.net", "private-path-canary", "changed-path-canary", "refused-host-canary", "insecure-canary",
		good.Secret, refused.Secret, responseCanary,
		"Personal-First-Canary", "Personal-Last-Canary", "personal.canary",
	}
	for _, value := range sealed {
		forbidden = append(forbidden, value)
	}
	for _, request := range received {
		forbidden = append(forbidden, request.Header.Get("webhook-signature"), string(request.Body))
	}
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		assert.NotContains(t, logs, value)
	}

	// The webhook lines carry only the agreed fields.
	allowed := map[string]bool{"level": true, "msg": true, "time": true, "operation": true, "endpoint_id": true, "delivery_id": true,
		"event_type": true, "record_id": true, "attempts": true, "status": true, "http_status": true, "error_code": true, "error": true}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		operation, _ := entry["operation"].(string)
		if !strings.HasPrefix(operation, "webhook_") {
			continue
		}
		for key := range entry {
			assert.True(t, allowed[key], "webhook log field %q in %s", key, line)
		}
	}
}

func strPtr(value string) *string { return &value }
