package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #570: the outbox rows of one change (one event, or one batch of records of
// one event type) are written by ONE INSERT on webhook_deliveries, in the
// caller's transaction, on SQLite and on a disposable TEST_MYSQL_DSN schema
// (the MySQL root skips without the variable and fails when it is
// unreachable).

func TestWebhookOutboxBatchInsertSQLite(t *testing.T) {
	runWebhookOutboxBatchInsert(t, openTicketIntakeDB(t, false))
}

func TestWebhookOutboxBatchInsertMySQL(t *testing.T) {
	runWebhookOutboxBatchInsert(t, openTicketIntakeDB(t, true))
}

// captureWebhookDeliveryInserts records the SQL of every create statement on
// webhook_deliveries run through db (and the sessions and transactions
// derived from it). It returns a getter and a stop function; stop removes the
// callback and is also registered with t.Cleanup.
func captureWebhookDeliveryInserts(t *testing.T, db *gorm.DB) (func() []string, func()) {
	t.Helper()
	name := fmt.Sprintf("test:webhook_delivery_insert_capture_%d", webhookTestSeq.Add(1))

	var mu sync.Mutex
	var statements []string
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "webhook_deliveries" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		statements = append(statements, tx.Statement.SQL.String())
	}))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			require.NoError(t, db.Callback().Create().Remove(name))
			assert.Nil(t, db.Callback().Create().Get(name), "the capture callback is gone after stop")
		})
	}
	t.Cleanup(stop)
	get := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), statements...)
	}
	return get, stop
}

// requireOneInsert asserts that exactly one statement was captured and that
// it is an INSERT.
func requireOneInsert(t *testing.T, statements []string) {
	t.Helper()
	require.Len(t, statements, 1, "one INSERT on webhook_deliveries: %v", statements)
	assert.True(t, strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statements[0])), "INSERT"),
		"the statement is an INSERT: %s", statements[0])
}

var errWebhookBatchRollback = errors.New("roll the batch insert back")

func runWebhookOutboxBatchInsert(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	ctx := context.Background()
	a := env.endpoint("https://example.com/batch-a", true, models.WebhookEventLeadCreated)
	b := env.endpoint("https://example.com/batch-b", true, models.WebhookEventLeadCreated, models.WebhookEventTicketCreated)
	c := env.endpoint("https://example.com/batch-c", true, models.WebhookEventLeadCreated)
	env.endpoint("https://example.com/batch-inactive", false, models.WebhookEventLeadCreated)
	env.endpoint("https://example.com/batch-tickets", true, models.WebhookEventTicketCreated)
	want := []uint{a.ID, b.ID, c.ID}

	reset := func(t *testing.T) {
		t.Helper()
		env.clearDeliveries()
		require.Zero(t, env.allDeliveries(), "each subtest starts on an empty outbox")
	}

	// Fails when EnqueueDeliveries inserts row by row: three endpoints give
	// three INSERTs instead of one.
	t.Run("one event: one insert, in the caller's transaction, rolled back with it", func(t *testing.T) {
		reset(t)
		statements, stop := captureWebhookDeliveryInserts(t, db)
		recordID := env.seedLead().ID
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := webhook.NewEmitter(env.repo).Bind(tx).Created(ctx, webhook.Lead, recordID); err != nil {
				return err
			}
			var rows []models.WebhookDelivery
			if err := tx.Where("event_type = ? AND record_id = ?", models.WebhookEventLeadCreated, recordID).
				Order("id").Find(&rows).Error; err != nil {
				return err
			}
			assert.Len(t, rows, 3, "visible through tx")
			got := []uint{}
			for _, row := range rows {
				got = append(got, row.EndpointID)
				assert.Equal(t, rows[0].EventID, row.EventID, "one event id")
			}
			assert.ElementsMatch(t, want, got)
			return errWebhookBatchRollback
		})
		require.ErrorIs(t, err, errWebhookBatchRollback)
		assert.Zero(t, env.allDeliveries(), "the rows rolled back with the caller's transaction")
		requireOneInsert(t, statements())
		stop()
	})

	// Fails when EnqueueDeliveries inserts row by row: the committed create
	// would show three INSERTs on webhook_deliveries.
	t.Run("a committed create writes one row per endpoint in one insert", func(t *testing.T) {
		reset(t)
		env.wire(env.repo)
		defer env.wire(nil)
		statements, stop := captureWebhookDeliveryInserts(t, db)
		lead := env.newLead()
		require.NoError(t, env.leads.Create(lead))
		assertFanOut(t, env, models.WebhookEventLeadCreated, lead.ID, want...)
		requireOneInsert(t, statements())
		stop()
	})

	// Fails when EnqueueDeliveries inserts row by row: two records times three
	// endpoints would be six INSERTs.
	t.Run("a batch of records is still one insert", func(t *testing.T) {
		reset(t)
		statements, stop := captureWebhookDeliveryInserts(t, db)
		first, second := env.seedLead().ID, env.seedLead().ID
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return webhook.NewEmitter(env.repo).Bind(tx).Created(ctx, webhook.Lead, first, second)
		}))
		assert.EqualValues(t, 6, env.allDeliveries())
		eventIDs := map[uint]string{}
		for _, recordID := range []uint{first, second} {
			rows := env.deliveries(models.WebhookEventLeadCreated, recordID)
			require.Len(t, rows, 3, "record %d", recordID)
			got := []uint{}
			for _, row := range rows {
				got = append(got, row.EndpointID)
				assert.Equal(t, rows[0].EventID, row.EventID, "one event id per record")
			}
			assert.ElementsMatch(t, want, got)
			eventIDs[recordID] = rows[0].EventID
		}
		assert.NotEqual(t, eventIDs[first], eventIDs[second], "each record has its own event id")
		requireOneInsert(t, statements())
		stop()
	})

	reset(t)
}
