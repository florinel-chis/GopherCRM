package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The embed script's idempotency key (#341). These checks run against the
// script as the API serves it, so a change to the asset or to its route is
// caught alike.

// servedEmbedScript fetches GET /api/v1/forms/public/embed.js through the real
// public routes and asserts it is byte-identical to the compiled-in asset.
func servedEmbedScript(t *testing.T) string {
	t.Helper()
	router, _ := hostedViewRouter(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/embed.js", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, formEmbedJS, recorder.Body.Bytes(), "the served script is formEmbedJS")
	return recorder.Body.String()
}

// skipJSLiteral returns the index just past the string literal opened at
// src[start] (one of ' " `), honouring backslash escapes.
func skipJSLiteral(src string, start int) int {
	quote := src[start]
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case quote:
			return i + 1
		}
	}
	return len(src)
}

// matchClosing returns the index of the delimiter closing the one at
// src[open] (open is '(' or '{'), skipping string literals, or -1.
func matchClosing(src string, open int) int {
	opener := src[open]
	closer := byte(')')
	if opener == '{' {
		closer = '}'
	}
	depth := 0
	for i := open; i < len(src); {
		switch c := src[i]; c {
		case '\'', '"', '`':
			i = skipJSLiteral(src, i)
			continue
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

// jsFunction extracts the declaration `function <name>(` with its whole body.
func jsFunction(t *testing.T, src, name string) string {
	t.Helper()
	marker := "function " + name + "("
	require.Equal(t, 1, strings.Count(src, marker), "exactly one declaration of %s", name)
	start := strings.Index(src, marker)
	open := strings.Index(src[start:], "{")
	require.NotEqual(t, -1, open)
	open += start
	end := matchClosing(src, open)
	require.NotEqual(t, -1, end, "the body of %s closes", name)
	return src[start : end+1]
}

func TestFormEmbedScriptIdempotencyKeyContract(t *testing.T) {
	script := servedEmbedScript(t)

	resolver := jsFunction(t, script, "resolveIdempotencyKey")
	for _, needle := range []string{
		"'data-idempotency-key'",
		"/^[A-Za-z0-9._:-]{1,128}$/",
		"'submission-'",
		"crypto",
		"getRandomValues",
		"new Uint8Array(16)",
	} {
		assert.Contains(t, resolver, needle)
	}
	assert.Contains(t, script, "crypto.getRandomValues")

	// One key per closure: exactly one call, at closure level, after the
	// data-form-key check and before the first request.
	const declaration = "var idempotencyKey = resolveIdempotencyKey(script);"
	assert.Equal(t, 1, strings.Count(script, declaration))
	assert.Equal(t, 2, strings.Count(script, "resolveIdempotencyKey("), "the declaration and one call")
	assert.Contains(t, script, "\n  "+declaration+"\n", "declared at closure level")
	formKeyAt := strings.Index(script, "script.getAttribute('data-form-key')")
	declarationAt := strings.Index(script, declaration)
	firstFetchAt := strings.Index(script, "fetchJSON(")
	require.NotEqual(t, -1, formKeyAt)
	require.NotEqual(t, -1, firstFetchAt)
	assert.Greater(t, declarationAt, formKeyAt, "resolved after the data-form-key check")
	assert.Less(t, declarationAt, firstFetchAt, "resolved before the first fetchJSON call")

	// Every submission carries the closure's key; a keyless render sends none.
	submit := jsFunction(t, script, "handleSubmit")
	assert.Regexp(t, regexp.MustCompile(`if \(idempotencyKey\) \{\s*body\.idempotency_key = idempotencyKey;\s*\}`), submit)
	assert.Equal(t, 1, strings.Count(script, "body.idempotency_key = idempotencyKey"))
	assert.NotContains(t, submit, "resolveIdempotencyKey(")
	assert.NotContains(t, submit, "getRandomValues")
}

func TestFormEmbedScriptIdempotencyKeyNoWeakFallback(t *testing.T) {
	script := servedEmbedScript(t)
	assert.NotContains(t, script, "Math.random")
	assert.NotContains(t, script, "randomUUID")
}

func TestFormEmbedScriptIdempotencyKeyNeverLogged(t *testing.T) {
	script := servedEmbedScript(t)

	calls := 0
	for offset := 0; ; {
		at := strings.Index(script[offset:], "console.")
		if at == -1 {
			break
		}
		at += offset
		open := strings.Index(script[at:], "(")
		require.NotEqual(t, -1, open, "console call at %d has arguments", at)
		open += at
		end := matchClosing(script, open)
		require.NotEqual(t, -1, end, "console call at %d closes", at)
		call := script[at : end+1]
		assert.NotContains(t, call, "idempotencyKey")
		assert.NotContains(t, call, "idempotency_key")
		calls++
		offset = end + 1
	}
	assert.Positive(t, calls, "the scan saw the script's console calls")
}

// embedKeyHarness evaluates the extracted resolveIdempotencyKey declaration
// with only `window` in scope (new Function), so a reference to anything else
// of the closure would throw, and prints the results as JSON.
const embedKeyHarness = `'use strict';
var fs = require('fs');
var nodeCrypto = require('crypto');
var source = fs.readFileSync(process.argv[2], 'utf8');
var win = {};
var resolve = new Function('window', source + '\nreturn resolveIdempotencyKey;')(win);
function tag(value) {
  return { getAttribute: function (name) { return name === 'data-idempotency-key' ? value : null; } };
}
var deterministic = {
  getRandomValues: function (bytes) {
    for (var i = 0; i < bytes.length; i++) { bytes[i] = i; }
    return bytes;
  }
};
var out = { attribute: {}, fallback: {} };
win.crypto = deterministic;
var valid = ['abc', 'A-Z_0.9:x', 'x'.repeat(128), 'Submission-1', '0123456789abcdef0123456789abcdef'];
valid.forEach(function (v) { out.attribute[v] = resolve(tag(v)); });
out.fallback.absent = resolve(tag(null));
out.fallback.empty = resolve(tag(''));
out.fallback.padded = resolve(tag(' abc '));
out.fallback.long = resolve(tag('x'.repeat(129)));
out.fallback.reserved = resolve(tag('submission-x'));
out.fallback.invalidChar = resolve(tag('a/b'));
win.crypto = nodeCrypto.webcrypto;
out.real1 = resolve(tag(null));
out.real2 = resolve(tag(null));
delete win.crypto;
out.noCrypto = resolve(tag(null));
win.crypto = {};
out.noGetRandomValues = resolve(tag(null));
win.crypto = { getRandomValues: function () { throw new Error('unavailable'); } };
out.throws = resolve(tag(null));
process.stdout.write(JSON.stringify(out));
`

type embedKeyResults struct {
	Attribute         map[string]string `json:"attribute"`
	Fallback          map[string]string `json:"fallback"`
	Real1             string            `json:"real1"`
	Real2             string            `json:"real2"`
	NoCrypto          string            `json:"noCrypto"`
	NoGetRandomValues string            `json:"noGetRandomValues"`
	Throws            string            `json:"throws"`
}

func TestFormEmbedScriptIdempotencyKeyBehaviour(t *testing.T) {
	script := servedEmbedScript(t)
	resolver := jsFunction(t, script, "resolveIdempotencyKey")

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required to run the embed script's key function: %v", err)
	}

	dir := t.TempDir()
	functionPath := filepath.Join(dir, "resolver.js")
	harnessPath := filepath.Join(dir, "harness.js")
	require.NoError(t, os.WriteFile(functionPath, []byte(resolver), 0o600))
	require.NoError(t, os.WriteFile(harnessPath, []byte(embedKeyHarness), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, harnessPath, functionPath)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("node harness failed: %v\n%s", err, stderr)
	}

	var results embedKeyResults
	require.NoError(t, json.Unmarshal(output, &results), "harness output: %s", output)

	// A valid attribute is returned exactly as read.
	require.Len(t, results.Attribute, 5)
	for value, got := range results.Attribute {
		assert.Equal(t, value, got, "attribute %q", value)
	}

	// Anything else falls back to 16 random bytes as lowercase hex.
	const vector = "000102030405060708090a0b0c0d0e0f"
	require.Len(t, vector, 32)
	for _, name := range []string{"absent", "empty", "padded", "long", "reserved", "invalidChar"} {
		assert.Equal(t, vector, results.Fallback[name], "fallback for the %s attribute", name)
	}

	hex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	assert.Regexp(t, hex, results.Real1)
	assert.Regexp(t, hex, results.Real2)
	assert.NotEqual(t, results.Real1, results.Real2, "real randomness gives distinct keys")

	// No secure source: no key at all.
	assert.Equal(t, "", results.NoCrypto)
	assert.Equal(t, "", results.NoGetRandomValues)
	assert.Equal(t, "", results.Throws)
}
