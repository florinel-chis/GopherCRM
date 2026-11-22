package server

import "github.com/florinel-chis/gophercrm/internal/idkit"

// Manages reports whether the provider serves the account the identities
// belong to: issuer is not empty and one identity's Issuer equals it byte for
// byte. A case variant, a trailing space or another issuer is not managed;
// the host's storage may compare more loosely, so the host passes the rows
// as stored and lets this judge decide.
func Manages(issuer string, identities []idkit.Identity) bool {
	if issuer == "" {
		return false
	}
	for _, identity := range identities {
		if identity.Issuer == issuer {
			return true
		}
	}
	return false
}

// ExternalID is the externalId of the account the identities belong to: the
// Subject of the first identity whose Issuer equals issuer byte for byte. The
// host passes the identities in ascending row-id order, so the oldest link
// wins. It returns "" when issuer is empty or no identity matches.
func ExternalID(issuer string, identities []idkit.Identity) string {
	if issuer == "" {
		return ""
	}
	for _, identity := range identities {
		if identity.Issuer == issuer {
			return identity.Subject
		}
	}
	return ""
}
