// Package oidctest is an in-process OpenID Connect issuer for tests.
//
// New starts an httptest TLS server that serves discovery, a JWKS, the
// authorization code exchange (client secret by HTTP Basic or in the form,
// or private_key_jwt against a registered certificate, always with PKCE
// S256) and a userinfo endpoint. Authorize plays the browser and the login
// page: it checks the authorization URL and returns a code for the claims
// the ID token should carry. Switches change what the endpoints answer
// (forced token errors, a redirected discovery, key rotation, userinfo
// modes) and counters record what they received, so a test can assert both
// the client's behaviour and its traffic.
//
// The issuer knows no provider-specific behaviour. A host adds its own
// endpoints with Handle, looks up the subject behind an access token with
// AccessSubject, and publishes extra keys depending on the JWKS request with
// SetJWKSHook.
//
// Only tests import this package.
package oidctest
