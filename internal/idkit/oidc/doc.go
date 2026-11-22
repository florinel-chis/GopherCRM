// Package oidc holds the generic OpenID Connect client helpers of the
// identity layer, over go-oidc and x/oauth2: the hardened issuer HTTP client,
// the cached discovered provider, the state, nonce and PKCE values of an
// authorization request, the client secret or certificate (private_key_jwt)
// code exchange, the claim extraction and subject and name rules, and the
// stable error types of a sign-in.
//
// The package logs nothing and defines no refusal codes: the host chooses the
// codes it puts into a RefusalError or an UnavailableError and does its own
// logging.
package oidc
