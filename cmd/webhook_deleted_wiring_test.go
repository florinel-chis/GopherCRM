package main

// The deleted webhook events (#437) in the shipped wiring. A ticket delete
// enqueues ticket.deleted through WithTicketWebhooks; every erasure unit of a
// lead, a customer or a user enqueues lead.deleted and customer.deleted
// through the erasure listener that each of the three repositories carries.

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// constructorCall returns the text of the one call of constructor in text,
// up to its closing parenthesis on the same line.
func constructorCall(t *testing.T, text, constructor string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(text, constructor), "one %s call", constructor)
	start := strings.Index(text, constructor)
	call := text[start:]
	end := strings.Index(call, "\n")
	require.Positive(t, end, "the end of the %s call", constructor)
	return call[:end]
}

func TestWebhookDeletedProductionWiring(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(source)

	assert.Contains(t, text, "erasureListener := service.NewWebhookErasureListener(webhookRepo)",
		"the erasure listener writes through the shared webhook repository")
	for constructor, option := range map[string]string{
		"repository.NewUserRepository(models.DB":     "repository.WithUserErasureListener(erasureListener)",
		"repository.NewLeadRepository(models.DB":     "repository.WithLeadErasureListener(erasureListener)",
		"repository.NewCustomerRepository(models.DB": "repository.WithCustomerErasureListener(erasureListener)",
	} {
		assert.Contains(t, constructorCall(t, text, constructor), option, "%s carries the erasure listener", constructor)
	}
	assert.Less(t, strings.Index(text, "webhookRepo := repository.NewWebhookRepository(models.DB)"),
		strings.Index(text, "erasureListener := service.NewWebhookErasureListener(webhookRepo)"),
		"the webhook repository is built before the listener")
	assert.Contains(t, text, "service.WithTicketWebhooks(webhookRepo, txManager)", "ticket delete enqueues ticket.deleted")
}
