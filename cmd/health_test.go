package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/service"
)

// The deploy pipeline decides a release is live when /health reports the
// commit it built, so the payload must carry the revision set at build time.
func TestHealthPayloadReportsTheBuildRevision(t *testing.T) {
	saved := revision
	t.Cleanup(func() { revision = saved })

	revision = "0123abc"
	payload := healthPayload(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), service.WebhookSenderStatus{})

	assert.Equal(t, "healthy", payload["status"])
	assert.Equal(t, "0123abc", payload["revision"])
	assert.Equal(t, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), payload["time"])
	assertWebhookSender(t, payload, `{"state":"running"}`)

	// A paused webhook sender (#559) keeps the top-level status healthy, so
	// no deploy check fails on it.
	paused := healthPayload(time.Now(), service.WebhookSenderStatus{Paused: true})
	assert.Equal(t, "healthy", paused["status"])
	assert.Equal(t, "0123abc", paused["revision"])
	assertWebhookSender(t, paused, `{"state":"paused","reason":"secret_unavailable"}`)
}

// assertWebhookSender checks the nested webhooks object, keyed state so that
// no "status":"healthy" match can be confused by it.
func assertWebhookSender(t *testing.T, payload map[string]interface{}, want string) {
	t.Helper()
	sender, err := json.Marshal(payload["webhooks"])
	require.NoError(t, err)
	assert.JSONEq(t, want, string(sender))
	assert.NotContains(t, string(sender), `"status"`)
}

func TestHealthPayloadDefaultsToDev(t *testing.T) {
	assert.Equal(t, "dev", healthPayload(time.Now(), service.WebhookSenderStatus{})["revision"], "an unreleased build says so")
}
