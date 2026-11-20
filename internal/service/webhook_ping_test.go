package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestWebhookPing covers POST /webhooks/endpoints/:id/test at the service: one
// signed webhook.ping request through the delivery send path, nothing written.
// SQLite only by design: the ping's only statement is the endpoint read.
func TestWebhookPing(t *testing.T) {
	// pingEnv wires a fresh environment whose registry pings through the
	// consumer's guard.
	pingEnv := func(t *testing.T, consumer *webhookConsumer, extra map[string][]netip.Addr) (*webhookEnv, WebhookService) {
		t.Helper()
		env := webhookRuntimeEnv(t)
		return env, NewWebhookService(env.repo, env.txManager, env.box, WithWebhookPingClients(consumer.guard(extra)))
	}
	// unchanged asserts the ping wrote nothing: the delivery count and the
	// endpoint row stay as they were.
	type snapshot struct {
		deliveries int64
		endpoint   models.WebhookEndpoint
	}
	take := func(t *testing.T, env *webhookEnv, id uint) snapshot {
		t.Helper()
		var row models.WebhookEndpoint
		require.NoError(t, env.db.Where("id = ?", id).Take(&row).Error)
		return snapshot{deliveries: env.allDeliveries(), endpoint: row}
	}
	unchanged := func(t *testing.T, env *webhookEnv, before snapshot) {
		t.Helper()
		after := take(t, env, before.endpoint.ID)
		assert.Equal(t, before.deliveries, after.deliveries, "no webhook_deliveries row is written")
		assert.True(t, before.endpoint.UpdatedAt.Equal(after.endpoint.UpdatedAt), "updated_at is unchanged")
		assert.Equal(t, before.endpoint.IsActive, after.endpoint.IsActive, "is_active is unchanged")
		assert.Equal(t, before.endpoint.SecretSealed, after.endpoint.SecretSealed)
		assert.Equal(t, before.endpoint.URL, after.endpoint.URL)
	}
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		env, svc := pingEnv(t, consumer, nil)
		endpoint := env.endpoint(consumer.url("/ping"), true, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingSucceeded, result.Outcome)
		require.NotNil(t, result.HTTPStatus)
		assert.Equal(t, http.StatusOK, *result.HTTPStatus)
		assert.Nil(t, result.ErrorCode)

		received := consumer.received()
		require.Len(t, received, 1, "exactly one request")
		request := received[0]
		assert.Equal(t, "/ping", request.Path)
		require.NoError(t, verifyStandardWebhook(endpoint.Secret, request.Header, request.Body, time.Now(), 5*time.Minute),
			"the ping verifies under the create-time secret")
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, WebhookUserAgent, request.Header.Get("User-Agent"))

		var body map[string]any
		require.NoError(t, json.Unmarshal(request.Body, &body))
		assert.Equal(t, models.WebhookEventPing, body["type"])
		id, _ := body["id"].(string)
		assert.True(t, strings.HasPrefix(id, "evt_"), id)
		assert.Equal(t, request.Header.Get(WebhookHeaderID), id)
		assert.Equal(t, result.EventID, id)
		assert.NotContains(t, body, "record_id")
		occurredAt, _ := body["occurred_at"].(string)
		_, err = time.Parse(webhookOccurredAtLayout, occurredAt)
		assert.NoError(t, err, "occurred_at is UTC with milliseconds")
		assert.Len(t, body, 3, "id, type and occurred_at only")
		unchanged(t, env, before)
	})

	t.Run("inactive", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		env, svc := pingEnv(t, consumer, nil)
		endpoint := env.endpoint(consumer.url("/ping"), false, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingSucceeded, result.Outcome)
		require.NotNil(t, result.HTTPStatus)
		assert.Equal(t, http.StatusOK, *result.HTTPStatus)
		assert.Nil(t, result.ErrorCode)
		assert.Len(t, consumer.received(), 1)
		unchanged(t, env, before)
	})

	t.Run("http_500", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		consumer.setHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		env, svc := pingEnv(t, consumer, nil)
		endpoint := env.endpoint(consumer.url("/ping"), true, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingFailed, result.Outcome)
		require.NotNil(t, result.HTTPStatus)
		assert.Equal(t, http.StatusInternalServerError, *result.HTTPStatus)
		require.NotNil(t, result.ErrorCode)
		assert.Equal(t, models.WebhookErrorHTTPStatus, *result.ErrorCode)
		assert.Len(t, consumer.received(), 1, "a failed ping is not retried")
		unchanged(t, env, before)
	})

	t.Run("redirect", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		})
		env, svc := pingEnv(t, consumer, nil)
		endpoint := env.endpoint(consumer.url("/ping"), true, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingFailed, result.Outcome)
		require.NotNil(t, result.ErrorCode)
		assert.Equal(t, models.WebhookErrorRedirect, *result.ErrorCode)
		received := consumer.received()
		require.Len(t, received, 1, "the redirect is not followed")
		assert.Equal(t, "/ping", received[0].Path)
		unchanged(t, env, before)
	})

	t.Run("refused", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		env, svc := pingEnv(t, consumer, map[string][]netip.Addr{"private.example.net": {netip.MustParseAddr("10.1.2.3")}})
		endpoint := env.endpoint("https://private.example.net/hook", true, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingFailed, result.Outcome)
		require.NotNil(t, result.ErrorCode)
		assert.Equal(t, models.WebhookErrorDestinationRefused, *result.ErrorCode)
		assert.Nil(t, result.HTTPStatus)
		assert.Zero(t, consumer.dials.Load(), "a refused destination is never dialled")
		assert.Empty(t, consumer.received())
		unchanged(t, env, before)
	})

	t.Run("timeout", func(t *testing.T) {
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
		env, svc := pingEnv(t, consumer, nil)
		impl, ok := svc.(*webhookService)
		require.True(t, ok)
		assert.Equal(t, WebhookRequestTimeout, impl.pingTimeout, "the ping is bounded like a delivery")
		const timeout = 300 * time.Millisecond
		impl.pingTimeout = timeout
		endpoint := env.endpoint(consumer.url("/slow"), true, models.WebhookEventLeadCreated)
		before := take(t, env, endpoint.ID)

		started := time.Now()
		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		elapsed := time.Since(started)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, elapsed, timeout)
		assert.Less(t, elapsed, timeout+5*time.Second, "the ping ends within the bound")
		assert.Equal(t, models.WebhookPingFailed, result.Outcome)
		require.NotNil(t, result.ErrorCode)
		assert.Equal(t, models.WebhookErrorTimeout, *result.ErrorCode)
		assert.Nil(t, result.HTTPStatus)
		unchanged(t, env, before)
	})

	t.Run("secret_unavailable", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		env, svc := pingEnv(t, consumer, nil)
		endpoint := env.endpoint(consumer.url("/ping"), true, models.WebhookEventLeadCreated)
		require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Where("id = ?", endpoint.ID).
			UpdateColumn("secret_sealed", "enc:v1:not-openable").Error)
		before := take(t, env, endpoint.ID)

		result, err := svc.TestEndpoint(ctx, endpoint.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingFailed, result.Outcome)
		require.NotNil(t, result.ErrorCode)
		assert.Equal(t, models.WebhookErrorSecretUnavailable, *result.ErrorCode)
		assert.Nil(t, result.HTTPStatus)
		assert.Zero(t, consumer.dials.Load(), "nothing is dialled without a secret")
		assert.Empty(t, consumer.received())
		unchanged(t, env, before)
	})

	t.Run("not_found", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		_, svc := pingEnv(t, consumer, nil)
		_, err := svc.TestEndpoint(ctx, 999999)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Zero(t, consumer.dials.Load())
	})

	t.Run("no_ping_clients", func(t *testing.T) {
		consumer := newWebhookConsumer(t)
		env := webhookRuntimeEnv(t)
		endpoint := env.endpoint(consumer.url("/ping"), true, models.WebhookEventLeadCreated)
		_, err := env.endpoints.TestEndpoint(ctx, endpoint.ID)
		assert.ErrorIs(t, err, ErrWebhooksUnavailable)
		assert.Zero(t, consumer.dials.Load())
	})

	t.Run("log_privacy", func(t *testing.T) {
		var buffer lockedBuffer
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		utils.Logger.SetOutput(&buffer)
		t.Cleanup(func() { utils.Logger = previous })

		consumer := newWebhookConsumer(t)
		const responseCanary = "RESPONSE-BODY-CANARY-5e1d"
		consumer.setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(responseCanary))
		})
		env, svc := pingEnv(t, consumer, map[string][]netip.Addr{"refused-host-canary.example.net": {netip.MustParseAddr("10.0.0.9")}})
		good := env.endpoint(consumer.url("/private-path-canary"), true, models.WebhookEventLeadCreated)
		refused := env.endpoint("https://refused-host-canary.example.net/hook", true, models.WebhookEventLeadCreated)

		goodResult, err := svc.TestEndpoint(ctx, good.ID)
		require.NoError(t, err)
		require.NotNil(t, goodResult.ErrorCode)
		assert.Equal(t, models.WebhookErrorHTTPStatus, *goodResult.ErrorCode)
		refusedResult, err := svc.TestEndpoint(ctx, refused.ID)
		require.NoError(t, err)
		require.NotNil(t, refusedResult.ErrorCode)
		assert.Equal(t, models.WebhookErrorDestinationRefused, *refusedResult.ErrorCode)

		var sealed []string
		require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Pluck("secret_sealed", &sealed).Error)
		received := consumer.received()
		require.Len(t, received, 1)

		logs := buffer.String()
		require.Contains(t, logs, `"operation":"webhook_endpoint_test"`)
		require.Contains(t, logs, `"endpoint_id"`)
		require.Contains(t, logs, `"outcome"`)
		require.Contains(t, logs, `"error_code"`)
		require.Equal(t, 2, strings.Count(logs, `"operation":"webhook_endpoint_test"`), "one line per ping")

		forbidden := []string{
			"example.com", "example.net", "private-path-canary", "refused-host-canary",
			"10.0.0.9", "127.0.0.1", ":" + consumer.port,
			good.Secret, refused.Secret, "whsec_", "v1,", models.WebhookEventPing,
			goodResult.EventID, refusedResult.EventID, responseCanary,
		}
		forbidden = append(forbidden, sealed...)
		for _, request := range received {
			forbidden = append(forbidden, request.Header.Get("webhook-signature"), string(request.Body))
		}
		for _, value := range forbidden {
			if value == "" {
				continue
			}
			assert.NotContains(t, logs, value)
		}

		// The ping lines carry only the agreed fields.
		allowed := map[string]bool{"level": true, "msg": true, "time": true, "operation": true, "endpoint_id": true,
			"outcome": true, "http_status": true, "error_code": true}
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			var entry map[string]interface{}
			if json.Unmarshal([]byte(line), &entry) != nil || entry["operation"] != "webhook_endpoint_test" {
				continue
			}
			for key := range entry {
				assert.True(t, allowed[key], "ping log field %q in %s", key, line)
			}
		}
	})
}
