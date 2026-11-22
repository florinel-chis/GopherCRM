// Package idkit and its subpackages hold the identity layer: OIDC sign-in
// over go-oidc and x/oauth2, the Entra rules, the mapping from claims to
// roles and the SCIM protocol.
//
// The host application provides storage, sessions and configuration through
// interfaces. No package under internal/idkit may depend on gin, gorm,
// internal/models, internal/repository, internal/utils (the global
// utils.Logger) or logrus, directly or through other packages;
// TestIDKitImportBoundary enforces this.
package idkit
