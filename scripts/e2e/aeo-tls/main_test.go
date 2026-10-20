package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTLSFixtureSupportsVerifiedLoopback(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, writeFixture(directory))
	certPath, keyPath := filepath.Join(directory, "certificate.pem"), filepath.Join(directory, "private-key.pem")
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	require.NoError(t, err)
	pemData, err := os.ReadFile(certPath)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pemData))
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), keyInfo.Mode().Perm())

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()

	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.NotEmpty(t, response.TLS.VerifiedChains)

	wrongHost := transport.Clone()
	wrongHost.TLSClientConfig.ServerName = "untrusted.example.test"
	defer wrongHost.CloseIdleConnections()
	_, err = (&http.Client{Transport: wrongHost}).Get(server.URL)
	var hostnameError x509.HostnameError
	require.True(t, errors.As(err, &hostnameError), "a wrong hostname must fail certificate verification: %v", err)
}
