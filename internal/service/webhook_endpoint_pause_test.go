package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// The endpoint circuit (#569): every sent attempt that fails adds one to
// webhook_endpoints.consecutive_failures, from the third consecutive failure
// paused_until holds the end of a pause (1 min doubling up to 1 h), discovery
// leaves the paused endpoint's rows out, a worker stops the group of an
// endpoint an attempt just paused, and a 2xx answer resets both columns. On
// SQLite and on a disposable TEST_MYSQL_DSN schema (the MySQL root skips
// without the variable and fails when it is unreachable). Every subtest runs
// on an injected clock truncated to the millisecond (paused_until is
// datetime(3), and MySQL rounds sub-millisecond digits).

func TestWebhookEndpointPauseSQLite(t *testing.T) {
	runWebhookEndpointPause(t, openTicketIntakeDB(t, false))
}

func TestWebhookEndpointPauseMySQL(t *testing.T) {
	runWebhookEndpointPause(t, openTicketIntakeDB(t, true))
}

func runWebhookEndpointPause(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	env := newWebhookEnv(t, db, nil)
	consumer := newWebhookConsumer(t)
	recordID := env.seedLead().ID
	clock := newWebhookClock(time.Now().UTC().Truncate(time.Millisecond))

	// Paths under /fail answer 503 unless healed; every other path 204.
	var healed sync.Map
	consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := healed.Load(r.URL.Path); !ok && strings.HasPrefix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	requestsTo := func(path string) int {
		n := 0
		for _, request := range consumer.received() {
			if request.Path == path {
				n++
			}
		}
		return n
	}
	runtime := func(opts ...webhook.WebhookRuntimeOption) *webhook.WebhookRuntime {
		return env.runtime(consumer.guard(nil), append([]webhook.WebhookRuntimeOption{webhook.WithWebhookClock(clock.Now)}, opts...)...)
	}
	stored := func(t *testing.T, id uint) models.WebhookEndpoint {
		t.Helper()
		var endpoint models.WebhookEndpoint
		require.NoError(t, db.Where("id = ?", id).Take(&endpoint).Error)
		return endpoint
	}
	// setCircuit writes the circuit state directly, as earlier sends would
	// have left it.
	setCircuit := func(t *testing.T, id uint, failures int, until *time.Time) {
		t.Helper()
		var value interface{}
		if until != nil {
			value = until.UTC()
		}
		require.NoError(t, db.Exec("UPDATE webhook_endpoints SET consecutive_failures = ?, paused_until = ? WHERE id = ?", failures, value, id).Error)
	}
	// retire finishes every pending row of an endpoint so that later
	// subtests discover only their own rows.
	retire := func(t *testing.T, id uint) {
		t.Helper()
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("endpoint_id = ? AND status = ?", id, models.WebhookDeliveryPending).
			Updates(map[string]interface{}{"status": models.WebhookDeliveryFailed, "next_attempt_at": nil}).Error)
	}
	due := func() time.Time { return clock.Now().Add(-time.Second) }

	t.Run("(a) the pause schedule", func(t *testing.T) {
		assert.Equal(t, 3, webhook.WebhookEndpointPauseAfter)
		assert.Equal(t, time.Minute, webhook.WebhookEndpointPauseBase)
		assert.Equal(t, time.Hour, webhook.WebhookEndpointPauseMax)
		want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
			time.Hour, time.Hour}
		for i, pause := range want {
			failures := i + 3
			assert.Equal(t, pause, webhook.WebhookEndpointPause(failures), "after %d failures", failures)
			until := webhook.WebhookEndpointPausedUntil(failures, clock.Now())
			require.NotNil(t, until)
			assert.True(t, until.Equal(clock.Now().Add(pause)))
		}
		for failures := 0; failures < 3; failures++ {
			assert.Zero(t, webhook.WebhookEndpointPause(failures))
			assert.Nil(t, webhook.WebhookEndpointPausedUntil(failures, clock.Now()), "no pause below 3 failures")
		}
	})

	// (b), (c) and (d) work on one endpoint in turn.
	path := "/fail-b"
	endpoint := env.endpoint(consumer.url(path), true, models.WebhookEventLeadCreated)

	t.Run("(b) skipped before paused_until, attempted at it", func(t *testing.T) {
		setCircuit(t, endpoint.ID, 2, nil)
		first := seedPoolDelivery(t, env, endpoint.ID, recordID, due())

		// The 3rd consecutive failure pauses the endpoint for a minute,
		// anchored at the finish time.
		finishAt := clock.Now()
		result, err := runtime().RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, requestsTo(path))
		state := stored(t, endpoint.ID)
		assert.Equal(t, 3, state.ConsecutiveFailures)
		require.NotNil(t, state.PausedUntil)
		assert.True(t, state.PausedUntil.Equal(finishAt.Add(time.Minute)), "paused_until = finish + 1 min, got %s", state.PausedUntil)

		// The row's own retry (1 min) is due at the same time, so a second
		// row due now proves the pause is what holds the endpoint back.
		second := seedPoolDelivery(t, env, endpoint.ID, recordID, due())
		clock.Set(state.PausedUntil.Add(-time.Second))
		result, err = runtime().RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.Attempted)
		assert.Equal(t, 1, requestsTo(path), "nothing is sent before paused_until")
		row := env.delivery(second.ID)
		assert.Equal(t, models.WebhookDeliveryPending, row.Status)
		assert.Equal(t, 0, row.Attempts)

		// At paused_until the endpoint is due again: one request, whose
		// failure (the 4th) pauses it for two minutes.
		clock.Set(*stored(t, endpoint.ID).PausedUntil)
		finishAt = clock.Now()
		result, err = runtime().RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 2, requestsTo(path), "one request at paused_until")
		state = stored(t, endpoint.ID)
		assert.Equal(t, 4, state.ConsecutiveFailures)
		require.NotNil(t, state.PausedUntil)
		assert.True(t, state.PausedUntil.Equal(finishAt.Add(2*time.Minute)), "got until %s", state.PausedUntil)
		assert.Equal(t, models.WebhookDeliveryPending, env.delivery(first.ID).Status, "the first row keeps its own retry schedule")
	})

	t.Run("(c) each further failure grows the pause up to an hour", func(t *testing.T) {
		// Each pass at paused_until sends one request, which fails and pauses
		// the endpoint again, so the rest of its group waits.
		failures := 4
		require.Equal(t, failures, stored(t, endpoint.ID).ConsecutiveFailures)
		for _, pause := range []time.Duration{4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
			time.Hour, time.Hour} {
			clock.Set(*stored(t, endpoint.ID).PausedUntil)
			finishAt := clock.Now()
			before := requestsTo(path)
			seedPoolDelivery(t, env, endpoint.ID, recordID, due())
			result, err := runtime().RunOnce(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, result.Attempted, "after %d failures", failures)
			assert.Equal(t, before+1, requestsTo(path), "one request at paused_until after %d failures", failures)
			failures++
			state := stored(t, endpoint.ID)
			assert.Equal(t, failures, state.ConsecutiveFailures)
			require.NotNil(t, state.PausedUntil)
			assert.True(t, state.PausedUntil.Equal(finishAt.Add(pause)), "after %d failures the pause is %s, got until %s", failures, pause,
				state.PausedUntil)
		}
		assert.Equal(t, 10, failures)
	})

	t.Run("(d) a success resets both columns", func(t *testing.T) {
		healed.Store(path, true)
		clock.Set(*stored(t, endpoint.ID).PausedUntil)
		seedPoolDelivery(t, env, endpoint.ID, recordID, due())
		result, err := runtime().RunOnce(ctx)
		require.NoError(t, err)
		assert.NotZero(t, result.Succeeded)
		state := stored(t, endpoint.ID)
		assert.Equal(t, 0, state.ConsecutiveFailures)
		assert.Nil(t, state.PausedUntil)
		var reset int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM webhook_endpoints WHERE id = ? AND consecutive_failures = 0 AND paused_until IS NULL",
			endpoint.ID).Scan(&reset).Error)
		assert.EqualValues(t, 1, reset, "the stored row reads 0 and NULL")
		retire(t, endpoint.ID)
	})

	t.Run("(e) other endpoints are not affected", func(t *testing.T) {
		pausedPath, healthyPath := "/fail-e-paused", "/ok-e-healthy"
		paused := env.endpoint(consumer.url(pausedPath), true, models.WebhookEventLeadCreated)
		healthy := env.endpoint(consumer.url(healthyPath), true, models.WebhookEventLeadCreated)
		defer retire(t, paused.ID)
		defer retire(t, healthy.ID)
		until := clock.Now().Add(time.Minute)
		setCircuit(t, paused.ID, 3, &until)
		var pausedRows []uint
		for i := 0; i < webhook.WebhookDiscoveryBatch+1; i++ {
			pausedRows = append(pausedRows, seedPoolDelivery(t, env, paused.ID, recordID, clock.Now().Add(-2*time.Minute)).ID)
		}
		healthyRow := seedPoolDelivery(t, env, healthy.ID, recordID, clock.Now().Add(-time.Minute))

		result, err := runtime(webhook.WithWebhookWorkers(2)).RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Succeeded)
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(healthyRow.ID).Status)
		assert.Equal(t, 1, requestsTo(healthyPath))
		assert.Zero(t, requestsTo(pausedPath), "nothing is sent to the paused endpoint")
		var untouched int64
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id IN ? AND status = ? AND attempts = 0", pausedRows,
			models.WebhookDeliveryPending).Count(&untouched).Error)
		assert.EqualValues(t, len(pausedRows), untouched, "the paused endpoint's rows stay pending with attempts 0")
	})

	t.Run("(f) an attempt that pauses the endpoint stops its group in the pass", func(t *testing.T) {
		path := "/fail-f"
		endpoint := env.endpoint(consumer.url(path), true, models.WebhookEventLeadCreated)
		defer retire(t, endpoint.ID)
		setCircuit(t, endpoint.ID, 2, nil)
		var rows []uint
		for i := 0; i < 5; i++ {
			rows = append(rows, seedPoolDelivery(t, env, endpoint.ID, recordID, due()).ID)
		}
		result, err := runtime().RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Retried)
		assert.Zero(t, result.Succeeded)
		assert.Zero(t, result.Failed)
		assert.Equal(t, 1, requestsTo(path), "exactly one request in the pass")
		assert.Equal(t, 1, env.delivery(rows[0]).Attempts, "the first row in id order was sent")
		for _, id := range rows[1:] {
			row := env.delivery(id)
			assert.Equal(t, models.WebhookDeliveryPending, row.Status)
			assert.Equal(t, 0, row.Attempts, "row %d was not claimed", id)
		}
	})

	t.Run("(g) attempts that send nothing do not count", func(t *testing.T) {
		inactive := env.endpoint(consumer.url("/fail-g-inactive"), false, models.WebhookEventLeadCreated)
		unreadable := env.endpoint(consumer.url("/fail-g-unreadable"), true, models.WebhookEventLeadCreated)
		defer retire(t, inactive.ID)
		defer retire(t, unreadable.ID)
		require.NoError(t, db.Model(&models.WebhookEndpoint{}).Where("id = ?", unreadable.ID).
			Update("secret_sealed", "enc:v1:not-a-real-ciphertext").Error)
		for i := 0; i < 3; i++ {
			seedPoolDelivery(t, env, inactive.ID, recordID, due())
			seedPoolDelivery(t, env, unreadable.ID, recordID, due())
		}
		result, err := runtime().RunOnce(ctx)
		require.NoError(t, err)
		// The inactive endpoint's rows fail; the unreadable endpoint is held
		// and its rows stay pending, unclaimed (#559).
		assert.Equal(t, 3, result.Failed)
		assert.Equal(t, 1, result.HeldEndpoints)
		var pending int64
		require.NoError(t, db.Model(&models.WebhookDelivery{}).
			Where("endpoint_id = ? AND status = ? AND attempts = 0", unreadable.ID, models.WebhookDeliveryPending).Count(&pending).Error)
		assert.EqualValues(t, 3, pending)
		assert.Zero(t, requestsTo("/fail-g-inactive")+requestsTo("/fail-g-unreadable"))
		for _, id := range []uint{inactive.ID, unreadable.ID} {
			state := stored(t, id)
			assert.Equal(t, 0, state.ConsecutiveFailures, "endpoint %d", id)
			assert.Nil(t, state.PausedUntil, "endpoint %d", id)
		}
	})

	t.Run("(h) a stale failure write after a reset changes nothing", func(t *testing.T) {
		endpoint := env.endpoint(consumer.url("/fail-h"), true, models.WebhookEventLeadCreated)
		updated, err := env.repo.RecordEndpointFailure(ctx, endpoint.ID, 0, nil)
		require.NoError(t, err)
		require.True(t, updated)
		updated, err = env.repo.RecordEndpointFailure(ctx, endpoint.ID, 1, nil)
		require.NoError(t, err)
		require.True(t, updated)
		require.Equal(t, 2, stored(t, endpoint.ID).ConsecutiveFailures)
		require.NoError(t, env.repo.RecordEndpointSuccess(ctx, endpoint.ID))
		until := clock.Now().Add(time.Minute)
		updated, err = env.repo.RecordEndpointFailure(ctx, endpoint.ID, 2, &until)
		require.NoError(t, err, "a lost fence is not an error")
		assert.False(t, updated, "a lost fence updates no row")
		state := stored(t, endpoint.ID)
		assert.Equal(t, 0, state.ConsecutiveFailures)
		assert.Nil(t, state.PausedUntil)
	})

	t.Run("(i) the pause log line", func(t *testing.T) {
		path := "/fail-i-private-path"
		endpoint := env.endpoint(consumer.url(path), true, models.WebhookEventLeadCreated)
		defer retire(t, endpoint.ID)
		setCircuit(t, endpoint.ID, 2, nil)
		seedPoolDelivery(t, env, endpoint.ID, recordID, due())

		logger := logrus.New()
		logger.SetOutput(io.Discard)
		logger.SetLevel(logrus.DebugLevel)
		hook := logtest.NewLocal(logger)
		_, err := runtime(webhook.WithWebhookLogger(logger)).RunOnce(ctx)
		require.NoError(t, err)

		state := stored(t, endpoint.ID)
		require.NotNil(t, state.PausedUntil)
		var paused []*logrus.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Webhook endpoint paused" {
				paused = append(paused, entry)
			}
			line, err := entry.String()
			require.NoError(t, err)
			for _, forbidden := range []string{consumer.url(path), "example.com", path, endpoint.Secret, "whsec_"} {
				assert.NotContains(t, line, forbidden)
			}
		}
		require.Len(t, paused, 1)
		entry := paused[0]
		assert.Equal(t, logrus.InfoLevel, entry.Level)
		keys := make([]string, 0, len(entry.Data))
		for key := range entry.Data {
			keys = append(keys, key)
		}
		assert.ElementsMatch(t, []string{"operation", "endpoint_id", "consecutive_failures", "paused_until"}, keys)
		assert.Equal(t, "webhook_endpoint_pause", entry.Data["operation"])
		assert.Equal(t, fmt.Sprint(endpoint.ID), fmt.Sprint(entry.Data["endpoint_id"]))
		assert.Equal(t, 3, entry.Data["consecutive_failures"])
		until, err := time.Parse(time.RFC3339Nano, fmt.Sprint(entry.Data["paused_until"]))
		require.NoError(t, err)
		assert.True(t, until.Equal(*state.PausedUntil), "the logged pause end is the stored one")
	})

	// Every endpoint of the earlier subtests has its pending rows retired, so
	// the only due rows in this pass are the two seeded here.
	t.Run("(j) a lost failure fence neither logs a pause nor stops the group", func(t *testing.T) {
		path := "/fail-j"
		endpoint := env.endpoint(consumer.url(path), true, models.WebhookEventLeadCreated)
		defer retire(t, endpoint.ID)
		setCircuit(t, endpoint.ID, 2, nil)
		seedPoolDelivery(t, env, endpoint.ID, recordID, due())
		seedPoolDelivery(t, env, endpoint.ID, recordID, due())

		logger := logrus.New()
		logger.SetOutput(io.Discard)
		logger.SetLevel(logrus.DebugLevel)
		hook := logtest.NewLocal(logger)
		// Replica B's 2xx reset lands between the first attempt's read (2
		// failures) and its failure write, so that write loses its fence.
		repo := newResetBeforeFailureWebhookRepository(env.repo, endpoint.ID)
		result, err := webhook.NewWebhookRuntime(repo, consumer.guard(nil), env.box, webhook.WithWebhookClock(clock.Now), webhook.WithWebhookLogger(logger)).RunOnce(ctx)
		require.NoError(t, err)

		assert.EqualValues(t, 1, repo.resets.Load(), "the concurrent reset was injected exactly once")
		assert.Equal(t, 2, requestsTo(path), "the group was not stopped: both rows were sent")
		assert.Equal(t, 2, result.Attempted)
		for _, entry := range hook.AllEntries() {
			assert.NotEqual(t, "Webhook endpoint paused", entry.Message, "a lost fence logs no pause")
		}
		state := stored(t, endpoint.ID)
		assert.Nil(t, state.PausedUntil, "no pause was stored")
		assert.Equal(t, 1, state.ConsecutiveFailures, "the first write lost to the reset, the second read 0 and wrote 1")
	})
}

// resetBeforeFailureWebhookRepository wraps the real repository: on the first
// RecordEndpointFailure for endpointID it first runs RecordEndpointSuccess for
// that endpoint, as another replica's 2xx answer would, and then delegates.
// Later calls go straight to the real repository.
type resetBeforeFailureWebhookRepository struct {
	repository.WebhookRepository
	endpointID uint
	resets     *atomic.Int32
}

func newResetBeforeFailureWebhookRepository(inner repository.WebhookRepository, endpointID uint) *resetBeforeFailureWebhookRepository {
	return &resetBeforeFailureWebhookRepository{WebhookRepository: inner, endpointID: endpointID, resets: &atomic.Int32{}}
}

func (r *resetBeforeFailureWebhookRepository) WithTx(tx *gorm.DB) repository.WebhookRepository {
	return &resetBeforeFailureWebhookRepository{WebhookRepository: r.WebhookRepository.WithTx(tx), endpointID: r.endpointID, resets: r.resets}
}

func (r *resetBeforeFailureWebhookRepository) RecordEndpointFailure(ctx context.Context, id uint, failuresBefore int, pausedUntil *time.Time) (bool, error) {
	if id == r.endpointID && r.resets.CompareAndSwap(0, 1) {
		if err := r.WebhookRepository.RecordEndpointSuccess(ctx, id); err != nil {
			return false, err
		}
	}
	return r.WebhookRepository.RecordEndpointFailure(ctx, id, failuresBefore, pausedUntil)
}
