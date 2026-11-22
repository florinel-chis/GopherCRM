package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-9 (#345): the endpoint registry over the real repository on SQLite.

func TestWebhookEndpointService(t *testing.T) {
	env := webhookRuntimeEnv(t)
	ctx := context.Background()

	t.Run("the secret is returned once and stored sealed", func(t *testing.T) {
		created, err := env.endpoints.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: " https://example.com/hook ",
			EventTypes: []string{models.WebhookEventTicketCreated, models.WebhookEventLeadCreated, models.WebhookEventLeadCreated}})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(created.Secret, webhook.WebhookSecretPrefix))
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(created.Secret, webhook.WebhookSecretPrefix))
		require.NoError(t, err)
		assert.Len(t, key, 32)
		assert.Equal(t, "https://example.com/hook", created.URL)

		var stored models.WebhookEndpoint
		require.NoError(t, env.db.First(&stored, created.ID).Error)
		assert.True(t, utils.IsSealed(stored.SecretSealed))
		assert.NotContains(t, stored.SecretSealed, created.Secret)
		assert.NotContains(t, stored.SecretSealed, strings.TrimPrefix(created.Secret, webhook.WebhookSecretPrefix))
		opened, err := utils.NewSecretBox(webhookTestMasterSecret, webhook.WebhookSecretContext).Open(stored.SecretSealed)
		require.NoError(t, err)
		assert.Equal(t, created.Secret, opened)
		_, err = utils.NewSecretBox(webhookTestMasterSecret, "configuration-secret").Open(stored.SecretSealed)
		assert.Error(t, err, "another context cannot open it")

		var raw string
		require.NoError(t, env.db.Raw("SELECT event_types FROM webhook_endpoints WHERE id = ?", created.ID).Scan(&raw).Error)
		assert.Equal(t, "lead.created,ticket.created", raw, "stored canonical")
		assert.True(t, stored.IsActive, "an omitted is_active stores true")

		createdJSON, err := json.Marshal(created)
		require.NoError(t, err)
		assert.Contains(t, string(createdJSON), `"event_types":["lead.created","ticket.created"]`)

		got, err := env.endpoints.GetEndpoint(ctx, created.ID)
		require.NoError(t, err)
		list, _, err := env.endpoints.ListEndpoints(ctx, 0, 100, "", "")
		require.NoError(t, err)
		active := false
		updated, err := env.endpoints.UpdateEndpoint(ctx, created.ID, webhook.WebhookEndpointPatch{IsActive: &active})
		require.NoError(t, err)
		for name, value := range map[string]interface{}{"get": got, "list": list, "update": updated} {
			body, err := json.Marshal(value)
			require.NoError(t, err)
			assert.NotContains(t, string(body), created.Secret, name)
			assert.NotContains(t, string(body), "whsec_", name)
			// No secret key and no sealed column; previous_secret_expires_at
			// (#503) is the one field whose name says secret.
			assert.NotContains(t, string(body), `"secret"`, name)
			assert.NotContains(t, string(body), "secret_sealed", name)
			assert.NotContains(t, string(body), stored.SecretSealed, name)
		}
		var reread models.WebhookEndpoint
		require.NoError(t, env.db.First(&reread, created.ID).Error)
		assert.False(t, reread.IsActive, "a PUT with is_active false stores false")
		assert.Equal(t, stored.SecretSealed, reread.SecretSealed, "the secret never changes")
	})

	t.Run("is_active false on create is stored false and gets no row", func(t *testing.T) {
		inactive := false
		created, err := env.endpoints.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: "https://example.com/off",
			EventTypes: []string{models.WebhookEventLeadCreated}, IsActive: &inactive})
		require.NoError(t, err)
		assert.False(t, created.IsActive)
		var stored models.WebhookEndpoint
		require.NoError(t, env.db.First(&stored, created.ID).Error)
		assert.False(t, stored.IsActive, "read back from the database")

		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", created.ID))
	})

	t.Run("validation and unknown ids", func(t *testing.T) {
		for _, input := range []webhook.WebhookEndpointInput{
			{URL: "http://example.com/x", EventTypes: []string{models.WebhookEventLeadCreated}},
			{URL: "https://example.com/x?a=1", EventTypes: []string{models.WebhookEventLeadCreated}},
			{URL: "https://10.0.0.5/x", EventTypes: []string{models.WebhookEventLeadCreated}},
			{URL: "https://example.com/" + strings.Repeat("a", 2048), EventTypes: []string{models.WebhookEventLeadCreated}},
			{URL: "https://example.com/x", EventTypes: nil},
			{URL: "https://example.com/x", EventTypes: []string{"lead.archived"}},
		} {
			_, err := env.endpoints.CreateEndpoint(ctx, input)
			var validation *webhook.WebhookValidationError
			require.ErrorAs(t, err, &validation)
			assert.ErrorIs(t, err, apperrors.ErrValidation)
			assert.NotContains(t, validation.Message, "example.com")
		}
		_, err := env.endpoints.UpdateEndpoint(ctx, 1, webhook.WebhookEndpointPatch{})
		assert.ErrorAs(t, err, new(*webhook.WebhookValidationError))
		_, err = env.endpoints.GetEndpoint(ctx, 999999)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		yes := true
		_, err = env.endpoints.UpdateEndpoint(ctx, 999999, webhook.WebhookEndpointPatch{IsActive: &yes})
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.ErrorIs(t, env.endpoints.DeleteEndpoint(ctx, 999999), apperrors.ErrNotFound)

		unwired := webhook.NewWebhookService(nil, nil, nil)
		_, err = unwired.CreateEndpoint(ctx, webhook.WebhookEndpointInput{URL: "https://example.com/x", EventTypes: []string{models.WebhookEventLeadCreated}})
		assert.ErrorIs(t, err, webhook.ErrWebhooksUnavailable)
	})

	t.Run("the delivery log refuses an unknown event_type with the catalogue's message", func(t *testing.T) {
		_, _, err := env.endpoints.ListDeliveries(ctx, repository.WebhookDeliveryFilter{EventType: "lead.archived"}, 0, 20)
		var validation *webhook.WebhookValidationError
		require.ErrorAs(t, err, &validation)
		assert.ErrorIs(t, err, apperrors.ErrValidation)
		assert.Equal(t, "event_type", validation.Field)
		assert.Equal(t, webhook.FilterRule(), validation.Message)
		assert.Contains(t, validation.Message, "ticket.deleted")
	})

	t.Run("delete removes the endpoint and its deliveries in one transaction, endpoint locked first", func(t *testing.T) {
		target := env.endpoint("https://example.com/target", true, models.WebhookEventLeadCreated)
		other := env.endpoint("https://example.com/other", true, models.WebhookEventLeadCreated)
		require.NoError(t, env.leads.Create(env.newLead()))
		require.NoError(t, env.leads.Create(env.newLead()))
		require.EqualValues(t, 2, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", target.ID))

		// A failure deleting the endpoint row rolls the delivery delete back.
		sentinel := errors.New("injected endpoint delete failure")
		name := "webhook_endpoint_test:fail_endpoint_delete"
		require.NoError(t, env.db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "webhook_endpoints" {
				_ = tx.AddError(sentinel)
			}
		}))
		err := env.endpoints.DeleteEndpoint(ctx, target.ID)
		require.NoError(t, env.db.Callback().Delete().Remove(name))
		require.ErrorIs(t, err, sentinel)
		assert.EqualValues(t, 2, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", target.ID), "the delivery delete rolled back")
		assert.EqualValues(t, 1, env.count(&models.WebhookEndpoint{}, "id = ?", target.ID))

		var mu sync.Mutex
		var order []string
		record := func(kind string) func(*gorm.DB) {
			return func(tx *gorm.DB) {
				if strings.HasPrefix(tx.Statement.Table, "webhook_") {
					mu.Lock()
					order = append(order, kind+" "+tx.Statement.Table)
					mu.Unlock()
				}
			}
		}
		require.NoError(t, env.db.Callback().Query().After("gorm:query").Register(name+":q", record("SELECT")))
		require.NoError(t, env.db.Callback().Delete().After("gorm:delete").Register(name+":d", record("DELETE")))
		err = env.endpoints.DeleteEndpoint(ctx, target.ID)
		require.NoError(t, env.db.Callback().Query().Remove(name+":q"))
		require.NoError(t, env.db.Callback().Delete().Remove(name+":d"))
		require.NoError(t, err)
		assert.Equal(t, []string{"SELECT webhook_endpoints", "DELETE webhook_deliveries", "DELETE webhook_endpoints"}, order)
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", target.ID))
		assert.Zero(t, env.count(&models.WebhookEndpoint{}, "id = ?", target.ID))
		assert.EqualValues(t, 2, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", other.ID), "other endpoints keep theirs")
	})

	var _ repository.WebhookRepository = env.repo
}
