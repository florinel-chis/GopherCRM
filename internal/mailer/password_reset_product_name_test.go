package mailer

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deliveredResetMail is the decoded subject and body of one reset mail as the
// relay received it.
type deliveredResetMail struct {
	rawSubject string
	subject    string
	body       string
}

// decodeDeliveredMail unfolds and decodes the Subject header and undoes the
// SMTP dot-stuffing and the quoted-printable body encoding.
func decodeDeliveredMail(t *testing.T, data string) deliveredResetMail {
	t.Helper()
	headers, wireBody, found := strings.Cut(data, "\r\n\r\n")
	require.True(t, found, "headers and body are separated by an empty line")
	require.Equal(t, 1, strings.Count(headers, "Subject:"), "exactly one Subject header")

	var raw string
	inSubject := false
	for _, line := range strings.Split(headers, "\r\n") {
		switch {
		case strings.HasPrefix(line, "Subject: "):
			raw, inSubject = strings.TrimPrefix(line, "Subject: "), true
		case inSubject && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			raw += line
		default:
			inSubject = false
		}
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(raw)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSuffix(wireBody, "\r\n"), "\r\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, ".")
	}
	body, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(strings.Join(lines, "\r\n"))))
	require.NoError(t, err)
	return deliveredResetMail{rawSubject: raw, subject: subject, body: string(body)}
}

// TestPasswordResetProductName (#292, AC-5) drives SendPasswordReset against an
// in-process relay on 127.0.0.1: the product name reaches the subject and the
// first body line, a blank or header-breaking name falls back to GopherCRM
// and the mail is still sent, a non-ASCII name is Q-encoded, and the reset
// link stays in the body unchanged.
func TestPasswordResetProductName(t *testing.T) {
	const resetURL = "http://localhost:5173/reset-password?token=abc123"

	for _, tc := range []struct {
		name        string
		productName string
		wantName    string
		wantEncoded bool
	}{
		{"stored name", "Acme Ltd", "Acme Ltd", false},
		{"surrounding whitespace is trimmed", "  Acme Ltd \t", "Acme Ltd", false},
		{"empty name", "", "GopherCRM", false},
		{"whitespace-only name", " \t ", "GopherCRM", false},
		{"name with CR", "Acme\rBcc: attacker@example.com", "GopherCRM", false},
		{"name with LF", "Acme\nBcc: attacker@example.com", "GopherCRM", false},
		{"name with CRLF", "Acme\r\nBcc: attacker@example.com", "GopherCRM", false},
		{"name with another control character", "Acme\x07Ltd", "GopherCRM", false},
		{"non-ASCII name", "Café Ünited", "Café Ünited", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, done := startRelay(t, relayOptions{})
			m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
			err, _ := sendWithin(t, func() error {
				return m.SendPasswordReset("jane@example.com", resetURL, tc.productName)
			})
			require.NoError(t, err, "the mail is sent whatever the product name")
			s := awaitSession(t, done)
			require.NoError(t, s.err)
			require.True(t, s.sawVerb("DATA"), "the relay received the message")
			assert.NotContains(t, s.data, "attacker@example.com", "nothing of a refused name reaches the message")

			mail := decodeDeliveredMail(t, s.data)
			assert.Equal(t, "Reset your "+tc.wantName+" password", mail.subject)
			if tc.wantEncoded {
				assert.True(t, strings.HasPrefix(mail.rawSubject, "=?utf-8?q?"),
					"a non-ASCII subject is Q-encoded, got %q", mail.rawSubject)
				for _, b := range []byte(mail.rawSubject) {
					require.Less(t, b, byte(0x80), "the encoded subject is 7-bit")
				}
			} else {
				assert.Equal(t, mail.subject, mail.rawSubject, "an ASCII subject is sent as is")
			}

			bodyLines := strings.Split(mail.body, "\r\n")
			require.NotEmpty(t, bodyLines)
			assert.Equal(t, "A password reset was requested for your "+tc.wantName+" account.", bodyLines[0])
			assert.Contains(t, bodyLines, resetURL, "the reset link stays in the body unchanged")
		})
	}
}
