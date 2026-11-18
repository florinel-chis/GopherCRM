//go:build unix

package service

// The Chrome renderer of the quote PDF (#384): a missing binary switches PDF
// output off instead of failing, a browser that outlives the bound is killed
// together with what it started, the browser's environment is built rather
// than inherited, and, when QUOTE_PDF_CHROME_PATH names one, a real binary
// prints a page to PDF.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// bound unless the whole process group is killed.
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

	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")
	// The stand-in records what it was given and writes a file that passes
	// for a PDF to the path of --print-to-pdf=.
	renderer, err := NewChromeQuotePDFRenderer(fakeChrome(t, fmt.Sprintf(`env > %q
for arg in "$@"; do
  echo "$arg" >> %q
  case "$arg" in --print-to-pdf=*) printf '%%%%PDF-1.4 fake' > "${arg#--print-to-pdf=}" ;; esac
done`, envFile, argsFile)))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pdf, err := renderer.Render(ctx, "http://127.0.0.1:9/quote-render#environment-canary")
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(pdf, []byte("%PDF")))

	rawEnv, err := os.ReadFile(envFile)
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

	rawArgs, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	var profile, output string
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			profile = value
		}
		if value, ok := strings.CutPrefix(arg, "--print-to-pdf="); ok {
			output = value
		}
		assert.NotEqual(t, "--no-sandbox", arg, "the sandbox is never switched off from Go")
	}
	require.NotEmpty(t, profile, "the browser gets its own profile directory")
	require.NotEmpty(t, output)
	renderDir := filepath.Dir(output)
	assert.Equal(t, renderDir, filepath.Dir(profile), "profile and output share the render's temp dir")
	assert.Equal(t, renderDir, env["HOME"], "HOME is the render's temp dir")
	for _, name := range []string{"TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		assert.Truef(t, strings.HasPrefix(env[name], renderDir+string(filepath.Separator)), "%s lies inside the render's temp dir: %q", name, env[name])
	}
	assert.Contains(t, args, "--headless")
	assert.Equal(t, "http://127.0.0.1:9/quote-render#environment-canary", args[len(args)-1], "the page URL is the last argument")

	_, statErr := os.Stat(renderDir)
	assert.True(t, os.IsNotExist(statErr), "the temp dir is removed after the render")
}

// The one test that drives a real browser. It skips without
// QUOTE_PDF_CHROME_PATH and is deliberately named in no no-skip gate: CI has
// no browser for it.
func TestChromeQuotePDFRendererRealBinary(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("QUOTE_PDF_CHROME_PATH"))
	if path == "" {
		t.Skip("QUOTE_PDF_CHROME_PATH is not set: no real browser to drive")
	}
	renderer, err := NewChromeQuotePDFRenderer(path)
	require.NoError(t, err)
	require.NotNil(t, renderer)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Quote</title></head><body><h1>Quote Q-000042</h1><p>Static page</p></body></html>`))
	}))
	defer page.Close()

	ctx, cancel := context.WithTimeout(context.Background(), QuotePDFRenderTimeout)
	defer cancel()
	pdf, err := renderer.Render(ctx, page.URL+"/quote-render#real-binary")
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF")), "the output starts with %%PDF")
	assert.Greater(t, len(pdf), 500)
}
