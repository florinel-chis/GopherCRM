package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrQuotePDFChromeNotFound says the configured Chrome path does not resolve
// to an executable file. It is not a reason to stop the boot: PDF output is
// then off.
var ErrQuotePDFChromeNotFound = errors.New("quote PDF browser binary is not an executable file")

// Errors of one render. Their text is fixed: neither the page URL, its
// fragment nor anything the browser wrote is ever part of an error.
var (
	errQuotePDFChromeTimeout = errors.New("quote PDF browser did not finish within the render bound")
	errQuotePDFChromeFailed  = errors.New("quote PDF browser failed")
	errQuotePDFChromeOutput  = errors.New("quote PDF browser did not write a PDF")
)

// quotePDFVirtualTimeBudgetMS is how much virtual time the page gets after it
// has loaded (Emulation.setVirtualTimePolicy). Chrome holds virtual time while
// a network request is pending, so the page's one API call finishes and React
// paints the document before the print starts; the budget is spent in a
// fraction of that as real time.
const quotePDFVirtualTimeBudgetMS = 10000

// ChromeQuotePDFRenderer prints a page to PDF with a browser binary run
// headless, one process per render. The binary is either chrome-headless-shell
// (what the Docker image ships: the old headless mode as its own binary) or a
// full Chrome or Chromium; the argument list of Render is valid for both.
//
// The browser is started with --remote-debugging-pipe and without a URL, and
// is driven over that pipe (its file descriptors 3 and 4): open a tab,
// navigate, wait for the page, print. The page URL, whose fragment is the
// render credential, is therefore never on a command line, and no debugging
// port is opened. File descriptors beyond the standard three cannot be handed
// to a child on Windows, so the renderer needs a Unix host; elsewhere every
// render fails with the fixed error.
//
// Every render gets a fresh temporary directory that holds the browser
// profile, HOME, TMPDIR and the XDG directories, and is removed afterwards: the browser needs nothing else writable, which is what
// a read-only root filesystem and a hardened systemd unit (ProtectHome,
// ProtectSystem=strict, PrivateTmp) leave it.
//
// The child's environment is built, not inherited: PATH, LANG, LC_ALL and TZ
// when they are set, and the directories above. JWT_SECRET, API_KEY_SECRET,
// DB_PASSWORD, SMTP_PASSWORD and every other variable of the server never
// reach a browser process.
//
// The browser runs in its own process group, and the whole group is killed
// when the context is done or the browser has exited, so no helper process
// outlives a render. --no-sandbox is never passed from here: an image that
// needs it sets it in the launcher it points QUOTE_PDF_CHROME_PATH at.
type ChromeQuotePDFRenderer struct {
	path string
}

// NewChromeQuotePDFRenderer returns the renderer for the binary at path
// (QUOTE_PDF_CHROME_PATH). An empty path is (nil, nil): PDF output is not
// configured. A path that is not an executable file is (nil,
// ErrQuotePDFChromeNotFound): the caller logs it and carries on with PDF
// output off. A bare command name is looked up on PATH.
func NewChromeQuotePDFRenderer(path string) (*ChromeQuotePDFRenderer, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return nil, ErrQuotePDFChromeNotFound
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return nil, ErrQuotePDFChromeNotFound
	}
	return &ChromeQuotePDFRenderer{path: resolved}, nil
}

// Render prints pageURL to PDF. The context bounds the whole render; when it
// is done the browser and its children are killed. It returns as soon as the
// browser has answered the print command, or when the browser has failed,
// closed its pipe or exited; the result must start with %PDF.
//
// The URL reaches the browser over the DevTools pipe, never as an argument:
// the fragment holds the render credential, and a command line is readable
// by every user of the host.
func (r *ChromeQuotePDFRenderer) Render(ctx context.Context, pageURL string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "gophercrm-quote-pdf-")
	if err != nil {
		return nil, errQuotePDFChromeFailed
	}
	defer func() { _ = os.RemoveAll(dir) }()

	profile := filepath.Join(dir, "profile")
	for _, sub := range []string{profile, filepath.Join(dir, "config"), filepath.Join(dir, "cache"), filepath.Join(dir, "tmp")} {
		if err := os.Mkdir(sub, 0o700); err != nil {
			return nil, errQuotePDFChromeFailed
		}
	}

	// The two pipes of --remote-debugging-pipe: the browser reads commands
	// on its file descriptor 3 and writes replies and events on 4.
	browserIn, commands, err := os.Pipe()
	if err != nil {
		return nil, errQuotePDFChromeFailed
	}
	replies, browserOut, err := os.Pipe()
	if err != nil {
		closeQuotePDFFiles(browserIn, commands)
		return nil, errQuotePDFChromeFailed
	}
	var closeOnce sync.Once
	closePipe := func() { closeOnce.Do(func() { closeQuotePDFFiles(commands, replies) }) }
	defer closePipe()

	// One argument list for both kinds of binary, checked against
	// chrome-headless-shell and a full Chrome: a full browser needs
	// --headless to start without a window, the headless shell accepts it
	// and is headless anyway; the first-run, extension, sync and keychain
	// switches only do something in a full browser and are ignored by the
	// headless shell, which has none of those parts. Neither needs a flag
	// the other refuses. The list holds no URL and nothing of the page: the
	// browser starts empty and is told what to do over the pipe. A debugging
	// port or address is never asked for, so nothing listens on a socket.
	cmd := exec.CommandContext(ctx, r.path,
		"--headless",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-crash-reporter",
		"--disable-breakpad",
		"--hide-scrollbars",
		// The profile lives for one render and HOME is the render's temp
		// dir: without these a macOS Chrome waits forever for a login
		// keychain it cannot find, and a Linux one may look for a desktop
		// keyring. The headless shell has no keychain and ignores both.
		"--use-mock-keychain",
		"--password-store=basic",
		"--user-data-dir="+profile,
		"--remote-debugging-pipe",
	)
	cmd.Env = quotePDFChromeEnv(dir)
	// Nothing on the standard streams: the browser's output goes nowhere, so
	// it can neither reach a log nor keep Wait from returning.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// ExtraFiles[i] is file descriptor 3+i of the child. A platform without
	// them (Windows) fails at Start, so a render there fails with the fixed
	// error.
	cmd.ExtraFiles = []*os.File{browserIn, browserOut}
	configureQuotePDFProcessGroup(cmd)
	// A helper that survives the kill must not hold Wait either.
	cmd.WaitDelay = 2 * time.Second

	err = cmd.Start()
	// The browser has its own copies now. While this process held these two
	// open, a dead browser would never show as EOF on the pipe.
	closeQuotePDFFiles(browserIn, browserOut)
	if err != nil {
		return nil, errQuotePDFChromeFailed
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	// Whatever happens, nothing of the browser's process group is left
	// behind, and Wait has returned before the temp dir is removed.
	defer func() {
		killQuotePDFProcessGroup(cmd)
		select {
		case <-exited:
		case <-time.After(cmd.WaitDelay + time.Second):
		}
	}()

	// A done context closes this side of the pipe, which ends a write the
	// browser never reads and the reader's pending read. The reader is gone
	// before Render returns: closing the pipe (deferred above, run before
	// this wait is over) unblocks it.
	stopClose := context.AfterFunc(ctx, closePipe)
	defer stopClose()
	stopReader := make(chan struct{})
	messages := readQuotePDFPipe(replies, stopReader)
	defer func() {
		close(stopReader)
		closePipe()
		for range messages {
		}
	}()

	pipe := &quotePDFPipe{commands: commands, messages: messages, exited: exited}
	pdf, err := pipe.printPage(ctx, pageURL)
	switch {
	case ctx.Err() != nil:
		return nil, errQuotePDFChromeTimeout
	case errors.Is(err, errQuotePDFChromeOutput):
		return nil, errQuotePDFChromeOutput
	case err != nil:
		// Whatever the browser said about the failure stays here.
		return nil, errQuotePDFChromeFailed
	case !bytes.HasPrefix(pdf, []byte("%PDF")):
		return nil, errQuotePDFChromeOutput
	}
	return pdf, nil
}

// quotePDFChromeEnv is the environment of the browser process: the search
// path and the locale and zone of the server, and every directory the browser
// writes to inside dir.
func quotePDFChromeEnv(dir string) []string {
	env := make([]string, 0, 8)
	for _, name := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			env = append(env, name+"="+value)
		}
	}
	return append(env,
		"HOME="+dir,
		"TMPDIR="+filepath.Join(dir, "tmp"),
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
		"XDG_CACHE_HOME="+filepath.Join(dir, "cache"),
	)
}
