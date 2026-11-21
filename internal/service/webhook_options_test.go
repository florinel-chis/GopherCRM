package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
)

// The webhook options keep the contract their comments state when an argument
// is nil: the services stay on their plain paths and hold the no-op emitter.
func TestWebhookOptionsNilArguments(t *testing.T) {
	tm := utils.NewTransactionManager(nil)
	emitter := webhook.NewEmitter(nil)

	t.Run("services hold the no-op emitter by default", func(t *testing.T) {
		assert.Equal(t, webhook.Nop, NewLeadService(nil, nil, nil, nil, nil).(*leadService).webhooks)
		assert.Equal(t, webhook.Nop, NewLeadService(nil, nil, nil, nil, nil, WithLeadWebhooks(nil)).(*leadService).webhooks)
		assert.Equal(t, webhook.Nop, NewCustomerService(nil, nil, nil).(*customerService).webhooks)
		assert.Equal(t, webhook.Nop, NewTicketService(nil, nil, nil).(*ticketService).webhooks)
		assert.Equal(t, webhook.Nop, NewBulkStatusService(nil, nil, nil, nil, WithBulkWebhooks(nil)).(*bulkStatusService).webhooks)
	})

	t.Run("WithCustomerWebhooks changes nothing unless both are non-nil", func(t *testing.T) {
		for name, opt := range map[string]CustomerServiceOption{
			"nil emitter": WithCustomerWebhooks(nil, tm),
			"nil manager": WithCustomerWebhooks(emitter, nil),
		} {
			s := NewCustomerService(nil, nil, nil, opt).(*customerService)
			assert.Nil(t, s.txManager, name)
			assert.Equal(t, webhook.Nop, s.webhooks, name)
		}
		s := NewCustomerService(nil, nil, nil, WithCustomerWebhooks(emitter, tm)).(*customerService)
		assert.Same(t, tm, s.txManager)
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
}
