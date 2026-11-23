// Package sso is the OpenID Connect sign-in over the host interfaces of
// package idkit: the authorization code flow with PKCE, the ID token and
// claim checks, the mapping from claims to a role (rolemap), the Entra ID
// additions (entra) and the rule that decides which account an identity
// signs in as.
//
// Start stores one single-use attempt through idkit.AttemptStore and returns
// the authorization URL with the raw state, which the host hands to the
// browser as its binding. Callback, in this order:
//
//  1. spends the attempt (its own committed statement, outside any
//     transaction), then refuses a missing or different binding and an
//     attempt started against another issuer;
//  2. discovers the issuer, exchanges the code, verifies the ID token and
//     its nonce, and checks the claims, the subject and the issuer;
//  3. reads overage groups from Microsoft Graph or missing groups from the
//     userinfo endpoint, and maps the groups or roles to a role;
//  4. runs one idkit.TxRunner transaction: the link rule over
//     idkit.IdentityStore, the sign-in or provisioning through
//     idkit.AccountStore, the identity link and the session through
//     idkit.SessionIssuer;
//  5. records the audit event through idkit.AuditSink after the
//     transaction and logs the outcome with the event id.
//
// The link rule: the exact (issuer, subject) pair names the account; an
// unknown pair links to an account only through the Entra ID link hint, and
// only to an identity the provider created (idkit.IdentitySourceSCIM) under
// the same issuer; any other unknown pair provisions an account. Email never
// links.
//
// Sign-out (logout.go): EndSessionURL builds the provider's end-session URL
// for an ID token hint of the signed-out account, and FrontChannelLogout ends
// the sessions of a provider session through idkit.SessionEnder.
//
// Every refusal is an oidc.RefusalError whose code is one of the Code
// constants or a code of the host's AccountStore; RefuseAccount attaches the
// account a refusal concerns. The log lines go only through idkit.Logger,
// with fixed messages and the attempt_id, error_code, audit_id, user_id and
// ended fields, never a code, state, nonce, verifier, token, sid, issuer,
// email, subject, name, claim value or URL.
package sso
