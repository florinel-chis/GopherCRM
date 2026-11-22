package oidc

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// HTTPTimeout is the timeout of every request of the issuer client.
const HTTPTimeout = 10 * time.Second

// NewHTTPClient builds the client that talks to an issuer: discovery, JWKS
// and the code exchange. It has a timeout, never follows a redirect and
// ignores HTTP(S)_PROXY. rootCAs nil means the system roots.
func NewHTTPClient(rootCAs *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if rootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:   HTTPTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
