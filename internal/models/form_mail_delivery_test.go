package models

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #216: a delivery record serialises its ids, kind, status, error code,
// attempt timestamps and the two computed flags, and never its recipient hash
// or deleted_at; a submission serialises deliveries as [] when none are loaded.
func TestFormMailDeliveryJSON(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	sentAt := now.Add(time.Second)
	delivery := FormMailDelivery{
		ID:            9,
		CreatedAt:     now,
		UpdatedAt:     now,
		DeletedAt:     gorm.DeletedAt{Time: now, Valid: true},
		SubmissionID:  4,
		FormID:        2,
		Kind:          FormMailKindNotification,
		Status:        FormMailStatusSent,
		RecipientHash: "recipient-hash-canary",
		Attempts:      1,
		LastAttemptAt: &now,
		SentAt:        &sentAt,
	}

	encoded, err := json.Marshal(delivery)
	require.NoError(t, err)
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{
		"attempts", "created_at", "error_code", "form_id", "id", "kind", "last_attempt_at",
		"retryable", "sent_at", "status", "stuck", "submission_id", "updated_at",
	}, keys)
	assert.NotContains(t, string(encoded), "recipient-hash-canary")
	assert.NotContains(t, string(encoded), "deleted_at")
	assert.NotContains(t, string(encoded), "recipient")
	assert.Equal(t, "notification", fields["kind"])
	assert.Equal(t, "sent", fields["status"])

	// Nullable timestamps serialise as null.
	pending := FormMailDelivery{ID: 1, Kind: FormMailKindFollowUp, Status: FormMailStatusPending}
	encoded, err = json.Marshal(pending)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &fields))
	assert.Nil(t, fields["last_attempt_at"])
	assert.Nil(t, fields["sent_at"])

	t.Run("retryable is status eligibility only", func(t *testing.T) {
		const stuckAfter = 15 * time.Minute
		clock := now.Add(time.Hour)
		cases := []struct {
			status    string
			updatedAt time.Time
			stuck     bool
			retryable bool
		}{
			{FormMailStatusFailed, clock, false, true},
			{FormMailStatusLogOnly, clock, false, true},
			{FormMailStatusSent, now, false, false},
			{FormMailStatusSkipped, now, false, false},
			{FormMailStatusPending, clock.Add(-14 * time.Minute), false, false},
			{FormMailStatusSending, clock.Add(-14 * time.Minute), false, false},
			{FormMailStatusPending, clock.Add(-16 * time.Minute), true, true},
			{FormMailStatusSending, clock.Add(-16 * time.Minute), true, true},
		}
		for _, c := range cases {
			d := FormMailDelivery{Status: c.status, UpdatedAt: c.updatedAt}
			d.Evaluate(clock, stuckAfter)
			assert.Equalf(t, c.stuck, d.Stuck, "stuck for %s at %s", c.status, c.updatedAt)
			assert.Equalf(t, c.retryable, d.Retryable, "retryable for %s at %s", c.status, c.updatedAt)

			encoded, err := json.Marshal(d)
			require.NoError(t, err)
			var flags struct {
				Stuck     bool `json:"stuck"`
				Retryable bool `json:"retryable"`
			}
			require.NoError(t, json.Unmarshal(encoded, &flags))
			assert.Equal(t, c.stuck, flags.Stuck)
			assert.Equal(t, c.retryable, flags.Retryable)
		}
	})

	t.Run("submission deliveries", func(t *testing.T) {
		for name, submission := range map[string]*FormSubmission{
			"pointer": {FormID: 2, Status: FormSubmissionReceived},
		} {
			encoded, err := json.Marshal(submission)
			require.NoError(t, err, name)
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &body))
			assert.JSONEq(t, `[]`, string(body["deliveries"]), "%s: none loaded is []", name)
		}

		// A value and a list element marshal the same way.
		list := []FormSubmission{{FormID: 2}, {FormID: 3, Deliveries: []FormMailDelivery{delivery}}}
		encoded, err := json.Marshal(list)
		require.NoError(t, err)
		var rows []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &rows))
		require.Len(t, rows, 2)
		assert.JSONEq(t, `[]`, string(rows[0]["deliveries"]))
		var loaded []map[string]interface{}
		require.NoError(t, json.Unmarshal(rows[1]["deliveries"], &loaded))
		require.Len(t, loaded, 1)
		assert.EqualValues(t, 9, loaded[0]["id"])
		assert.NotContains(t, string(rows[1]["deliveries"]), "recipient-hash-canary")
		// The submission's own fields are still there.
		assert.Contains(t, rows[1], "form_id")
		assert.Contains(t, rows[1], "data")
		assert.Contains(t, rows[1], "status")
	})
}
