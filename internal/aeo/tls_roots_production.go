//go:build !gophercrm_e2e

package aeo

const e2eTLSBuild = false

// InstallE2ETestRoots is a no-op outside the gophercrm_e2e build: a normal
// binary never trusts the disposable test CA, whatever the environment says.
func InstallE2ETestRoots(string) error { return nil }
