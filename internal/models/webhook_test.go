package models

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #345: webhook_endpoints and webhook_deliveries on SQLite, created by
// MigrateDatabase (also on a database that predates them), and their JSON.

func openWebhookSchemaDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "webhooks.db"),
	}))
	require.NoError(t, MigrateDatabase())
}

func webhookColumns(t *testing.T, table string) ([]string, map[string]sqliteColumn) {
	t.Helper()
	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table).Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	names := []string{}
	for _, column := range columns {
		byName[column.Name] = column
		names = append(names, column.Name)
	}
	sort.Strings(names)
	return names, byName
}

func webhookIndexes(t *testing.T, table string) map[string]sqliteIndex {
	t.Helper()
	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list(?)", table).Scan(&indexes).Error)
	out := map[string]sqliteIndex{}
	for _, index := range indexes {
		if index.Origin == "pk" {
			continue
		}
		out[index.Name] = index
	}
	return out
}

func assertWebhookSchemaSQLite(t *testing.T) {
	t.Helper()
	require.True(t, DB.Migrator().HasTable("webhook_endpoints"))
	require.True(t, DB.Migrator().HasTable("webhook_deliveries"))

	names, endpoint := webhookColumns(t, "webhook_endpoints")
	assert.Equal(t, []string{"created_at", "event_types", "id", "is_active", "secret_sealed", "updated_at", "url"}, names,
		"exactly these columns, no deleted_at")
	assert.Equal(t, 1, endpoint["id"].PK)
	for _, name := range []string{"url", "event_types", "secret_sealed", "is_active"} {
		assert.Equal(t, 1, endpoint[name].NotNull, "%s is NOT NULL", name)
	}
	assert.Equal(t, "varchar(2048)", strings.ToLower(endpoint["url"].Type))
	assert.Equal(t, "varchar(255)", strings.ToLower(endpoint["event_types"].Type))
	assert.Equal(t, "varchar(255)", strings.ToLower(endpoint["secret_sealed"].Type))
	assert.Nil(t, endpoint["is_active"].Dflt, "is_active has no column default (GORM would substitute it for an explicit false)")
	assert.Empty(t, webhookIndexes(t, "webhook_endpoints"), "the endpoint table has only its primary key")

	names, delivery := webhookColumns(t, "webhook_deliveries")
	assert.Equal(t, []string{"attempts", "created_at", "endpoint_id", "error_code", "event_id", "event_type", "id", "last_attempt_at",
		"last_http_status", "lease_until", "next_attempt_at", "occurred_at", "record_id", "status", "updated_at"}, names)
	for _, name := range []string{"endpoint_id", "event_id", "event_type", "record_id", "occurred_at", "status", "attempts", "error_code"} {
		assert.Equal(t, 1, delivery[name].NotNull, "%s is NOT NULL", name)
	}
	for _, name := range []string{"next_attempt_at", "last_attempt_at", "lease_until", "last_http_status"} {
		assert.Equal(t, 0, delivery[name].NotNull, "%s is nullable", name)
	}
	assert.Equal(t, "varchar(64)", strings.ToLower(delivery["event_id"].Type))
	assert.Equal(t, "varchar(32)", strings.ToLower(delivery["event_type"].Type))
	assert.Equal(t, "varchar(20)", strings.ToLower(delivery["status"].Type))
	assert.Equal(t, "varchar(50)", strings.ToLower(delivery["error_code"].Type))
	require.NotNil(t, delivery["status"].Dflt)
	assert.Equal(t, "pending", strings.Trim(*delivery["status"].Dflt, `'"`))
	require.NotNil(t, delivery["attempts"].Dflt)
	assert.Equal(t, "0", strings.Trim(*delivery["attempts"].Dflt, `'"`))
	require.NotNil(t, delivery["error_code"].Dflt)
	assert.Equal(t, "", strings.Trim(*delivery["error_code"].Dflt, `'"`))

	for _, table := range []string{"webhook_endpoints", "webhook_deliveries"} {
		var fkCount int64
		require.NoError(t, DB.Raw("SELECT COUNT(*) FROM pragma_foreign_key_list(?)", table).Scan(&fkCount).Error)
		assert.Zero(t, fkCount, "%s has no foreign key", table)
	}

	indexes := webhookIndexes(t, "webhook_deliveries")
	assert.Len(t, indexes, 3)
	for name, want := range map[string]struct {
		unique  int
		columns []string
	}{
		"idx_webhook_deliveries_endpoint_event": {1, []string{"endpoint_id", "event_id"}},
		"idx_webhook_deliveries_status_next":    {0, []string{"status", "next_attempt_at"}},
		"idx_webhook_deliveries_updated_at":     {0, []string{"updated_at"}},
	} {
		require.Contains(t, indexes, name)
		assert.Equal(t, want.unique, indexes[name].Unique, name)
		assert.Equal(t, want.columns, customerExternalIDIndexColumns(t, name), name)
	}
}

func TestWebhookSchemaSQLite(t *testing.T) {
	openWebhookSchemaDB(t)
	assertWebhookSchemaSQLite(t)

	t.Run("rows round-trip and the endpoint/event pair is unique", func(t *testing.T) {
		endpoint := &WebhookEndpoint{URL: "https://hooks.example.com/a", EventTypes: WebhookEventTypes{WebhookEventTicketCreated, WebhookEventLeadCreated},
			SecretSealed: "enc:v1:x", IsActive: false}
		require.NoError(t, DB.Create(endpoint).Error)
		var stored WebhookEndpoint
		require.NoError(t, DB.First(&stored, endpoint.ID).Error)
		assert.False(t, stored.IsActive, "an explicit false is stored false")
		assert.Equal(t, WebhookEventTypes{WebhookEventLeadCreated, WebhookEventTicketCreated}, stored.EventTypes, "stored canonical")
		var raw string
		require.NoError(t, DB.Raw("SELECT event_types FROM webhook_endpoints WHERE id = ?", endpoint.ID).Scan(&raw).Error)
		assert.Equal(t, "lead.created,ticket.created", raw)

		occurred := time.Now().UTC().Truncate(time.Millisecond)
		first := &WebhookDelivery{EndpointID: endpoint.ID, EventID: "evt_0123", EventType: WebhookEventLeadCreated, RecordID: 7,
			OccurredAt: occurred, Status: WebhookDeliveryPending, NextAttemptAt: &occurred}
		require.NoError(t, DB.Create(first).Error)
		err := DB.Create(&WebhookDelivery{EndpointID: endpoint.ID, EventID: "evt_0123", EventType: WebhookEventLeadCreated, RecordID: 7,
			OccurredAt: occurred, Status: WebhookDeliveryPending}).Error
		require.Error(t, err, "a second row of one event for one endpoint is refused")
		assert.Contains(t, err.Error(), "UNIQUE constraint failed")
		var reread WebhookDelivery
		require.NoError(t, DB.First(&reread, first.ID).Error)
		assert.Equal(t, 0, reread.Attempts)
		assert.Equal(t, "", reread.ErrorCode)
		require.NotNil(t, reread.NextAttemptAt)
		assert.True(t, reread.NextAttemptAt.Equal(occurred))
	})

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase changes nothing")

	// An existing database that predates the two tables (the previous head)
	// gets them, with the same shape, on the next start.
	t.Run("existing database without the tables", func(t *testing.T) {
		require.NoError(t, DB.Exec("DROP TABLE webhook_deliveries").Error)
		require.NoError(t, DB.Exec("DROP TABLE webhook_endpoints").Error)
		require.False(t, DB.Migrator().HasTable("webhook_endpoints"))
		require.False(t, DB.Migrator().HasTable("webhook_deliveries"))
		require.NoError(t, MigrateDatabase())
		assertWebhookSchemaSQLite(t)
		assert.Equal(t, schema, schemaSnapshot(t), "the upgraded schema equals the fresh one")
	})
}

func TestWebhookJSON(t *testing.T) {
	created := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	endpoint := WebhookEndpoint{ID: 4, CreatedAt: created, UpdatedAt: created, URL: "https://hooks.example.com/x",
		EventTypes: WebhookEventTypes{WebhookEventLeadCreated}, SecretSealed: "enc:v1:sealed-canary", IsActive: true}
	raw, err := json.Marshal(endpoint)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sealed-canary")
	assert.NotContains(t, string(raw), "secret")
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"created_at", "event_types", "id", "is_active", "updated_at", "url"}, keys)
	assert.Equal(t, []interface{}{"lead.created"}, decoded["event_types"], "event_types is an array")

	raw, err = json.Marshal(WebhookEndpoint{})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"event_types":[]`, "an empty subset is an empty array, never null")

	createdRaw, err := json.Marshal(WebhookEndpointCreated{WebhookEndpoint: endpoint, Secret: "whsec_c2VjcmV0"})
	require.NoError(t, err)
	assert.Contains(t, string(createdRaw), `"secret":"whsec_c2VjcmV0"`)
	assert.Contains(t, string(createdRaw), `"url":"https://hooks.example.com/x"`)
	assert.NotContains(t, string(createdRaw), "sealed-canary")

	lease := created.Add(time.Minute)
	status := 503
	delivery := WebhookDelivery{ID: 9, EndpointID: 4, EventID: "evt_x", EventType: WebhookEventLeadCreated, RecordID: 42,
		OccurredAt: created, Status: WebhookDeliveryPending, Attempts: 1, NextAttemptAt: &lease, LastAttemptAt: &created,
		LeaseUntil: &lease, LastHTTPStatus: &status, ErrorCode: WebhookErrorHTTPStatus}
	raw, err = json.Marshal(delivery)
	require.NoError(t, err)
	decoded = map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	keys = keys[:0]
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"attempts", "created_at", "endpoint_id", "error_code", "event_id", "event_type", "id", "last_attempt_at",
		"last_http_status", "next_attempt_at", "occurred_at", "record_id", "status", "updated_at"}, keys, "lease_until is never serialised")
	assert.EqualValues(t, 503, decoded["last_http_status"])

	raw, err = json.Marshal(WebhookDelivery{Status: WebhookDeliverySucceeded})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"next_attempt_at":null`)
	assert.Contains(t, string(raw), `"last_http_status":null`)

	t.Run("event types scan and canonicalise", func(t *testing.T) {
		var types WebhookEventTypes
		require.NoError(t, types.Scan("ticket.created,lead.created"))
		assert.Equal(t, WebhookEventTypes{"ticket.created", "lead.created"}, types)
		require.NoError(t, types.Scan([]byte("")))
		assert.Equal(t, WebhookEventTypes{}, types)
		value, err := WebhookEventTypes{"ticket.created", "lead.created", "ticket.created"}.Value()
		require.NoError(t, err)
		assert.Equal(t, "lead.created,ticket.created", value)
		assert.True(t, IsWebhookEventType("customer.created"))
		assert.False(t, IsWebhookEventType("Customer.Created"))
		assert.False(t, IsWebhookEventType("lead.archived"))
		// The deleted types (#437): nine in all, and every one subscribed fits
		// varchar(255) without a migration.
		assert.Equal(t, []string{"customer.created", "customer.deleted", "customer.updated", "lead.created", "lead.deleted",
			"lead.updated", "ticket.created", "ticket.deleted", "ticket.updated"}, WebhookEventTypeValues)
		for _, deleted := range []string{"lead.deleted", "customer.deleted", "ticket.deleted"} {
			assert.True(t, IsWebhookEventType(deleted), deleted)
		}
		all, err := WebhookEventTypes(WebhookEventTypeValues).Value()
		require.NoError(t, err)
		assert.Len(t, all, 134)
	})
}
