package main

// Issue #501 through the router production uses: setupRouter (and with it
// setupDependencies) mounts POST /webhooks/endpoints/{id}/test and hands the
// webhook service the egress guard through WithWebhookPingClients. Without the
// option the ping answers 500 (ErrWebhooksUnavailable).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

func TestWebhookPingProductionWiring(t *testing.T) {
	// The source half: the registry main.go builds carries the ping option.
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	call := string(source)
	start := strings.Index(call, "webhook.NewWebhookService(")
	require.GreaterOrEqual(t, start, 0, "main.go builds the webhook service")
	call = call[start:]
	end := strings.Index(call, "\n\n")
	require.Greater(t, end, 0)
	assert.Contains(t, call[:end], "webhook.WithWebhookPingClients(", "main.go wires the test ping to the egress guard")
	assert.Contains(t, call[:end], "WithWebhookPingClients(egress.New(", "the test ping uses the public-only egress guard")

	boot := bootAutomation(t, false)
	admin := &models.User{Email: "admin@webhook-ping-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)

	box := utils.NewSecretBox(boot.cfg.API.APIKeySecret, webhook.WebhookSecretContext)
	sealed, err := box.Seal("whsec_" + "c2lnbmluZy1rZXktb2YtdGhlLXdpcmluZy10ZXN0")
	require.NoError(t, err)
	endpoint := &models.WebhookEndpoint{URL: "https://localhost/hook", EventTypes: models.WebhookEventTypes{models.WebhookEventLeadCreated},
		SecretSealed: sealed, IsActive: false}
	require.NoError(t, models.DB.Create(endpoint).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	auth := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT)
	token, err := auth.GenerateJWT(admin)
	require.NoError(t, err)

	countDeliveries := func() int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.WebhookDelivery{}).Count(&n).Error)
		return n
	}
	before := countDeliveries()

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("%s/webhooks/endpoints/%d/test", boot.cfg.API.Prefix, endpoint.ID), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var envelope struct {
		Success bool                       `json:"success"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
	require.True(t, envelope.Success)
	assert.JSONEq(t, `"failed"`, string(envelope.Data["outcome"]))
	assert.JSONEq(t, `"destination_refused"`, string(envelope.Data["error_code"]), "localhost is refused by the production guard")
	require.Contains(t, envelope.Data, "http_status")
	assert.Equal(t, "null", string(envelope.Data["http_status"]))
	assert.NotContains(t, recorder.Body.String(), "whsec_")
	assert.NotContains(t, recorder.Body.String(), sealed)
	assert.Equal(t, before, countDeliveries(), "the ping writes no delivery row")

	var stored models.WebhookEndpoint
	require.NoError(t, models.DB.Where("id = ?", endpoint.ID).Take(&stored).Error)
	assert.False(t, stored.IsActive, "the endpoint is left untouched")
	assert.True(t, stored.UpdatedAt.Equal(endpoint.UpdatedAt))
}
