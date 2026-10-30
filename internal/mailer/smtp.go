package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// SMTPMailer sends mail through an SMTP relay. It drives the conversation
// itself with a net/smtp Client over a connection it dials and bounds; it does
// not use smtp.SendMail, which has no timeout.
//
// Every delivery is one bounded exchange: the dial is limited by
// DefaultDialTimeout and the whole conversation, cleanup included, by
// DefaultSendTimeout, at which point the TCP connection is closed. STARTTLS is
// opportunistic: it is used whenever the relay advertises it, verifying the
// certificate against the configured host name, and a failed handshake aborts
// the delivery; a relay that does not offer it (or an attacker who strips the
// offer) receives an unauthenticated message in cleartext. Implicit TLS (port
// 465) is not supported. PLAIN auth is used when a username is configured, and net/smtp's
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
		// The transport error only, sanitised by utils.LogError. The body may
		// carry a single-use link, the recipient is a person's address, and
		// the subject can be rendered from personal data (automation subjects
		// expand {{lead.email}} and similar placeholders), so none of them is
		// logged. A relay that rejects a recipient normally echoes the address
		// in its reply text ("550 5.1.1 <who@example.com>: Recipient address
		// rejected"), so the log keeps the SMTP code, which is what a
		// diagnosis needs, and drops the text. The raw error is still returned
		// for errors.As.
		log().WithError(utils.LogError(err)).Error("Failed to send email")
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
	encodedBody, err := encodeBody(body)
	if err != nil {
		return err
	}
	msg := strings.Join([]string{
		"From: " + sender.String(),
		"To: " + recipient.String(),
		"Subject: " + encodeSubject(subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		encodedBody,
	}, "\r\n")

	envelopeFrom, err := envelopeAddress("sender", sender.Address)
	if err != nil {
		return err
	}
	envelopeTo, err := envelopeAddress("recipient", recipient.Address)
	if err != nil {
		return err
	}
	return m.deliver(envelopeFrom, envelopeTo, []byte(msg))
}

// deliver runs one bounded SMTP exchange. The connection carries a single
// deadline for the whole conversation and is closed on every path. A failure
// caused by the deadline matches os.ErrDeadlineExceeded through errors.Is,
// whichever way it surfaced (an I/O timeout, or the watchdog's close). Any
// other error — a relay reply such as a 535, a TLS or protocol failure — is
// returned as it is, even when cleanup after it ran into the deadline.
func (m *SMTPMailer) deliver(envelopeFrom, envelopeTo envelope, msg []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	err := m.converse(ctx, envelopeFrom, envelopeTo, msg)
	if err != nil && ctx.Err() != nil && causedByDeadline(err) && !errors.Is(err, os.ErrDeadlineExceeded) {
		err = fmt.Errorf("%w: %w", os.ErrDeadlineExceeded, err)
	}
	return err
}

// causedByDeadline reports whether err is the I/O failure the deadline or the
// watchdog's close produces, as opposed to a reply or protocol error.
func causedByDeadline(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (m *SMTPMailer) converse(ctx context.Context, envelopeFrom, envelopeTo envelope, msg []byte) error {

	dialCtx, cancelDial := context.WithTimeout(ctx, m.dialTimeout)
	conn, err := m.dial(dialCtx, "tcp", net.JoinHostPort(m.host, strconv.Itoa(m.port)))
	cancelDial()
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	// The raw TCP connection is closed on every path, and at the latest when
	// the overall deadline passes: the watchdog unblocks anything the
	// connection deadline does not cover, such as the TLS close_notify, which
	// sets its own five-second write deadline. Closing the raw connection
	// rather than the TLS layer on error paths skips that close_notify.
	stopWatchdog := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopWatchdog()
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}

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
	if envelopeFrom.needsUTF8 || envelopeTo.needsUTF8 {
		// net/smtp's Client.Mail adds the SMTPUTF8 parameter itself when the
		// relay advertises the extension; without it the address cannot be
		// sent at all.
		if ok, _ := client.Extension("SMTPUTF8"); !ok {
			role := "recipient"
			if envelopeFrom.needsUTF8 {
				role = "sender"
			}
			return fmt.Errorf("%s: relay does not offer SMTPUTF8: %w", role, ErrInvalidAddress)
		}
	}
	if err := client.Mail(envelopeFrom.wire); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(envelopeTo.wire); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	// The data writer dot-stuffs the body and ends it with CRLF.CRLF.
	if _, err := w.Write(msg); err != nil {
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
// display name, whose addr-spec can be put on the envelope (see
// envelopeAddress). The value is not part of the error: a malformed recipient
// may be someone's address, a malformed one an injection attempt.
func parseAddress(role, value string) (*mail.Address, error) {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
	}
	if _, err := envelopeAddress(role, addr.Address); err != nil {
		return nil, err
	}
	return addr, nil
}

// envelope is an addr-spec serialised for MAIL FROM / RCPT TO.
type envelope struct {
	wire      string
	needsUTF8 bool // non-ASCII: only deliverable when the relay offers SMTPUTF8
}

const atomSpecials = "!#$%&'*+-/=?^_`{|}~"

// envelopeAddress serialises the addr-spec net/mail returned (with any
// quoting removed) for the SMTP envelope, following RFC 5321: a dot-atom local
// part goes out as is, any other local part as a quoted string with '"' and
// '\' escaped. Refused with ErrInvalidAddress, without echoing the value:
// CR, LF and every other control character; '<' and '>' anywhere, even where
// quoting would make them legal, because they are what lets text escape the
// angle brackets of the command and inject ESMTP parameters
// ("x> NOTIFY=NEVER"@example.com); and domain literals. Non-ASCII is allowed
// (RFC 6531) but flagged, so the delivery can insist on SMTPUTF8: in the local
// part as printable letters, digits and marks, in a domain label as any
// character but a control, a format control other than ZWNJ/ZWJ, or
// whitespace (see isDomain), so that every label IDNA2008 permits, such as
// col·legi.cat (U+00B7, RFC 5892), reaches a relay that offers SMTPUTF8.
func envelopeAddress(role, addr string) (envelope, error) {
	invalid := envelope{}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return invalid, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
	}
	local, domain := addr[:at], addr[at+1:]
	needsUTF8 := false
	for _, r := range addr {
		if r < 0x20 || r == 0x7f || r == '<' || r == '>' || r == utf8.RuneError ||
			(r >= 0x80 && (unicode.IsControl(r) || unicode.IsSpace(r))) {
			return invalid, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
		}
		if r >= 0x80 {
			needsUTF8 = true
		}
	}
	for _, r := range local {
		if r >= 0x80 && !unicode.IsPrint(r) {
			return invalid, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
		}
	}
	if !isDomain(domain) {
		return invalid, fmt.Errorf("%s: %w", role, ErrInvalidAddress)
	}
	if !isDotAtom(local, atomSpecials) {
		local = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(local) + `"`
	}
	return envelope{wire: local + "@" + domain, needsUTF8: needsUTF8}, nil
}

// isDomain reports whether s is a domain the envelope can carry: non-empty
// labels separated by single dots, made of ASCII letters, digits and '-', or
// any non-ASCII character except whitespace, controls and format controls
// (ZWNJ and ZWJ, which RFC 5892 permits in context, excepted). A domain
// literal ("[127.0.0.1]") is refused: '[' is not a label character.
// Whitespace and C0/C1 controls were already refused by the caller; they are
// checked again so that the function stands on its own.
func isDomain(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			if r < 0x80 {
				alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
				if !alnum && r != '-' {
					return false
				}
				continue
			}
			if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) ||
				(unicode.Is(unicode.Cf, r) && r != 0x200C && r != 0x200D) {
				return false
			}
		}
	}
	return true
}

// isDotAtom reports whether s is a dot-atom: non-empty labels separated by
// single dots, made of ASCII letters and digits, the given specials, or
// non-ASCII letters, digits and marks (RFC 6531 UTF8-non-ascii).
func isDotAtom(s, specials string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			unicodeWord := r >= 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
			if !alnum && !unicodeWord && !strings.ContainsRune(specials, r) {
				return false
			}
		}
	}
	return true
}

// encodeSubject returns the Subject value: unchanged when it is plain ASCII,
// otherwise as RFC 2047 encoded words folded onto continuation lines.
// mime.QEncoding keeps each encoded word within 75 characters but separates
// the words with a single space, so a long non-ASCII subject would otherwise
// be one line far beyond the 998-byte limit; whitespace between encoded words
// is not part of the decoded text, so folding at those spaces is lossless.
func encodeSubject(subject string) string {
	if mime.QEncoding.Encode("utf-8", subject) == subject {
		return subject
	}
	const (
		prefix   = "=?utf-8?q?"
		suffix   = "?="
		maxLine  = 76 // RFC 2047 section 2: lines with encoded words
		firstPad = len("Subject: ")
		contPad  = len(" ")
	)
	var lines []string
	var word strings.Builder
	limit := maxLine - firstPad - len(prefix) - len(suffix)
	for _, r := range subject {
		chunk := qEncodeRune(r)
		if word.Len() > 0 && word.Len()+len(chunk) > limit {
			lines = append(lines, prefix+word.String()+suffix)
			word.Reset()
			limit = maxLine - contPad - len(prefix) - len(suffix)
		}
		word.WriteString(chunk)
	}
	lines = append(lines, prefix+word.String()+suffix)
	return strings.Join(lines, "\r\n ")
}

// qEncodeRune returns the RFC 2047 Q encoding of one rune's UTF-8 bytes, so
// an encoded word never splits a character.
func qEncodeRune(r rune) string {
	var buf [utf8.UTFMax]byte
	var b strings.Builder
	for _, c := range buf[:utf8.EncodeRune(buf[:], r)] {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("!*+-/", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}

// encodeBody normalises the line endings and encodes the body as
// quoted-printable, which keeps the wire 7-bit and wraps long lines with soft
// breaks. Dot-stuffing is left to the SMTP data writer.
func encodeBody(body string) (string, error) {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(normalizeLineEndings(body))); err != nil {
		return "", fmt.Errorf("encode body: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("encode body: %w", err)
	}
	return b.String(), nil
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
