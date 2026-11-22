// Package entra holds the Microsoft Entra ID rules of the identity layer: the
// issuer host and tenant segment of an Entra ID issuer URL, the tenant and
// object id link hint, and the Microsoft Graph resolution of a group overage
// (getMemberObjects) with its limits: one deadline over every page, a page
// count, a page size, and every request on the Graph origin over https.
//
// The package trusts one tenant, the one of the configured issuer. It logs
// nothing, reads no environment and defines no refusal codes: the host
// chooses the codes it answers with and does its own logging.
package entra
