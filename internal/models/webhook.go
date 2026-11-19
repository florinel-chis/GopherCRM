package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Outbound webhooks (#345). An admin registers HTTPS endpoints, each with its
// own signing secret and a subset of the event types below. Every create path
// of a lead, a customer or a ticket, and since #408 every update of one that
// changed a stored value, writes one webhook_deliveries row per active
// subscribed endpoint inside its writing transaction (an outbox), and a
// background runtime delivers the committed rows. Both tables hold ids, event
// types, statuses and timestamps only: a delivery carries the record id, and a
// consumer fetches the record through the API, where erasure and visibility
// apply at fetch time.

// The event types an endpoint may subscribe to. They are untyped strings.
const (
	WebhookEventLeadCreated     = "lead.created"
	WebhookEventLeadUpdated     = "lead.updated"
	WebhookEventCustomerCreated = "customer.created"
	WebhookEventCustomerUpdated = "customer.updated"
	WebhookEventTicketCreated   = "ticket.created"
	WebhookEventTicketUpdated   = "ticket.updated"
)

// WebhookEventTypeValues lists every event type in canonical (sorted) order.
var WebhookEventTypeValues = []string{
	WebhookEventCustomerCreated, WebhookEventCustomerUpdated,
	WebhookEventLeadCreated, WebhookEventLeadUpdated,
	WebhookEventTicketCreated, WebhookEventTicketUpdated,
}

// IsWebhookEventType reports whether value is one of the six event types,
// compared exactly.
func IsWebhookEventType(value string) bool {
	for _, known := range WebhookEventTypeValues {
		if value == known {
			return true
		}
	}
	return false
}

// The states of a delivery row.
const (
	WebhookDeliveryPending   = "pending"
	WebhookDeliverySending   = "sending"
	WebhookDeliverySucceeded = "succeeded"
	WebhookDeliveryFailed    = "failed"
)

// IsWebhookDeliveryStatus reports whether value is one of the four states.
func IsWebhookDeliveryStatus(value string) bool {
	switch value {
	case WebhookDeliveryPending, WebhookDeliverySending, WebhookDeliverySucceeded, WebhookDeliveryFailed:
		return true
	}
	return false
}

// Error codes of a failed attempt. They never carry a transport's own text,
// which may quote the URL.
const (
	WebhookErrorHTTPStatus         = "http_status"
	WebhookErrorRedirect           = "redirect"
	WebhookErrorTimeout            = "timeout"
	WebhookErrorConnectionFailed   = "connection_failed"
	WebhookErrorDestinationRefused = "destination_refused"
	WebhookErrorEndpointInactive   = "endpoint_inactive"
	WebhookErrorSecretUnavailable  = "secret_unavailable"
	WebhookErrorLeaseExpired       = "lease_expired"
)

const (
	// WebhookEndpointURLMaxLength is the longest accepted endpoint URL, in
	// characters (the column is varchar(2048)).
	WebhookEndpointURLMaxLength = 2048
	// WebhookMaxAttempts is the attempt cap of one delivery: the attempt that
	// reaches it stores failed and is never retried.
	WebhookMaxAttempts = 10
	// webhookEventTypesColumnMaxBytes mirrors varchar(255).
	webhookEventTypesColumnMaxBytes = 255
)

// WebhookEventTypes is the subscribed subset of an endpoint. It is stored as a
// canonical comma-separated list (deduplicated, sorted) and marshalled as a
// JSON array.
type WebhookEventTypes []string

// CanonicalWebhookEventTypes deduplicates and sorts values. It does not
// validate them.
func CanonicalWebhookEventTypes(values []string) WebhookEventTypes {
	seen := make(map[string]struct{}, len(values))
	out := make(WebhookEventTypes, 0, len(values))
	for _, value := range values {
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Contains reports whether eventType is subscribed.
func (t WebhookEventTypes) Contains(eventType string) bool {
	for _, value := range t {
		if value == eventType {
			return true
		}
	}
	return false
}

// Value stores the canonical comma-separated form.
func (t WebhookEventTypes) Value() (driver.Value, error) {
	joined := strings.Join(CanonicalWebhookEventTypes(t), ",")
	if len(joined) > webhookEventTypesColumnMaxBytes {
		return nil, fmt.Errorf("webhook event types exceed %d bytes", webhookEventTypesColumnMaxBytes)
	}
	return joined, nil
}

// Scan reads the comma-separated form.
func (t *WebhookEventTypes) Scan(src interface{}) error {
	var raw string
	switch value := src.(type) {
	case nil:
		raw = ""
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return fmt.Errorf("unsupported webhook event types value %T", src)
	}
	parsed := WebhookEventTypes{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parsed = append(parsed, part)
		}
	}
	*t = parsed
	return nil
}

// MarshalJSON renders the subset as an array, never null.
func (t WebhookEventTypes) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(t))
}

// WebhookEndpoint is one registered consumer. The signing secret is stored
// sealed (utils.SecretBox, context webhook-endpoint-secret) and never
// serialised; it is returned once, in the create response.
//
// @Description A registered outbound webhook endpoint (admin only). Never carries the signing secret; it is returned once, by POST /webhooks/endpoints.
type WebhookEndpoint struct {
	// Unique identifier, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"4" readonly:"true"`
	// When the endpoint was registered (RFC 3339). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-20T09:30:00Z" readonly:"true"`
	// When the endpoint last changed (RFC 3339). Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-20T09:30:00Z" readonly:"true"`
	// Target URL: https, no credentials, query string or fragment, at most 2048 characters; the host must resolve to public addresses only.
	URL string `gorm:"type:varchar(2048);not null" json:"url" maxLength:"2048" example:"https://hooks.example.com/gophercrm"`
	// Subscribed event types, deduplicated and sorted.
	EventTypes WebhookEventTypes `gorm:"type:varchar(255);not null" json:"event_types" swaggertype:"array,string" enums:"customer.created,customer.updated,lead.created,lead.updated,ticket.created,ticket.updated" example:"lead.created,ticket.created"`
	// Sealed signing secret; never serialised.
	SecretSealed string `gorm:"type:varchar(255);not null" json:"-" swaggerignore:"true"`
	// Whether new events are enqueued for this endpoint. An inactive endpoint's pending deliveries fail with endpoint_inactive.
	IsActive bool `gorm:"not null" json:"is_active" example:"true"`
}

// TableName pins the table name.
func (WebhookEndpoint) TableName() string { return "webhook_endpoints" }

// WebhookEndpointCreated is the create response: the endpoint plus its signing
// secret, shown this once.
//
// @Description A newly registered webhook endpoint with its signing secret. This is the only response that ever carries the secret.
type WebhookEndpointCreated struct {
	WebhookEndpoint
	// Signing secret: whsec_ followed by the base64 of 32 random bytes. Shown only in this response; store it now.
	Secret string `json:"secret" example:"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"`
}

// WebhookDelivery is one event for one endpoint: the outbox row and its
// delivery log entry. It holds ids, the event type, a status, counters, an
// HTTP status code, an error code and timestamps only.
//
// attempts and last_http_status carry size:32, not type:int: GORM reads the
// tag "int" as its generic integer type and the MySQL dialector then emits
// bigint, while a 32-bit size gives int, the type the SQL migration creates.
//
// @Description One outbound webhook delivery (one event for one endpoint) and its outcome. Holds ids, the event type, the status, counters and timestamps; never a URL, a secret or a payload.
type WebhookDelivery struct {
	// Unique identifier, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"91" readonly:"true"`
	// When the row was enqueued (RFC 3339). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-20T09:30:00Z" readonly:"true"`
	// When the row last changed (RFC 3339); finished rows are pruned 30 days after it. Read-only.
	UpdatedAt time.Time `gorm:"index:idx_webhook_deliveries_updated_at" json:"updated_at" example:"2026-11-20T09:30:01Z" readonly:"true"`
	// The endpoint the event is delivered to.
	EndpointID uint `gorm:"not null;uniqueIndex:idx_webhook_deliveries_endpoint_event,priority:1" json:"endpoint_id" example:"4" readonly:"true"`
	// Event id (evt_ followed by 32 hex characters), shared by every endpoint's row of one event; sent as webhook-id and as the body's id.
	EventID string `gorm:"type:varchar(64);not null;uniqueIndex:idx_webhook_deliveries_endpoint_event,priority:2" json:"event_id" example:"evt_4f1c2a9be07d4c4fa1c3d2e5f6a7b8c9" readonly:"true"`
	// Event type.
	EventType string `gorm:"type:varchar(32);not null" json:"event_type" enums:"lead.created,lead.updated,customer.created,customer.updated,ticket.created,ticket.updated" example:"lead.created" readonly:"true"`
	// Id of the created or updated lead, customer or ticket: the only record datum sent.
	RecordID uint `gorm:"not null" json:"record_id" example:"42" readonly:"true"`
	// When the record was created or updated (RFC 3339, UTC).
	OccurredAt time.Time `gorm:"not null" json:"occurred_at" example:"2026-11-20T09:30:00.123Z" readonly:"true"`
	// Delivery state: pending (due at next_attempt_at), sending (claimed by the sender), succeeded (a 2xx answer) or failed (the attempt cap was reached, or the endpoint or its secret is unusable).
	Status string `gorm:"type:varchar(20);not null;default:'pending';index:idx_webhook_deliveries_status_next,priority:1" json:"status" enums:"pending,sending,succeeded,failed" example:"succeeded" readonly:"true"`
	// Attempts made so far (at most 10).
	Attempts int `gorm:"size:32;not null;default:0" json:"attempts" example:"1" readonly:"true"`
	// When the next attempt is due (RFC 3339); null once the row is finished.
	NextAttemptAt *time.Time `gorm:"index:idx_webhook_deliveries_status_next,priority:2" json:"next_attempt_at" extensions:"x-nullable" example:"2026-11-20T09:31:00Z" readonly:"true"`
	// When the last attempt started (RFC 3339); null before the first.
	LastAttemptAt *time.Time `json:"last_attempt_at" extensions:"x-nullable" example:"2026-11-20T09:30:00Z" readonly:"true"`
	// Claim lease of the sender; never serialised.
	LeaseUntil *time.Time `json:"-" swaggerignore:"true"`
	// HTTP status code of the last answer; null when none was received.
	LastHTTPStatus *int `gorm:"size:32" json:"last_http_status" extensions:"x-nullable" example:"200" readonly:"true"`
	// Why the last attempt failed: http_status (a non-2xx answer), redirect (a 3xx answer, not followed), timeout, connection_failed, destination_refused (the host resolved to a non-public address), endpoint_inactive, secret_unavailable or lease_expired. Empty after a success.
	ErrorCode string `gorm:"type:varchar(50);not null;default:''" json:"error_code" enums:",http_status,redirect,timeout,connection_failed,destination_refused,endpoint_inactive,secret_unavailable,lease_expired" example:"" readonly:"true"`
}

// TableName pins the table name.
func (WebhookDelivery) TableName() string { return "webhook_deliveries" }
