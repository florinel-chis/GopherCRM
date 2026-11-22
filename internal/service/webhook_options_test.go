package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The webhook options keep the contract their comments state when an argument
// is nil: the services stay on their plain paths and hold the no-op emitter.
func TestWebhookOptionsNilArguments(t *testing.T) {
	tm := utils.NewTransactionManager(nil)
	emitter := webhook.NewEmitter(nil)

	t.Run("services hold the no-op emitter by default", func(t *testing.T) {
		assert.Equal(t, webhook.Nop, NewLeadService(nil, nil, nil, nil, nil).(*leadService).webhooks)
		assert.Equal(t, webhook.Nop, NewLeadService(nil, nil, nil, nil, nil, WithLeadWebhooks(nil)).(*leadService).webhooks)
		assert.Equal(t, webhook.Nop, NewCustomerService(nil, nil, nil, tm).(*customerService).tx.webhooks)
		assert.Equal(t, webhook.Nop, NewTicketService(nil, nil, nil).(*ticketService).webhooks)
		assert.Equal(t, webhook.Nop, NewBulkStatusService(nil, nil, nil, nil, WithBulkWebhooks(nil)).(*bulkStatusService).webhooks)
	})

	t.Run("the customer service requires its manager and a nil emitter is Nop", func(t *testing.T) {
		s := NewCustomerService(nil, nil, nil, tm, WithCustomerWebhooks(nil)).(*customerService)
		assert.Equal(t, webhook.Nop, s.tx.webhooks)
		assert.Same(t, tm, s.tx.tm, "the manager is stored")
		s = NewCustomerService(nil, nil, nil, tm, WithCustomerWebhooks(emitter)).(*customerService)
		assert.Equal(t, emitter, s.tx.webhooks)
		assert.Same(t, tm, s.tx.tm)
		assert.PanicsWithValue(t, txManagerRequired, func() {
			NewCustomerService(nil, nil, nil, nil, WithCustomerWebhooks(emitter))
		}, "a nil manager is a wiring mistake")
	})

	t.Run("WithTicketWebhooks takes the transactional paths only with both", func(t *testing.T) {
		s := NewTicketService(nil, nil, nil, WithTicketWebhooks(nil, tm)).(*ticketService)
		assert.Same(t, tm, s.txManager, "a non-nil manager is still stored")
		assert.False(t, s.ticketWritesInTx())
		assert.Equal(t, webhook.Nop, s.webhooks)

		s = NewTicketService(nil, nil, nil, WithTicketWebhooks(emitter, nil)).(*ticketService)
		assert.False(t, s.ticketWritesInTx())

		s = NewTicketService(nil, nil, nil, WithTicketWebhooks(emitter, nil), WithTicketCommentMail(nil, tm, nil)).(*ticketService)
		assert.False(t, s.ticketWritesInTx(), "a manager from another option is not enough")

		s = NewTicketService(nil, nil, nil, WithTicketCommentMail(nil, tm, nil)).(*ticketService)
		assert.False(t, s.ticketWritesInTx(), "comment mail alone keeps the plain paths")
		assert.False(t, s.ticketEventsWired())

		s = NewTicketService(nil, nil, nil, WithTicketWebhooks(emitter, tm)).(*ticketService)
		assert.True(t, s.ticketWritesInTx())
	})

	t.Run("the deal service holds Nop by default and with a nil emitter, else the emitter", func(t *testing.T) {
		s := NewDealService(nil, nil, nil, nil, nil, nil, tm).(*dealService)
		assert.Equal(t, webhook.Nop, s.webhooks)
		assert.False(t, s.webhooksWired())
		s = NewDealService(nil, nil, nil, nil, nil, nil, tm, WithDealWebhooks(nil)).(*dealService)
		assert.Equal(t, webhook.Nop, s.webhooks)
		assert.False(t, s.webhooksWired())
		// webhook.NewEmitter(nil) is Nop itself; a repository makes a real one.
		live := webhook.NewEmitter(repository.NewWebhookRepository(nil))
		require.NotEqual(t, webhook.Nop, live)
		s = NewDealService(nil, nil, nil, nil, nil, nil, tm, WithDealWebhooks(live)).(*dealService)
		assert.Equal(t, live, s.webhooks)
		assert.True(t, s.webhooksWired())
	})

	t.Run("the task service holds Nop by default and with a nil emitter or manager, else the emitter", func(t *testing.T) {
		live := webhook.NewEmitter(repository.NewWebhookRepository(nil))
		require.NotEqual(t, webhook.Nop, live)

		s := NewTaskService(nil, nil, nil, nil, nil, nil).(*taskService)
		assert.Equal(t, webhook.Nop, s.webhooks)
		assert.False(t, s.webhooksWired())

		s = NewTaskService(nil, nil, nil, nil, nil, nil, WithTaskWebhooks(nil, tm)).(*taskService)
		assert.Equal(t, webhook.Nop, s.webhooks)
		assert.Same(t, tm, s.txManager, "a non-nil manager is still stored")
		assert.False(t, s.webhooksWired())

		s = NewTaskService(nil, nil, nil, nil, nil, nil, WithTaskWebhooks(live, nil)).(*taskService)
		assert.Equal(t, webhook.Nop, s.webhooks)
		assert.Nil(t, s.txManager)
		assert.False(t, s.webhooksWired())

		s = NewTaskService(nil, nil, nil, nil, nil, nil, WithTaskRowLock(tm), WithTaskWebhooks(live, nil)).(*taskService)
		assert.Equal(t, webhook.Nop, s.webhooks, "a manager from another option is not enough")
		assert.False(t, s.webhooksWired())

		s = NewTaskService(nil, nil, nil, nil, nil, nil, WithTaskWebhooks(live, tm)).(*taskService)
		assert.Equal(t, live, s.webhooks)
		assert.Same(t, tm, s.txManager)
		assert.True(t, s.webhooksWired())
	})
}
