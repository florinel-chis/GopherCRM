//go:build unix

package service

// The Chrome renderer of the quote PDF (#384): a missing binary switches PDF
// output off instead of failing, a browser that outlives the bound is killed
// together with what it started, the browser's environment is built rather
// than inherited, the page URL reaches the browser over its DevTools pipe and
// never as an argument (#396), and, when QUOTE_PDF_CHROME_PATH names one, a
// real binary prints a page to PDF.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChrome writes an executable shell script and returns its path.
func fakeChrome(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-chrome")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	return path
}

// The stand-in browser of the pipe tests. Render builds the child's
// environment, so the script, not the test, sets the marker that turns the
// test binary into the stand-in.
const (
	pipeChromeModeEnv = "GOPHERCRM_TEST_QUOTE_PDF_BROWSER"
	pipeChromeDirEnv  = "GOPHERCRM_TEST_QUOTE_PDF_BROWSER_DIR"
)

// pipeChrome is a stand-in browser that speaks the DevTools pipe protocol on
// file descriptors 3 and 4, and the files it records to.
type pipeChrome struct {
	path     string
	envFile  string
	argsFile string
	urlFile  string
}

// newPipeChrome writes the stand-in: a script that records its environment
// and arguments and then execs this test binary as TestHelperQuotePDFBrowser,
// which keeps file descriptors 3 and 4. mode is "print" (answer
// Page.printToPDF with pdf), "error" (answer it with an error that holds the
// page URL) or "exit" (exit without an answer).
func newPipeChrome(t *testing.T, mode string, pdf []byte) *pipeChrome {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "answer.pdf"), pdf, 0o600))
	browser := &pipeChrome{
		envFile:  filepath.Join(dir, "env.txt"),
		argsFile: filepath.Join(dir, "args.txt"),
		urlFile:  filepath.Join(dir, "navigate.txt"),
	}
	browser.path = fakeChrome(t, fmt.Sprintf(`env > %q
for arg in "$@"; do echo "$arg" >> %q; done
%s=%q %s=%q exec %q -test.run='^TestHelperQuotePDFBrowser$'`,
		browser.envFile, browser.argsFile, pipeChromeModeEnv, mode, pipeChromeDirEnv, dir, self))
	return browser
}

// args returns the arguments the stand-in was started with.
func (b *pipeChrome) args(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(b.argsFile)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// navigated returns the URL the stand-in received in Page.navigate.
func (b *pipeChrome) navigated(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(b.urlFile)
	require.NoError(t, err, "the stand-in received a Page.navigate")
	return string(raw)
}

// TestHelperQuotePDFBrowser is not a test: it is the stand-in browser, run
// when the script of newPipeChrome execs the test binary. In an ordinary run
// the marker is absent and it returns at once.
func TestHelperQuotePDFBrowser(t *testing.T) {
	mode := os.Getenv(pipeChromeModeEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(pipeChromeDirEnv)
	if mode == "exit" {
		os.Exit(0)
	}
	commands := bufio.NewReader(os.NewFile(3, "devtools-commands"))
	replies := os.NewFile(4, "devtools-replies")
	send := func(message map[string]any) {
		raw, err := json.Marshal(message)
		if err != nil {
			os.Exit(3)
		}
		if _, err := replies.Write(append(raw, 0)); err != nil {
			os.Exit(3)
		}
	}
	const session = "stand-in-session"
	pageURL := ""
	for {
		raw, err := commands.ReadBytes(0)
		if err != nil {
			// The renderer closed its end: the render is over.
			os.Exit(0)
		}
		var command struct {
			ID        int64  `json:"id"`
			Method    string `json:"method"`
			SessionID string `json:"sessionId"`
			Params    struct {
				URL string `json:"url"`
			} `json:"params"`
		}
		if err := json.Unmarshal(bytes.TrimSuffix(raw, []byte{0}), &command); err != nil {
			os.Exit(3)
		}
		reply := map[string]any{"id": command.ID, "result": map[string]any{}}
		if command.SessionID != "" {
			reply["sessionId"] = command.SessionID
		}
		var event string
		switch command.Method {
		case "Target.createTarget":
			reply["result"] = map[string]any{"targetId": "stand-in-target"}
		case "Target.attachToTarget":
			reply["result"] = map[string]any{"sessionId": session}
		case "Page.navigate":
			pageURL = command.Params.URL
			if err := os.WriteFile(filepath.Join(dir, "navigate.txt"), []byte(pageURL), 0o600); err != nil {
				os.Exit(3)
			}
			reply["result"] = map[string]any{"frameId": "stand-in-frame"}
			event = "Page.loadEventFired"
		case "Emulation.setVirtualTimePolicy":
			event = "Emulation.virtualTimeBudgetExpired"
		case "Page.printToPDF":
			if mode == "error" {
				delete(reply, "result")
				reply["error"] = map[string]any{"code": -32000, "message": "Printing failed for " + pageURL}
				break
			}
			pdf, err := os.ReadFile(filepath.Join(dir, "answer.pdf"))
			if err != nil {
				os.Exit(3)
			}
			reply["result"] = map[string]any{"data": base64.StdEncoding.EncodeToString(pdf), "stream": ""}
		}
		send(reply)
		if event != "" {
			send(map[string]any{"method": event, "params": map[string]any{}, "sessionId": command.SessionID})
		}
	}
}

// processGone reports whether no process has this pid any more. A zombie
// still answers signal 0, so the caller polls.
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestChromeQuotePDFRendererMissingBinary(t *testing.T) {
	t.Run("an empty path is PDF off without an error", func(t *testing.T) {
		for _, path := range []string{"", "   "} {
			renderer, err := NewChromeQuotePDFRenderer(path)
			require.NoError(t, err)
			assert.Nil(t, renderer)
		}
	})

	t.Run("a path that does not exist is reported and yields no renderer", func(t *testing.T) {
		renderer, err := NewChromeQuotePDFRenderer(filepath.Join(t.TempDir(), "no-such-chrome"))
		require.ErrorIs(t, err, ErrQuotePDFChromeNotFound)
		assert.Nil(t, renderer)
	})

	t.Run("a file that is not executable and a directory are refused", func(t *testing.T) {
		plain := filepath.Join(t.TempDir(), "chrome")
		require.NoError(t, os.WriteFile(plain, []byte("not a program"), 0o644))
		for _, path := range []string{plain, t.TempDir()} {
			renderer, err := NewChromeQuotePDFRenderer(path)
			require.ErrorIs(t, err, ErrQuotePDFChromeNotFound)
			assert.Nil(t, renderer)
		}
	})

	t.Run("a service without a renderer has PDF output off", func(t *testing.T) {
		renderer, err := NewChromeQuotePDFRenderer(filepath.Join(t.TempDir(), "no-such-chrome"))
		require.Error(t, err)
		var svc QuoteService = &quoteService{appBaseURL: "https://crm.example.test"}
		if renderer != nil {
			svc = WithQuotePDF(svc, renderer)
		}
		assert.False(t, svc.PDFAvailable())
	})

	t.Run("an executable file is accepted", func(t *testing.T) {
		renderer, err := NewChromeQuotePDFRenderer(fakeChrome(t, "exit 0"))
		require.NoError(t, err)
		require.NotNil(t, renderer)
	})
}

func TestChromeQuotePDFRendererTimeout(t *testing.T) {
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	selfPID := filepath.Join(dir, "self.pid")
	// The browser stand-in starts a child and waits for it: both outlive the
	// bound unless the whole process group is killed. It never speaks on the
	// pipe, and both hold the browser's ends of it open, so nothing but the
	// bound ends this render.
	renderer, err := NewChromeQuotePDFRenderer(fakeChrome(t, fmt.Sprintf(
		"echo $$ > %q\nsleep 60 & echo $! > %q; wait", selfPID, childPID)))
	require.NoError(t, err)

	// The bound starts once the stand-in has recorded both pids, so a slow
	// first start of a new executable (macOS scans it) cannot eat the bound.
	const bound = 1500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		pdf []byte
		err error
		at  time.Time
	}
	done := make(chan outcome, 1)
	go func() {
		pdf, err := renderer.Render(ctx, "http://127.0.0.1:9/quote-render#timeout-canary")
		done <- outcome{pdf: pdf, err: err, at: time.Now()}
	}()
	recorded := func(path string) bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.HasSuffix(string(raw), "\n")
	}
	require.Eventually(t, func() bool { return recorded(selfPID) && recorded(childPID) },
		10*time.Second, 10*time.Millisecond, "the stand-in started and recorded its pids")
	started := time.Now()
	timer := time.AfterFunc(bound, cancel)
	defer timer.Stop()

	var out outcome
	select {
	case out = <-done:
	case <-time.After(bound + 10*time.Second):
		t.Fatal("Render did not return after its bound")
	}
	pdf, err, elapsed := out.pdf, out.err, out.at.Sub(started)

	require.Error(t, err)
	assert.Nil(t, pdf)
	assert.NotContains(t, err.Error(), "timeout-canary", "the page URL is not part of the error")
	assert.NotContains(t, err.Error(), "127.0.0.1")
	assert.GreaterOrEqual(t, elapsed, bound-100*time.Millisecond, "the browser ran until the bound")
	assert.Less(t, elapsed, bound+3*time.Second, "Render returns at the bound, not when the child would have finished")

	readPID := func(path string) int {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "the stand-in recorded its pids before the bound")
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		require.NoError(t, err)
		require.Positive(t, pid)
		return pid
	}
	script, child := readPID(selfPID), readPID(childPID)
	require.NotEqual(t, script, child)
	// Bounded wait, no fixed sleep: the kill is asynchronous.
	require.Eventually(t, func() bool { return processGone(script) && processGone(child) },
		5*time.Second, 20*time.Millisecond, "the script (%d) and its child (%d) are both gone", script, child)
}

func TestChromeQuotePDFRendererEnvironment(t *testing.T) {
	t.Setenv("JWT_SECRET", "jwt-secret-canary-0123456789abcdef0123456789")
	t.Setenv("API_KEY_SECRET", "api-key-secret-canary")
	t.Setenv("DB_PASSWORD", "db-password-canary")
	t.Setenv("SMTP_PASSWORD", "smtp-password-canary")
	t.Setenv("TZ", "Europe/Bucharest")

	// The stand-in records what it was given and prints over the pipe.
	browser := newPipeChrome(t, "print", []byte("%PDF-1.4 fake"))
	renderer, err := NewChromeQuotePDFRenderer(browser.path)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pdf, err := renderer.Render(ctx, "http://127.0.0.1:9/quote-render#environment-canary")
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(pdf, []byte("%PDF")))

	rawEnv, err := os.ReadFile(browser.envFile)
	require.NoError(t, err)
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(rawEnv)), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = value
		}
	}
	for _, name := range []string{"JWT_SECRET", "API_KEY_SECRET", "DB_PASSWORD", "SMTP_PASSWORD"} {
		assert.NotContains(t, env, name, "%s must not reach the browser process", name)
	}
	assert.NotContains(t, string(rawEnv), "canary", "no value of the server's secrets either")
	assert.Equal(t, os.Getenv("PATH"), env["PATH"])
	assert.Equal(t, "Europe/Bucharest", env["TZ"])

	args := browser.args(t)
	var profile string
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			profile = value
		}
		assert.NotEqual(t, "--no-sandbox", arg, "the sandbox is never switched off from Go")
	}
	require.NotEmpty(t, profile, "the browser gets its own profile directory")
	renderDir := filepath.Dir(profile)
	assert.Equal(t, renderDir, env["HOME"], "HOME is the render's temp dir")
	for _, name := range []string{"TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		assert.Truef(t, strings.HasPrefix(env[name], renderDir+string(filepath.Separator)), "%s lies inside the render's temp dir: %q", name, env[name])
	}
	assert.Contains(t, args, "--headless")

	_, statErr := os.Stat(renderDir)
	assert.True(t, os.IsNotExist(statErr), "the temp dir is removed after the render")
}

// The launch arguments are one list for chrome-headless-shell and for a full
// Chrome or Chromium (#393). The keychain switches are what keeps a full
// browser from waiting on a keychain or keyring it cannot reach, --headless is
// what a full browser needs to start without a window, and the sandbox is
// never switched off from Go: an image that has to does it in the launcher
// QUOTE_PDF_CHROME_PATH names. Since #396 the list holds no URL: the browser
// is started with --remote-debugging-pipe and gets the page URL, whose
// fragment is the render credential, in Page.navigate over that pipe.
func TestChromeQuotePDFRendererLaunchArguments(t *testing.T) {
	browser := newPipeChrome(t, "print", []byte("%PDF-1.4 fake"))
	renderer, err := NewChromeQuotePDFRenderer(browser.path)
	require.NoError(t, err)

	const fragment = "launch-arguments-canary"
	const pageURL = "http://127.0.0.1:9/quote-render#" + fragment
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pdf, err := renderer.Render(ctx, pageURL)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF")))

	args := browser.args(t)
	assert.Contains(t, args, "--use-mock-keychain", "a macOS Chrome must not wait for a login keychain")
	assert.Contains(t, args, "--password-store=basic", "a Linux Chrome must not look for a desktop keyring")
	assert.Contains(t, args, "--headless", "a full Chrome or Chromium needs it to start without a window")
	assert.Contains(t, args, "--remote-debugging-pipe", "the browser is driven over its DevTools pipe")
	var profile string
	for _, arg := range args {
		assert.NotEqual(t, "--no-sandbox", arg, "the sandbox is never switched off from Go")
		assert.Falsef(t, strings.HasPrefix(arg, "--no-sandbox"), "no form of --no-sandbox either: %q", arg)
		for _, part := range []string{pageURL, "quote-render", fragment} {
			assert.NotContainsf(t, arg, part, "no argument holds the page URL or a part of it (%q)", part)
		}
		for _, prefix := range []string{"--remote-debugging-port", "--remote-debugging-address", "--print-to-pdf"} {
			assert.Falsef(t, strings.HasPrefix(arg, prefix), "%s is never passed: %q", prefix, arg)
		}
		if value, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			profile = value
		}
	}
	require.NotEmpty(t, profile, "the profile directory is passed as --user-data-dir=")
	renderDir := filepath.Dir(profile)
	assert.Equal(t, "profile", filepath.Base(profile), "the profile lies inside the render's directory")
	assert.Truef(t, strings.HasPrefix(filepath.Base(renderDir), "gophercrm-quote-pdf-"),
		"the render's directory is the one Render made: %q", renderDir)

	assert.Equal(t, pageURL, browser.navigated(t),
		"the browser gets the exact page URL, fragment included, in Page.navigate")

	rawEnv, err := os.ReadFile(browser.envFile)
	require.NoError(t, err)
	home := ""
	for _, line := range strings.Split(strings.TrimSpace(string(rawEnv)), "\n") {
		if value, ok := strings.CutPrefix(line, "HOME="); ok {
			home = value
		}
	}
	assert.Equal(t, renderDir, home, "HOME is the render's directory")
}

// openFileDescriptors counts the file descriptors this process holds.
func openFileDescriptors(t *testing.T) int {
	t.Helper()
	// Names only: an entry of /dev/fd may be gone before it could be looked
	// at, the directory's own descriptor among them.
	dir, err := os.Open("/dev/fd")
	require.NoError(t, err)
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(-1)
	require.NoError(t, err)
	return len(names)
}

// The exchange over the DevTools pipe (#396), with the stand-in browser: a
// large answer arrives whole, a browser that goes away or reports an error
// fails the render at once and without its words, and no render leaves a
// pipe end open in this process.
func TestChromeQuotePDFRendererPipe(t *testing.T) {
	const fragment = "pipe-fragment-canary"
	const pageURL = "http://pipe-host-canary.test:9/quote-render#" + fragment
	render := func(t *testing.T, browser *pipeChrome) ([]byte, error, time.Duration) {
		t.Helper()
		renderer, err := NewChromeQuotePDFRenderer(browser.path)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		pdf, err := renderer.Render(ctx, pageURL)
		return pdf, err, time.Since(started)
	}
	large := make([]byte, 3<<20/2)
	_, err := rand.Read(large)
	require.NoError(t, err)
	copy(large, "%PDF-1.7\n")

	t.Run("a PDF answer larger than 1 MiB comes back byte-identical", func(t *testing.T) {
		require.Greater(t, len(large), 1<<20)
		pdf, err, _ := render(t, newPipeChrome(t, "print", large))
		require.NoError(t, err)
		require.Equal(t, len(large), len(pdf))
		assert.True(t, bytes.Equal(large, pdf), "the bytes are the ones the browser sent")
	})

	// The first render above has made whatever this process opens once (the
	// poller). From here on a render must give back every descriptor.
	before := openFileDescriptors(t)

	t.Run("a browser that exits without answering fails the render at once", func(t *testing.T) {
		pdf, err, elapsed := render(t, newPipeChrome(t, "exit", nil))
		require.ErrorIs(t, err, errQuotePDFChromeFailed)
		assert.Nil(t, pdf)
		assert.Less(t, elapsed, 5*time.Second, "not a wait until the bound")
	})

	t.Run("an error of the browser that holds the page URL is not passed on", func(t *testing.T) {
		browser := newPipeChrome(t, "error", nil)
		pdf, err, elapsed := render(t, browser)
		require.ErrorIs(t, err, errQuotePDFChromeFailed)
		assert.Nil(t, pdf)
		assert.Less(t, elapsed, 5*time.Second)
		assert.Equal(t, pageURL, browser.navigated(t), "the stand-in had the URL to put into its error")
		assert.NotContains(t, err.Error(), fragment)
		assert.NotContains(t, err.Error(), "pipe-host-canary")
		assert.NotContains(t, err.Error(), "quote-render")
	})

	t.Run("an answer that is not a PDF is an error", func(t *testing.T) {
		pdf, err, _ := render(t, newPipeChrome(t, "print", []byte("<html>not a PDF</html>")))
		require.ErrorIs(t, err, errQuotePDFChromeOutput)
		assert.Nil(t, pdf)
	})

	t.Run("no render leaves a file descriptor open", func(t *testing.T) {
		pdf, err, _ := render(t, newPipeChrome(t, "print", []byte("%PDF-1.4 fake")))
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(pdf, []byte("%PDF")))
		assert.Equal(t, before, openFileDescriptors(t),
			"the pipe ends of a failed and of a successful render are all closed")
	})
}

// The one test that drives a real browser. It skips without
// QUOTE_PDF_CHROME_PATH and is deliberately named in no no-skip gate: CI has
// no browser for it. The page holds the render open with a request the test
// answers late; while it is open, the process list of the host shows the
// browser with its pipe switch and shows the page URL nowhere.
func TestChromeQuotePDFRendererRealBinary(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("QUOTE_PDF_CHROME_PATH"))
	if path == "" {
		t.Skip("QUOTE_PDF_CHROME_PATH is not set: no real browser to drive")
	}
	renderer, err := NewChromeQuotePDFRenderer(path)
	require.NoError(t, err)
	require.NotNil(t, renderer)

	// The render directory is made under TMPDIR: this makes it findable.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	arrived := make(chan struct{})
	release := make(chan struct{})
	var arriveOnce, releaseOnce sync.Once
	releaseData := func() { releaseOnce.Do(func() { close(release) }) }
	// A failure must not leave the handler, and with it the server, blocked.
	t.Cleanup(releaseData)
	var served atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		arriveOnce.Do(func() { close(arrived) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Late line"))
		served.Store(true)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Quote</title></head><body><h1>Quote Q-000042</h1><p id="late">Loading</p>
<script>fetch('/data').then(function (r) { return r.text(); }).then(function (t) { document.getElementById('late').textContent = t; });</script>
</body></html>`))
	})
	page := httptest.NewServer(mux)
	defer page.Close()
	defer releaseData()
	hostPort := strings.TrimPrefix(page.URL, "http://")

	// Long and new for every run, so that no other process of a shared
	// machine can hold it.
	random := make([]byte, 16)
	_, err = rand.Read(random)
	require.NoError(t, err)
	canary := "real-binary-" + hex.EncodeToString(random)

	ctx, cancel := context.WithTimeout(context.Background(), QuotePDFRenderTimeout)
	defer cancel()
	type outcome struct {
		pdf    []byte
		err    error
		served bool
	}
	done := make(chan outcome, 1)
	go func() {
		pdf, err := renderer.Render(ctx, page.URL+"/quote-render#"+canary)
		done <- outcome{pdf: pdf, err: err, served: served.Load()}
	}()

	select {
	case <-arrived:
	case out := <-done:
		t.Fatalf("Render returned before the page asked for its data: %v", out.err)
	case <-ctx.Done():
		t.Fatal("the page never asked for its data")
	}

	// The render is held open by the pending request: look at the host.
	renderDirs, err := filepath.Glob(filepath.Join(tmp, "gophercrm-quote-pdf-*"))
	require.NoError(t, err)
	require.Len(t, renderDirs, 1, "one render directory under TMPDIR")
	renderDir := renderDirs[0]
	listing, err := exec.Command("ps", "axww", "-o", "args=").Output()
	require.NoError(t, err)
	// A line of the listing is never printed: on a host that renders real
	// quotes it could hold a credential.
	var withPipe, withCanary, withHostPort, withPort int
	for _, line := range strings.Split(string(listing), "\n") {
		if strings.Contains(line, canary) {
			withCanary++
		}
		if strings.Contains(line, hostPort) {
			withHostPort++
		}
		if !strings.Contains(line, renderDir) {
			continue
		}
		if strings.Contains(line, "--remote-debugging-pipe") {
			withPipe++
		}
		if strings.Contains(line, "--remote-debugging-port") {
			withPort++
		}
	}
	assert.Positive(t, withPipe, "the browser of this render runs with --remote-debugging-pipe")
	assert.Zero(t, withCanary, "no command line of the host holds the fragment")
	assert.Zero(t, withHostPort, "no command line of the host holds the page's host and port")
	assert.Zero(t, withPort, "the browser of this render has no debugging port switch")
	// The browser writes this file when it opens a debugging port.
	_, statErr := os.Stat(filepath.Join(renderDir, "profile", "DevToolsActivePort"))
	assert.True(t, os.IsNotExist(statErr), "no DevToolsActivePort file in the profile: no debugging port is open")

	select {
	case out := <-done:
		t.Fatalf("Render returned while the page's request was pending: %v", out.err)
	default:
	}
	releaseData()

	var out outcome
	select {
	case out = <-done:
	case <-time.After(QuotePDFRenderTimeout + 5*time.Second):
		t.Fatal("Render did not return")
	}
	require.NoError(t, out.err)
	require.True(t, bytes.HasPrefix(out.pdf, []byte("%PDF")), "the output starts with %%PDF")
	assert.Greater(t, len(out.pdf), 500)
	assert.True(t, out.served, "the page's late request was served before Render returned")
}
