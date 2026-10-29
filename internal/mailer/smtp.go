package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const (
	// DefaultDialTimeout bounds establishing the TCP connection to the relay.
	DefaultDialTimeout = 10 * time.Second
	// DefaultSendTimeout bounds one whole delivery: dial, greeting, EHLO,
	// STARTTLS, AUTH, MAIL/RCPT/DATA and QUIT share this single deadline, so a
	// relay that stalls or trickles its replies cannot hold the caller longer.
	DefaultSendTimeout = 30 * time.Second
)

// errAuthNotOffered is returned when credentials are configured but the relay
// does not advertise AUTH. Submitting unauthenticated instead would silently
// change how the relay treats the message, so the delivery fails.
var errAuthNotOffered = errors.New("smtp: relay does not offer AUTH")

// SMTPMailer sends mail through a real SMTP relay using net/smtp.
//
// Every delivery is one bounded exchange: the dial is limited by
// DefaultDialTimeout and the whole conversation by DefaultSendTimeout, after
// which the connection is closed. STARTTLS is used whenever the relay
// advertises it, verifying the certificate against the configured host name;
// a failed handshake aborts the delivery. Implicit TLS (port 465) is not
// supported. PLAIN auth is used when a username is configured, and net/smtp's
// PlainAuth refuses to send the credentials over a connection that is not
// encrypted unless the relay is localhost.
type SMTPMailer struct {
	host     string
	port     int
	username string
	password string
	from     string

	dialTimeout time.Duration
	timeout     time.Duration
	// dial opens the TCP connection; tests replace it to reach a local relay
	// under a remote host name.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	// tlsConfig is the base STARTTLS configuration (nil: system roots).
	// ServerName is always the configured host.
	tlsConfig *tls.Config
}

func NewSMTPMailer(cfg config.SMTPConfig) *SMTPMailer {
	return &SMTPMailer{
		host:     cfg.Host,
		port:     cfg.Port,
		username: cfg.User,
		password: cfg.Password,
		from:     cfg.From,

		dialTimeout: DefaultDialTimeout,
		timeout:     DefaultSendTimeout,
		dial:        (&net.Dialer{}).DialContext,
	}
}

func (m *SMTPMailer) SendPasswordReset(to, resetURL string) error {
	subject := "Reset your GopherCRM password"
	if err := validateHeaders(to, subject); err != nil {
		// The offending value is not logged: it is the injection attempt.
		log().WithError(err).Warn("Refused to send password reset email with an invalid header or address")
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
		// The link is logged redacted, the transport error through
		// utils.LogError (an SMTP reply keeps its code and loses its text,
		// which may echo the address), and the recipient not at all. The raw
		// error is still returned for errors.As.
		log().WithError(utils.LogError(err)).
			WithField("reset_url", redactResetURL(resetURL)).
			Error("Failed to send password reset email")
		return fmt.Errorf("send password reset mail: %w", err)
	}
	return nil
}

// Send delivers an arbitrary plaintext message. A recipient or subject that
// contains CR or LF is refused with ErrInvalidHeader, and a recipient that is
// not a single address with ErrInvalidAddress, before any relay is contacted.
func (m *SMTPMailer) Send(to, subject, body string) error {
	if err := validateHeaders(to, subject); err != nil {
		// The offending value is not logged: it is the injection attempt.
		log().WithError(err).Warn("Refused to send email with an invalid header or address")
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

// send composes a plaintext RFC 5322 message and submits it to the relay. It
// deliberately does not log: only the caller knows which parts of the body are
// sensitive, so the log line belongs there. The header checks are repeated
// here so that no future caller of send can bypass them.
func (m *SMTPMailer) send(to, subject, body string) error {
	if err := validateHeaders(to, subject); err != nil {
		return err
	}
	if strings.ContainsAny(m.from, "\r\n") {
		return fmt.Errorf("sender: %w", ErrInvalidHeader)
	}
	sender, err := parseAddress("sender", m.from)
	if err != nil {
		return err
	}
	recipient, err := parseAddress("recipient", to)
	if err != nil {
		return err
	}
	msg := strings.Join([]string{
		"From: " + sender.String(),
		"To: " + recipient.String(),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		normalizeLineEndings(body),
	}, "\r\n")

	return m.deliver(sender.Address, recipient.Address, []byte(msg))
}

// deliver runs one bounded SMTP exchange. The connection carries a single
// deadline for the whole conversation and is closed on every path.
func (m *SMTPMailer) deliver(envelopeFrom, envelopeTo string, msg []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	dialCtx, cancelDial := context.WithTimeout(ctx, m.dialTimeout)
	conn, err := m.dial(dialCtx, "tcp", net.JoinHostPort(m.host, strconv.Itoa(m.port)))
	cancelDial()
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp connect: %w", err)
	}

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	// Close tears down the connection, including the TLS layer after
	// STARTTLS. After a successful QUIT it is a harmless second close.
	defer func() { _ = client.Close() }()

	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(m.startTLSConfig()); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if m.username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errAuthNotOffered
		}
		// PlainAuth refuses an unencrypted connection to anything but
		// localhost before a single credential byte is written.
		if err := client.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(envelopeTo); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	// The data writer dot-stuffs the body and ends it with CRLF.CRLF.
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	// The relay has accepted the message once DATA is answered; a failed
	// QUIT does not make the delivery a failure.
	_ = client.Quit()
	return nil
}

func (m *SMTPMailer) startTLSConfig() *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.tlsConfig != nil {
		cfg = m.tlsConfig.Clone()
	}
	cfg.ServerName = m.host
	return cfg
}

// parseAddress accepts exactly one RFC 5322 address, with or without a
// display name. The value is not part of the error: a malformed recipient
// may be someone's address, a malformed one an injection attempt.
func parseAddress(role, value string) (*mail.Address, error) {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
	}
	return addr, nil
}

// normalizeLineEndings converts a body to the CRLF endings mail expects:
// CRLF stays, and a bare LF or a bare CR becomes CRLF. Bodies composed from
// user-editable templates arrive with bare newlines. Dot-stuffing is left to
// the SMTP data writer.
func normalizeLineEndings(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}
