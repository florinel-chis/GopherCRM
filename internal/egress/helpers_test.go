package egress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test doubles for the guard: a local TLS server standing in for the
// internet, a scripted resolver and a dialer that records every validated
// address. internal/aeo keeps its own copies for the provider tests.

// publicTestAddr is what the scripted resolver answers for trusted hosts. The
// fake dialer never connects to it: it records the validated literal and
// connects to the local TLS server instead.
var publicTestAddr = netip.MustParseAddr("93.184.216.34")

type internetRequest struct {
	Host   string
	Path   string
	Header http.Header
	Body   []byte
}

type internetHandler func(w http.ResponseWriter, r *http.Request, body []byte)

type fakeInternet struct {
	server *httptest.Server
	roots  *x509.CertPool

	mu       sync.Mutex
	handler  internetHandler
	answers  map[string][]netip.Addr
	sequence map[string][][]netip.Addr
	dialErr  error
	lookups  []string
	dials    []string
	requests []internetRequest
}

var fakeInternetHosts = []string{
	"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com",
	"api.moonshot.ai", "api.perplexity.ai", "llm.example.com",
}

func newFakeInternet(t *testing.T, certHosts ...string) *fakeInternet {
	t.Helper()
	if len(certHosts) == 0 {
		certHosts = fakeInternetHosts
	}
	cert, roots := newTestCertificate(t, certHosts)
	fi := &fakeInternet{
		roots:    roots,
		answers:  map[string][]netip.Addr{},
		sequence: map[string][][]netip.Addr{},
	}
	for _, host := range fakeInternetHosts {
		fi.answers[host] = []netip.Addr{publicTestAddr}
	}
	fi.server = httptest.NewUnstartedServer(http.HandlerFunc(fi.serve))
	fi.server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	// Refused handshakes are expected in some tests; keep them off stderr.
	fi.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	fi.server.StartTLS()
	t.Cleanup(fi.server.Close)
	return fi
}

// newTestCertificate issues a self-signed certificate for hosts, trusted only
// through the returned pool. Verification itself is never relaxed.
func newTestCertificate(t *testing.T, hosts []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "AEO egress test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

func (fi *fakeInternet) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fi.mu.Lock()
	fi.requests = append(fi.requests, internetRequest{Host: r.Host, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	handler := fi.handler
	fi.mu.Unlock()
	if handler == nil {
		handler = answerOK
	}
	handler(w, r, body)
}

// answerOK is a plain successful answer.
func answerOK(w http.ResponseWriter, _ *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (fi *fakeInternet) setHandler(handler internetHandler) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.handler = handler
}

func (fi *fakeInternet) reset() {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.handler, fi.dialErr = nil, nil
	fi.lookups, fi.dials, fi.requests = nil, nil, nil
}

// LookupNetIP makes fakeInternet the guard's Resolver.
func (fi *fakeInternet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.lookups = append(fi.lookups, host)
	if queue := fi.sequence[host]; len(queue) > 0 {
		fi.sequence[host] = queue[1:]
		return queue[0], nil
	}
	if addrs, ok := fi.answers[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (fi *fakeInternet) dial(ctx context.Context, _, address string) (net.Conn, error) {
	fi.mu.Lock()
	fi.dials = append(fi.dials, address)
	dialErr := fi.dialErr
	fi.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dialErr != nil {
		return nil, dialErr
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", fi.server.Listener.Addr().String())
}

func (fi *fakeInternet) snapshot() (lookups, dials []string, requests []internetRequest) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return append([]string(nil), fi.lookups...), append([]string(nil), fi.dials...),
		append([]internetRequest(nil), fi.requests...)
}

func assertNoTraffic(t *testing.T, fi *fakeInternet) {
	t.Helper()
	lookups, dials, requests := fi.snapshot()
	assert.Empty(t, lookups, "no DNS lookup may happen for a refused endpoint")
	assert.Empty(t, dials, "no connection may be opened for a refused endpoint")
	assert.Empty(t, requests, "no request may carry the credential")
}

type scriptedResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
	err     error
	hosts   []string
}

func (r *scriptedResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	if r.err != nil {
		return nil, r.err
	}
	index := len(r.hosts) - 1
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	return r.answers[index], nil
}

func (r *scriptedResolver) lookups() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts)
}

type recordingDialer struct {
	mu        sync.Mutex
	addresses []string
	fail      map[string]bool
}

func (d *recordingDialer) dial(_ context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses = append(d.addresses, address)
	if d.fail[address] {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (d *recordingDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...)
}

func parseAddrs(values ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParseAddr(value))
	}
	return out
}
