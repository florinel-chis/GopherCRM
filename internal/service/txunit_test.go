package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTxUnit pins txDeps.Run: one transaction on the caller's context whose
// repositories and outlets commit or roll back together.
func TestTxUnit(t *testing.T) {
	t.Run("a nil manager panics", func(t *testing.T) {
		assert.PanicsWithValue(t, txManagerRequired, func() { newTxDeps(nil) })
	})

	// newUnitEnv opens a database with one active endpoint subscribed to
	// customer.created and returns deps bound to it with webhooks wired.
	newUnitEnv := func(t *testing.T) (*webhookEnv, txDeps) {
		t.Helper()
		env := newWebhookEnv(t, openTicketIntakeDB(t, false), nil)
		env.endpoint("https://example.com/hook", true, models.WebhookEventCustomerCreated)
		deps := newTxDeps(env.txManager)
		deps.customers = repository.NewCustomerRepository(env.db)
		deps.webhooks = webhook.NewEmitter(env.repo)
		return env, deps
	}

	t.Run("the repository write and the outbox row commit together", func(t *testing.T) {
		env, deps := newUnitEnv(t)
		customer := env.newCustomer()
		require.NoError(t, deps.Run(context.Background(), func(ctx context.Context, u txUnit) error {
			require.NotNil(t, u.Tx)
			if err := u.Customers.Create(customer); err != nil {
				return err
			}
			return u.Webhooks.Created(ctx, webhook.Customer, customer.ID)
		}))
		assert.Equal(t, int64(1), env.count(&models.Customer{}, "id = ?", customer.ID))
		assert.Len(t, env.deliveries(models.WebhookEventCustomerCreated, customer.ID), 1)
	})

	t.Run("an error from fn rolls both back and is returned unchanged", func(t *testing.T) {
		env, deps := newUnitEnv(t)
		customer := env.newCustomer()
		failure := errors.New("callback failed")
		err := deps.Run(context.Background(), func(ctx context.Context, u txUnit) error {
			if err := u.Customers.Create(customer); err != nil {
				return err
			}
			if err := u.Webhooks.Created(ctx, webhook.Customer, customer.ID); err != nil {
				return err
			}
			return failure
		})
		assert.Same(t, failure, err)
		require.NotZero(t, customer.ID)
		assert.Zero(t, env.count(&models.Customer{}, "id = ?", customer.ID))
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("without an emitter nothing is emitted and without automation the outlet is nil", func(t *testing.T) {
		env, deps := newUnitEnv(t)
		deps.webhooks = webhook.Nop
		customer := env.newCustomer()
		require.NoError(t, deps.Run(context.Background(), func(ctx context.Context, u txUnit) error {
			assert.Nil(t, u.Automation)
			if err := u.Customers.Create(customer); err != nil {
				return err
			}
			return u.Webhooks.Created(ctx, webhook.Customer, customer.ID)
		}))
		assert.Equal(t, int64(1), env.count(&models.Customer{}, "id = ?", customer.ID))
		assert.Zero(t, env.allDeliveries(), "the no-op emitter writes nothing")

		require.NoError(t, env.db.AutoMigrate(&models.AutomationEvent{}))
		deps.automation = repository.NewAutomationRepository(env.db)
		failure := errors.New("roll back")
		err := deps.Run(context.Background(), func(ctx context.Context, u txUnit) error {
			require.NotNil(t, u.Automation)
			if err := u.Automation.RecordEvent(&models.AutomationEvent{EntityType: models.AutomationEntityLead, EntityID: 1,
				EventType: models.AutomationEventLeadCreated, Origin: "test", CreatedAt: time.Now().UTC()}); err != nil {
				return err
			}
			assert.Equal(t, int64(1), countIn(t, u, &models.AutomationEvent{}), "the event is written in the unit's transaction")
			return failure
		})
		assert.Same(t, failure, err)
		assert.Zero(t, env.count(&models.AutomationEvent{}, "1 = 1"), "the automation event rolls back with the unit")
	})

	t.Run("a cancelled context fails before fn runs", func(t *testing.T) {
		_, deps := newUnitEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := deps.Run(ctx, func(context.Context, txUnit) error {
			called = true
			return nil
		})
		assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
		assert.False(t, called, "fn must not run")
	})

	t.Run("inside an enclosing transaction the unit nests", func(t *testing.T) {
		env, deps := newUnitEnv(t)
		customer := env.newCustomer()
		enclosing := errors.New("enclosing rolls back")
		err := env.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
			outer, ok := utils.GetTxFromContext(ctx)
			require.True(t, ok)
			require.NoError(t, deps.Run(ctx, func(ctx context.Context, u txUnit) error {
				if err := u.Customers.Create(customer); err != nil {
					return err
				}
				return u.Webhooks.Created(ctx, webhook.Customer, customer.ID)
			}))
			var n int64
			require.NoError(t, outer.Model(&models.Customer{}).Where("id = ?", customer.ID).Count(&n).Error)
			assert.Equal(t, int64(1), n, "the unit's write is visible to the enclosing transaction")
			return enclosing
		})
		assert.Same(t, enclosing, err)
		assert.Zero(t, env.count(&models.Customer{}, "id = ?", customer.ID), "rolling back the enclosing transaction removes the unit's write")
		assert.Zero(t, env.allDeliveries())
	})
}

func countIn(t *testing.T, u txUnit, model interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, u.Tx.Model(model).Count(&n).Error)
	return n
}
