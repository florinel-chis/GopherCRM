// Package mailer sends transactional mail. It exposes a narrow interface so
// services never depend on a transport: production wires the SMTP
// implementation, while development and tests fall back to a logging mailer
// that records the delivery without ever writing the raw token-bearing link
// to the logs.
package mailer

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// ErrInvalidHeader is returned, through errors.Is, when a recipient or a
// subject contains a carriage return or a line feed. Either would end the
// header line and let the rest of the value inject further headers (an extra
// recipient, a forged subject) into the message, so the SMTP transport refuses
// the message before it contacts any relay.
var ErrInvalidHeader = errors.New("mail header contains a line break")

// validateHeaders refuses header values that contain CR or LF.
func validateHeaders(to, subject string) error {
	for name, value := range map[string]string{"recipient": to, "subject": subject} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s: %w", name, ErrInvalidHeader)
		}
	}
	return nil
}

// Mailer delivers transactional mail.
type Mailer interface {
	// SendPasswordReset delivers a password-reset link to the recipient.
	// resetURL contains the raw single-use token; implementations must never
	// log it — only the mail transport may see the full link.
	SendPasswordReset(to, resetURL string) error

	// Send delivers a plaintext message. Bodies may embed single-use links,
	// so implementations must never log the body — only the recipient and
	// the subject.
	Send(to, subject, body string) error
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
