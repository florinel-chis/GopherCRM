package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// The webhook outbox (#345). Each create path that is wired with a webhook
// repository calls enqueueWebhookEvent with the repository bound to its own
// transaction, after the record insert: one plain read of the active
// endpoints subscribed to the event type, then one pending delivery row per
// endpoint, all sharing one event id, due at once (next_attempt_at =
// occurred_at, UTC). A rollback of the create removes the record and every
// row, so the runtime, which reads committed rows only, sends nothing. Without
// the option (a nil repository) nothing is read or written.
//
// The paths: LeadService.Create and the form create_lead path for a new lead
// (lead.created), CustomerService.Create and LeadService.ConvertToCustomer
// (customer.created, the latter inside each retry attempt), TicketService.Create
// and form ticket intake with outcome created (ticket.created).

// newWebhookEventID returns evt_ followed by 32 lowercase hex characters from
// crypto/rand.
func newWebhookEventID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("webhook event id: %w", err)
	}
	return "evt_" + hex.EncodeToString(raw[:]), nil
}

// webhooksTx binds the outbox repository to tx, or returns nil when webhooks
// are not wired.
func webhooksTx(repo repository.WebhookRepository, tx *gorm.DB) repository.WebhookRepository {
	if repo == nil {
		return nil
	}
	return repo.WithTx(tx)
}

// enqueueWebhookEvent writes the outbox rows of one created record. hooks is
// bound to the creating transaction; nil means webhooks are not wired.
func enqueueWebhookEvent(ctx context.Context, hooks repository.WebhookRepository, eventType string, recordID uint) error {
	if hooks == nil {
		return nil
	}
	endpoints, err := hooks.ListActiveEndpointsFor(ctx, eventType)
	if err != nil {
		return fmt.Errorf("webhook endpoints for %s: %w", eventType, err)
	}
	if len(endpoints) == 0 {
		return nil
	}
	eventID, err := newWebhookEventID()
	if err != nil {
		return err
	}
	occurredAt := time.Now().UTC().Truncate(time.Millisecond)
	rows := make([]models.WebhookDelivery, 0, len(endpoints))
	for _, endpoint := range endpoints {
		due := occurredAt
		rows = append(rows, models.WebhookDelivery{
			EndpointID:    endpoint.ID,
			EventID:       eventID,
			EventType:     eventType,
			RecordID:      recordID,
			OccurredAt:    occurredAt,
			Status:        models.WebhookDeliveryPending,
			Attempts:      0,
			NextAttemptAt: &due,
		})
	}
	if err := hooks.EnqueueDeliveries(ctx, rows); err != nil {
		return fmt.Errorf("webhook outbox for %s %d: %w", eventType, recordID, err)
	}
	utils.Logger.WithFields(logrus.Fields{
		"operation":  "webhook_enqueue",
		"event_type": eventType,
		"record_id":  recordID,
	}).Debug("Webhook deliveries enqueued")
	return nil
}
