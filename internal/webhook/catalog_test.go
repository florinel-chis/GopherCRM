package webhook_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var twelveTypes = []string{"customer.created", "customer.deleted", "customer.updated", "deal.created", "deal.deleted",
	"deal.updated", "lead.created", "lead.deleted", "lead.updated", "ticket.created", "ticket.deleted", "ticket.updated"}

func TestCatalogue(t *testing.T) {
	t.Run("the default registers twelve sorted event types", func(t *testing.T) {
		assert.Equal(t, twelveTypes, webhook.AllEventTypes())
		assert.True(t, sort.StringsAreSorted(webhook.AllEventTypes()))
		// Every type subscribed fits the varchar(512) column.
		all, err := models.WebhookEventTypes(webhook.AllEventTypes()).Value()
		require.NoError(t, err)
		assert.Len(t, all, 173)
		// The slice is a copy.
		types := webhook.AllEventTypes()
		types[0] = "changed"
		assert.Equal(t, twelveTypes, webhook.AllEventTypes())
	})

	t.Run("the event type check compares exactly", func(t *testing.T) {
		for _, known := range twelveTypes {
			assert.True(t, webhook.IsEventType(known), known)
		}
		for _, unknown := range []string{"Customer.Created", "lead.archived", "webhook.ping", models.WebhookEventPing, "", "lead"} {
			assert.False(t, webhook.IsEventType(unknown), unknown)
		}
		assert.NotContains(t, webhook.AllEventTypes(), models.WebhookEventPing)
	})

	t.Run("EventType joins a registered entity and an action", func(t *testing.T) {
		got, ok := webhook.EventType(webhook.Ticket, webhook.Deleted)
		assert.True(t, ok)
		assert.Equal(t, "ticket.deleted", got)
		_, ok = webhook.EventType("widget", webhook.Created)
		assert.False(t, ok)
		_, ok = webhook.EventType(webhook.Lead, "archived")
		assert.False(t, ok)
	})

	t.Run("the validation messages", func(t *testing.T) {
		assert.Equal(t, "must list 1 to 12 of lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated, ticket.deleted, deal.created, deal.updated and deal.deleted",
			webhook.SubscribeRule())
		assert.Equal(t, "must be lead.created, lead.updated, lead.deleted, customer.created, customer.updated, customer.deleted, ticket.created, ticket.updated, ticket.deleted, deal.created, deal.updated or deal.deleted",
			webhook.FilterRule())
	})

	t.Run("an extra entity extends every derived list", func(t *testing.T) {
		c := webhook.NewCatalogue(webhook.Lead, webhook.Customer, webhook.Ticket, "widget")
		types := c.AllEventTypes()
		assert.Len(t, types, 12)
		for _, widget := range []string{"widget.created", "widget.updated", "widget.deleted"} {
			assert.Contains(t, types, widget)
			assert.True(t, c.IsEventType(widget), widget)
			assert.False(t, webhook.IsEventType(widget), "the default does not register %s", widget)
		}
		assert.True(t, sort.StringsAreSorted(types))
		assert.Contains(t, c.SubscribeRule(), "must list 1 to 12 of ")
		assert.Contains(t, c.SubscribeRule(), "ticket.deleted, widget.created, widget.updated and widget.deleted")
		assert.Contains(t, c.FilterRule(), "widget.updated or widget.deleted")
	})
}

// TestCatalogueSwaggerEnums keeps the event type lists of the API description
// equal to the catalogue.
func TestCatalogueSwaggerEnums(t *testing.T) {
	raw, err := os.ReadFile("../../api/swagger.json")
	require.NoError(t, err)
	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc))

	want := webhook.AllEventTypes()
	found := map[string][]string{}
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "enum" {
					if list, ok := child.([]any); ok {
						values := make([]string, 0, len(list))
						relevant := false
						for _, item := range list {
							s, _ := item.(string)
							values = append(values, s)
							if webhook.IsEventType(s) {
								relevant = true
							}
						}
						if relevant {
							found[path] = values
						}
					}
				}
				walk(path+"/"+key, child)
			}
		case []any:
			for i, child := range v {
				walk(path+"/"+jsonIndex(i), child)
			}
		}
	}
	walk("", doc)

	assert.Len(t, found, 7, "event type enums: %v", keys(found))
	for path, values := range found {
		assert.ElementsMatch(t, want, values, path)
	}
}

func jsonIndex(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
