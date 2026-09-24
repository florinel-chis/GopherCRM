package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The deploy pipeline decides a release is live when /health reports the
// commit it built, so the payload must carry the revision set at build time.
func TestHealthPayloadReportsTheBuildRevision(t *testing.T) {
	saved := revision
	t.Cleanup(func() { revision = saved })

	revision = "0123abc"
	payload := healthPayload(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))

	assert.Equal(t, "healthy", payload["status"])
	assert.Equal(t, "0123abc", payload["revision"])
	assert.Equal(t, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), payload["time"])
}

func TestHealthPayloadDefaultsToDev(t *testing.T) {
	assert.Equal(t, "dev", healthPayload(time.Now())["revision"], "an unreleased build says so")
}
