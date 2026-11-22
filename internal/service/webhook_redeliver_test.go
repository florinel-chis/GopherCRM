package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// Manual redelivery of a logged delivery (#502): RedeliverDelivery puts a
// succeeded or failed row back into the queue with one conditional UPDATE by
// primary key, and the runtime sends it again under the same event id, on
// SQLite and on a disposable TEST_MYSQL_DSN schema (the MySQL root skips
// without the variable and fails when it is unreachable).

func TestWebhookRedeliverSQLite(t *testing.T) {
	runWebhookRedeliver(t, openTicketIntakeDB(t, false))
}

func TestWebhookRedeliverMySQL(t *testing.T) {
	runWebhookRedeliver(t, openTicketIntakeDB(t, true))
}

// redeliverRow is one seeded delivery row.
type redeliverRow struct {
	status     string
	attempts   int
	recordID   uint
	next       *time.Time
	lease      *time.Time
	httpStatus *int
	errorCode  string
}

func runWebhookRedeliver(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()

	seed := func(t *testing.T, endpointID uint, spec redeliverRow) models.WebhookDelivery {
		t.Helper()
		eventID, err := webhook.NewEventID()
		require.NoError(t, err)
		occurred := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		var lastAttempt *time.Time
		if spec.attempts > 0 {
			at := occurred.Add(time.Minute)
			lastAttempt = &at
		}
		recordID := spec.recordID
		if recordID == 0 {
			recordID = env.seedLead().ID
		}
		row := &models.WebhookDelivery{EndpointID: endpointID, EventID: eventID, EventType: models.WebhookEventLeadCreated,
			RecordID: recordID, OccurredAt: occurred, Status: spec.status, Attempts: spec.attempts, NextAttemptAt: spec.next,
			LeaseUntil: spec.lease, LastAttemptAt: lastAttempt, LastHTTPStatus: spec.httpStatus, ErrorCode: spec.errorCode}
		require.NoError(t, env.db.Create(row).Error)
		return env.delivery(row.ID)
	}
	failedRow := func(t *testing.T, endpointID uint, recordID uint) models.WebhookDelivery {
		status := http.StatusServiceUnavailable
		return seed(t, endpointID, redeliverRow{status: models.WebhookDeliveryFailed, attempts: models.WebhookMaxAttempts,
			recordID: recordID, httpStatus: &status, errorCode: models.WebhookErrorHTTPStatus})
	}
	// requeued asserts the row the call returned and the stored row are the
	// same delivery, back in the queue.
	requeued := func(t *testing.T, before models.WebhookDelivery, returned *models.WebhookDelivery, after time.Time) {
		t.Helper()
		require.NotNil(t, returned)
		for _, row := range []models.WebhookDelivery{*returned, env.delivery(before.ID)} {
			assert.Equal(t, before.ID, row.ID)
			assert.Equal(t, before.EventID, row.EventID, "the same event id")
			assert.Equal(t, before.RecordID, row.RecordID)
			assert.Equal(t, before.EndpointID, row.EndpointID)
			assert.Equal(t, models.WebhookDeliveryPending, row.Status)
			assert.Equal(t, 0, row.Attempts, "the full retry budget")
			require.NotNil(t, row.NextAttemptAt)
			assert.False(t, row.NextAttemptAt.After(after), "due at once: %s after %s", row.NextAttemptAt, after)
			assert.Nil(t, row.LeaseUntil)
			assert.Nil(t, row.LastHTTPStatus)
			assert.Equal(t, "", row.ErrorCode)
		}
	}
	// endpoint registers an endpoint on a fresh consumer, with no other row
	// due, and returns it with a runtime on the real clock.
	endpoint := func(t *testing.T, active bool) (*models.WebhookEndpointCreated, *webhookConsumer, *webhook.WebhookRuntime) {
		t.Helper()
		env.clearDeliveries()
		consumer := newWebhookConsumer(t)
		created := env.endpoint(consumer.url("/hook"), active, models.WebhookEventLeadCreated)
		return created, consumer, env.runtime(consumer.guard(nil))
	}

	t.Run("full cycle: a failed row is re-sent under the same event id", func(t *testing.T) {
		hook, consumer, rt := endpoint(t, true)
		before := failedRow(t, hook.ID, 0)

		returned, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		after := time.Now().UTC()
		require.NoError(t, err)
		requeued(t, before, returned, after)

		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		assert.Equal(t, 1, result.Succeeded)
		received := consumer.received()
		require.Len(t, received, 1, "exactly one request")
		assert.Equal(t, before.EventID, received[0].Header.Get("webhook-id"))
		require.NoError(t, verifyStandardWebhook(hook.Secret, received[0].Header, received[0].Body, time.Now(), 5*time.Minute))
		stored := env.delivery(before.ID)
		assert.Equal(t, models.WebhookDeliverySucceeded, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
	})

	t.Run("a succeeded row is redelivered without a new row", func(t *testing.T) {
		hook, consumer, rt := endpoint(t, true)
		status := http.StatusOK
		before := seed(t, hook.ID, redeliverRow{status: models.WebhookDeliverySucceeded, attempts: 1, httpStatus: &status})
		rows := env.allDeliveries()

		returned, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		after := time.Now().UTC()
		require.NoError(t, err)
		requeued(t, before, returned, after)
		assert.Equal(t, rows, env.allDeliveries(), "SELECT COUNT(*) FROM webhook_deliveries is unchanged")

		_, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		require.Len(t, consumer.received(), 1)
		assert.Equal(t, rows, env.allDeliveries())
	})

	t.Run("a delivery whose record is gone is re-sent with its record_id", func(t *testing.T) {
		hook, consumer, rt := endpoint(t, true)
		const missingRecord = 987654321
		var n int64
		require.NoError(t, env.db.Unscoped().Model(&models.Lead{}).Where("id = ?", missingRecord).Count(&n).Error)
		require.Zero(t, n, "no lead row with that id")
		before := failedRow(t, hook.ID, missingRecord)

		returned, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		after := time.Now().UTC()
		require.NoError(t, err)
		requeued(t, before, returned, after)

		_, err = rt.RunOnce(ctx)
		require.NoError(t, err)
		received := consumer.received()
		require.Len(t, received, 1)
		assert.Equal(t, before.EventID, received[0].Header.Get("webhook-id"))
		var body map[string]any
		require.NoError(t, json.Unmarshal(received[0].Body, &body))
		assert.Equal(t, strconv.Itoa(missingRecord), strconv.FormatFloat(body["record_id"].(float64), 'f', -1, 64))
		stored := env.delivery(before.ID)
		assert.Equal(t, models.WebhookDeliverySucceeded, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
	})

	t.Run("a pending row is refused and unchanged", func(t *testing.T) {
		hook, consumer, _ := endpoint(t, true)
		before := seed(t, hook.ID, redeliverRow{status: models.WebhookDeliveryPending, attempts: 2,
			next: timePointer(time.Now().UTC().Add(time.Minute).Truncate(time.Second))})
		_, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
		assert.Equal(t, before, env.delivery(before.ID))
		assert.Empty(t, consumer.received())
	})

	t.Run("a sending row is refused and unchanged", func(t *testing.T) {
		hook, consumer, _ := endpoint(t, true)
		before := seed(t, hook.ID, redeliverRow{status: models.WebhookDeliverySending, attempts: 3,
			lease: timePointer(time.Now().UTC().Add(time.Minute).Truncate(time.Second))})
		_, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
		assert.Equal(t, before, env.delivery(before.ID))
		assert.Empty(t, consumer.received())
	})

	t.Run("worker claims first", func(t *testing.T) {
		hook, _, _ := endpoint(t, true)
		now := time.Now().UTC()
		due := seed(t, hook.ID, redeliverRow{status: models.WebhookDeliveryPending, attempts: 4, next: timePointer(now.Add(-time.Second))})
		claimed, err := env.repo.ClaimDelivery(ctx, due.ID, now, now.Add(2*time.Minute))
		require.NoError(t, err)
		require.Equal(t, 5, claimed.Attempts)

		_, err = env.endpoints.RedeliverDelivery(ctx, due.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
		stored := env.delivery(due.ID)
		assert.Equal(t, models.WebhookDeliverySending, stored.Status)
		assert.Equal(t, claimed.Attempts, stored.Attempts, "the claimed attempts are kept")
		assert.Equal(t, *claimed, stored, "the claimed row is unchanged")
	})

	t.Run("redeliver commits first", func(t *testing.T) {
		hook, consumer, rt := endpoint(t, true)
		before := failedRow(t, hook.ID, 0)
		returned, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		require.NoError(t, err)
		require.Equal(t, 0, returned.Attempts, "the requeue resets attempts")

		now := time.Now().UTC()
		var wins, misses int
		var won *models.WebhookDelivery
		for i := 0; i < 2; i++ {
			claimed, err := env.repo.ClaimDelivery(ctx, before.ID, now, now.Add(2*time.Minute))
			switch {
			case err == nil:
				wins++
				won = claimed
			case assert.ErrorIs(t, err, repository.ErrWebhookDeliveryNotClaimed):
				misses++
			}
		}
		assert.Equal(t, 1, wins, "exactly one claim succeeds")
		assert.Equal(t, 1, misses, "the other is not claimed")
		require.NotNil(t, won)
		assert.Equal(t, 1, won.Attempts, "the first attempt of a fresh budget")
		assert.Equal(t, models.WebhookDeliverySending, won.Status)

		// Through the runtime: two passes after a redelivery send one request.
		second := failedRow(t, hook.ID, 0)
		_, err = env.endpoints.RedeliverDelivery(ctx, second.ID)
		require.NoError(t, err)
		for pass := 0; pass < 2; pass++ {
			_, err := rt.RunOnce(ctx)
			require.NoError(t, err)
		}
		received := consumer.received()
		require.Len(t, received, 1, "the receiver gets exactly one request")
		assert.Equal(t, second.EventID, received[0].Header.Get("webhook-id"))
		stored := env.delivery(second.ID)
		assert.Equal(t, models.WebhookDeliverySucceeded, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
	})

	t.Run("a second redeliver of the pending row is refused", func(t *testing.T) {
		hook, _, _ := endpoint(t, true)
		before := failedRow(t, hook.ID, 0)
		_, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		require.NoError(t, err)
		pending := env.delivery(before.ID)
		_, err = env.endpoints.RedeliverDelivery(ctx, before.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
		assert.Equal(t, pending, env.delivery(before.ID), "the pending row is unchanged")
	})

	t.Run("an inactive endpoint is refused and the row unchanged", func(t *testing.T) {
		hook, consumer, _ := endpoint(t, false)
		before := failedRow(t, hook.ID, 0)
		_, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookEndpointUnavailable)
		assert.Equal(t, before, env.delivery(before.ID))
		assert.Empty(t, consumer.received())
	})

	t.Run("a deleted endpoint is refused and the row unchanged", func(t *testing.T) {
		hook, _, _ := endpoint(t, true)
		before := failedRow(t, hook.ID, 0)
		require.NoError(t, env.db.Where("id = ?", hook.ID).Delete(&models.WebhookEndpoint{}).Error)
		_, err := env.endpoints.RedeliverDelivery(ctx, before.ID)
		assert.ErrorIs(t, err, webhook.ErrWebhookEndpointUnavailable)
		assert.Equal(t, before, env.delivery(before.ID))
	})

	t.Run("an unknown id is not found", func(t *testing.T) {
		var maxID uint
		require.NoError(t, env.db.Model(&models.WebhookDelivery{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error)
		_, err := env.endpoints.RedeliverDelivery(ctx, maxID+1000)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.NotErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
	})

	t.Run("log privacy", func(t *testing.T) {
		var buffer lockedBuffer
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		utils.Logger.SetOutput(&buffer)
		t.Cleanup(func() { utils.Logger = previous })

		hook, consumer, _ := endpoint(t, true)
		inactive := env.endpoint(consumer.url("/inactive-path-canary"), false, models.WebhookEventLeadCreated)
		ok := failedRow(t, hook.ID, 0)
		_, err := env.endpoints.RedeliverDelivery(ctx, ok.ID)
		require.NoError(t, err)
		_, err = env.endpoints.RedeliverDelivery(ctx, ok.ID)
		require.ErrorIs(t, err, webhook.ErrWebhookDeliveryNotRedeliverable)
		refused := failedRow(t, inactive.ID, 0)
		_, err = env.endpoints.RedeliverDelivery(ctx, refused.ID)
		require.ErrorIs(t, err, webhook.ErrWebhookEndpointUnavailable)

		logs := buffer.String()
		require.Equal(t, 3, strings.Count(logs, `"operation":"webhook_delivery_redeliver"`), "one line per call: %s", logs)
		var sealed []string
		require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Pluck("secret_sealed", &sealed).Error)
		forbidden := []string{hook.URL, inactive.URL, "example.com", "inactive-path-canary", ":" + consumer.port,
			hook.Secret, inactive.Secret, "whsec_", "enc:v1:", ok.EventID, refused.EventID}
		forbidden = append(forbidden, sealed...)
		for _, value := range forbidden {
			assert.NotContains(t, logs, value)
		}
		allowed := map[string]bool{"level": true, "msg": true, "time": true, "operation": true, "delivery_id": true,
			"endpoint_id": true, "error_code": true}
		levels := map[string]int{}
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			var entry map[string]interface{}
			if json.Unmarshal([]byte(line), &entry) != nil || entry["operation"] != "webhook_delivery_redeliver" {
				continue
			}
			levels[entry["level"].(string)]++
			for key := range entry {
				assert.True(t, allowed[key], "redeliver log field %q in %s", key, line)
			}
		}
		assert.Equal(t, map[string]int{"info": 1, "warning": 2}, levels, "one Info on success, one Warn per 409")
	})
}

func timePointer(t time.Time) *time.Time { return &t }
