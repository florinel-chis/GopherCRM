package mailer

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A recipient or a subject that contains CR or LF would end its header line
// and let the remainder inject further headers (an extra recipient, a forged
// subject). Both SMTP entry points refuse such a value with ErrInvalidHeader
// before any relay is contacted: the listener below stands in for the relay
// and must see no connection at all, and a clean message afterwards proves the
// listener would have seen one.
func TestSMTPMailerRejectsHeaderInjection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	relay := listener.(*net.TCPListener)
	port := listener.Addr().(*net.TCPAddr).Port
	m := NewSMTPMailer(config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "no-reply@example.com"})

	logger, hook := test.NewNullLogger()
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })

	const injected = "attacker@example.com"
	for _, tc := range []struct {
		name, to, subject string
	}{
		{"CRLF in the recipient", "victim@example.com\r\nBcc: " + injected, "Hello"},
		{"LF in the recipient", "victim@example.com\nBcc: " + injected, "Hello"},
		{"CR in the recipient", "victim@example.com\rBcc: " + injected, "Hello"},
		{"CRLF in the subject", "victim@example.com", "Hello\r\nBcc: " + injected},
		{"LF in the subject", "victim@example.com", "Hello\nBcc: " + injected},
		{"CR in the subject", "victim@example.com", "Hello\rBcc: " + injected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.Send(tc.to, tc.subject, "body")
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidHeader), "Send: %v", err)
			assert.NotContains(t, err.Error(), injected, "the injected value is not echoed")

			// SendPasswordReset owns its subject; only the recipient is input.
			err = m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidHeader), "SendPasswordReset: %v", err)
			assert.NotContains(t, err.Error(), injected)
		})
	}
	for _, entry := range hook.AllEntries() {
		rendered, err := entry.String()
		require.NoError(t, err)
		assert.NotContains(t, rendered, injected, "the offending value is not logged either")
	}

	// No relay was contacted: an Accept with a short deadline times out. A
	// dial would already sit in the kernel's backlog by the time Send returned,
	// so Accept would hand it over at once.
	require.NoError(t, relay.SetDeadline(time.Now().Add(300*time.Millisecond)))
	conn, err := relay.Accept()
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err, "a refused message must not open a connection to the relay")
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout(), "expected the accept to time out, got %v", err)

	// Control: a clean message does reach the listener, so the silence above
	// is the refusal and not a misconfigured test.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.Send("victim@example.com", "Hello", "body")
	}()
	require.NoError(t, relay.SetDeadline(time.Now().Add(5*time.Second)))
	conn, err = relay.Accept()
	require.NoError(t, err, "a clean message must dial the relay")
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Send did not return after the relay closed the connection")
	}
}
