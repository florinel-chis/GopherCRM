package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// The DevTools pipe of a browser started with --remote-debugging-pipe: the
// browser reads commands on file descriptor 3 and writes replies and events
// on file descriptor 4. Each message, in both directions, is one JSON object
// followed by a NUL byte. There is no socket and no port: only the process
// that started the browser holds the other ends.

// quotePDFPipeExitGrace is how long a message the browser wrote before it
// exited may still take to be read. EOF normally ends the wait at once; the
// grace covers a helper process that keeps the browser's end open.
const quotePDFPipeExitGrace = 500 * time.Millisecond

// errQuotePDFPipeClosed is the internal reason of a failed exchange. Render
// maps every pipe error to one of its fixed errors; nothing the browser sent
// is part of any of them.
var errQuotePDFPipeClosed = errors.New("quote PDF browser pipe closed")

// quotePDFPipeMessage is a reply (ID set) or an event (Method set) of the
// browser.
type quotePDFPipeMessage struct {
	ID        int64           `json:"id"`
	Method    string          `json:"method"`
	SessionID string          `json:"sessionId"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
}

// quotePDFPipeCommand is one command to the browser.
type quotePDFPipeCommand struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// quotePDFPipe is the client side of the DevTools pipe for one render. It is
// used by one goroutine; a second one, started by readQuotePDFPipe, feeds
// messages.
type quotePDFPipe struct {
	commands io.Writer
	messages <-chan quotePDFPipeMessage
	// exited is closed when the browser process has exited.
	exited <-chan struct{}
	grace  <-chan time.Time
	lastID int64
}

// readQuotePDFPipe reads the browser's messages from r into the returned
// channel until r is closed, the browser's end is closed, a message is not
// JSON, or stop is closed. The channel is closed when the reader is done.
// The reader has no line limit: the PDF comes back as one message of
// megabytes.
func readQuotePDFPipe(r io.Reader, stop <-chan struct{}) <-chan quotePDFPipeMessage {
	messages := make(chan quotePDFPipeMessage)
	go func() {
		defer close(messages)
		reader := bufio.NewReaderSize(r, 64*1024)
		for {
			raw, err := reader.ReadBytes(0)
			if err != nil {
				return
			}
			var message quotePDFPipeMessage
			if err := json.Unmarshal(bytes.TrimSuffix(raw, []byte{0}), &message); err != nil {
				return
			}
			select {
			case messages <- message:
			case <-stop:
				return
			}
		}
	}()
	return messages
}

// next returns the browser's next message. It fails when the context is
// done, the pipe is closed, or the browser has exited and what it had
// written is read.
func (p *quotePDFPipe) next(ctx context.Context) (quotePDFPipeMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return quotePDFPipeMessage{}, ctx.Err()
		case message, ok := <-p.messages:
			if !ok {
				return quotePDFPipeMessage{}, errQuotePDFPipeClosed
			}
			return message, nil
		case <-p.exited:
			p.exited = nil
			p.grace = time.After(quotePDFPipeExitGrace)
		case <-p.grace:
			return quotePDFPipeMessage{}, errQuotePDFPipeClosed
		}
	}
}

// call sends one command and waits for its reply, passing over events and
// other replies. A reply with an error member fails the call; its content is
// dropped.
func (p *quotePDFPipe) call(ctx context.Context, sessionID, method string, params, result any) error {
	p.lastID++
	id := p.lastID
	raw, err := json.Marshal(quotePDFPipeCommand{ID: id, Method: method, Params: params, SessionID: sessionID})
	if err != nil {
		return errQuotePDFPipeClosed
	}
	if _, err := p.commands.Write(append(raw, 0)); err != nil {
		return errQuotePDFPipeClosed
	}
	for {
		message, err := p.next(ctx)
		if err != nil {
			return err
		}
		if message.ID != id {
			continue
		}
		if len(message.Error) > 0 && !bytes.Equal(message.Error, []byte("null")) {
			return errQuotePDFPipeClosed
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(message.Result, result); err != nil {
			return errQuotePDFPipeClosed
		}
		return nil
	}
}

// waitEvent waits for the next event of that name in the session.
func (p *quotePDFPipe) waitEvent(ctx context.Context, sessionID, method string) error {
	for {
		message, err := p.next(ctx)
		if err != nil {
			return err
		}
		if message.ID == 0 && message.Method == method && message.SessionID == sessionID {
			return nil
		}
	}
}

// printPage opens a tab, navigates it to pageURL, lets the page finish its
// network requests and paint, and returns what Page.printToPDF answered. The
// renderer knows nothing of the page's markup: readiness is the load event
// followed by a virtual-time budget that the browser holds while a request
// is pending.
func (p *quotePDFPipe) printPage(ctx context.Context, pageURL string) ([]byte, error) {
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := p.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := p.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	session := attached.SessionID
	if session == "" {
		return nil, errQuotePDFPipeClosed
	}

	if err := p.call(ctx, session, "Page.enable", nil, nil); err != nil {
		return nil, err
	}
	var navigated struct {
		ErrorText string `json:"errorText"`
	}
	if err := p.call(ctx, session, "Page.navigate", map[string]any{"url": pageURL}, &navigated); err != nil {
		return nil, err
	}
	if navigated.ErrorText != "" {
		return nil, errQuotePDFPipeClosed
	}
	// The load event that follows the reply is the one of this navigation.
	if err := p.waitEvent(ctx, session, "Page.loadEventFired"); err != nil {
		return nil, err
	}

	if err := p.call(ctx, session, "Emulation.setVirtualTimePolicy", map[string]any{
		"policy": "pauseIfNetworkFetchesPending",
		"budget": quotePDFVirtualTimeBudgetMS,
	}, nil); err != nil {
		return nil, err
	}
	if err := p.waitEvent(ctx, session, "Emulation.virtualTimeBudgetExpired"); err != nil {
		return nil, err
	}

	// Backgrounds as on screen, no header or footer of the browser, and the
	// page size the document's own CSS asks for.
	var printed struct {
		Data string `json:"data"`
	}
	if err := p.call(ctx, session, "Page.printToPDF", map[string]any{
		"printBackground":     true,
		"displayHeaderFooter": false,
		"preferCSSPageSize":   true,
	}, &printed); err != nil {
		return nil, err
	}
	pdf, err := base64.StdEncoding.DecodeString(printed.Data)
	if err != nil {
		return nil, errQuotePDFChromeOutput
	}
	return pdf, nil
}

// closeQuotePDFFiles closes every file that is not nil.
func closeQuotePDFFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
