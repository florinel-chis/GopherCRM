package mailer

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #216: the development LogMailer says that it only logs, through the optional
// LogOnlyReporter interface; the SMTP transport does not implement it, and the
// Mailer interface itself is unchanged.
func TestLogMailerReportsLogOnly(t *testing.T) {
	var logMailer Mailer = NewLogMailer()
	reporter, ok := logMailer.(LogOnlyReporter)
	require.True(t, ok, "LogMailer implements LogOnlyReporter")
	assert.True(t, reporter.LogsOnly())

	var fallback Mailer = NewFromConfig(config.SMTPConfig{})
	reporter, ok = fallback.(LogOnlyReporter)
	require.True(t, ok, "the no-SMTP_HOST fallback is the LogMailer")
	assert.True(t, reporter.LogsOnly())

	var smtp Mailer = NewSMTPMailer(config.SMTPConfig{Host: "smtp.example.test", Port: 25, From: "crm@example.test"})
	_, ok = smtp.(LogOnlyReporter)
	assert.False(t, ok, "SMTPMailer does not implement LogOnlyReporter")

	// The Mailer interface still has exactly its two methods: a type with only
	// Send and SendPasswordReset satisfies it.
	var _ Mailer = plainMailer{}
	_, ok = interface{}(plainMailer{}).(LogOnlyReporter)
	assert.False(t, ok)
}

type plainMailer struct{}

func (plainMailer) SendPasswordReset(string, string) error { return nil }
func (plainMailer) Send(string, string, string) error      { return nil }
