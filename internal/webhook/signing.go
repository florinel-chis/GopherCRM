package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Webhook signatures (#345) follow the Standard Webhooks scheme: the signed
// content is "<webhook-id>.<webhook-timestamp>.<body>", the key is the
// base64-decoded part of the whsec_ secret, and the signature header carries
// "v1," followed by the base64 HMAC-SHA256. A consumer recomputes it, compares
// in constant time, refuses a timestamp far from its own clock (five minutes
// is the usual tolerance) and deduplicates on webhook-id. During the overlap
// window of a secret rotation (#503) the header carries two space-separated
// signatures, under the new secret first and the previous one second; a
// consumer accepts the request when any of them verifies.

// WebhookSecretPrefix starts every endpoint signing secret.
const WebhookSecretPrefix = "whsec_"

// WebhookUserAgent is sent on every delivery.
const WebhookUserAgent = "GopherCRM-Webhooks/1"

// The delivery headers.
const (
	WebhookHeaderID        = "webhook-id"
	WebhookHeaderTimestamp = "webhook-timestamp"
	WebhookHeaderSignature = "webhook-signature"
)

// WebhookOccurredAtLayout renders occurred_at in UTC with milliseconds.
const WebhookOccurredAtLayout = "2006-01-02T15:04:05.000Z"

// errWebhookSecretInvalid is returned for a secret that is not whsec_ plus a
// non-empty base64 key. It never quotes the secret.
var errWebhookSecretInvalid = errors.New("webhook secret is not a whsec_ base64 key")

// webhookPayload is the thin body: ids, the type and the time only. The field
// order is the wire order.
type webhookPayload struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurred_at"`
	RecordID   uint   `json:"record_id"`
}

// WebhookBody renders the exact body of a delivery:
// {"id":"evt_…","type":"…","occurred_at":"<RFC 3339 UTC, ms>","record_id":<id>}.
func WebhookBody(delivery *models.WebhookDelivery) ([]byte, error) {
	return json.Marshal(webhookPayload{
		ID:         delivery.EventID,
		Type:       delivery.EventType,
		OccurredAt: delivery.OccurredAt.UTC().Format(WebhookOccurredAtLayout),
		RecordID:   delivery.RecordID,
	})
}

// webhookKey decodes the key of a whsec_ secret.
func webhookKey(secret string) ([]byte, error) {
	if !strings.HasPrefix(secret, WebhookSecretPrefix) {
		return nil, errWebhookSecretInvalid
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, WebhookSecretPrefix))
	if err != nil || len(key) == 0 {
		return nil, errWebhookSecretInvalid
	}
	return key, nil
}

// SignWebhook returns "v1," + base64(HMAC-SHA256(key, eventID + "." +
// timestamp + "." + body)), key being the base64-decoded part of the whsec_
// secret and timestamp the decimal Unix seconds.
func SignWebhook(secret, eventID string, unixSeconds int64, body []byte) (string, error) {
	key, err := webhookKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(eventID))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(unixSeconds, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// NewWebhookRequest builds one signed attempt of delivery to url at sentAt:
// every attempt gets its own timestamp and signatures, one per secret in
// secrets (the current secret first, then the previous one inside a rotation
// window).
func NewWebhookRequest(ctx context.Context, url string, secrets []string, delivery *models.WebhookDelivery, sentAt time.Time) (*http.Request, error) {
	body, err := WebhookBody(delivery)
	if err != nil {
		return nil, err
	}
	return newSignedWebhookRequest(ctx, url, secrets, delivery.EventID, body, sentAt)
}

// webhookPingPayload is the body of a test ping: no record_id and no record
// data. The field order is the wire order.
type webhookPingPayload struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurred_at"`
}

// webhookPingBody renders the exact body of a test ping:
// {"id":"evt_…","type":"webhook.ping","occurred_at":"<RFC 3339 UTC, ms>"}.
func webhookPingBody(eventID string, occurredAt time.Time) ([]byte, error) {
	return json.Marshal(webhookPingPayload{
		ID:         eventID,
		Type:       models.WebhookEventPing,
		OccurredAt: occurredAt.UTC().Format(WebhookOccurredAtLayout),
	})
}

// newSignedWebhookRequest builds one signed POST of body to url at sentAt
// with the Standard Webhooks headers, eventID being the webhook-id. The
// webhook-signature header holds one "v1," signature per secret, in the order
// given, joined by one space. No secret is an error.
func newSignedWebhookRequest(ctx context.Context, url string, secrets []string, eventID string, body []byte, sentAt time.Time) (*http.Request, error) {
	if len(secrets) == 0 {
		return nil, errWebhookSecretInvalid
	}
	timestamp := sentAt.Unix()
	signatures := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		signature, err := SignWebhook(secret, eventID, timestamp, body)
		if err != nil {
			return nil, err
		}
		signatures = append(signatures, signature)
	}
	signature := strings.Join(signatures, " ")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(WebhookHeaderID, eventID)
	req.Header.Set(WebhookHeaderTimestamp, strconv.FormatInt(timestamp, 10))
	req.Header.Set(WebhookHeaderSignature, signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", WebhookUserAgent)
	return req, nil
}
