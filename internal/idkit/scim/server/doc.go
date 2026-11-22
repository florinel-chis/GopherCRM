// Package server is the SCIM 2.0 provider over net/http: the routes under
// scim.BasePath, the reading of request bodies, the mapping of errors to SCIM
// answers and the rule that decides which accounts the provider serves.
//
// The host supplies the accounts and groups through the Users and Groups
// interfaces and a Logger, builds a Server with New and mounts it as an
// http.Handler. The host authenticates every request in front of the
// server (a bearer token, its limits and the record of refusals); the
// server never sees a credential. Resource locations lie under
// Options.BaseURL.
//
// Bodies are read leniently, the way identity providers such as Entra ID
// send them: any Content-Type, exactly one JSON object of at most
// MaxBodyBytes, and every attribute the provider does not support ignored.
// A body that is not readable answers 400 invalidSyntax without a call to
// the host.
//
// No answer and no log line holds a request value: error details are fixed
// texts, and the Logger receives fixed messages and fields only, with the
// host's own error.
package server
