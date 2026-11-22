package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// Signing-secret rotation with an overlap window (#503): RotateEndpointSecret
// keeps the replaced secret as the previous one for webhook.WebhookSecretOverlap, and
// every delivery and test ping inside that window carries a signature under
// each secret, new first. On SQLite and on a disposable TEST_MYSQL_DSN schema
// (the MySQL root skips without the variable and fails when it is
// unreachable). No sleeps: the runtime runs on an injected clock.

func TestWebhookSecretRotationSQLite(t *testing.T) {
	runWebhookSecretRotation(t, openTicketIntakeDB(t, false))
}

func TestWebhookSecretRotationMySQL(t *testing.T) {
	runWebhookSecretRotation(t, openTicketIntakeDB(t, true))
}

// signatureParts splits a webhook-signature header into its v1 values.
func signatureParts(t *testing.T, header string) []string {
	t.Helper()
	parts := strings.Split(header, " ")
	for _, part := range parts {
		require.True(t, strings.HasPrefix(part, "v1,"), "every value is a v1 signature: %q", part)
	}
	return parts
}

// verifiesPart reports whether secret verifies the one signature part of
// request at now.
func verifiesPart(request consumerRequest, part, secret string, now time.Time) bool {
	header := request.Header.Clone()
	header.Set(webhook.WebhookHeaderSignature, part)
	return verifyStandardWebhook(secret, header, request.Body, now, 5*time.Minute) == nil
}

func runWebhookSecretRotation(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()

	// endpoint starts every subtest from an empty registry and delivery log:
	// one endpoint on a fresh consumer.
	endpoint := func(t *testing.T, active bool) (*models.WebhookEndpointCreated, *webhookConsumer) {
		t.Helper()
		env.clearDeliveries()
		require.NoError(t, env.db.Where("1 = 1").Delete(&models.WebhookEndpoint{}).Error)
		consumer := newWebhookConsumer(t)
		return env.endpoint(consumer.url("/hook"), active, models.WebhookEventLeadCreated), consumer
	}
	// stored reads the endpoint row.
	stored := func(t *testing.T, id uint) models.WebhookEndpoint {
		t.Helper()
		var row models.WebhookEndpoint
		require.NoError(t, env.db.Where("id = ?", id).Take(&row).Error)
		return row
	}
	// deliverAt queues one due delivery for the endpoint and runs one pass of
	// a runtime whose clock reads at; it returns the one request received.
	deliverAt := func(t *testing.T, endpointID uint, consumer *webhookConsumer, at time.Time, opts ...webhook.WebhookRuntimeOption) consumerRequest {
		t.Helper()
		eventID, err := webhook.NewEventID()
		require.NoError(t, err)
		due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		row := &models.WebhookDelivery{EndpointID: endpointID, EventID: eventID, EventType: models.WebhookEventLeadCreated,
			RecordID: 42, OccurredAt: due, Status: models.WebhookDeliveryPending, NextAttemptAt: &due}
		require.NoError(t, env.db.Create(row).Error)
		before := len(consumer.received())
		clock := newWebhookClock(at)
		rt := env.runtime(consumer.guard(nil), append([]webhook.WebhookRuntimeOption{webhook.WithWebhookClock(clock.Now)}, opts...)...)
		result, err := rt.RunOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, result.Attempted)
		require.Equal(t, 1, result.Succeeded, "the delivery is sent and succeeds")
		received := consumer.received()
		require.Len(t, received, before+1)
		return received[len(received)-1]
	}
	rotate := func(t *testing.T, id uint) *models.WebhookEndpointSecretRotated {
		t.Helper()
		rotated, err := env.endpoints.RotateEndpointSecret(ctx, id)
		require.NoError(t, err)
		return rotated
	}
	open := func(t *testing.T, sealed string) string {
		t.Helper()
		secret, err := env.box.Open(sealed)
		require.NoError(t, err)
		return secret
	}

	t.Run("a: two signatures inside the window, one after it, then cleared", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		rotated := rotate(t, created.ID)
		row := stored(t, created.ID)
		require.NotNil(t, row.PreviousSecretExpiresAt)
		// The stored expiry, not the computed one: MySQL rounds datetime(3).
		expires := *row.PreviousSecretExpiresAt

		inside := expires.Add(-webhook.WebhookSecretOverlap + time.Hour)
		request := deliverAt(t, created.ID, consumer, inside)
		parts := signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 2, "two signatures inside the window")
		assert.True(t, verifiesPart(request, parts[0], rotated.Secret, inside), "the first verifies with the new secret")
		assert.False(t, verifiesPart(request, parts[0], created.Secret, inside), "the first does not verify with the old secret")
		assert.True(t, verifiesPart(request, parts[1], created.Secret, inside), "the second verifies with the old secret")
		assert.False(t, verifiesPart(request, parts[1], rotated.Secret, inside), "the second does not verify with the new secret")
		inWindow := stored(t, created.ID)
		assert.NotNil(t, inWindow.PreviousSecretSealed, "nothing is cleared inside the window")
		assert.NotNil(t, inWindow.PreviousSecretExpiresAt)

		for name, offset := range map[string]time.Duration{"at expires_at": 0, "one second after": time.Second} {
			t.Run(name, func(t *testing.T) {
				created, consumer := endpoint(t, true)
				rotated := rotate(t, created.ID)
				before := stored(t, created.ID)
				require.NotNil(t, before.PreviousSecretExpiresAt)
				at := before.PreviousSecretExpiresAt.Add(offset)

				request := deliverAt(t, created.ID, consumer, at)
				parts := signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature))
				require.Len(t, parts, 1, "one signature once the window is over")
				assert.True(t, verifiesPart(request, parts[0], rotated.Secret, at), "it verifies with the new secret")
				assert.False(t, verifiesPart(request, parts[0], created.Secret, at), "and not with the old one")

				after := stored(t, created.ID)
				assert.Nil(t, after.PreviousSecretSealed, "the expired previous secret is cleared")
				assert.Nil(t, after.PreviousSecretExpiresAt)
				assert.Equal(t, before.SecretSealed, after.SecretSealed, "the current secret is untouched")
				assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "clearing does not bump updated_at")
			})
		}
	})

	t.Run("b: the rotate result and the stored pair", func(t *testing.T) {
		created, _ := endpoint(t, true)
		before := time.Now().UTC()
		rotated := rotate(t, created.ID)
		after := time.Now().UTC()

		assert.Regexp(t, `^whsec_[A-Za-z0-9+/]{43}=$`, rotated.Secret)
		assert.NotEqual(t, created.Secret, rotated.Secret)
		assert.Equal(t, created.ID, rotated.ID)
		require.NotNil(t, rotated.PreviousSecretExpiresAt)

		row := stored(t, created.ID)
		assert.Equal(t, rotated.Secret, open(t, row.SecretSealed), "secret_sealed opens to the new secret")
		require.NotNil(t, row.PreviousSecretSealed)
		assert.Equal(t, created.Secret, open(t, *row.PreviousSecretSealed), "previous_secret_sealed opens to the old secret")
		require.NotNil(t, row.PreviousSecretExpiresAt)
		lower := before.Add(webhook.WebhookSecretOverlap).Truncate(time.Millisecond).Add(-time.Millisecond)
		upper := after.Add(webhook.WebhookSecretOverlap).Truncate(time.Millisecond).Add(2 * time.Millisecond)
		for _, expires := range []time.Time{*row.PreviousSecretExpiresAt, *rotated.PreviousSecretExpiresAt} {
			assert.False(t, expires.Before(lower), "%s is not before %s", expires, lower)
			assert.False(t, expires.After(upper), "%s is not after %s", expires, upper)
		}
		raw, err := json.Marshal(rotated)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), row.SecretSealed)
		assert.NotContains(t, string(raw), *row.PreviousSecretSealed)
		assert.NotContains(t, string(raw), created.Secret)
	})

	t.Run("c: a second rotation inside the window keeps two secrets", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		second := rotate(t, created.ID)
		third := rotate(t, created.ID)
		row := stored(t, created.ID)
		require.NotNil(t, row.PreviousSecretSealed)
		assert.Equal(t, second.Secret, open(t, *row.PreviousSecretSealed))
		assert.Equal(t, third.Secret, open(t, row.SecretSealed))

		at := row.PreviousSecretExpiresAt.Add(-time.Hour)
		request := deliverAt(t, created.ID, consumer, at)
		parts := signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 2)
		assert.True(t, verifiesPart(request, parts[0], third.Secret, at), "the first is the third secret's")
		assert.True(t, verifiesPart(request, parts[1], second.Secret, at), "the second is the second secret's")
		assert.Error(t, verifyStandardWebhook(created.Secret, request.Header, request.Body, at, 5*time.Minute),
			"the first secret verifies neither")
	})

	t.Run("d: concurrent rotations keep exactly the two returned secrets", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		start := make(chan struct{})
		results := make([]*models.WebhookEndpointSecretRotated, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], errs[i] = env.endpoints.RotateEndpointSecret(ctx, created.ID)
			}(i)
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the concurrent rotations did not finish within 30 s")
		}
		for i := range results {
			require.NoError(t, errs[i])
			require.NotNil(t, results[i])
		}
		returned := []string{results[0].Secret, results[1].Secret}
		require.NotEqual(t, returned[0], returned[1])

		row := stored(t, created.ID)
		require.NotNil(t, row.PreviousSecretSealed)
		storedPair := []string{open(t, row.SecretSealed), open(t, *row.PreviousSecretSealed)}
		assert.ElementsMatch(t, returned, storedPair, "the stored pair is exactly the two returned secrets, in either commit order")

		at := row.PreviousSecretExpiresAt.Add(-time.Hour)
		request := deliverAt(t, created.ID, consumer, at)
		require.Len(t, signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature)), 2)
		for _, secret := range returned {
			assert.NoError(t, verifyStandardWebhook(secret, request.Header, request.Body, at, 5*time.Minute))
		}
		assert.Error(t, verifyStandardWebhook(created.Secret, request.Header, request.Body, at, 5*time.Minute),
			"the create secret is gone")
	})

	t.Run("e: an unopenable previous secret is dropped with one Warn", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		rotated := rotate(t, created.ID)
		row := stored(t, created.ID)
		require.NotNil(t, row.PreviousSecretSealed)
		originalPrevious := *row.PreviousSecretSealed
		const unopenable = "enc:v1:not-openable"
		require.NoError(t, env.db.Model(&models.WebhookEndpoint{}).Where("id = ?", created.ID).
			Update("previous_secret_sealed", unopenable).Error)

		var buffer lockedBuffer
		logger := logrus.New()
		logger.SetFormatter(&logrus.JSONFormatter{})
		logger.SetLevel(logrus.DebugLevel)
		logger.SetOutput(&buffer)

		at := row.PreviousSecretExpiresAt.Add(-time.Hour)
		request := deliverAt(t, created.ID, consumer, at, webhook.WithWebhookLogger(logger))
		parts := signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 1, "the current signature only")
		assert.True(t, verifiesPart(request, parts[0], rotated.Secret, at))

		logs := buffer.String()
		warnings := 0
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			var entry map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
			if entry["error_code"] == "previous_secret_unavailable" {
				warnings++
				assert.Equal(t, "warning", entry["level"])
				assert.EqualValues(t, created.ID, entry["endpoint_id"])
				for key := range entry {
					assert.Contains(t, []string{"level", "msg", "time", "operation", "endpoint_id", "error_code"}, key)
				}
			}
		}
		assert.Equal(t, 1, warnings, "one Warn with error_code=previous_secret_unavailable")
		for _, forbidden := range []string{
			rotated.Secret, created.Secret,
			strings.TrimPrefix(rotated.Secret, webhook.WebhookSecretPrefix), strings.TrimPrefix(created.Secret, webhook.WebhookSecretPrefix),
			row.SecretSealed, originalPrevious, unopenable,
		} {
			assert.NotContains(t, logs, forbidden)
		}
	})

	t.Run("f: an endpoint without a previous secret sends one signature", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		row := stored(t, created.ID)
		require.Nil(t, row.PreviousSecretSealed, "the upgraded shape: both previous columns NULL")
		require.Nil(t, row.PreviousSecretExpiresAt)
		at := time.Now().UTC()
		request := deliverAt(t, created.ID, consumer, at)
		parts := signatureParts(t, request.Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 1)
		assert.True(t, verifiesPart(request, parts[0], created.Secret, at))
	})

	t.Run("g: the test ping signs twice inside the window and writes nothing", func(t *testing.T) {
		created, consumer := endpoint(t, true)
		pinger := webhook.NewWebhookService(env.repo, env.txManager, env.box, webhook.WithWebhookPingClients(consumer.guard(nil)))
		rotated := rotate(t, created.ID)
		before := stored(t, created.ID)
		deliveries := env.allDeliveries()

		result, err := pinger.TestEndpoint(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingSucceeded, result.Outcome)
		received := consumer.received()
		require.Len(t, received, 1)
		parts := signatureParts(t, received[0].Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 2)
		now := time.Now()
		assert.True(t, verifiesPart(received[0], parts[0], rotated.Secret, now))
		assert.True(t, verifiesPart(received[0], parts[1], created.Secret, now))
		assert.Equal(t, before, stored(t, created.ID), "the ping writes nothing: previous_* and updated_at unchanged")
		assert.Equal(t, deliveries, env.allDeliveries())

		// After expiry (written straight to the row: the ping has no clock)
		// it signs once and still clears nothing.
		past := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		require.NoError(t, env.db.Exec("UPDATE webhook_endpoints SET previous_secret_expires_at = ? WHERE id = ?", past, created.ID).Error)
		expired := stored(t, created.ID)
		result, err = pinger.TestEndpoint(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookPingSucceeded, result.Outcome)
		received = consumer.received()
		require.Len(t, received, 2)
		parts = signatureParts(t, received[1].Header.Get(webhook.WebhookHeaderSignature))
		require.Len(t, parts, 1)
		assert.True(t, verifiesPart(received[1], parts[0], rotated.Secret, time.Now()))
		assert.Equal(t, expired, stored(t, created.ID), "the ping does not clear the expired previous secret")
		assert.NotNil(t, stored(t, created.ID).PreviousSecretSealed)
	})

	t.Run("h: unknown id and inactive endpoint", func(t *testing.T) {
		created, _ := endpoint(t, false)
		_, err := env.endpoints.RotateEndpointSecret(ctx, created.ID+100000)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound), "%v", err)

		rotated, err := env.endpoints.RotateEndpointSecret(ctx, created.ID)
		require.NoError(t, err, "an inactive endpoint can be rotated")
		assert.False(t, rotated.IsActive)
		assert.Equal(t, rotated.Secret, open(t, stored(t, created.ID).SecretSealed))
	})

	t.Run("i: a clear computed before a newer rotation committed is fenced", func(t *testing.T) {
		created, _ := endpoint(t, true)
		rotate(t, created.ID)
		// The runtime read an expired previous secret at reading...
		expired := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		require.NoError(t, env.db.Exec("UPDATE webhook_endpoints SET previous_secret_expires_at = ? WHERE id = ?", expired, created.ID).Error)
		reading := time.Now().UTC()
		// ...then a rotation committed first...
		newest := rotate(t, created.ID)
		committed := stored(t, created.ID)
		require.NotNil(t, committed.PreviousSecretSealed)
		require.True(t, committed.PreviousSecretExpiresAt.After(reading))

		// ...and the clear from the earlier reading arrives last.
		require.NoError(t, env.repo.ClearExpiredPreviousSecret(ctx, created.ID, reading))
		after := stored(t, created.ID)
		assert.Equal(t, committed, after, "the newer rotation's previous secret stays in place")
		assert.Equal(t, newest.Secret, open(t, after.SecretSealed))
	})
}
