package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errRollback = errors.New("roll back")

type emitterEnv struct {
	db   *gorm.DB
	repo repository.WebhookRepository
}

func newEmitterEnv(t *testing.T) *emitterEnv {
	t.Helper()
	// The emitter logs through utils.Logger, which only the application
	// configures; the tests get a quiet one and restore the previous value.
	previous := utils.Logger
	quiet := logrus.New()
	quiet.SetOutput(io.Discard)
	utils.Logger = quiet
	t.Cleanup(func() { utils.Logger = previous })
	db := testsupport.Open(t, testsupport.DialectFor(false), "webhook_emitter",
		testsupport.Models(&models.WebhookEndpoint{}, &models.WebhookDelivery{}))
	return &emitterEnv{db: db, repo: repository.NewWebhookRepository(db)}
}

func (e *emitterEnv) endpoint(t *testing.T, active bool, types ...string) uint {
	t.Helper()
	endpoint := &models.WebhookEndpoint{URL: "https://example.com/hook", EventTypes: models.WebhookEventTypes(types),
		SecretSealed: "sealed", IsActive: active}
	require.NoError(t, e.repo.CreateEndpoint(context.Background(), endpoint))
	return endpoint.ID
}

func (e *emitterEnv) rows(t *testing.T) []models.WebhookDelivery {
	t.Helper()
	var rows []models.WebhookDelivery
	require.NoError(t, e.db.Order("id").Find(&rows).Error)
	return rows
}

func (e *emitterEnv) emit(t *testing.T, emitter webhook.Emitter, call func(ctx context.Context, tx webhook.TxEmitter) error) error {
	t.Helper()
	return e.db.Transaction(func(tx *gorm.DB) error {
		return call(context.Background(), emitter.Bind(tx))
	})
}

func (e *emitterEnv) reset(t *testing.T) {
	t.Helper()
	require.NoError(t, e.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.WebhookDelivery{}).Error)
	require.NoError(t, e.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.WebhookEndpoint{}).Error)
}

var eventIDPattern = regexp.MustCompile(`^evt_[0-9a-f]{32}$`)

func TestEmitter(t *testing.T) {
	env := newEmitterEnv(t)
	emitter := webhook.NewEmitter(env.repo)

	for _, tc := range []struct {
		action string
		call   func(ctx context.Context, tx webhook.TxEmitter, ids ...uint) error
	}{
		{"created", func(ctx context.Context, tx webhook.TxEmitter, ids ...uint) error {
			return tx.Created(ctx, webhook.Customer, ids...)
		}},
		{"updated", func(ctx context.Context, tx webhook.TxEmitter, ids ...uint) error {
			return tx.Updated(ctx, webhook.Customer, ids...)
		}},
		{"deleted", func(ctx context.Context, tx webhook.TxEmitter, ids ...uint) error {
			return tx.Deleted(ctx, webhook.Customer, ids...)
		}},
	} {
		t.Run(tc.action+" writes one pending row per active subscribed endpoint", func(t *testing.T) {
			env.reset(t)
			eventType := "customer." + tc.action
			first := env.endpoint(t, true, eventType)
			second := env.endpoint(t, true, eventType, "lead.created")
			env.endpoint(t, false, eventType)           // inactive
			env.endpoint(t, true, "customer.something") // unsubscribed
			env.endpoint(t, true, "lead."+tc.action)    // another entity
			require.NoError(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
				return tc.call(ctx, tx, 11, 12)
			}))
			rows := env.rows(t)
			require.Len(t, rows, 4)
			byRecord := map[uint][]models.WebhookDelivery{}
			for _, row := range rows {
				byRecord[row.RecordID] = append(byRecord[row.RecordID], row)
				assert.Equal(t, eventType, row.EventType)
				assert.Equal(t, models.WebhookDeliveryPending, row.Status)
				assert.Zero(t, row.Attempts)
				require.NotNil(t, row.NextAttemptAt)
				assert.True(t, row.NextAttemptAt.Equal(row.OccurredAt), "due at once")
				assert.Equal(t, row.OccurredAt.UTC().Truncate(1e6), row.OccurredAt.UTC(), "millisecond precision")
				assert.Regexp(t, eventIDPattern, row.EventID)
			}
			require.Len(t, byRecord, 2)
			for recordID, recordRows := range byRecord {
				require.Len(t, recordRows, 2, "record %d", recordID)
				assert.Equal(t, recordRows[0].EventID, recordRows[1].EventID, "one event id per record")
				assert.ElementsMatch(t, []uint{first, second}, []uint{recordRows[0].EndpointID, recordRows[1].EndpointID})
			}
			assert.NotEqual(t, byRecord[11][0].EventID, byRecord[12][0].EventID, "each record its own event id")
		})
	}

	t.Run("deleted de-duplicates the ids", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "lead.deleted")
		require.NoError(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Deleted(ctx, webhook.Lead, 5, 5, 6)
		}))
		rows := env.rows(t)
		require.Len(t, rows, 2)
		assert.ElementsMatch(t, []uint{5, 6}, []uint{rows[0].RecordID, rows[1].RecordID})
		assert.NotEqual(t, rows[0].EventID, rows[1].EventID)
	})

	t.Run("no ids writes nothing", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, webhook.AllEventTypes()...)
		require.NoError(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			if err := tx.Created(ctx, webhook.Lead); err != nil {
				return err
			}
			if err := tx.Updated(ctx, webhook.Ticket); err != nil {
				return err
			}
			return tx.Deleted(ctx, webhook.Customer)
		}))
		assert.Empty(t, env.rows(t))
	})

	t.Run("the no-op emitter writes nothing", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, webhook.AllEventTypes()...)
		assert.Equal(t, webhook.Nop, webhook.NewEmitter(nil), "a nil repository is the no-op emitter")
		require.NoError(t, env.emit(t, webhook.Nop, func(ctx context.Context, tx webhook.TxEmitter) error {
			if err := tx.Created(ctx, webhook.Lead, 1); err != nil {
				return err
			}
			if err := tx.Updated(ctx, webhook.Lead, 1); err != nil {
				return err
			}
			return tx.Deleted(ctx, webhook.Lead, 1)
		}))
		require.NoError(t, webhook.NopTx.Created(context.Background(), webhook.Ticket, 2))
		var count int64
		require.NoError(t, env.db.Model(&models.WebhookDelivery{}).Count(&count).Error)
		assert.Zero(t, count)
		assert.Nil(t, webhook.NewErasureListener(webhook.Nop), "no listener without webhooks")
		assert.Nil(t, webhook.NewErasureListener(nil))
	})

	t.Run("an unregistered entity is refused and writes nothing", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "company.created", "company.deleted")
		err := env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Created(ctx, "company", 3)
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not registered")
		require.Error(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Deleted(ctx, "company", 3)
		}))
		assert.Empty(t, env.rows(t))
	})

	t.Run("an entity registered only in a test catalogue", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "widget.created")
		widgets := webhook.NewEmitterFor(webhook.NewCatalogue(webhook.Lead, "widget"), env.repo)
		require.NoError(t, env.emit(t, widgets, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Created(ctx, "widget", 7)
		}))
		rows := env.rows(t)
		require.Len(t, rows, 1)
		assert.Equal(t, "widget.created", rows[0].EventType)
		assert.EqualValues(t, 7, rows[0].RecordID)
		assert.Equal(t, models.WebhookDeliveryPending, rows[0].Status)

		require.Error(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Created(ctx, "widget", 8)
		}), "the default emitter refuses widget")
		assert.Len(t, env.rows(t), 1)
	})

	t.Run("rows written through a transaction that rolls back are gone", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "ticket.updated")
		err := env.db.Transaction(func(tx *gorm.DB) error {
			if err := emitter.Bind(tx).Updated(context.Background(), webhook.Ticket, 9); err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&models.WebhookDelivery{}).Count(&count).Error; err != nil {
				return err
			}
			assert.EqualValues(t, 1, count, "visible inside the transaction")
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)
		assert.Empty(t, env.rows(t))
	})

	t.Run("the erasure listener writes lead then customer deleted events", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "lead.deleted", "customer.deleted")
		listener := webhook.NewErasureListener(emitter)
		require.NotNil(t, listener)
		require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
			return listener(tx, repository.ErasedRecords{LeadIDs: []uint{4, 4}, CustomerIDs: []uint{2}})
		}))
		rows := env.rows(t)
		require.Len(t, rows, 2)
		assert.Equal(t, "lead.deleted", rows[0].EventType)
		assert.EqualValues(t, 4, rows[0].RecordID)
		assert.Equal(t, "customer.deleted", rows[1].EventType)
		assert.EqualValues(t, 2, rows[1].RecordID)
	})

	t.Run("the enqueue line goes to utils.Logger with the agreed fields", func(t *testing.T) {
		env.reset(t)
		env.endpoint(t, true, "lead.created")
		var sink bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&sink)
		logger.SetLevel(logrus.DebugLevel)
		logger.SetFormatter(&logrus.JSONFormatter{})
		previous := utils.Logger
		utils.Logger = logger
		t.Cleanup(func() { utils.Logger = previous })

		require.NoError(t, env.emit(t, emitter, func(ctx context.Context, tx webhook.TxEmitter) error {
			return tx.Created(ctx, webhook.Lead, 1)
		}))
		lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
		require.Len(t, lines, 1, "exactly one entry: %q", sink.String())
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
		assert.Equal(t, "Webhook deliveries enqueued", entry["msg"])
		assert.Equal(t, "debug", entry["level"])
		fields := []string{}
		for key := range entry {
			switch key {
			case "time", "level", "msg":
			default:
				fields = append(fields, key)
			}
		}
		assert.ElementsMatch(t, []string{"operation", "event_type", "record_id"}, fields)
		assert.Equal(t, "webhook_enqueue", entry["operation"])
		assert.Equal(t, "lead.created", entry["event_type"])
		assert.EqualValues(t, 1, entry["record_id"])
	})
}
