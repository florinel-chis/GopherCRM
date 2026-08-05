package mailer

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
}
