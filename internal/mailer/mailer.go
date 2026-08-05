// Package mailer sends transactional mail. It exposes a narrow interface so
// services never depend on a transport: production wires the SMTP
// implementation, while development and tests fall back to a logging mailer
// that records the delivery without ever writing the raw token-bearing link
// to the logs.
package mailer

import (
	"net/url"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Mailer delivers account-security mail.
type Mailer interface {
	// SendPasswordReset delivers a password-reset link to the recipient.
	// resetURL contains the raw single-use token; implementations must never
	// log it — only the mail transport may see the full link.
	SendPasswordReset(to, resetURL string) error
}

// NewFromConfig picks the implementation from configuration: SMTP when
// SMTP_HOST is set, otherwise the logging fallback.
func NewFromConfig(cfg config.SMTPConfig) Mailer {
	if cfg.Host != "" {
		return NewSMTPMailer(cfg)
	}
	return NewLogMailer()
}

// log returns the application logger, falling back to the logrus standard
// logger when utils.InitLogger has not run (e.g. in isolated tests), so the
// mailer never depends on initialization order.
func log() *logrus.Logger {
	if utils.Logger != nil {
		return utils.Logger
	}
	return logrus.StandardLogger()
}

// redactResetURL strips the token value from a reset link so the remainder is
// safe to log. On any parse failure it returns a fixed placeholder rather than
// risking the raw value.
func redactResetURL(resetURL string) string {
	u, err := url.Parse(resetURL)
	if err != nil {
		return "[unparseable reset URL redacted]"
	}
	q := u.Query()
	if q.Has("token") {
		q.Set("token", "REDACTED")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
