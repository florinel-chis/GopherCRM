package main

// The updated webhook events (#408) in the shipped wiring. The three PUT
// paths reuse WithLeadWebhooks, WithCustomerWebhooks and WithTicketWebhooks,
// which TestWebhookProductionWiring pins; this root pins the option on the
// bulk status service the routes get, and the one on the automation engine
// (the assign_owner step).

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookUpdatedProductionWiring(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(source)

	require.Equal(t, 1, strings.Count(text, "service.NewBulkStatusService("), "one bulk status service")
	start := strings.Index(text, "service.NewBulkStatusService(")
	call := text[start:]
	end := strings.Index(call, "\n\t)\n")
	require.Positive(t, end, "the end of the NewBulkStatusService call")
	call = call[:end]

	assert.Contains(t, call, "service.WithBulkWebhooks(webhookEmitter)", "the bulk status service enqueues lead.updated and ticket.updated")
	assert.Contains(t, text, "handler.NewBulkHandler(bulkService)", "the routes get that service")

	require.Equal(t, 1, strings.Count(text, "service.NewAutomationEngine("), "one automation engine")
	start = strings.Index(text, "service.NewAutomationEngine(")
	engineCall := callText(text[start:])
	require.NotEmpty(t, engineCall, "the end of the NewAutomationEngine call")
	assert.Contains(t, engineCall, "service.WithAutomationWebhooks(webhookEmitter)", "the assign_owner step enqueues lead.updated, ticket.updated and deal.updated")

	require.Equal(t, 1, strings.Count(text, "service.NewDealService("), "one deal service")
	start = strings.Index(text, "service.NewDealService(")
	dealCall := callText(text[start:])
	require.NotEmpty(t, dealCall, "the end of the NewDealService call")
	assert.Contains(t, dealCall, "service.WithDealWebhooks(webhookEmitter)", "the deal writes enqueue deal.created, deal.updated and deal.deleted (#515)")
}

// callText returns the call that opens at the first '(' of source, up to its
// matching ')', or "" when it is not closed.
func callText(source string) string {
	depth := 0
	for i, r := range source {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return source[:i+1]
			}
		}
	}
	return ""
}
