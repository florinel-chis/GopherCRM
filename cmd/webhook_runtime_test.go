package main

// Outbound webhooks (#345) through the real wiring: setupRouter over a SQLite
// file, exactly as main builds it, with the real public-only egress guard.

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestWebhookProductionWiring(t *testing.T) {
	t.Run("the source wires every create path, the runtime and the drain", func(t *testing.T) {
		source, err := os.ReadFile("main.go")
		require.NoError(t, err)
		text := string(source)
		for _, fragment := range []string{
			"webhookRepo := repository.NewWebhookRepository(models.DB)",
			"utils.NewSecretBox(cfg.API.APIKeySecret, service.WebhookSecretContext)",
			"service.WithLeadWebhooks(webhookRepo)",
			"service.WithCustomerWebhooks(webhookRepo, txManager)",
			"service.WithTicketWebhooks(webhookRepo, txManager)",
			"service.WithFormWebhooks(webhookRepo)",
			"handler.SetupWebhookRoutes(protected, webhookHandler)",
			"webhooks.start(backgroundCtx)",
			"combineStops(automation.Stop, formRetention.Stop, bulkRecovery.Stop, webhooks.Stop, aeoRuntime.Stop)",
		} {
			assert.Contains(t, text, fragment)
		}
		assert.Equal(t, 1, strings.Count(text, "repository.NewWebhookRepository("), "one WebhookRepository")
		assert.Equal(t, "webhook-endpoint-secret", service.WebhookSecretContext)
	})

	t.Run("a localhost endpoint gets destination_refused and Stop leaves no goroutine", func(t *testing.T) {
		boot := bootAutomation(t, false)
		box := utils.NewSecretBox(boot.cfg.API.APIKeySecret, service.WebhookSecretContext)
		sealed, err := box.Seal("whsec_c2VjcmV0LWtleS1vZi10aGUtd2lyaW5nLXRlc3Q=")
		require.NoError(t, err)
		endpoint := &models.WebhookEndpoint{URL: "https://localhost/hook", EventTypes: models.WebhookEventTypes{models.WebhookEventLeadCreated},
			SecretSealed: sealed, IsActive: true}
		require.NoError(t, models.DB.Create(endpoint).Error)
		due := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
		delivery := &models.WebhookDelivery{EndpointID: endpoint.ID, EventID: "evt_0123456789abcdef0123456789abcdef",
			EventType: models.WebhookEventLeadCreated, RecordID: 1, OccurredAt: due, Status: models.WebhookDeliveryPending, NextAttemptAt: &due}
		require.NoError(t, models.DB.Create(delivery).Error)

		var built *service.WebhookRuntime
		webhookRuntimeBuilt = func(rt *service.WebhookRuntime) { built = rt }
		t.Cleanup(func() { webhookRuntimeBuilt = nil })

		baseline := runtime.NumGoroutine()
		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
				defer cancel()
				_ = stop(ctx)
			}
		})
		require.NotNil(t, built, "setupDependencies builds the webhook runtime")
		assert.Equal(t, service.WebhookPollInterval, built.Interval())
		assert.Equal(t, service.WebhookRequestTimeout, built.RequestTimeout())

		// The first pass runs at start; the secret opens (the right context),
		// and the real guard refuses localhost before any connection.
		deadline := time.Now().Add(automationBootTimeout)
		var row models.WebhookDelivery
		for {
			require.NoError(t, models.DB.First(&row, delivery.ID).Error)
			if row.Attempts > 0 && row.Status != models.WebhookDeliverySending {
				break
			}
			require.True(t, time.Now().Before(deadline), "the boot pass attempts the delivery")
			time.Sleep(20 * time.Millisecond)
		}
		assert.Equal(t, models.WebhookErrorDestinationRefused, row.ErrorCode)
		assert.Equal(t, models.WebhookDeliveryPending, row.Status, "retried later")
		assert.Nil(t, row.LastHTTPStatus)

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))
		stopped = true
		awaitGoroutines(t, baseline)

		again, cancelAgain := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancelAgain()
		require.NoError(t, stop(again), "the handle main receives can be called twice")
	})
}
