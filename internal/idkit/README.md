# idkit

`internal/idkit` is the identity layer of GopherCRM: OpenID Connect sign-in over
[go-oidc](https://github.com/coreos/go-oidc) and
[x/oauth2](https://pkg.go.dev/golang.org/x/oauth2), the mapping from claims to roles, the
Entra ID additions and a SCIM 2.0 server. The application that embeds it (the host) provides
storage, sessions, configuration and logging through interfaces, so nothing in the layer knows
about a database, a web framework or the host's models. The package comment in
`internal/idkit/doc.go` is the short version of this file.

## Packages

- `internal/idkit`: the host interfaces and their types and errors (`internal/idkit/host.go`).
- `internal/idkit/oidc`: generic OpenID Connect client helpers: the issuer HTTP client, the
  cached discovered provider, the state, nonce and PKCE values, the code exchange, the claim
  rules and the sign-in error types (`internal/idkit/oidc/errors.go` `oidc.RefusalError`).
- `internal/idkit/entra`: the Entra ID rules: the issuer and tenant checks, the oid/tid link
  hint (`internal/idkit/entra/link.go` `entra.LinkHint`) and the Microsoft Graph group-overage
  resolution.
- `internal/idkit/rolemap`: the first-match mapping from a groups or roles claim to a role id
  (`internal/idkit/rolemap/rolemap.go` `rolemap.Match`).
- `internal/idkit/sso`: the sign-in itself, over the host interfaces
  (`internal/idkit/sso/service.go` `sso.Service`).
- `internal/idkit/scim`: the SCIM wire shapes, filters and PatchOp parsers, with no state.
- `internal/idkit/scim/server`: the SCIM 2.0 server over `net/http`
  (`internal/idkit/scim/server/server.go` `server.Server`).
- `internal/idkit/oidctest`: an in-process OpenID Connect issuer for tests.
- `internal/idkit/scim/scimtest`: a SCIM client for tests.
- `internal/idkit/example`: a runnable host over in-memory stores (see Example).

No non-test package under `internal/idkit` may reach gin, gorm, `internal/models`,
`internal/repository`, `internal/utils` or logrus, directly or through other packages;
`internal/idkit/boundary_test.go` `TestIDKitImportBoundary` enforces this.

## Host interfaces

`internal/idkit/host.go` declares what the layer needs from the host. Account and attempt ids
cross the boundary as canonical decimal strings.

- `idkit.IdentityStore` (`internal/idkit/host.go`): the links between an (issuer, subject)
  pair and an account.
- `idkit.AccountStore` (`internal/idkit/host.go`): signs an existing account in with the
  mapped role, or provisions a new one; its refusals are `oidc.RefusalError` values, and
  `sso.RefuseAccount` (`internal/idkit/sso/codes.go`) attaches the account a refusal concerns.
- `idkit.SessionIssuer` (`internal/idkit/host.go`): issues the session of a signed-in account.
- `idkit.AttemptStore` (`internal/idkit/host.go`): the started sign-ins; a claim is single use.
- `idkit.AuditSink` (`internal/idkit/host.go`): records the outcome of every callback.
- `idkit.ConfigSource` (`internal/idkit/host.go`): the sign-in configuration, re-read on every
  call.
- `idkit.TxRunner` (`internal/idkit/host.go`): runs one transaction and carries it in the ctx.
- `idkit.Clock` and `idkit.Logger`.

`IdentityStore`, `AccountStore` and `SessionIssuer` join the transaction a ctx carries.
`AttemptStore`, `AuditSink` and `ConfigSource` run only outside one and refuse a ctx that
carries one (`idkit.ErrInTransaction`; the audit sink writes nothing), so a spent attempt is
never made usable again by a rollback and an audit record survives a failed sign-in.

## Sign-in

`Service.Start` (`internal/idkit/sso/service.go`) stores one single-use attempt (the hash of the
state, the nonce, the PKCE verifier and the issuer) and returns the authorization URL with the
raw state, which the host hands to the browser as its binding (a cookie, for instance).
`Service.Callback` (`internal/idkit/sso/service.go`) then:

1. spends the attempt, outside any transaction, and refuses a missing or different binding and
   an attempt started against another issuer;
2. discovers the issuer, exchanges the code, verifies the ID token and its nonce, and checks
   the claims, the subject and the issuer;
3. reads groups from the userinfo endpoint when a groups mapping is configured and the token
   carries none (and, on Entra ID only, overage groups from Microsoft Graph), and maps the
   groups or roles to a role; no matching rule is the refusal `no_mapping_match`;
4. runs one `TxRunner` transaction: the link rule, the sign-in or provisioning through the
   `AccountStore`, the identity link and the session;
5. records the audit event after the transaction and logs the outcome.

The link rule is `Service.signInAccount` (`internal/idkit/sso/link.go`):

- The exact (issuer, subject) pair signs in as its account; a stored row that differs byte for
  byte is refused.
- Under any issuer, a SCIM `externalId` equal to the ID token's `sub` therefore signs in as that
  account: a SCIM create stores the identity (configured issuer, `externalId`, source `scim`).
- The Entra ID oid+tid link applies only to Entra ID issuers, and only to identities of source
  `scim` under the same issuer; it then links the token's pair to that account.
- Any other unknown pair provisions a new account.
- Email never links an identity to an existing account.

The refusal and unavailable codes the sign-in decides are the `sso.CodeStateInvalid` family
(`internal/idkit/sso/codes.go`); the host's `AccountStore` adds its own (`account_inactive`,
`email_taken` and the like).

## SCIM server

`server.New` (`internal/idkit/scim/server/server.go`) builds a `server.Server` over the host's
`server.Users`, `server.Groups` and `server.Logger`. It is an `http.Handler` the host mounts
under `scim.BasePath` (`internal/idkit/scim/scim.go`); `server.Routes`
(`internal/idkit/scim/server/server.go`) lists what it answers.

- The host authenticates every request in front of the server (a bearer token, its limits and
  the record of refusals). The server never sees a credential.
- The server serves managed accounts only: `server.Manages`
  (`internal/idkit/scim/server/managed.go`) decides which accounts those are (an identity
  under the configured issuer), and `server.ExternalID`
  (`internal/idkit/scim/server/managed.go`) gives their `externalId`.
- A Users or Groups refusal is one of the sentinels in `internal/idkit/scim/server/errors.go`
  (`server.ErrNotFound` and the rest); anything else answers 500 and goes to the `Logger`.
- Groups are the groups-claim values of the role mapping; they are listed and their members
  patched, never created or deleted as new values.

## Issuers

The layer is written for a generic OpenID Connect issuer first: discovery, the authorization
code flow with PKCE, ID token verification by go-oidc and a groups or roles claim, read from
the userinfo endpoint when the token leaves it out. Nothing provider-specific runs for such an
issuer.

The Entra ID additions (the oid+tid link hint, the Microsoft Graph group overage, the Graph
scope and the application signing keys) apply only when the issuer's host is the Entra ID host,
`sso.Options` (`internal/idkit/sso/service.go`) field `EntraIssuerHost`, which defaults to
`entra.IssuerHost` (`internal/idkit/entra/issuer.go`).

## Logging

The sign-in logs only through `idkit.Logger`, with fixed messages and the `attempt_id`,
`error_code`, `audit_id` and `user_id` fields. The SCIM server logs only through
`server.Logger`, with a fixed message, a component field and the host's own error, which the
host redacts. Neither passes a code, state, nonce, verifier, token, secret, email, subject,
name, claim value, URL or body. A host keeps the same rule in its own log lines.

## Test kits

- `oidctest.New` (`internal/idkit/oidctest/issuer.go`) starts an in-process TLS issuer with
  discovery, token, JWKS and userinfo endpoints and no provider-specific code; `SetPath` sets
  the issuer path, and `Authorize` plays the browser and the login page.
- `scimtest.Client` (`internal/idkit/scim/scimtest/client.go`) sends SCIM requests straight to
  an `http.Handler` with a bearer token and decodes the answers and SCIM error bodies.

`oidctest.New` takes a `testing.TB`, so only test files import the kits.

## Example

`internal/idkit/example` is a small host over in-memory, mutex-guarded stores: one
implementation of every host interface and of the SCIM server's `Users` and `Groups`, each
keeping its interface's contract (the transaction runner undoes every write of a failed
transaction, for instance). Its app serves `POST /auth/start`, `POST /auth/callback` and the
SCIM server behind a bearer token. The tests drive it end to end against `oidctest`, shaped as a
generic issuer with a groups claim, through `scimtest`:

```sh
go test ./internal/idkit/example
```

## Limits

- The layer stays a thin layer over go-oidc and x/oauth2; it does not implement the protocol
  itself.
- It lives inside the GopherCRM module, in `internal/idkit`, with no module of its own.
- It ships no storage implementation: the host provides every store.
- The SCIM request parsing is the layer's own.
- Each install trusts one issuer and, on Entra ID, one tenant: the configured one.

## Licence

MIT, like the rest of the repository (`LICENSE`). The layer depends on go-oidc and go-jose
(Apache-2.0) and x/oauth2 (BSD-3-Clause); the issuer test kit also uses golang-jwt (MIT).
