package mailer

import (
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A relay that rejects a recipient normally echoes the address in its reply
// ("550 5.1.1 <victim@example.com>: Recipient address rejected"), and the
// net/smtp client hands that text back as a *textproto.Error. The quote email
// promises that no log line of the call carries the recipient, so Send must
// log the reply's code and not its text, while still returning the raw error
// to its caller. The stub below is the smallest relay that gets a client as
// far as RCPT TO and refuses it there.
func TestSMTPMailerSendLogOmitsTheRelayReply(t *testing.T) {
	const (
		recipient = "victim@example.com"
		subject   = "Quote Q-000042 from GopherCRM"
		body      = "token-bearing body"
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	served := make(chan error, 1)
	go func() { served <- rejectRecipient(listener) }()

	logger, hook := test.NewNullLogger()
	previous := utils.Logger
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })

	m := NewSMTPMailer(config.SMTPConfig{
		Host: "127.0.0.1",
		Port: listener.Addr().(*net.TCPAddr).Port,
		From: "no-reply@example.com",
	})
	err = m.Send(recipient, subject, body)
	require.Error(t, err)

	select {
	case relayErr := <-served:
		require.NoError(t, relayErr, "the stub relay did not get the client as far as RCPT TO")
	case <-time.After(5 * time.Second):
		t.Fatal("the stub relay did not finish")
	}

	// The returned error is the relay's reply, address and all: a caller that
	// logs it must sanitise it itself (the quote service does not log it).
	var reply *textproto.Error
	require.ErrorAs(t, err, &reply)
	assert.Equal(t, 550, reply.Code)
	assert.Contains(t, err.Error(), recipient, "the stub reproduces a reply that echoes the address")

	// The log line keeps the code and drops the text.
	entries := hook.AllEntries()
	require.Len(t, entries, 1, "one failure line and nothing else")
	rendered, renderErr := entries[0].String()
	require.NoError(t, renderErr)
	assert.Contains(t, rendered, "smtp_code=550", "the SMTP code is what a diagnosis needs")
	assert.NotContains(t, rendered, recipient, "the relay's reply text is not logged")
	assert.NotContains(t, rendered, body)
	_, hasSubject := entries[0].Data["subject"]
	assert.False(t, hasSubject, "subjects can be rendered from personal data and are not logged")
	assert.NotContains(t, rendered, subject)
	_, hasTo := entries[0].Data["to"]
	assert.False(t, hasTo, "the generic failure line carries no recipient field")
}

// rejectRecipient serves one SMTP session: it greets, accepts EHLO/HELO and
// MAIL FROM, answers RCPT TO with a 550 that echoes the address, and returns
// nil once the client hangs up after the rejection (the mailer closes the
// connection without QUIT on an error). It returns an error if the client
// leaves before RCPT TO, so the test cannot pass on a connection refusal.
func rejectRecipient(listener net.Listener) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	text := textproto.NewConn(conn)
	if err := text.PrintfLine("220 stub ESMTP"); err != nil {
		return err
	}
	rejected := false
	for {
		line, err := text.ReadLine()
		if err != nil {
			if rejected {
				return nil
			}
			return fmt.Errorf("client left before RCPT TO: %w", err)
		}
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			err = text.PrintfLine("250 stub")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			err = text.PrintfLine("250 2.1.0 Ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			address := strings.Trim(line[len("RCPT TO:"):], "<> ")
			rejected = true
			err = text.PrintfLine("550 5.1.1 <%s>: Recipient address rejected: User unknown", address)
		case strings.HasPrefix(verb, "QUIT"):
			return text.PrintfLine("221 2.0.0 Bye")
		default:
			err = text.PrintfLine("502 5.5.2 Command not implemented")
		}
		if err != nil {
			return err
		}
	}
}
