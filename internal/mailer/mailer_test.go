package mailer

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both implementations must satisfy the interface; a missing method here is a
// compile error rather than a runtime surprise at wiring time.
var (
	_ Mailer = (*SMTPMailer)(nil)
	_ Mailer = (*LogMailer)(nil)
)

func TestRedactResetURL_StripsToken(t *testing.T) {
	raw := "http://localhost:5173/reset-password?token=super-secret-raw-token"
	redacted := redactResetURL(raw)

	assert.NotContains(t, redacted, "super-secret-raw-token")
	assert.Contains(t, redacted, "token=REDACTED")
	assert.Contains(t, redacted, "/reset-password")
}

func TestRedactResetURL_UnparseableInputYieldsPlaceholder(t *testing.T) {
	redacted := redactResetURL("http://exa mple.com/%zz?token=leaky")
	assert.NotContains(t, redacted, "leaky")
}

func TestNewFromConfig_SelectsImplementation(t *testing.T) {
	m := NewFromConfig(config.SMTPConfig{Host: ""})
	_, isLog := m.(*LogMailer)
	assert.True(t, isLog, "empty SMTP_HOST must select the logging fallback")

	m = NewFromConfig(config.SMTPConfig{Host: "smtp.example.com", Port: 587})
	_, isSMTP := m.(*SMTPMailer)
	assert.True(t, isSMTP, "a configured SMTP_HOST must select the SMTP transport")
}

func TestLogMailer_NeverFails(t *testing.T) {
	require.NoError(t, NewLogMailer().SendPasswordReset(
		"user@example.com", "http://localhost:5173/reset-password?token=abc"))
	require.NoError(t, NewLogMailer().Send(
		"user@example.com", "Confirm your email address",
		"Open http://localhost:8080/api/v1/forms/public/confirm?token=abc to confirm."))
}

func TestLogMailer_SendNeverLogsTheBody(t *testing.T) {
	logger, hook := test.NewNullLogger()
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })

	require.NoError(t, NewLogMailer().Send(
		"user@example.com", "Confirm your email address",
		"Open http://localhost:8080/api/v1/forms/public/confirm?token=super-secret-raw-token to confirm."))

	entry := hook.LastEntry()
	require.NotNil(t, entry)
	assert.Equal(t, "user@example.com", entry.Data["to"])
	assert.Equal(t, "Confirm your email address", entry.Data["subject"])

	rendered, err := entry.String()
	require.NoError(t, err)
	assert.NotContains(t, rendered, "super-secret-raw-token",
		"message bodies carry single-use links and must never reach the logs")
}

func TestSMTPMailer_SendReportsTransportFailure(t *testing.T) {
	// Port 0 is never connectable, so this exercises the error path without a
	// relay. The recipient must not leak the body into the returned error.
	m := NewSMTPMailer(config.SMTPConfig{Host: "127.0.0.1", Port: 0, From: "no-reply@example.com"})

	err := m.Send("user@example.com", "Confirm your email address", "token-bearing body")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "token-bearing body")
}
