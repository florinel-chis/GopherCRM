//go:build gophercrm_e2e

package aeo

import (
	"crypto/x509"
	"os"
)

const e2eTLSBuild = true

func init() {
	if os.Getenv("GOPHERCRM_E2E_AEO_TLS") != "1" {
		return
	}
	certificate, err := os.ReadFile(os.Getenv("E2E_AEO_CERT"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(certificate) {
		panic("invalid disposable AEO test CA configuration")
	}
	// run.sh opts into x509usefallbackroots=1 for its child processes. This
	// works on macOS as well as Linux and retains certificate/hostname checks.
	// The production build never installs these roots.
	x509.SetFallbackRoots(roots)
}
