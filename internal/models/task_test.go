package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskJSONKeys marshals v and decodes it back into a generic map.
func taskJSONKeys(t *testing.T, v interface{}) (map[string]interface{}, string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded, string(encoded)
}

// A task discloses three fields of its deal and nothing else: the amount,
// notes, owner, lead and customer stay out, whichever of them the preload held.
func TestTaskJSONDealSummary(t *testing.T) {
	const notes = "deal-notes-canary"
	dealID, leadID, customerID := uint(88), uint(55), uint(31)
	deal := &Deal{
		BaseModel:   BaseModel{ID: dealID},
		Title:       "Acme rollout",
		Stage:       DealStageProposal,
		AmountCents: 1250000,
		Currency:    "EUR",
		Notes:       notes,
		OwnerID:     7,
		Owner:       &User{BaseModel: BaseModel{ID: 7}, Email: "owner-canary@example.com"},
		LeadID:      &leadID,
		Lead:        &Lead{BaseModel: BaseModel{ID: leadID}, Email: "lead-canary@example.com"},
		CustomerID:  &customerID,
		Customer:    &Customer{BaseModel: BaseModel{ID: customerID}, Email: "customer-canary@example.com"},
	}

	t.Run("a loaded deal gives deal_id and exactly id, title and stage", func(t *testing.T) {
		task := Task{Title: "Linked", DealID: &dealID, Deal: deal}
		for name, value := range map[string]interface{}{"value": task, "pointer": &task} {
			decoded, body := taskJSONKeys(t, value)
			assert.EqualValues(t, dealID, decoded["deal_id"], name)
			assert.Equal(t, map[string]interface{}{
				"id":    float64(dealID),
				"title": "Acme rollout",
				"stage": string(DealStageProposal),
			}, decoded["deal"], name)
			for _, hidden := range []string{notes, "1250000", "amount_cents", "owner-canary", "lead-canary", "customer-canary"} {
				assert.NotContains(t, body, hidden, name)
			}
		}
	})

	t.Run("every element of a slice is summarised", func(t *testing.T) {
		encoded, err := json.Marshal([]Task{{Title: "Linked", DealID: &dealID, Deal: deal}})
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"deal":{"id":88,"title":"Acme rollout","stage":"proposal"}`)
		assert.NotContains(t, string(encoded), notes)
	})

	t.Run("a deal that is not loaded has no deal key", func(t *testing.T) {
		decoded, _ := taskJSONKeys(t, Task{Title: "Not loaded", DealID: &dealID})
		assert.EqualValues(t, dealID, decoded["deal_id"])
		assert.NotContains(t, decoded, "deal")
	})

	t.Run("a task without a link has neither key", func(t *testing.T) {
		decoded, _ := taskJSONKeys(t, Task{Title: "Unlinked"})
		assert.NotContains(t, decoded, "deal_id")
		assert.NotContains(t, decoded, "deal")
	})

	t.Run("the summary comes from the preloaded deal only", func(t *testing.T) {
		stale := &QuoteDealSummary{ID: 99, Title: "Stale summary", Stage: DealStageWon}
		decoded, body := taskJSONKeys(t, Task{Title: "Stale", DealID: &dealID, DealSummary: stale})
		assert.NotContains(t, decoded, "deal")
		assert.NotContains(t, body, "Stale summary")

		decoded, _ = taskJSONKeys(t, Task{Title: "Unlinked", DealSummary: stale})
		assert.NotContains(t, decoded, "deal")
	})

	t.Run("the rest of the task is unchanged", func(t *testing.T) {
		decoded, _ := taskJSONKeys(t, Task{BaseModel: BaseModel{ID: 5}, Title: "Plain", Status: TaskStatusPending, AssignedToID: 7})
		assert.EqualValues(t, 5, decoded["id"])
		assert.Equal(t, "Plain", decoded["title"])
		assert.Equal(t, string(TaskStatusPending), decoded["status"])
		assert.EqualValues(t, 7, decoded["assigned_to_id"])
		assert.Contains(t, decoded, "assigned_to")
	})
}
