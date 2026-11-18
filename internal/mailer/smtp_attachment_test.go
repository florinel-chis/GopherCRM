package mailer

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both mailers that ship can carry one attachment; the Mailer interface
// itself does not ask for it.
var (
	_ AttachmentSender = (*SMTPMailer)(nil)
	_ AttachmentSender = (*LogMailer)(nil)
)

// TestSMTPAttachment drives SendWithAttachment against the in-process relay
// and reads the message the relay received.
func TestSMTPAttachment(t *testing.T) {
	const (
		to      = "jane@example.com"
		subject = "Quote Q-000042 from GopherCRM"
		body    = "Hello,\r\n\r\nQuote Q-000042 is attached. Total: 1.234,50 €\r\n"
	)
	// Longer than one base64 line, with bytes outside ASCII and a line that
	// would look like a boundary if it were not encoded.
	data := append([]byte("%PDF-1.7\n--not-a-boundary\n"), bytes.Repeat([]byte{0x00, 0xff, 0x10, 0x80, '\n'}, 200)...)
	data = append(data, []byte("\n%%EOF\n")...)

	t.Run("multipart message with the text part and the base64 file", func(t *testing.T) {
		addr, done := startRelay(t, relayOptions{})
		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})

		err, _ := sendWithin(t, func() error {
			return m.SendWithAttachment(to, subject, body, Attachment{
				Filename: "Quote-Q-000042.pdf", ContentType: "application/pdf", Data: data,
			})
		})
		require.NoError(t, err)
		session := awaitSession(t, done)
		require.NoError(t, session.err)

		msg, err := mail.ReadMessage(strings.NewReader(session.data))
		require.NoError(t, err)
		assert.Equal(t, "1.0", msg.Header.Get("MIME-Version"))
		assert.Equal(t, subject, msg.Header.Get("Subject"))
		assert.Equal(t, "<"+to+">", msg.Header.Get("To"))

		mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		require.NoError(t, err)
		require.Equal(t, "multipart/mixed", mediaType)
		require.NotEmpty(t, params["boundary"])

		reader := multipart.NewReader(msg.Body, params["boundary"])

		// multipart.Reader hides Content-Transfer-Encoding of a
		// quoted-printable part and decodes it; NextRawPart leaves both alone.
		text, err := reader.NextRawPart()
		require.NoError(t, err)
		textType, textParams, err := mime.ParseMediaType(text.Header.Get("Content-Type"))
		require.NoError(t, err)
		assert.Equal(t, "text/plain", textType)
		assert.Equal(t, "UTF-8", textParams["charset"])
		assert.Equal(t, "quoted-printable", text.Header.Get("Content-Transfer-Encoding"))
		decodedBody, err := io.ReadAll(quotedprintable.NewReader(text))
		require.NoError(t, err)
		assert.Equal(t, strings.TrimRight(body, "\r\n"), strings.TrimRight(string(decodedBody), "\r\n"),
			"the text part decodes to the body")

		file, err := reader.NextRawPart()
		require.NoError(t, err)
		fileType, fileParams, err := mime.ParseMediaType(file.Header.Get("Content-Type"))
		require.NoError(t, err)
		assert.Equal(t, "application/pdf", fileType)
		assert.Equal(t, "Quote-Q-000042.pdf", fileParams["name"])
		assert.Equal(t, "base64", file.Header.Get("Content-Transfer-Encoding"))
		disposition, dispositionParams, err := mime.ParseMediaType(file.Header.Get("Content-Disposition"))
		require.NoError(t, err)
		assert.Equal(t, "attachment", disposition)
		assert.Equal(t, "Quote-Q-000042.pdf", dispositionParams["filename"])

		encoded, err := io.ReadAll(file)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimRight(string(encoded), "\r\n"), "\r\n")
		require.Greater(t, len(lines), 2, "the file spans several lines")
		for i, line := range lines {
			if i < len(lines)-1 {
				assert.Len(t, line, 76, "base64 line %d", i)
			} else {
				assert.LessOrEqual(t, len(line), 76)
			}
		}
		decodedFile, err := base64.StdEncoding.DecodeString(strings.Join(lines, ""))
		require.NoError(t, err)
		assert.Equal(t, data, decodedFile, "the base64 part decodes to the attachment bytes")

		_, err = reader.NextRawPart()
		assert.ErrorIs(t, err, io.EOF, "exactly two parts")
	})

	t.Run("a filename or content type that could leave its header is refused before the relay", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			a    Attachment
		}{
			{"CRLF in the filename", Attachment{Filename: "a.pdf\r\nBcc: attacker@example.com", ContentType: "application/pdf"}},
			{"LF in the filename", Attachment{Filename: "a\n.pdf", ContentType: "application/pdf"}},
			{"CR in the filename", Attachment{Filename: "a\r.pdf", ContentType: "application/pdf"}},
			{"double quote in the filename", Attachment{Filename: `a".pdf`, ContentType: "application/pdf"}},
			{"empty filename", Attachment{ContentType: "application/pdf"}},
			{"CRLF in the content type", Attachment{Filename: "a.pdf", ContentType: "application/pdf\r\nBcc: attacker@example.com"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hook := captureLogs(t)
				m := NewSMTPMailer(config.SMTPConfig{Host: "127.0.0.1", Port: 25, From: "no-reply@example.com"})
				m.dial = func(context.Context, string, string) (net.Conn, error) {
					t.Error("a refused message must not dial the relay")
					return nil, errors.New("dial refused by test")
				}
				tc.a.Data = data
				err := m.SendWithAttachment(to, subject, body, tc.a)
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidHeader)
				assert.NotContains(t, err.Error(), "attacker@example.com")
				assert.NotContains(t, renderedLogs(t, hook), "attacker@example.com")
			})
		}
	})

	t.Run("the recipient and the subject are checked as in Send", func(t *testing.T) {
		m := NewSMTPMailer(config.SMTPConfig{Host: "127.0.0.1", Port: 25, From: "no-reply@example.com"})
		m.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Error("a refused message must not dial the relay")
			return nil, errors.New("dial refused by test")
		}
		a := Attachment{Filename: "a.pdf", ContentType: "application/pdf", Data: data}
		assert.ErrorIs(t, m.SendWithAttachment("jane@example.com\r\nBcc: x@example.com", subject, body, a), ErrInvalidHeader)
		assert.ErrorIs(t, m.SendWithAttachment(to, "Hello\r\nBcc: x@example.com", body, a), ErrInvalidHeader)
		assert.Error(t, m.SendWithAttachment("not-an-address", subject, body, a))
	})

	t.Run("a transport failure is reported and logs neither recipient, subject, body nor file name", func(t *testing.T) {
		hook := captureLogs(t)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := listener.Addr().String()
		require.NoError(t, listener.Close())

		m := newTestMailer(t, addr, "127.0.0.1", config.SMTPConfig{})
		err, _ = sendWithin(t, func() error {
			return m.SendWithAttachment(to, subject, "secret body text", Attachment{
				Filename: "Quote-Q-000042.pdf", ContentType: "application/pdf", Data: data,
			})
		})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrInvalidHeader)
		logs := renderedLogs(t, hook)
		for _, private := range []string{to, subject, "secret body text", "Quote-Q-000042.pdf"} {
			assert.NotContains(t, logs, private)
		}
	})
}

// TestLogMailerAttachment: the log-only mailer records the recipient, the
// subject and the attachment's name and size, never the body or the file.
func TestLogMailerAttachment(t *testing.T) {
	hook := captureLogs(t)

	require.NoError(t, NewLogMailer().SendWithAttachment(
		"user@example.com", "Quote Q-000042 from GopherCRM",
		"Open http://localhost:5173/quote-response?token=super-secret-raw-token to answer.",
		Attachment{Filename: "Quote-Q-000042.pdf", ContentType: "application/pdf", Data: []byte("%PDF-file-content-marker")}))

	entry := hook.LastEntry()
	require.NotNil(t, entry)
	assert.Equal(t, "user@example.com", entry.Data["to"])
	assert.Equal(t, "Quote Q-000042 from GopherCRM", entry.Data["subject"])
	assert.Equal(t, "Quote-Q-000042.pdf", entry.Data["attachment"])
	assert.Equal(t, len("%PDF-file-content-marker"), entry.Data["attachment_bytes"])

	rendered := renderedLogs(t, hook)
	assert.NotContains(t, rendered, "super-secret-raw-token", "the body is never logged")
	assert.NotContains(t, rendered, "file-content-marker", "the file is never logged")
}
