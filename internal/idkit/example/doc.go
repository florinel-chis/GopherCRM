// Package example is a small, runnable host of the identity layer. It shows
// what an application provides to idkit and how it mounts the sign-in and
// the SCIM server, without a database and without GopherCRM.
//
// stores.go holds in-memory implementations of every host interface of
// package idkit (IdentityStore, AccountStore, AttemptStore, SessionIssuer,
// AuditSink, ConfigSource, TxRunner and Logger) and of the SCIM server's
// Users and Groups. Each keeps the contract its interface states: the
// TxRunner rolls back every in-memory write of a failed transaction, the
// stores that run only outside a transaction refuse a ctx that carries one,
// an attempt is spent once, and SCIM serves only the accounts
// server.Manages accepts.
//
// app.go is a net/http application over those stores. App serves
//
//   - POST /auth/start: the authorization URL, with the raw state set as
//     the browser binding cookie;
//   - POST /auth/callback: the code and state the issuer redirected back
//     with, answered with the account id and an access token, or with the
//     refusal code;
//   - everything under scim.BasePath: the SCIM 2.0 server, behind a bearer
//     token the app checks before the server sees the request.
//
// The tests drive the app end to end against the in-process issuer of
// package oidctest, shaped as a generic OpenID Connect issuer with a groups
// claim, and the SCIM client of package scimtest. Run them with:
//
//	go test ./internal/idkit/example
package example
