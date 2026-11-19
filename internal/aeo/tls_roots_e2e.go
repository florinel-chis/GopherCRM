//go:build gophercrm_e2e

package aeo

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

const e2eTLSBuild = true

// InstallE2ETestRoots installs the disposable per-run test CA of `make e2e`
// as the process's fallback certificate roots, so the browser-test AEO
// provider fixture and the OpenID Connect test issuer are trusted. It does
// nothing unless GOPHERCRM_E2E_AEO_TLS is "1", and it refuses (installing
// nothing) when serverMode is production, whichever way that mode was set.
// x509.SetFallbackRoots may be called only once per process, so main calls
// this exactly once, before any AEO or OIDC client exists.
func InstallE2ETestRoots(serverMode string) error {
	if os.Getenv("GOPHERCRM_E2E_AEO_TLS") != "1" {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(serverMode)) == "production" {
		return errors.New("refusing the e2e test TLS roots in production mode: unset GOPHERCRM_E2E_AEO_TLS or use a normal build")
	}
	certificate, err := os.ReadFile(os.Getenv("E2E_AEO_CERT"))
	if err != nil {
		return fmt.Errorf("e2e test TLS roots: read E2E_AEO_CERT: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return errors.New("e2e test TLS roots: E2E_AEO_CERT holds no PEM certificate")
	}
	// run.sh opts into x509usefallbackroots=1 for its child processes. This
	// works on macOS as well as Linux and retains certificate/hostname checks.
	// The production build never installs these roots.
	x509.SetFallbackRoots(roots)
	return nil
}
