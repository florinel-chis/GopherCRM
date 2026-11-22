package main

// The webhook sender's secret check (#559) through the real wiring: GET
// /health reads the status of the runtime setupDependencies builds and starts.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

func TestWebhookSecretHoldHealthWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	// Sealed with another master secret than the configured one, as after a
	// change of API_KEY_SECRET.
	otherBox := utils.NewSecretBox(boot.cfg.API.APIKeySecret+"-before-the-change", webhook.WebhookSecretContext)
	sealed, err := otherBox.Seal("whsec_c2VjcmV0LWtleS1vZi10aGUtaG9sZC10ZXN0")
	require.NoError(t, err)
	const endpointURL = "https://localhost/hold-wiring-private-path"
	endpoint := &models.WebhookEndpoint{URL: endpointURL, EventTypes: models.WebhookEventTypes{models.WebhookEventLeadCreated},
		SecretSealed: sealed, IsActive: true}
	require.NoError(t, models.DB.Create(endpoint).Error)
	due := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	delivery := &models.WebhookDelivery{EndpointID: endpoint.ID, EventID: "evt_59559559559559559559559559559559",
		EventType: models.WebhookEventLeadCreated, RecordID: 1, OccurredAt: due, Status: models.WebhookDeliveryPending, NextAttemptAt: &due}
	require.NoError(t, models.DB.Create(delivery).Error)

	admin := &models.User{Email: "admin@webhook-hold-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin",
		Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)

	var built *webhook.WebhookRuntime
	webhookRuntimeBuilt = func(rt *webhook.WebhookRuntime) { built = rt }
	t.Cleanup(func() { webhookRuntimeBuilt = nil })

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})
	require.NotNil(t, built, "setupDependencies builds the webhook runtime")

	health := func(t *testing.T) map[string]interface{} {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		data, ok := envelope.Data.(map[string]interface{})
		require.True(t, ok, recorder.Body.String())
		assert.Equal(t, "healthy", data["status"])
		assert.NotEmpty(t, data["revision"])
		return data
	}

	result, err := built.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.HeldEndpoints)
	assert.Equal(t, map[string]interface{}{"state": "paused", "reason": "secret_unavailable"}, health(t)["webhooks"])

	var row models.WebhookDelivery
	require.NoError(t, models.DB.First(&row, delivery.ID).Error)
	assert.Equal(t, models.WebhookDeliveryPending, row.Status, "held, not failed")
	assert.Zero(t, row.Attempts)

	token, err := service.NewAuthService(repository.NewUserRepository(models.DB),
		repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT).GenerateJWT(admin)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("%s/webhooks/endpoints/%d/rotate-secret", boot.cfg.API.Prefix, endpoint.ID), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	result, err = built.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, result.HeldEndpoints)
	assert.Equal(t, map[string]interface{}{"state": "running"}, health(t)["webhooks"])

	assert.Equal(t, 1, boot.logs.count("Webhook deliveries held: endpoint signing secret cannot be opened"))
	assert.Equal(t, 1, boot.logs.count("Webhook deliveries resumed"))
	assert.Zero(t, boot.logs.count(endpointURL), "the endpoint URL is never logged")
	assert.Zero(t, boot.logs.count("hold-wiring-private-path"))
}
