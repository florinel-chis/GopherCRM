// Package idkit and its subpackages hold the identity layer: OIDC sign-in
// over go-oidc and x/oauth2, the Entra rules, the mapping from claims to
// roles and the SCIM protocol.
//
// The host application provides storage, sessions and configuration through
// interfaces. No package under internal/idkit may depend on gin, gorm,
// internal/models, internal/repository, internal/utils (the global
// utils.Logger) or logrus, directly or through other packages;
// TestIDKitImportBoundary enforces this.
//
// host.go declares those interfaces: IdentityStore, AccountStore,
// AttemptStore, SessionIssuer, AuditSink, ConfigSource, TxRunner, Clock and
// Logger, with the types and errors they use. IdentityStore, AccountStore and
// SessionIssuer join a transaction carried by ctx; AttemptStore, AuditSink
// and ConfigSource run only outside one.
//
// Package sso is the OpenID Connect sign-in over those interfaces: the start
// and the callback, the token and claim checks, the role mapping and the
// rule that decides which account an identity signs in as.
//
// Package scim/server is the SCIM 2.0 provider over net/http on the
// protocol package scim: the routes, the lenient body reading, the mapping of
// its error sentinels to SCIM answers and the rule that decides which
// accounts the provider manages (Manages, ExternalID). The host implements
// its Users, Groups and Logger interfaces, authenticates requests in front
// of it and mounts it as an http.Handler.
//
// Two test kits sit next to the layer: oidctest, an in-process OpenID
// Connect issuer, and scim/scimtest, a SCIM client; only tests import them.
package idkit
