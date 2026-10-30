package mailer

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSMTPInternationalisedDomains covers #180: a domain label may hold any
// non-ASCII character the CRM's validator accepts (U+00B7 in col·legi.cat is
// allowed by RFC 5892), delivered only when the relay advertises SMTPUTF8,
// while domain literals, controls, whitespace and angle brackets stay refused
// whatever the relay offers.
func TestSMTPInternationalisedDomains(t *testing.T) {
	deliverable := []struct {
		name, to, rcpt, toHeader string
	}{
		{"middle dot", "info@col·legi.cat",
			"RCPT TO:<info@col·legi.cat>", "To: <info@col·legi.cat>"},
		{"accented letters and a middle dot in one label", "secretaria@intel·ligència.cat",
			"RCPT TO:<secretaria@intel·ligència.cat>", "To: <secretaria@intel·ligència.cat>"},
		{"display name", "Col·legi <info@col·legi.cat>",
			"RCPT TO:<info@col·legi.cat>", "To: =?utf-8?q?Col=C2=B7legi?= <info@col·legi.cat>"},
	}

	for _, tc := range deliverable {
		t.Run("with SMTPUTF8: "+tc.name, func(t *testing.T) {
			addr, done := startRelay(t, relayOptions{smtputf8: true})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, _ := sendWithin(t, func() error { return m.Send(tc.to, "Hello", "body") })
			require.NoError(t, err)
			s := awaitSession(t, done)
			require.NoError(t, s.err)
			assert.Contains(t, s.commands, "MAIL FROM:<no-reply@example.com> SMTPUTF8", "the SMTPUTF8 parameter is sent")
			assert.Contains(t, s.commands, tc.rcpt, "the envelope carries the exact address")
			assert.Contains(t, s.data, tc.toHeader+"\r\n")
		})

		t.Run("with SMTPUTF8, password reset: "+tc.name, func(t *testing.T) {
			addr, done := startRelay(t, relayOptions{smtputf8: true})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, _ := sendWithin(t, func() error {
				return m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
			})
			require.NoError(t, err)
			s := awaitSession(t, done)
			require.NoError(t, s.err)
			assert.Contains(t, s.commands, tc.rcpt)
		})

		t.Run("without SMTPUTF8: "+tc.name, func(t *testing.T) {
			hook := captureLogs(t)
			addr, done := startRelay(t, relayOptions{})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, _ := sendWithin(t, func() error { return m.Send(tc.to, "Hello", "body") })
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidAddress)
			s := awaitSession(t, done)
			assert.False(t, s.sawVerb("MAIL"), "refused before MAIL FROM")
			assert.False(t, s.sawVerb("RCPT"))
			assert.False(t, s.sawVerb("DATA"))

			addr, done = startRelay(t, relayOptions{})
			m = newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			resetErr, _ := sendWithin(t, func() error {
				return m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
			})
			require.Error(t, resetErr)
			assert.ErrorIs(t, resetErr, ErrInvalidAddress)
			s = awaitSession(t, done)
			assert.False(t, s.sawVerb("MAIL"), "refused before MAIL FROM")

			logs := renderedLogs(t, hook)
			for _, fragment := range []string{"col·legi", "intel·ligència", "info@", "secretaria@"} {
				assert.NotContains(t, err.Error(), fragment, "the address is not echoed in the error")
				assert.NotContains(t, resetErr.Error(), fragment, "the address is not echoed in the error")
				assert.NotContains(t, logs, fragment, "the address is not logged")
			}
		})
	}

	t.Run("internationalised sender domain with SMTPUTF8", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{smtputf8: true})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{From: "avisos@col·legi.cat"})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.NoError(t, err)
		s := awaitSession(t, done)
		require.NoError(t, s.err)
		assert.Contains(t, s.commands, "MAIL FROM:<avisos@col·legi.cat> SMTPUTF8")
	})

	t.Run("internationalised sender domain without SMTPUTF8", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{From: "avisos@col·legi.cat"})
		err, _ := sendWithin(t, func() error { return m.Send("jane@example.com", "Hello", "body") })
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidAddress)
		assert.NotContains(t, err.Error(), "col·legi")
		s := awaitSession(t, done)
		assert.False(t, s.sawVerb("MAIL"), "refused before MAIL FROM")
	})

	// in builds a col·legi.cat domain with r inside its first label; invisible
	// characters are kept out of the source this way.
	in := func(r rune) string { return "col·le" + string(r) + "gi.cat" }
	const (
		c1Control     = 0x0085
		bidiOverride  = 0x202E
		byteOrderMark = 0xFEFF
		noBreakSpace  = 0x00A0
		ideoSpace     = 0x3000
		lineSeparator = 0x2028
		zeroWidthJoin = 0x200D
	)

	// Refused in both cases, before any relay is dialled. Several of these
	// (C1 controls, non-ASCII whitespace, format controls) pass net/mail and
	// are caught only by the envelope check.
	refused := []struct {
		name, to string
		want     error
	}{
		{"IPv4 domain literal", "info@[127.0.0.1]", ErrInvalidAddress},
		{"IPv6 domain literal", "info@[IPv6:::1]", ErrInvalidAddress},
		{"domain literal with an internationalised local part", "infó@[127.0.0.1]", ErrInvalidAddress},
		{"C0 control in the domain", "info@" + in(0x01), ErrInvalidAddress},
		{"DEL in the domain", "info@" + in(0x7F), ErrInvalidAddress},
		{"C1 control in the domain", "info@" + in(c1Control), ErrInvalidAddress},
		{"bidi override in the domain", "info@" + in(bidiOverride), ErrInvalidAddress},
		{"byte order mark in the domain", "info@" + in(byteOrderMark), ErrInvalidAddress},
		{"space in the domain", "info@" + in(' '), ErrInvalidAddress},
		{"tab in the domain", "info@" + in('\t'), ErrInvalidAddress},
		{"no-break space in the domain", "info@" + in(noBreakSpace), ErrInvalidAddress},
		{"ideographic space in the domain", "info@" + in(ideoSpace), ErrInvalidAddress},
		{"line separator in the domain", "info@" + in(lineSeparator), ErrInvalidAddress},
		{"opening angle bracket in the domain", "info@" + in('<'), ErrInvalidAddress},
		{"closing angle bracket in the domain", "info@" + in('>'), ErrInvalidAddress},
		{"angle bracket behind a display name", "Col·legi <info@" + in('>') + ">", ErrInvalidAddress},
		{"empty label", "info@col·legi..cat", ErrInvalidAddress},
		{"CRLF after the domain", "info@col·legi.cat\r\nBcc: x@example.com", ErrInvalidHeader},
		{"LF in the domain", "info@" + in('\n'), ErrInvalidHeader},
		{"CR in the domain", "info@" + in('\r'), ErrInvalidHeader},
	}
	for _, smtputf8 := range []bool{true, false} {
		label := "without SMTPUTF8"
		if smtputf8 {
			label = "with SMTPUTF8"
		}
		for _, tc := range refused {
			t.Run(label+": refused "+tc.name, func(t *testing.T) {
				hook := captureLogs(t)
				addr, _ := startRelay(t, relayOptions{smtputf8: smtputf8})
				m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
				var dials atomic.Int32
				dial := m.dial
				m.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
					dials.Add(1)
					return dial(ctx, network, address)
				}

				err, _ := sendWithin(t, func() error { return m.Send(tc.to, "Hello", "body") })
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.want)
				resetErr, _ := sendWithin(t, func() error {
					return m.SendPasswordReset(tc.to, "http://localhost:5173/reset-password?token=abc")
				})
				require.Error(t, resetErr)
				assert.ErrorIs(t, resetErr, tc.want)
				assert.Zero(t, dials.Load(), "refused before the relay is dialled")

				logs := renderedLogs(t, hook)
				for _, e := range []error{err, resetErr} {
					assert.NotContains(t, e.Error(), "col·le", "the address is not echoed")
					assert.NotContains(t, e.Error(), "info@")
				}
				assert.NotContains(t, logs, "col·le", "the address is not logged")
				assert.NotContains(t, logs, "info@")
			})
		}
	}

	t.Run("envelope check refuses what net/mail lets through", func(t *testing.T) {
		for _, domain := range []string{
			"[127.0.0.1]", in(c1Control), in(noBreakSpace), in(bidiOverride), in(byteOrderMark),
			in('<'), in('>'), in(ideoSpace), in(lineSeparator), in('\r'), in('\n'), in(' '),
		} {
			_, err := envelopeAddress("recipient", "info@"+domain)
			assert.ErrorIs(t, err, ErrInvalidAddress, "%q", domain)
		}
		for _, domain := range []string{
			"col·legi.cat", "intel·ligència.cat", "xn--colegi-0ua.cat", in(zeroWidthJoin), "例え.テスト",
		} {
			env, err := envelopeAddress("recipient", "info@"+domain)
			require.NoError(t, err, "%q", domain)
			assert.Equal(t, "info@"+domain, env.wire)
			assert.Equal(t, strings.ContainsFunc(domain, func(r rune) bool { return r >= 0x80 }), env.needsUTF8)
		}
	})
}
