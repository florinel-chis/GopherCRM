package mailer

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The roots in this file drive SMTPMailer against an in-process relay on a
// 127.0.0.1 listener. Timeouts are injected per mailer, so a stalled relay
// costs a few hundred milliseconds rather than the production 30 seconds.

const (
	testTimeout = 400 * time.Millisecond
	// watchdog bounds every Send in these tests: a transport that ignores its
	// deadline fails the test instead of hanging the package.
	watchdog = 5 * time.Second
	// remoteHost is a name net/smtp does not treat as localhost, and one the
	// httptest certificate covers. Mailers that use it dial the listener
	// through the injected dial function.
	remoteHost = "example.com"
)

// relayOptions configures one fake relay session.
type relayOptions struct {
	stallGreeting bool
	stallOn       string      // verb (upper case) the relay never answers; "DATA-END" stalls after the message
	tls           *tls.Config // non-nil: advertise STARTTLS
	auth          bool        // advertise AUTH PLAIN
	rcptReply     string      // default "250 2.1.5 Ok"
	authReply     string      // default "235 2.7.0 Authentication successful"
	// stallHandshake answers STARTTLS with 220, reads the ClientHello and
	// then never answers it.
	stallHandshake bool
	// stallAfterTLS never answers the first command after the handshake.
	stallAfterTLS bool
	// freezeAfter answers this verb and then neither reads nor writes until
	// release is closed, so a client writing a large message fills the
	// socket buffers and blocks.
	freezeAfter string
	release     chan struct{}
}

// relaySession is what the fake relay observed.
type relaySession struct {
	commands       []string // command lines in order, as received
	tlsAt          map[string]bool
	data           string // the DATA payload as it was on the wire, terminator excluded
	closedByClient bool   // the client closed the connection while the relay waited
	err            error
}

func (s relaySession) sawVerb(verb string) bool {
	for _, c := range s.commands {
		if strings.HasPrefix(strings.ToUpper(c), verb) {
			return true
		}
	}
	return false
}

// startRelay accepts one connection on a fresh 127.0.0.1 listener and serves
// it according to opts. The returned channel delivers the session once the
// connection ends.
func startRelay(t *testing.T, opts relayOptions) (string, <-chan relaySession) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan relaySession, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- relaySession{err: err}
			return
		}
		done <- serveRelay(conn, opts)
	}()
	return listener.Addr().String(), done
}

func serveRelay(conn net.Conn, opts relayOptions) (s relaySession) {
	s.tlsAt = map[string]bool{}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(watchdog))

	// waitForClose blocks until the client hangs up, which is how a stalled
	// relay observes the client giving up and cleaning up its connection.
	waitForClose := func(c net.Conn) relaySession {
		buf := make([]byte, 512)
		for {
			if _, err := c.Read(buf); err != nil {
				var netErr net.Error
				s.closedByClient = !(errors.As(err, &netErr) && netErr.Timeout())
				return s
			}
		}
	}
	if opts.stallGreeting {
		return waitForClose(conn)
	}

	active := conn
	reader := bufio.NewReader(active)
	write := func(lines ...string) error {
		_, err := active.Write([]byte(strings.Join(lines, "\r\n") + "\r\n"))
		return err
	}
	if s.err = write("220 relay.test ESMTP"); s.err != nil {
		return s
	}
	encrypted := false
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			s.closedByClient = true
			return s
		}
		line := strings.TrimRight(raw, "\r\n")
		s.commands = append(s.commands, line)
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		if i := strings.Index(verb, ":"); i >= 0 {
			verb = verb[:i]
		}
		s.tlsAt[verb] = encrypted
		if verb == opts.stallOn || (encrypted && opts.stallAfterTLS) {
			return waitForClose(active)
		}
		switch verb {
		case "EHLO":
			lines := []string{"250-relay.test"}
			if opts.tls != nil && !encrypted {
				lines = append(lines, "250-STARTTLS")
			}
			if opts.auth {
				lines = append(lines, "250-AUTH PLAIN")
			}
			lines = append(lines, "250 SIZE 1048576")
			err = write(lines...)
		case "HELO":
			err = write("250 relay.test")
		case "STARTTLS":
			if err = write("220 2.0.0 Ready to start TLS"); err != nil {
				break
			}
			if opts.stallHandshake {
				hello := make([]byte, 5)
				if _, readErr := reader.Read(hello); readErr != nil || hello[0] != 0x16 {
					s.err = errors.New("expected a TLS ClientHello record")
					return s
				}
				return waitForClose(active)
			}
			tlsConn := tls.Server(active, opts.tls)
			if err = tlsConn.Handshake(); err != nil {
				s.err = err
				return s
			}
			active, reader, encrypted = tlsConn, bufio.NewReader(tlsConn), true
		case "AUTH":
			reply := opts.authReply
			if reply == "" {
				reply = "235 2.7.0 Authentication successful"
			}
			err = write(reply)
		case "MAIL":
			err = write("250 2.1.0 Ok")
		case "RCPT":
			reply := opts.rcptReply
			if reply == "" {
				reply = "250 2.1.5 Ok"
			}
			err = write(reply)
		case "DATA":
			if err = write("354 End data with <CR><LF>.<CR><LF>"); err != nil {
				break
			}
			if opts.freezeAfter == "DATA" {
				select {
				case <-opts.release:
				case <-time.After(2 * watchdog):
				}
				return s
			}
			var data strings.Builder
			for {
				chunk, readErr := reader.ReadString('\n')
				if readErr != nil {
					s.closedByClient = true
					return s
				}
				if chunk == ".\r\n" {
					break
				}
				data.WriteString(chunk)
			}
			s.data = data.String()
			if opts.stallOn == "DATA-END" {
				return waitForClose(active)
			}
			err = write("250 2.0.0 Ok: queued")
		case "RSET", "NOOP":
			err = write("250 2.0.0 Ok")
		case "QUIT":
			_ = write("221 2.0.0 Bye")
			return s
		default:
			err = write("502 5.5.2 Command not implemented")
		}
		if err != nil {
			s.err = err
			return s
		}
	}
}

// newTestMailer returns a mailer for the relay at addr with short timeouts.
// With host == remoteHost the mailer believes it talks to a remote relay
// while its dial function connects to the local listener.
func newTestMailer(t *testing.T, addr, host string, cfg config.SMTPConfig) *SMTPMailer {
	t.Helper()
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	require.NoError(t, err)
	if cfg.From == "" {
		cfg.From = "no-reply@example.com"
	}
	cfg.Host, cfg.Port = host, tcpAddr.Port
	m := NewSMTPMailer(cfg)
	m.dialTimeout, m.timeout = testTimeout, testTimeout
	if host != "127.0.0.1" {
		m.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}
	return m
}

// testCertificate returns the httptest server certificate (valid for
// 127.0.0.1 and example.com) as a server config and a client trust pool.
func testCertificate(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	serverConfig := &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12}
	srv.Close()
	return serverConfig, pool
}

// sendWithin runs fn and fails the test when it outlives the watchdog.
func sendWithin(t *testing.T, fn func() error) (error, time.Duration) {
	t.Helper()
	result := make(chan error, 1)
	start := time.Now()
	go func() { result <- fn() }()
	select {
	case err := <-result:
		return err, time.Since(start)
	case <-time.After(watchdog):
		t.Fatalf("send did not return within %s", watchdog)
		return nil, 0
	}
}

func awaitSession(t *testing.T, done <-chan relaySession) relaySession {
	t.Helper()
	select {
	case s := <-done:
		return s
	case <-time.After(watchdog):
		t.Fatal("the fake relay did not finish its session")
		return relaySession{}
	}
}

func captureLogs(t *testing.T) *test.Hook {
	t.Helper()
	logger, hook := test.NewNullLogger()
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })
	return hook
}

func renderedLogs(t *testing.T, hook *test.Hook) string {
	t.Helper()
	var all strings.Builder
	for _, entry := range hook.AllEntries() {
		rendered, err := entry.String()
		require.NoError(t, err)
		all.WriteString(rendered)
	}
	return all.String()
}

func TestSMTPDeadlines(t *testing.T) {
	assertTimedOut := func(t *testing.T, err error, elapsed time.Duration, done <-chan relaySession) {
		t.Helper()
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrDeadlineExceeded), "expected a deadline error, got %v", err)
		assert.Less(t, elapsed, testTimeout+time.Second, "the deadline bounds the exchange")
		assert.GreaterOrEqual(t, elapsed, testTimeout-50*time.Millisecond, "the relay was given the full deadline")
		s := awaitSession(t, done)
		assert.True(t, s.closedByClient, "the client closes its connection when it gives up")
	}

	t.Run("stalled greeting", func(t *testing.T) {
		t.Parallel()
		addr, done := startRelay(t, relayOptions{stallGreeting: true})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		assertTimedOut(t, err, elapsed, done)
	})

	for _, verb := range []string{"EHLO", "MAIL", "RCPT", "DATA", "DATA-END"} {
		t.Run("stalled "+verb+" after greeting", func(t *testing.T) {
			t.Parallel()
			addr, done := startRelay(t, relayOptions{stallOn: verb})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
			assertTimedOut(t, err, elapsed, done)
		})
	}

	for _, tc := range []struct {
		name string
		opts relayOptions
	}{
		{"stalled STARTTLS reply", relayOptions{stallOn: "STARTTLS"}},
		{"stalled TLS handshake", relayOptions{stallHandshake: true}},
		{"stalled after the TLS handshake", relayOptions{stallAfterTLS: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			serverTLS, pool := testCertificate(t)
			tc.opts.tls = serverTLS
			addr, done := startRelay(t, tc.opts)
			m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{})
			m.tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
			assertTimedOut(t, err, elapsed, done)
		})
	}

	t.Run("frozen relay during DATA over TLS", func(t *testing.T) {
		// The client blocks writing a large message to a relay that stopped
		// reading. Cleanup must not add the TLS close_notify grace period on
		// top of the overall deadline.
		t.Parallel()
		serverTLS, pool := testCertificate(t)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		addr, _ := startRelay(t, relayOptions{tls: serverTLS, freezeAfter: "DATA", release: release})
		m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{})
		m.tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		body := strings.Repeat(strings.Repeat("x", 70)+"\n", 40000) // ~2.8 MB, beyond the socket buffers
		err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", body) })
		require.Error(t, err)
		assert.Less(t, elapsed, testTimeout+500*time.Millisecond, "the overall deadline is a hard bound")
	})

	t.Run("one deadline covers the whole conversation", func(t *testing.T) {
		t.Parallel()
		// A relay that answers every command just inside a per-command
		// timeout must still be cut off by the overall deadline.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(watchdog))
			text := textproto.NewConn(conn)
			step := testTimeout / 3
			time.Sleep(step)
			_ = text.PrintfLine("220 slow relay")
			for {
				if _, err := text.ReadLine(); err != nil {
					return
				}
				time.Sleep(step)
				_ = text.PrintfLine("250 ok")
			}
		}()
		m := newTestMailer(t, listener.Addr().String(), "127.0.0.1", config.SMTPConfig{})
		err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrDeadlineExceeded), "expected a deadline error, got %v", err)
		assert.Less(t, elapsed, testTimeout+time.Second)
	})

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := listener.Addr().String()
		require.NoError(t, listener.Close())
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.Less(t, elapsed, testTimeout+time.Second)
	})

	t.Run("dial bounded by the dial timeout", func(t *testing.T) {
		t.Parallel()
		m := NewSMTPMailer(config.SMTPConfig{Host: remoteHost, Port: 25, From: "no-reply@example.com"})
		m.dialTimeout, m.timeout = 150*time.Millisecond, watchdog
		var dialled atomic.Bool
		m.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialled.Store(true)
			<-ctx.Done() // a black-holed SYN: only the context ends the wait
			return nil, ctx.Err()
		}
		err, elapsed := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.True(t, dialled.Load())
		assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected the dial deadline, got %v", err)
		assert.Less(t, elapsed, time.Second, "the dial timeout, not the session timeout, bounds the dial")
	})

	t.Run("production defaults", func(t *testing.T) {
		t.Parallel()
		m := NewSMTPMailer(config.SMTPConfig{Host: "smtp.example.com", Port: 587})
		assert.Equal(t, 10*time.Second, m.dialTimeout)
		assert.Equal(t, 30*time.Second, m.timeout)
		assert.Equal(t, DefaultDialTimeout, m.dialTimeout)
		assert.Equal(t, DefaultSendTimeout, m.timeout)
	})
}

func TestSMTPHeaders(t *testing.T) {
	// refusingMailer fails the test if it ever dials: every rejection below
	// must happen before a relay is contacted.
	refusingMailer := func(t *testing.T, from string) *SMTPMailer {
		m := NewSMTPMailer(config.SMTPConfig{Host: "127.0.0.1", Port: 25, From: from})
		m.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Error("a refused message must not dial the relay")
			return nil, errors.New("dial refused by test")
		}
		return m
	}
	const injected = "attacker@example.com"

	for _, tc := range []struct {
		name, from, to, subject string
	}{
		{"CRLF in the recipient", "", "victim@example.com\r\nBcc: " + injected, "Hello"},
		{"LF in the recipient", "", "victim@example.com\nBcc: " + injected, "Hello"},
		{"CR in the recipient", "", "victim@example.com\rBcc: " + injected, "Hello"},
		{"CRLF in a recipient display name", "", "Victim\r\nBcc: " + injected + " <victim@example.com>", "Hello"},
		{"CRLF in the subject", "", "victim@example.com", "Hello\r\nBcc: " + injected},
		{"LF in the subject", "", "victim@example.com", "Hello\nBcc: " + injected},
		{"CR in the subject", "", "victim@example.com", "Hello\rBcc: " + injected},
		{"CRLF in the sender", "no-reply@example.com\r\nBcc: " + injected, "victim@example.com", "Hello"},
		{"LF in the sender", "no-reply@example.com\nBcc: " + injected, "victim@example.com", "Hello"},
		{"CR in the sender", "no-reply@example.com\rBcc: " + injected, "victim@example.com", "Hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := captureLogs(t)
			from := tc.from
			if from == "" {
				from = "no-reply@example.com"
			}
			m := refusingMailer(t, from)
			err := m.Send(tc.to, tc.subject, "body")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidHeader)
			assert.NotContains(t, err.Error(), injected, "the injected value is not echoed")

			if tc.subject == "Hello" {
				err = m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidHeader)
				assert.NotContains(t, err.Error(), injected)
			}
			assert.NotContains(t, renderedLogs(t, hook), injected, "the offending value is not logged")
		})
	}

	for _, tc := range []struct {
		name, from, to string
	}{
		{"recipient without an address", "", "not-an-address"},
		{"recipient list", "", "jane@example.com, attacker@example.com"},
		{"empty recipient", "", ""},
		{"recipient with an unterminated angle address", "", "Jane <jane@example.com"},
		{"sender without an address", "GopherCRM", "jane@example.com"},
		{"sender list", "a@example.com, b@example.com", "jane@example.com"},
		// net/mail accepts quoted local parts; their unquoted Address would
		// smuggle ESMTP parameters into RCPT TO or lose its quoting.
		{"quoted local part with ESMTP parameters", "", `"x> NOTIFY=NEVER ORCPT=rfc822;a@b"@example.com`},
		{"quoted local part with a space", "", `"a b"@example.com`},
		{"quoted local part behind a display name", "", `Jane <"x>y"@example.com>`},
		{"domain literal", "", "jane@[127.0.0.1]"},
		{"non-ASCII local part", "", "jané@example.com"},
		{"leading dot", "", ".jane@example.com"},
		{"double dot", "", "ja..ne@example.com"},
		{"quoted sender local part", `"no reply"@example.com`, "jane@example.com"},
		{"sender with ESMTP parameters", `"x> SIZE=1"@example.com`, "jane@example.com"},
	} {
		t.Run("invalid address: "+tc.name, func(t *testing.T) {
			from := tc.from
			if from == "" {
				from = "no-reply@example.com"
			}
			m := refusingMailer(t, from)
			err := m.Send(tc.to, "Hello", "body")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidAddress)
			if tc.to != "" {
				assert.NotContains(t, err.Error(), tc.to, "the value is not echoed")
			}
			err = m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidAddress)
		})
	}

	for _, tc := range []struct {
		name, from, to, rcpt, mailFrom, toHeader, fromHeader string
	}{
		{"bare addresses", "no-reply@example.com", "jane@example.com",
			"RCPT TO:<jane@example.com>", "MAIL FROM:<no-reply@example.com>",
			"To: <jane@example.com>", "From: <no-reply@example.com>"},
		{"display names", "GopherCRM <no-reply@example.com>", "Jane Doe <jane@example.com>",
			"RCPT TO:<jane@example.com>", "MAIL FROM:<no-reply@example.com>",
			`To: "Jane Doe" <jane@example.com>`, `From: "GopherCRM" <no-reply@example.com>`},
		{"quoted display name with a comma", "no-reply@localhost", `"Doe, Jane" <jane@example.com>`,
			"RCPT TO:<jane@example.com>", "MAIL FROM:<no-reply@localhost>",
			`To: "Doe, Jane" <jane@example.com>`, "From: <no-reply@localhost>"},
		{"plus addressing and subdomains", "no-reply@mail.example.com", "jane+crm@sub.example.co.uk",
			"RCPT TO:<jane+crm@sub.example.co.uk>", "MAIL FROM:<no-reply@mail.example.com>",
			"To: <jane+crm@sub.example.co.uk>", "From: <no-reply@mail.example.com>"},
	} {
		t.Run("legitimate addresses: "+tc.name, func(t *testing.T) {
			addr, done := startRelay(t, relayOptions{})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{From: tc.from})
			err, _ := sendWithin(t, func() error { return m.Send(tc.to, "Hello", "body") })
			require.NoError(t, err)
			s := awaitSession(t, done)
			require.NoError(t, s.err)
			assert.Contains(t, s.commands, tc.rcpt, "the envelope carries the bare address")
			assert.Contains(t, s.commands, tc.mailFrom)
			assert.Contains(t, s.data, tc.toHeader+"\r\n")
			assert.Contains(t, s.data, tc.fromHeader+"\r\n")
		})
	}

	for _, tc := range []struct {
		name, body, want string
	}{
		{"multiline body",
			"Line one\nLine two\r\n.leading dot\n\n..two dots\rbare CR\n.\nlast",
			"Line one\r\nLine two\r\n.leading dot\r\n\r\n..two dots\r\nbare CR\r\n.\r\nlast"},
		{"non-ASCII body with a 2,000-character line",
			"Grüße, Zoë\n" + strings.Repeat("ä", 1000) + strings.Repeat("y", 1000) + "\nEnde",
			"Grüße, Zoë\r\n" + strings.Repeat("ä", 1000) + strings.Repeat("y", 1000) + "\r\nEnde"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, done := startRelay(t, relayOptions{})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", tc.body) })
			require.NoError(t, err)
			s := awaitSession(t, done)
			require.NoError(t, s.err)

			headers, wireBody, found := strings.Cut(s.data, "\r\n\r\n")
			require.True(t, found, "headers and body are separated by an empty line")
			assert.Contains(t, headers, "Subject: Hello")
			assert.Equal(t, 1, strings.Count(headers, "Subject:"))
			assert.Contains(t, headers, "Content-Type: text/plain; charset=UTF-8")
			assert.Contains(t, headers, "Content-Transfer-Encoding: quoted-printable")
			assert.NotRegexp(t, `[^\r]\n`, s.data, "no bare LF anywhere in the message")
			assert.NotRegexp(t, "\r[^\n]", s.data, "no bare CR anywhere in the message")
			for _, line := range strings.Split(s.data, "\r\n") {
				assert.LessOrEqual(t, len(line), 998, "RFC 5322 line limit")
				for _, b := range []byte(line) {
					require.Less(t, b, byte(0x80), "the wire carries 7-bit data only")
				}
			}

			// Undo the SMTP dot-stuffing, then the quoted-printable encoding.
			lines := strings.Split(strings.TrimSuffix(wireBody, "\r\n"), "\r\n")
			for i, line := range lines {
				lines[i] = strings.TrimPrefix(line, ".")
			}
			decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(strings.Join(lines, "\r\n"))))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(decoded))
		})
	}

	t.Run("long non-ASCII subject is folded", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		subject := strings.Repeat("é", 200) + " " + strings.Repeat("ab ", 17) + "abc"
		require.Equal(t, 255, utf8.RuneCountInString(subject))
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", subject, "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		headers, _, _ := strings.Cut(s.data, "\r\n\r\n")
		for _, line := range strings.Split(headers, "\r\n") {
			assert.LessOrEqual(t, len(line), 998, "RFC 5322 line limit")
			assert.LessOrEqual(t, len(line), 90, "folded close to the recommended 78")
		}
		// Unfold (CRLF followed by whitespace) and decode.
		var raw string
		for _, line := range strings.Split(headers, "\r\n") {
			switch {
			case strings.HasPrefix(line, "Subject: "):
				raw = strings.TrimPrefix(line, "Subject: ")
			case raw != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
				raw += line
			case raw != "":
				goto done
			}
		}
	done:
		decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
		require.NoError(t, err)
		assert.Equal(t, subject, decoded)
	})

	t.Run("non-ASCII subject is encoded", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Angebot für Sie", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		assert.Contains(t, s.data, "Subject: =?utf-8?q?Angebot_f=C3=BCr_Sie?=\r\n")
	})
}

func TestSMTPTLSAuth(t *testing.T) {
	const (
		user     = "relay-user"
		password = "relay-password-s3cret"
	)

	t.Run("STARTTLS used when advertised", func(t *testing.T) {
		serverTLS, pool := testCertificate(t)
		addr, done := startRelay(t, relayOptions{tls: serverTLS})
		m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{})
		m.tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		require.NoError(t, s.err)
		assert.True(t, s.sawVerb("STARTTLS"))
		assert.True(t, s.tlsAt["MAIL"], "the envelope is sent after STARTTLS")
		assert.True(t, s.tlsAt["DATA"], "the message is sent after STARTTLS")
		assert.Contains(t, s.data, "body")
	})

	t.Run("untrusted certificate fails closed", func(t *testing.T) {
		serverTLS, _ := testCertificate(t)
		addr, done := startRelay(t, relayOptions{tls: serverTLS, auth: true})
		m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{User: user, Password: password})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.NotContains(t, err.Error(), password)
		s := awaitSession(t, done)
		assert.False(t, s.sawVerb("AUTH"), "no credentials after a failed handshake")
		assert.False(t, s.sawVerb("MAIL"), "no envelope after a failed handshake")
	})

	t.Run("AUTH refused over cleartext to a remote host", func(t *testing.T) {
		hook := captureLogs(t)
		addr, done := startRelay(t, relayOptions{auth: true})
		m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{User: user, Password: password})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.NotContains(t, err.Error(), password)
		s := awaitSession(t, done)
		assert.False(t, s.sawVerb("AUTH"), "PLAIN credentials never cross a cleartext connection to a remote host")
		assert.False(t, s.sawVerb("MAIL"))
		for _, c := range s.commands {
			assert.NotContains(t, c, base64.StdEncoding.EncodeToString([]byte("\x00"+user+"\x00"+password)))
		}
		assert.NotContains(t, renderedLogs(t, hook), password)
	})

	t.Run("AUTH required but not offered", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{User: user, Password: password})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		s := awaitSession(t, done)
		assert.False(t, s.sawVerb("MAIL"), "configured credentials are not silently skipped")
	})

	t.Run("auth success over STARTTLS", func(t *testing.T) {
		serverTLS, pool := testCertificate(t)
		addr, done := startRelay(t, relayOptions{tls: serverTLS, auth: true})
		m := newTestMailer(t, addr, remoteHost, config.SMTPConfig{User: user, Password: password})
		m.tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		require.NoError(t, s.err)
		assert.True(t, s.tlsAt["AUTH"], "credentials only after STARTTLS")
		var authLine string
		for _, c := range s.commands {
			if strings.HasPrefix(c, "AUTH PLAIN ") {
				authLine = c
			}
		}
		require.NotEmpty(t, authLine)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authLine, "AUTH PLAIN "))
		require.NoError(t, err)
		assert.Equal(t, "\x00"+user+"\x00"+password, string(decoded))
		assert.True(t, s.sawVerb("QUIT"), "the session ends with QUIT")
	})

	t.Run("auth over cleartext to localhost", func(t *testing.T) {
		// net/smtp's PlainAuth rule: a local relay may take PLAIN without TLS.
		addr, done := startRelay(t, relayOptions{auth: true})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{User: user, Password: password})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		assert.True(t, s.sawVerb("AUTH PLAIN"))
		assert.True(t, s.sawVerb("DATA"))
	})

	t.Run("no credentials, no AUTH", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{auth: true})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		assert.False(t, s.sawVerb("AUTH"))
	})
}

func TestSMTPFailureRedaction(t *testing.T) {
	const (
		user       = "relay-user"
		password   = "relay-password-s3cret"
		token      = "single-use-token-value-1234567890"
		resetURL   = "http://localhost:5173/reset-password?token=" + token
		replyText  = "relay-reply-text-that-must-not-be-logged"
		recipient  = "victim@example.com"
		confirmURL = "http://localhost:8080/api/v1/forms/public/confirm?token=" + token
	)
	assertClean := func(t *testing.T, err error, logs string) {
		t.Helper()
		require.Error(t, err)
		for _, secret := range []string{password, token, confirmURL} {
			assert.NotContains(t, err.Error(), secret, "returned error")
			assert.NotContains(t, logs, secret, "logs")
		}
		assert.NotContains(t, logs, replyText, "logs keep the SMTP code, not the relay's reply text")
		assert.NotContains(t, logs, recipient, "the failure line carries no recipient")
	}

	for _, tc := range []struct {
		name string
		opts relayOptions
		code string
	}{
		{"relay rejects the recipient", relayOptions{rcptReply: "550 5.1.1 <" + recipient + ">: " + replyText}, "smtp_code=550"},
		{"relay rejects the credentials", relayOptions{auth: true, authReply: "535 5.7.8 " + replyText + " " + password}, "smtp_code=535"},
		{"relay sends a malformed reply", relayOptions{rcptReply: "550X " + recipient + " " + replyText}, "smtp_protocol_error"},
	} {
		t.Run(tc.name+" (password reset)", func(t *testing.T) {
			hook := captureLogs(t)
			addr, done := startRelay(t, tc.opts)
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{User: user, Password: password})
			if !tc.opts.auth {
				m.username, m.password = "", ""
			}
			err, _ := sendWithin(t, func() error { return m.SendPasswordReset(recipient, resetURL) })
			awaitSession(t, done)
			logs := renderedLogs(t, hook)
			// The returned error is the relay's reply for errors.As; it is
			// the caller's to sanitise. Only the logs are checked for the text.
			require.Error(t, err)
			if strings.Contains(tc.opts.authReply, password) {
				// A relay that echoes the password is beyond the mailer's
				// control in the returned error, but never in the log.
				assert.NotContains(t, logs, password)
				assert.NotContains(t, logs, replyText)
				assert.Contains(t, logs, tc.code)
				return
			}
			assertClean(t, err, logs)
			assert.Contains(t, logs, tc.code)
			assert.Contains(t, logs, "token=REDACTED", "the reset link is logged redacted")
		})
		t.Run(tc.name+" (generic send)", func(t *testing.T) {
			hook := captureLogs(t)
			addr, done := startRelay(t, tc.opts)
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{User: user, Password: password})
			if !tc.opts.auth {
				m.username, m.password = "", ""
			}
			err, _ := sendWithin(t, func() error {
				return m.Send(recipient, "Confirm your email address", "Open "+confirmURL+" to confirm.")
			})
			awaitSession(t, done)
			logs := renderedLogs(t, hook)
			require.Error(t, err)
			assert.NotContains(t, logs, password)
			assert.NotContains(t, logs, replyText)
			assert.NotContains(t, logs, token)
			assert.NotContains(t, err.Error(), token, "the body never reaches the error")
			assert.Contains(t, logs, tc.code)
		})
	}

	for _, tc := range []struct {
		name string
		opts relayOptions
		host string
	}{
		{"stalled relay", relayOptions{auth: true, stallOn: "MAIL"}, "127.0.0.1"},
		{"cleartext AUTH refused", relayOptions{auth: true}, remoteHost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := captureLogs(t)
			addr, done := startRelay(t, tc.opts)
			m := newTestMailer(t, addr, tc.host, config.SMTPConfig{User: user, Password: password})
			err, _ := sendWithin(t, func() error { return m.SendPasswordReset(recipient, resetURL) })
			awaitSession(t, done)
			assertClean(t, err, renderedLogs(t, hook))

			hook.Reset()
			addr, done = startRelay(t, tc.opts)
			m = newTestMailer(t, addr, tc.host, config.SMTPConfig{User: user, Password: password})
			err, _ = sendWithin(t, func() error {
				return m.Send(recipient, "Confirm your email address", "Open "+confirmURL+" to confirm.")
			})
			awaitSession(t, done)
			assertClean(t, err, renderedLogs(t, hook))
		})
	}

	t.Run("subject is not logged", func(t *testing.T) {
		// Automation subjects are rendered from templates such as
		// {{lead.email}}, so the subject can carry personal data.
		const personal = "lead.person@example.org"
		hook := captureLogs(t)
		addr, done := startRelay(t, relayOptions{rcptReply: "550 5.1.1 rejected"})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, _ := sendWithin(t, func() error { return m.Send(recipient, "Follow up with "+personal, "body") })
		awaitSession(t, done)
		require.Error(t, err)
		logs := renderedLogs(t, hook)
		assert.Contains(t, logs, "smtp_code=550")
		assert.NotContains(t, logs, personal)
		assert.NotContains(t, logs, "Follow up")
	})

	t.Run("connection refused", func(t *testing.T) {
		hook := captureLogs(t)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := listener.Addr().String()
		require.NoError(t, listener.Close())
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{User: user, Password: password})
		err, _ = sendWithin(t, func() error { return m.SendPasswordReset(recipient, resetURL) })
		assertClean(t, err, renderedLogs(t, hook))
	})
}
