package entra

import "github.com/florinel-chis/gophercrm/internal/idkit/oidc"

// LinkHint is the object id under which a token may be linked to an
// account provisioned for the configured issuer, or "". It returns oid only
// when the issuer URL's host is host (IsIssuer), tid is non-empty and equals
// the issuer's tenant segment (Tenant) byte for byte, and oid is a valid
// subject (oidc.ValidSubject). tid and oid are the decoded string claims
// (oidc.String), so a claim that is not a string arrives as "" and gives no
// hint.
func LinkHint(issuerURL, host, tid, oid string) string {
	if !IsIssuer(issuerURL, host) {
		return ""
	}
	if tid != "" && tid == Tenant(issuerURL) && oidc.ValidSubject(oid) {
		return oid
	}
	return ""
}
