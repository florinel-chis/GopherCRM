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

	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := m.host + ":" + strconv.Itoa(m.port)
	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}

	if err := smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg)); err != nil {
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
