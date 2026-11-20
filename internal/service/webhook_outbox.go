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

// The webhook outbox (#345, updated events #408). Each create or update path
// that is wired with a webhook repository calls enqueueWebhookEvent with the
// repository bound to its own transaction, after the record write: one plain
// read of the active endpoints subscribed to the event type, then one pending
// delivery row per endpoint, all sharing one event id, due at once
// (next_attempt_at = occurred_at, UTC). A rollback of the write removes the
// record change and every row, so the runtime, which reads committed rows
// only, sends nothing. Without the option (a nil repository) nothing is read
// or written.
//
// The create paths: LeadService.Create and the form create_lead path for a new
// lead (lead.created), CustomerService.Create and LeadService.ConvertToCustomer
// (customer.created, the latter inside each retry attempt), TicketService.Create
// and form ticket intake with outcome created (ticket.created).
//
// The update paths (#408): LeadService.Update (lead.updated),
// CustomerService.Update (customer.updated), TicketService.Update
// (ticket.updated), and BulkSetLeadStatus / BulkSetTicketStatus (one
// lead.updated / ticket.updated per record, through enqueueWebhookEvents so
// the endpoints are read once per batch). An updated event is written only
// when the write changed at least one stored column of the record, comparing
// the row read under lock inside the writing transaction with the row written
// (updated_at excluded): an update that sends the stored values writes no
// row. The caller's values come from a read made before the transaction, so a
// write that reverts a column another request committed in between is a
// change and emits an event. Assign, conversion, delete, ticket comments,
// automation steps and bulk task status emit no updated event.
//
// The deleted paths (#437): TicketService.Delete (ticket.deleted, in one
// transaction with the soft delete when the service is wired), and every
// erasure unit of the lead, customer and user repositories (lead.deleted and
// customer.deleted for each lead and customer the unit found live and erased,
// cascades included), through the erasure listener of
// NewWebhookErasureListener at the end of the unit. A deleted event goes to
// every active endpoint subscribed to the deleted type, including endpoints
// that never received an event for the record: one plain read of the
// endpoints, then one insert, and no read of webhook_deliveries. An erasure
// writes exactly the row a delete writes: no reason and no personal data.

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

// enqueueWebhookEvent writes the outbox rows of one created or updated
// record. hooks is bound to the writing transaction; nil means webhooks are
// not wired.
func enqueueWebhookEvent(ctx context.Context, hooks repository.WebhookRepository, eventType string, recordID uint) error {
	return enqueueWebhookEvents(ctx, hooks, eventType, []uint{recordID})
}

// enqueueWebhookEvents writes the outbox rows of several records of one event
// type in one batch: the subscribed endpoints are read once, every record
// gets its own event id (shared by its row per endpoint), and all rows go in
// one insert. No record ids means nothing is read or written.
func enqueueWebhookEvents(ctx context.Context, hooks repository.WebhookRepository, eventType string, recordIDs []uint) error {
	if hooks == nil || len(recordIDs) == 0 {
		return nil
	}
	endpoints, err := hooks.ListActiveEndpointsFor(ctx, eventType)
	if err != nil {
		return fmt.Errorf("webhook endpoints for %s: %w", eventType, err)
	}
	if len(endpoints) == 0 {
		return nil
	}
	occurredAt := time.Now().UTC().Truncate(time.Millisecond)
	rows := make([]models.WebhookDelivery, 0, len(endpoints)*len(recordIDs))
	for _, recordID := range recordIDs {
		eventID, err := newWebhookEventID()
		if err != nil {
			return err
		}
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
	}
	if err := hooks.EnqueueDeliveries(ctx, rows); err != nil {
		if len(recordIDs) == 1 {
			return fmt.Errorf("webhook outbox for %s %d: %w", eventType, recordIDs[0], err)
		}
		return fmt.Errorf("webhook outbox for %d %s events: %w", len(recordIDs), eventType, err)
	}
	// One line per record, with the agreed fields only.
	for _, recordID := range recordIDs {
		utils.Logger.WithFields(logrus.Fields{
			"operation":  "webhook_enqueue",
			"event_type": eventType,
			"record_id":  recordID,
		}).Debug("Webhook deliveries enqueued")
	}
	return nil
}

// enqueueDeletedWebhookEvents writes the outbox rows of deleted records of
// one deleted event type: one row per (active endpoint subscribed to
// deletedType, distinct record id), whether or not the endpoint received an
// earlier event for the record. It de-duplicates recordIDs (one event per
// distinct id) and then writes what enqueueWebhookEvents writes: the endpoints
// are read once, each record gets its own event id shared by its rows, and all
// rows go in one insert. webhook_deliveries is not read. nil hooks or no
// record ids mean nothing is read or written; an event type that is not a
// deleted type is refused.
func enqueueDeletedWebhookEvents(ctx context.Context, hooks repository.WebhookRepository, deletedType string, recordIDs []uint) error {
	if hooks == nil || len(recordIDs) == 0 {
		return nil
	}
	switch deletedType {
	case models.WebhookEventLeadDeleted, models.WebhookEventCustomerDeleted, models.WebhookEventTicketDeleted:
	default:
		return fmt.Errorf("webhook event type %s is not a deleted event", deletedType)
	}
	distinct := make([]uint, 0, len(recordIDs))
	seen := make(map[uint]struct{}, len(recordIDs))
	for _, recordID := range recordIDs {
		if _, dup := seen[recordID]; dup {
			continue
		}
		seen[recordID] = struct{}{}
		distinct = append(distinct, recordID)
	}
	return enqueueWebhookEvents(ctx, hooks, deletedType, distinct)
}

// NewWebhookErasureListener returns the erasure listener that writes the
// lead.deleted and customer.deleted events of an erasure unit, with the
// outbox bound to the unit's transaction (#437). A nil repository returns nil
// (no listener).
func NewWebhookErasureListener(repo repository.WebhookRepository) repository.ErasureListener {
	if repo == nil {
		return nil
	}
	return func(tx *gorm.DB, erased repository.ErasedRecords) error {
		hooks := repo.WithTx(tx)
		ctx := context.Background()
		if err := enqueueDeletedWebhookEvents(ctx, hooks, models.WebhookEventLeadDeleted, erased.LeadIDs); err != nil {
			return err
		}
		return enqueueDeletedWebhookEvents(ctx, hooks, models.WebhookEventCustomerDeleted, erased.CustomerIDs)
	}
}

// equalUintPtr reports whether two nullable ids hold the same value.
func equalUintPtr(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equalTimePtr reports whether two nullable instants are the same instant.
func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
