package main

// The updated webhook events (#408) in the shipped wiring. The three PUT
// paths reuse WithLeadWebhooks, WithCustomerWebhooks and WithTicketWebhooks,
// which TestWebhookProductionWiring pins; this root pins the one new option,
// on the bulk status service the routes get.

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

	assert.Contains(t, call, "service.WithBulkWebhooks(webhookRepo)", "the bulk status service enqueues lead.updated and ticket.updated")
	assert.Contains(t, text, "handler.NewBulkHandler(bulkService)", "the routes get that service")
}
