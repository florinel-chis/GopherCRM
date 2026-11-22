package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// The per-pass secret check (#559): an active endpoint whose current signing
// secret the runtime's box cannot open is held. Its due rows are left out of
// discovery in the statement, never claimed or written, and the endpoint
// circuit is not touched; the rows go out once the secret opens again (a
// runtime with the right key, or a rotation). On SQLite and on a disposable
// TEST_MYSQL_DSN schema (the MySQL root skips without the variable and fails
// when it is unreachable). Every subtest runs on an injected clock truncated to
// the millisecond.

func TestWebhookSecretHoldSQLite(t *testing.T) {
	runWebhookSecretHold(t, openTicketIntakeDB(t, false))
}

func TestWebhookSecretHoldMySQL(t *testing.T) {
	runWebhookSecretHold(t, openTicketIntakeDB(t, true))
}

// webhookOtherMasterSecret stands for a master secret other than the one the
// endpoints were sealed with (a changed API_KEY_SECRET or JWT_SECRET).
const webhookOtherMasterSecret = "webhook-other-master-secret-9876543210"

// unreadableSecretRepository returns every endpoint from GetEndpoint with a
// current secret that does not open, while ListActiveEndpointSecrets still
// returns the stored one: the secret changes between the check and the
// attempt.
type unreadableSecretRepository struct {
	repository.WebhookRepository
}

func (u unreadableSecretRepository) GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error) {
	endpoint, err := u.WebhookRepository.GetEndpoint(ctx, id)
	if err != nil || endpoint == nil {
		return endpoint, err
	}
	copied := *endpoint
	copied.SecretSealed = "enc:v1:not-a-real-ciphertext"
	return &copied, nil
}

// syncBuffer is a log sink safe for the runtime's worker goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runWebhookSecretHold(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	env := newWebhookEnv(t, db, nil)
	consumer := newWebhookConsumer(t)
	recordID := env.seedLead().ID
	clock := newWebhookClock(time.Now().UTC().Truncate(time.Millisecond))
	due := func() time.Time { return clock.Now().Add(-time.Second) }

	wrongBox := utils.NewSecretBox(webhookOtherMasterSecret, webhook.WebhookSecretContext)
	// wrongService is the endpoint registry of the process that runs with the
	// other master secret: its rotations seal with that key.
	wrongService := webhook.NewWebhookService(env.repo, env.txManager, wrongBox)

	wrongRuntime := func(opts ...webhook.WebhookRuntimeOption) *webhook.WebhookRuntime {
		return webhook.NewWebhookRuntime(env.repo, consumer.guard(nil), wrongBox, append([]webhook.WebhookRuntimeOption{webhook.WithWebhookClock(clock.Now)}, opts...)...)
	}
	rightRuntime := func(opts ...webhook.WebhookRuntimeOption) *webhook.WebhookRuntime {
		return env.runtime(consumer.guard(nil), append([]webhook.WebhookRuntimeOption{webhook.WithWebhookClock(clock.Now)}, opts...)...)
	}
	requestsTo := func(path string) int {
		n := 0
		for _, request := range consumer.received() {
			if request.Path == path {
				n++
			}
		}
		return n
	}
	storedEndpoint := func(t *testing.T, id uint) models.WebhookEndpoint {
		t.Helper()
		var endpoint models.WebhookEndpoint
		require.NoError(t, db.Where("id = ?", id).Take(&endpoint).Error)
		return endpoint
	}
	rowsOf := func(t *testing.T, endpointID uint) []models.WebhookDelivery {
		t.Helper()
		var rows []models.WebhookDelivery
		require.NoError(t, db.Where("endpoint_id = ?", endpointID).Order("id").Find(&rows).Error)
		return rows
	}
	// retire deactivates an endpoint and finishes its pending rows so that
	// later subtests neither check nor discover them.
	retire := func(t *testing.T, id uint) {
		t.Helper()
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("endpoint_id = ? AND status = ?", id, models.WebhookDeliveryPending).
			Updates(map[string]interface{}{"status": models.WebhookDeliveryFailed, "next_attempt_at": nil}).Error)
		require.NoError(t, db.Model(&models.WebhookEndpoint{}).Where("id = ?", id).Update("is_active", false).Error)
	}
	// heldPair registers two active endpoints sealed with the right key, each
	// with two due rows.
	heldPair := func(t *testing.T, name string) (*models.WebhookEndpointCreated, *models.WebhookEndpointCreated) {
		t.Helper()
		a := env.endpoint(consumer.url("/"+name+"-a"), true, models.WebhookEventLeadCreated)
		b := env.endpoint(consumer.url("/"+name+"-b"), true, models.WebhookEventLeadCreated)
		t.Cleanup(func() { retire(t, a.ID); retire(t, b.ID) })
		for i := 0; i < 2; i++ {
			seedPoolDelivery(t, env, a.ID, recordID, due())
			seedPoolDelivery(t, env, b.ID, recordID, due())
		}
		return a, b
	}
	assertUntouched := func(t *testing.T, before, after []models.WebhookDelivery) {
		t.Helper()
		require.Len(t, after, len(before))
		for i := range before {
			was, now := before[i], after[i]
			assert.Equal(t, models.WebhookDeliveryPending, now.Status, "row %d", now.ID)
			assert.Equal(t, 0, now.Attempts, "row %d", now.ID)
			assert.Equal(t, was.ErrorCode, now.ErrorCode, "row %d", now.ID)
			assert.Nil(t, now.LastAttemptAt, "row %d", now.ID)
			assert.Nil(t, now.LeaseUntil, "row %d", now.ID)
			require.NotNil(t, now.NextAttemptAt)
			assert.True(t, now.NextAttemptAt.Equal(*was.NextAttemptAt), "row %d next_attempt_at", now.ID)
		}
	}
	assertCircuitUntouched := func(t *testing.T, ids ...uint) {
		t.Helper()
		for _, id := range ids {
			state := storedEndpoint(t, id)
			assert.Equal(t, 0, state.ConsecutiveFailures, "endpoint %d", id)
			assert.Nil(t, state.PausedUntil, "endpoint %d", id)
		}
	}
	assertDelivered := func(t *testing.T, endpointID uint) {
		t.Helper()
		rows := rowsOf(t, endpointID)
		require.NotEmpty(t, rows)
		for _, row := range rows {
			assert.Equal(t, models.WebhookDeliverySucceeded, row.Status, "row %d", row.ID)
			assert.Equal(t, 1, row.Attempts, "row %d", row.ID)
		}
	}

	t.Run("(a) rows of endpoints whose secret does not open stay pending and untouched", func(t *testing.T) {
		a, b := heldPair(t, "hold-a")
		beforeA, beforeB := rowsOf(t, a.ID), rowsOf(t, b.ID)
		dials := consumer.dials.Load()

		rt := wrongRuntime()
		assert.False(t, rt.SenderStatus().Paused, "not paused before the first check")
		for pass := 1; pass <= 2; pass++ {
			result, err := rt.RunOnce(ctx)
			require.NoError(t, err)
			assert.Zero(t, result.Attempted, "pass %d", pass)
			assert.Equal(t, 2, result.HeldEndpoints, "pass %d", pass)
			assert.True(t, rt.SenderStatus().Paused, "pass %d", pass)
		}
		assert.Equal(t, dials, consumer.dials.Load(), "nothing is dialled")
		assertUntouched(t, beforeA, rowsOf(t, a.ID))
		assertUntouched(t, beforeB, rowsOf(t, b.ID))
		assertCircuitUntouched(t, a.ID, b.ID)
	})

	t.Run("(b) a runtime with the right key delivers the held rows", func(t *testing.T) {
		a, b := heldPair(t, "hold-b")
		result, err := wrongRuntime().RunOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, result.HeldEndpoints)

		rt := rightRuntime()
		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.HeldEndpoints)
		assert.Equal(t, 4, result.Attempted)
		assert.Equal(t, 4, result.Succeeded)
		assert.False(t, rt.SenderStatus().Paused)
		assertDelivered(t, a.ID)
		assertDelivered(t, b.ID)
		assert.Equal(t, 2, requestsTo("/hold-b-a"))
		assert.Equal(t, 2, requestsTo("/hold-b-b"))
	})

	t.Run("(c) a rotation releases the rotated endpoint only", func(t *testing.T) {
		a, b := heldPair(t, "hold-c")
		beforeB := rowsOf(t, b.ID)
		rt := wrongRuntime()
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, result.HeldEndpoints)

		rotatedA, err := wrongService.RotateEndpointSecret(ctx, a.ID)
		require.NoError(t, err)
		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.HeldEndpoints)
		assert.Equal(t, 2, result.Succeeded)
		assert.True(t, rt.SenderStatus().Paused, "b is still held")
		assertDelivered(t, a.ID)
		assertUntouched(t, beforeB, rowsOf(t, b.ID))
		assert.Zero(t, requestsTo("/hold-c-b"))
		for _, request := range consumer.received() {
			if request.Path == "/hold-c-a" {
				assert.NoError(t, verifyStandardWebhook(rotatedA.Secret, request.Header, request.Body, clock.Now(), 5*time.Minute),
					"signed with the rotated secret")
			}
		}

		_, err = wrongService.RotateEndpointSecret(ctx, b.ID)
		require.NoError(t, err)
		result, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.HeldEndpoints)
		assert.Equal(t, 2, result.Succeeded)
		assert.False(t, rt.SenderStatus().Paused)
		assertDelivered(t, b.ID)
		assertCircuitUntouched(t, a.ID, b.ID)
	})

	t.Run("(d) a held backlog does not fill the discovery batch", func(t *testing.T) {
		held := env.endpoint(consumer.url("/hold-d-held"), true, models.WebhookEventLeadCreated)
		readable, err := wrongService.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: consumer.url("/hold-d-readable"),
			EventTypes: []string{models.WebhookEventLeadCreated}, IsActive: boolPtr(true)})
		require.NoError(t, err)
		t.Cleanup(func() { retire(t, held.ID); retire(t, readable.ID) })
		for i := 0; i < webhook.WebhookDiscoveryBatch+5; i++ {
			seedPoolDelivery(t, env, held.ID, recordID, clock.Now().Add(-time.Hour).Add(time.Duration(i)*time.Millisecond))
		}
		row := seedPoolDelivery(t, env, readable.ID, recordID, due())

		result, err := wrongRuntime().RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.HeldEndpoints)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Succeeded)
		stored := env.delivery(row.ID)
		assert.Equal(t, models.WebhookDeliverySucceeded, stored.Status)
		assert.Equal(t, 1, requestsTo("/hold-d-readable"))
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ? AND (status <> ? OR attempts <> 0)",
			held.ID, models.WebhookDeliveryPending), "no held row is touched")
	})

	t.Run("(e) a secret that stops opening after the check is retried, not failed", func(t *testing.T) {
		endpoint := env.endpoint(consumer.url("/hold-e"), true, models.WebhookEventLeadCreated)
		t.Cleanup(func() { retire(t, endpoint.ID) })
		row := seedPoolDelivery(t, env, endpoint.ID, recordID, due())
		dials := consumer.dials.Load()

		rt := webhook.NewWebhookRuntime(unreadableSecretRepository{env.repo}, consumer.guard(nil), env.box, webhook.WithWebhookClock(clock.Now))
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Zero(t, result.HeldEndpoints, "the check opened the stored secret")
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Retried)
		assert.Zero(t, result.Failed)
		assert.False(t, rt.SenderStatus().Paused)

		stored := env.delivery(row.ID)
		assert.Equal(t, models.WebhookDeliveryPending, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
		assert.Equal(t, models.WebhookErrorSecretUnavailable, stored.ErrorCode)
		require.NotNil(t, stored.NextAttemptAt)
		assert.True(t, stored.NextAttemptAt.Equal(clock.Now().Add(webhook.WebhookRetryBase)),
			"next attempt at the finish + 1 min, got %v", stored.NextAttemptAt)
		assert.Equal(t, dials, consumer.dials.Load(), "nothing is dialled")
		assertCircuitUntouched(t, endpoint.ID)
	})

	t.Run("(f) logs", func(t *testing.T) {
		a, b := heldPair(t, "hold-f-private-path")
		sink := &syncBuffer{}
		logger := logrus.New()
		logger.SetOutput(sink)
		logger.SetFormatter(&logrus.JSONFormatter{})
		logger.SetLevel(logrus.DebugLevel)
		rt := wrongRuntime(webhook.WithWebhookLogger(logger))

		entries := func(t *testing.T) []map[string]interface{} {
			t.Helper()
			var out []map[string]interface{}
			scanner := bufio.NewScanner(strings.NewReader(sink.String()))
			for scanner.Scan() {
				var entry map[string]interface{}
				require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry), scanner.Text())
				out = append(out, entry)
			}
			return out
		}
		withMessage := func(all []map[string]interface{}, msg string) []map[string]interface{} {
			var out []map[string]interface{}
			for _, entry := range all {
				if entry["msg"] == msg {
					out = append(out, entry)
				}
			}
			return out
		}
		keys := func(entry map[string]interface{}) []string {
			var out []string
			for key := range entry {
				out = append(out, key)
			}
			sort.Strings(out)
			return out
		}
		endpointIDs := func(found []map[string]interface{}) []uint {
			var out []uint
			for _, entry := range found {
				out = append(out, uint(entry["endpoint_id"].(float64)))
			}
			sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
			return out
		}
		const heldMsg = "Webhook deliveries held: endpoint signing secret cannot be opened"
		const resumedMsg = "Webhook deliveries resumed"

		for pass := 0; pass < 2; pass++ {
			_, err := rt.RunOnce(ctx)
			require.NoError(t, err)
		}
		held := withMessage(entries(t), heldMsg)
		require.Len(t, held, 2, "one line per held endpoint across two passes")
		assert.Equal(t, []uint{a.ID, b.ID}, endpointIDs(held))
		for _, entry := range held {
			assert.Equal(t, "error", entry["level"])
			assert.Equal(t, []string{"endpoint_id", "error_code", "level", "msg", "operation", "time"}, keys(entry))
			assert.Equal(t, "webhook_secret_check", entry["operation"])
			assert.Equal(t, models.WebhookErrorSecretUnavailable, entry["error_code"])
		}
		assert.Empty(t, withMessage(entries(t), resumedMsg))

		sealedA, sealedB := storedEndpoint(t, a.ID).SecretSealed, storedEndpoint(t, b.ID).SecretSealed
		rotatedA, err := wrongService.RotateEndpointSecret(ctx, a.ID)
		require.NoError(t, err)
		rotatedB, err := wrongService.RotateEndpointSecret(ctx, b.ID)
		require.NoError(t, err)
		for pass := 0; pass < 2; pass++ {
			result, err := rt.RunOnce(ctx)
			require.NoError(t, err)
			require.Zero(t, result.HeldEndpoints)
		}
		all := entries(t)
		assert.Len(t, withMessage(all, heldMsg), 2, "no further held line")
		resumed := withMessage(all, resumedMsg)
		require.Len(t, resumed, 2, "one resumed line per endpoint, not repeated")
		assert.Equal(t, []uint{a.ID, b.ID}, endpointIDs(resumed))
		for _, entry := range resumed {
			assert.Equal(t, "info", entry["level"])
			assert.Equal(t, []string{"endpoint_id", "level", "msg", "operation", "time"}, keys(entry))
			assert.Equal(t, "webhook_secret_check", entry["operation"])
		}

		forbidden := []string{a.URL, b.URL, "example.com", "hold-f-private-path", sealedA, sealedB,
			storedEndpoint(t, a.ID).SecretSealed, storedEndpoint(t, b.ID).SecretSealed,
			a.Secret, b.Secret, rotatedA.Secret, rotatedB.Secret, "whsec_"}
		for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
			for _, value := range forbidden {
				assert.NotContains(t, line, value)
			}
		}
	})
}
