package mailer

import (
	"fmt"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// SMTPMailer sends mail through a real SMTP relay using net/smtp.
// PLAIN auth is used when a username is configured; otherwise the message is
// submitted unauthenticated (e.g. a local relay).
type SMTPMailer struct {
	host     string
	port     int
	username string
	password string
	from     string
}

func NewSMTPMailer(cfg config.SMTPConfig) *SMTPMailer {
	return &SMTPMailer{
		host:     cfg.Host,
		port:     cfg.Port,
		username: cfg.User,
		password: cfg.Password,
		from:     cfg.From,
	}
}

func (m *SMTPMailer) SendPasswordReset(to, resetURL string) error {
	subject := "Reset your GopherCRM password"
	body := strings.Join([]string{
		"A password reset was requested for your GopherCRM account.",
		"",
		"Open the link below to choose a new password. The link is valid for one hour and can be used once:",
		"",
		resetURL,
		"",
		"If you did not request this, you can ignore this message; your password is unchanged.",
	}, "\r\n")

	if err := m.send(to, subject, body); err != nil {
		// Log the failure with the redacted link only — the raw token must not
		// reach the logs even on the error path.
		log().WithError(err).
			WithField("to", to).
			WithField("reset_url", redactResetURL(resetURL)).
			Error("Failed to send password reset email")
		return fmt.Errorf("send password reset mail: %w", err)
	}
	return nil
}

// Send delivers an arbitrary plaintext message.
func (m *SMTPMailer) Send(to, subject, body string) error {
	if err := m.send(to, subject, body); err != nil {
		// Recipient and subject only: the body may carry a single-use link.
		log().WithError(err).
			WithField("to", to).
			WithField("subject", subject).
			Error("Failed to send email")
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// send composes a plaintext RFC 822 message and submits it to the relay. It
// deliberately does not log: only the caller knows which parts of the body are
// sensitive, so the log line belongs there.
func (m *SMTPMailer) send(to, subject, body string) error {
	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		normalizeLineEndings(body),
	}, "\r\n")

	addr := m.host + ":" + strconv.Itoa(m.port)
	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}

	return smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg))
}

// normalizeLineEndings converts a body to the CRLF endings mail expects,
// leaving an already-CRLF body untouched. Bodies composed from user-editable
// templates arrive with bare newlines.
func normalizeLineEndings(body string) string {
	return strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
}
