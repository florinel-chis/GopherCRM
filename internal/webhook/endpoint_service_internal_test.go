package webhook

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookPingTimeoutDefault pins the bound of one test ping: a delivery's
// WebhookRequestTimeout unless WithWebhookPingTimeout gives a positive value.
func TestWebhookPingTimeoutDefault(t *testing.T) {
	impl, ok := NewWebhookService(nil, nil, nil).(*webhookService)
	require.True(t, ok)
	assert.Equal(t, WebhookRequestTimeout, impl.pingTimeout, "the ping is bounded like a delivery")

	for _, d := range []time.Duration{0, -time.Second} {
		impl, ok = NewWebhookService(nil, nil, nil, WithWebhookPingTimeout(d)).(*webhookService)
		require.True(t, ok)
		assert.Equal(t, WebhookRequestTimeout, impl.pingTimeout, "a non-positive value keeps the default")
	}

	impl, ok = NewWebhookService(nil, nil, nil, WithWebhookPingTimeout(300*time.Millisecond)).(*webhookService)
	require.True(t, ok)
	assert.Equal(t, 300*time.Millisecond, impl.pingTimeout)
}
