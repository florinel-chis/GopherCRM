package mailer

import (
	"fmt"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
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
	if err := validateHeaders(to, subject); err != nil {
		// The offending value is not logged: it is the injection attempt.
		log().WithError(err).Warn("Refused to send password reset email with an invalid header")
		return fmt.Errorf("send password reset mail: %w", err)
	}
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

// Send delivers an arbitrary plaintext message. A recipient or subject that
// contains CR or LF is refused with ErrInvalidHeader before any relay is
// contacted.
func (m *SMTPMailer) Send(to, subject, body string) error {
	if err := validateHeaders(to, subject); err != nil {
		// The offending value is not logged: it is the injection attempt.
		log().WithError(err).Warn("Refused to send email with an invalid header")
		return fmt.Errorf("send mail: %w", err)
	}
	if err := m.send(to, subject, body); err != nil {
		// Subject only. The body may carry a single-use link, and the recipient
		// is a person's address: the quote email in particular promises that
		// no log line of the call carries it, so the generic failure line
		// keeps the fixed subject (every caller passes a constant) and the
		// transport error sanitised by utils.LogError. A relay that rejects a
		// recipient normally echoes the address in its reply text ("550 5.1.1
		// <who@example.com>: Recipient address rejected"), so the log keeps the
		// SMTP code, which is what a diagnosis needs, and drops the text. The
		// raw error is still returned for errors.As.
		log().WithError(utils.LogError(err)).
			WithField("subject", subject).
			Error("Failed to send email")
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// send composes a plaintext RFC 822 message and submits it to the relay. It
// deliberately does not log: only the caller knows which parts of the body are
// sensitive, so the log line belongs there. The header check is repeated here
// so that no future caller of send can bypass it.
func (m *SMTPMailer) send(to, subject, body string) error {
	if err := validateHeaders(to, subject); err != nil {
		return err
	}
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
