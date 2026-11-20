package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-1 (#345): the Standard Webhooks signature, the headers and the exact
// thin body.

// The published Standard Webhooks test vector.
const (
	vectorSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	vectorID        = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	vectorTimestamp = int64(1614265330)
	vectorBody      = `{"test": 2432232314}`
	vectorSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

var webhookBodyPattern = regexp.MustCompile(`^\{"id":"evt_[0-9a-f]{32}","type":"lead\.created","occurred_at":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z","record_id":42\}$`)

func TestWebhookSignature(t *testing.T) {
	t.Run("pinned vector", func(t *testing.T) {
		signature, err := SignWebhook(vectorSecret, vectorID, vectorTimestamp, []byte(vectorBody))
		require.NoError(t, err)
		assert.Equal(t, vectorSignature, signature)

		header := map[string][]string{
			"Webhook-Id":        {vectorID},
			"Webhook-Timestamp": {strconv.FormatInt(vectorTimestamp, 10)},
			"Webhook-Signature": {signature},
		}
		at := time.Unix(vectorTimestamp, 0)
		require.NoError(t, verifyStandardWebhook(vectorSecret, header, []byte(vectorBody), at, 5*time.Minute),
			"the independent verifier accepts the pinned signature")
		assert.Error(t, verifyStandardWebhook(vectorSecret, header, []byte(vectorBody), at.Add(10*time.Minute), 5*time.Minute),
			"a stale timestamp is refused by the verifier")
	})

	t.Run("one changed byte of the id, the timestamp or the body breaks it", func(t *testing.T) {
		at := time.Unix(vectorTimestamp, 0)
		cases := map[string]struct {
			id        string
			timestamp int64
			body      string
		}{
			"id":        {vectorID[:len(vectorID)-1] + "L", vectorTimestamp, vectorBody},
			"timestamp": {vectorID, vectorTimestamp + 1, vectorBody},
			"body":      {vectorID, vectorTimestamp, `{"test": 2432232315}`},
		}
		for name, c := range cases {
			signature, err := SignWebhook(vectorSecret, c.id, c.timestamp, []byte(c.body))
			require.NoError(t, err)
			assert.NotEqual(t, vectorSignature, signature, name)

			// The pinned signature presented with the altered input fails.
			header := map[string][]string{
				"Webhook-Id":        {c.id},
				"Webhook-Timestamp": {strconv.FormatInt(c.timestamp, 10)},
				"Webhook-Signature": {vectorSignature},
			}
			assert.Error(t, verifyStandardWebhook(vectorSecret, header, []byte(c.body), at, 5*time.Minute), name)
		}
		other, err := SignWebhook("whsec_"+base64.StdEncoding.EncodeToString([]byte("another key entirely, 32 bytes!!")), vectorID, vectorTimestamp, []byte(vectorBody))
		require.NoError(t, err)
		assert.NotEqual(t, vectorSignature, other, "another key")
	})

	t.Run("refuses a secret that is not a whsec_ key", func(t *testing.T) {
		for _, secret := range []string{"", "whsec_", "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "whsec_!!!not-base64"} {
			_, err := SignWebhook(secret, vectorID, vectorTimestamp, []byte(vectorBody))
			assert.Error(t, err, "%q", secret)
		}
	})

	t.Run("a delivery request carries the headers and the exact body", func(t *testing.T) {
		secret, err := newWebhookSecret()
		require.NoError(t, err)
		eventID, err := newWebhookEventID()
		require.NoError(t, err)
		assert.Regexp(t, `^evt_[0-9a-f]{32}$`, eventID)
		occurred := time.Date(2026, 11, 20, 9, 30, 0, 123456789, time.FixedZone("CET", 3600))
		delivery := &models.WebhookDelivery{ID: 9, EndpointID: 4, EventID: eventID, EventType: models.WebhookEventLeadCreated,
			RecordID: 42, OccurredAt: occurred, Status: models.WebhookDeliverySending, Attempts: 1}

		sentAt := time.Unix(1795000000, 0)
		req, err := NewWebhookRequest(context.Background(), "https://example.com/hook", []string{secret}, delivery, sentAt)
		require.NoError(t, err)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Regexp(t, webhookBodyPattern, string(body))
		assert.Equal(t, `{"id":"`+eventID+`","type":"lead.created","occurred_at":"2026-11-20T08:30:00.123Z","record_id":42}`, string(body),
			"occurred_at is UTC with milliseconds, and no other key is sent")

		assert.Equal(t, "POST", req.Method)
		assert.Equal(t, eventID, req.Header.Get("webhook-id"))
		assert.Equal(t, "1795000000", req.Header.Get("webhook-timestamp"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "GopherCRM-Webhooks/1", req.Header.Get("User-Agent"))
		signature, err := SignWebhook(secret, eventID, 1795000000, body)
		require.NoError(t, err)
		assert.Equal(t, signature, req.Header.Get("webhook-signature"))
		require.NoError(t, verifyStandardWebhook(secret, req.Header, body, sentAt, 5*time.Minute))

		for i := range body {
			tampered := bytes.Clone(body)
			tampered[i] ^= 0x01
			assert.Error(t, verifyStandardWebhook(secret, req.Header, tampered, sentAt, 5*time.Minute), "byte %d of the body", i)
		}

		// A retry gets a fresh timestamp and signature; both verify.
		retry, err := NewWebhookRequest(context.Background(), "https://example.com/hook", []string{secret}, delivery, sentAt.Add(time.Minute))
		require.NoError(t, err)
		retryBody, err := io.ReadAll(retry.Body)
		require.NoError(t, err)
		assert.Equal(t, body, retryBody, "the body is the same event")
		assert.Equal(t, "1795000060", retry.Header.Get("webhook-timestamp"))
		assert.NotEqual(t, req.Header.Get("webhook-signature"), retry.Header.Get("webhook-signature"))
		require.NoError(t, verifyStandardWebhook(secret, retry.Header, retryBody, sentAt.Add(time.Minute), 5*time.Minute))
	})

	t.Run("two secrets give two space-separated signatures, the first secret first (#503)", func(t *testing.T) {
		newSecret, err := newWebhookSecret()
		require.NoError(t, err)
		oldSecret, err := newWebhookSecret()
		require.NoError(t, err)
		eventID, err := newWebhookEventID()
		require.NoError(t, err)
		delivery := &models.WebhookDelivery{ID: 9, EndpointID: 4, EventID: eventID, EventType: models.WebhookEventLeadCreated,
			RecordID: 42, OccurredAt: time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)}
		sentAt := time.Unix(1795000000, 0)

		req, err := NewWebhookRequest(context.Background(), "https://example.com/hook", []string{newSecret, oldSecret}, delivery, sentAt)
		require.NoError(t, err)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		signedNew, err := SignWebhook(newSecret, eventID, 1795000000, body)
		require.NoError(t, err)
		signedOld, err := SignWebhook(oldSecret, eventID, 1795000000, body)
		require.NoError(t, err)
		assert.Equal(t, signedNew+" "+signedOld, req.Header.Get("webhook-signature"), "exactly v1,<new> v1,<old>")

		// Each part verifies with its own secret over the same id, timestamp
		// and body, and only with it.
		parts := strings.Split(req.Header.Get("webhook-signature"), " ")
		require.Len(t, parts, 2)
		for i, secret := range []string{newSecret, oldSecret} {
			only := req.Header.Clone()
			only.Set("webhook-signature", parts[i])
			require.NoError(t, verifyStandardWebhook(secret, only, body, sentAt, 5*time.Minute), "part %d", i)
			other := []string{oldSecret, newSecret}[i]
			assert.Error(t, verifyStandardWebhook(other, only, body, sentAt, 5*time.Minute), "part %d with the other secret", i)
		}

		// One secret is still exactly SignWebhook's output, and none is refused.
		single, err := NewWebhookRequest(context.Background(), "https://example.com/hook", []string{newSecret}, delivery, sentAt)
		require.NoError(t, err)
		assert.Equal(t, signedNew, single.Header.Get("webhook-signature"))
		_, err = NewWebhookRequest(context.Background(), "https://example.com/hook", nil, delivery, sentAt)
		assert.Error(t, err)
	})

	t.Run("generated secrets are whsec_ plus 32 random bytes", func(t *testing.T) {
		seen := map[string]bool{}
		for i := 0; i < 8; i++ {
			secret, err := newWebhookSecret()
			require.NoError(t, err)
			require.Regexp(t, `^whsec_[A-Za-z0-9+/]{43}=$`, secret)
			key, err := base64.StdEncoding.DecodeString(secret[len("whsec_"):])
			require.NoError(t, err)
			assert.Len(t, key, 32)
			assert.False(t, seen[secret])
			seen[secret] = true
		}
	})
}
