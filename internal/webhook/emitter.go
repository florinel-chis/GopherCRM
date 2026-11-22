package webhook

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

// The webhook outbox (#345, updated events #408, deleted events #437). Every
// write path of a registered entity binds the service's Emitter to its own
// transaction and calls Created, Updated or Deleted after the record write:
// one plain read of the active endpoints subscribed to the event type, then
// one pending delivery row per (endpoint, record) in one insert, each record
// with its own event id shared by its rows, due at once (next_attempt_at =
// occurred_at, UTC, truncated to the millisecond). A rollback of the write
// removes the record change and every row, so the runtime, which reads
// committed rows only, sends nothing. A service built without webhooks holds
// Nop, which reads and writes nothing.
//
// The create paths: LeadService.Create and the form create_lead path for a new
// lead (lead.created), CustomerService.Create and LeadService.ConvertToCustomer
// (customer.created, the latter inside each retry attempt), TicketService.Create
// and form ticket intake with outcome created (ticket.created),
// DealService.Create (deal.created, #515), and TaskService.Create and
// CreateWithLabels and the automation create_task step (task.created, #516, the
// latter in the step transaction).
//
// The update paths (#408): LeadService.Update and LeadService.Assign
// (lead.updated), LeadService.ConvertToCustomer (lead.updated next to
// customer.created, inside each retry attempt), CustomerService.Update
// (customer.updated), TicketService.Update (ticket.updated), DealService.Update
// and DealService.ChangeStage (deal.updated, #515, the row read through
// DealLockingReader), the automation assign_owner step on a lead, ticket or
// deal run (lead.updated / ticket.updated / deal.updated, in the step
// transaction), TaskService.Update and UpdateWithLabels (task.updated, #516,
// the row read through TaskLockingReader, and a changed label set read through
// TaskLockingReader.GetLabelIDsForUpdate counts as a change), and
// BulkSetLeadStatus / BulkSetTicketStatus / BulkSetTaskStatus (one
// lead.updated / ticket.updated / task.updated per record in one call, so the
// endpoints are read once per batch). An updated event is written only when the write
// changed the record's WebhookState() (internal/models), the rule of the
// event: the state of the row read under lock inside the writing transaction
// is compared with the state written (updated_at excluded), so an update that
// sends the stored values writes no row. The caller's values come from a read
// made before the transaction, so a write that reverts a column another
// request committed in between is a change and emits an event.
// CustomerService.Assign, delete, ticket comments and the other automation
// steps emit no updated event. Neither do a company delete, which unlinks the
// company's leads, customers and deals (companyRepository.UnlinkDeals among
// them), the erasure scrub of the text of the deals an erased customer or lead
// reaches (scrubReachedDeals), nor a label hard delete, whose labelRepository
// purge of task_labels changes the label set of every task that carried it.
//
// The deleted paths (#437): TicketService.Delete (ticket.deleted, in one
// transaction with the soft delete when the service is wired),
// DealService.Delete (deal.deleted, #515, likewise), TaskService.Delete
// (task.deleted, #516, likewise, after a locking read of the live task inside
// the transaction, so a delete that lost a race emits nothing), and every
// erasure unit of the lead, customer and user repositories (lead.deleted and
// customer.deleted for each lead and customer the unit found live and erased,
// cascades included), through the listener of NewErasureListener at the end
// of the unit. Deleted de-duplicates the ids. A deleted event goes to every
// active endpoint subscribed to the deleted type, including endpoints that
// never received an event for the record: one plain read of the endpoints,
// then one insert, and no read of webhook_deliveries. An erasure writes
// exactly the row a delete writes: no reason and no personal data.
//
// A new entity needs one catalogue entry (catalog.go) and one Created,
// Updated or Deleted call per write path; the event types, the event type
// check and the validation messages follow from the catalogue.

// Emitter writes webhook events into the outbox of a transaction.
type Emitter interface {
	// Bind returns the emitter writing through tx.
	Bind(tx *gorm.DB) TxEmitter
}

// TxEmitter writes the events of records written in one transaction.
type TxEmitter interface {
	Created(ctx context.Context, e Entity, ids ...uint) error
	Updated(ctx context.Context, e Entity, ids ...uint) error
	Deleted(ctx context.Context, e Entity, ids ...uint) error
}

// Nop is the emitter of a service without webhooks: it reads and writes
// nothing and returns nil.
var Nop Emitter = nopEmitter{}

type nopEmitter struct{}

func (nopEmitter) Bind(*gorm.DB) TxEmitter { return nopTx{} }

type nopTx struct{}

func (nopTx) Created(context.Context, Entity, ...uint) error { return nil }
func (nopTx) Updated(context.Context, Entity, ...uint) error { return nil }
func (nopTx) Deleted(context.Context, Entity, ...uint) error { return nil }

// NopTx is the bound form of Nop, for a write that runs without a
// transaction.
var NopTx TxEmitter = nopTx{}

// NewEmitter returns the emitter of the default catalogue writing through
// repo. A nil repo returns Nop.
func NewEmitter(repo repository.WebhookRepository) Emitter {
	return NewEmitterFor(defaultCatalogue, repo)
}

// NewEmitterFor returns the emitter of catalogue writing through repo. A nil
// repo returns Nop.
func NewEmitterFor(catalogue Catalogue, repo repository.WebhookRepository) Emitter {
	if repo == nil {
		return Nop
	}
	return outboxEmitter{catalogue: catalogue, repo: repo}
}

type outboxEmitter struct {
	catalogue Catalogue
	repo      repository.WebhookRepository
}

func (e outboxEmitter) Bind(tx *gorm.DB) TxEmitter {
	return boundEmitter{catalogue: e.catalogue, hooks: e.repo.WithTx(tx)}
}

type boundEmitter struct {
	catalogue Catalogue
	hooks     repository.WebhookRepository
}

func (b boundEmitter) Created(ctx context.Context, e Entity, ids ...uint) error {
	return b.emit(ctx, e, Created, ids)
}

func (b boundEmitter) Updated(ctx context.Context, e Entity, ids ...uint) error {
	return b.emit(ctx, e, Updated, ids)
}

// Deleted writes one event per distinct id.
func (b boundEmitter) Deleted(ctx context.Context, e Entity, ids ...uint) error {
	return b.emit(ctx, e, Deleted, distinct(ids))
}

func (b boundEmitter) emit(ctx context.Context, e Entity, a Action, ids []uint) error {
	eventType, ok := b.catalogue.EventType(e, a)
	if !ok {
		return fmt.Errorf("webhook entity %q is not registered", string(e))
	}
	return enqueue(ctx, b.hooks, eventType, ids)
}

// distinct drops repeated ids, keeping the first occurrence's order.
func distinct(ids []uint) []uint {
	out := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// NewEventID returns evt_ followed by 32 lowercase hex characters from
// crypto/rand.
func NewEventID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("webhook event id: %w", err)
	}
	return "evt_" + hex.EncodeToString(raw[:]), nil
}

// enqueue writes the outbox rows of several records of one event type in one
// batch: the subscribed endpoints are read once, every record gets its own
// event id (shared by its row per endpoint), and all rows go in one insert.
// No record ids means nothing is read or written.
func enqueue(ctx context.Context, hooks repository.WebhookRepository, eventType string, recordIDs []uint) error {
	if len(recordIDs) == 0 {
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
		eventID, err := NewEventID()
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

// NewErasureListener returns the erasure listener that writes the
// lead.deleted and then the customer.deleted events of an erasure unit, bound
// to the unit's transaction (#437). Nop or a nil emitter returns nil (no
// listener), so a build without webhooks adds no call to the erasure unit.
func NewErasureListener(emitter Emitter) repository.ErasureListener {
	if emitter == nil || emitter == Nop {
		return nil
	}
	return func(tx *gorm.DB, erased repository.ErasedRecords) error {
		events := emitter.Bind(tx)
		ctx := context.Background()
		if err := events.Deleted(ctx, Lead, erased.LeadIDs...); err != nil {
			return err
		}
		return events.Deleted(ctx, Customer, erased.CustomerIDs...)
	}
}
