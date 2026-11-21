package service

import "sort"

// The vocabulary of the sign-in audit log (#549): every method, outcome and
// reason code an auth_audit_log row can carry, declared once. The writers
// pass these constants, and the filter sets of GET /audit/sign-ins are built
// from the lists below, so a code that can be written is always a code the
// list accepts. TestAuthAuditWritersUseRegisteredValues fails when a writer
// passes anything else or a constant is declared but not listed.

// Methods of an auth_audit_log row.
const (
	AuthAuditMethodPassword = "password"
	AuthAuditMethodOIDC     = "oidc"
	AuthAuditMethodRefresh  = "refresh"
	AuthAuditMethodAPIKey   = "api_key"
	AuthAuditMethodSCIM     = "scim"
)

// Outcomes of an auth_audit_log row.
const (
	AuthAuditOutcomeSuccess = "success"
	AuthAuditOutcomeRefused = "refused"
)

// Reason codes of password sign-ins, refreshes, API keys and SCIM tokens.
const (
	AuthAuditReasonInvalidCredentials = "invalid_credentials"
	AuthAuditReasonAccountLocked      = "account_locked"
	AuthAuditReasonAccountInactive    = "account_inactive"
	AuthAuditReasonAccountErased      = "account_erased"
	AuthAuditReasonPasswordChanged    = "password_changed"
	AuthAuditReasonSSOAccount         = "sso_account"
	AuthAuditReasonInternalError      = "internal_error"
	AuthAuditReasonTokenInvalid       = "token_invalid"
	AuthAuditReasonUnavailable        = "unavailable"
	AuthAuditReasonKeyInvalid         = "key_invalid"
	AuthAuditReasonKeyRevoked         = "key_revoked"
	AuthAuditReasonKeyExpired         = "key_expired"
	AuthAuditReasonSSODisabled        = "sso_disabled"
	// A key owned by a customer account (#532); the same code as the single
	// sign-on refusal of a customer login.
	AuthAuditReasonCustomerAccount = "customer_login_role"
	// SCIM refusals (#468); a bad SCIM token reuses token_invalid.
	AuthAuditReasonTokenMissing = "token_missing"
	AuthAuditReasonTokenRevoked = "token_revoked"
)

// Single sign-on refusal codes (oidcRefuse, oidcRefuseWith). They are also
// the error_code of the refusal's log line. An erased, inactive or customer
// account reuses account_erased, account_inactive and customer_login_role.
const (
	AuthAuditReasonBindingMismatch         = "binding_mismatch"
	AuthAuditReasonBindingMissing          = "binding_missing"
	AuthAuditReasonClaimsInvalid           = "claims_invalid"
	AuthAuditReasonEmailMissing            = "email_missing"
	AuthAuditReasonEmailTaken              = "email_taken"
	AuthAuditReasonGroupOverage            = "group_overage"
	AuthAuditReasonGroupsMalformed         = "groups_malformed"
	AuthAuditReasonGroupsUnavailable       = "groups_unavailable"
	AuthAuditReasonIDTokenInvalid          = "id_token_invalid"
	AuthAuditReasonIDTokenMissing          = "id_token_missing"
	AuthAuditReasonIdentityMismatch        = "identity_mismatch"
	AuthAuditReasonIdentityTaken           = "identity_taken"
	AuthAuditReasonIssuerChanged           = "issuer_changed"
	AuthAuditReasonIssuerInvalid           = "issuer_invalid"
	AuthAuditReasonLastAdmin               = "last_admin"
	AuthAuditReasonNoMappingMatch          = "no_mapping_match"
	AuthAuditReasonNonceMismatch           = "nonce_mismatch"
	AuthAuditReasonRoleMissing             = "role_missing"
	AuthAuditReasonRolesMalformed          = "roles_malformed"
	AuthAuditReasonStateInvalid            = "state_invalid"
	AuthAuditReasonSubjectInvalid          = "subject_invalid"
	AuthAuditReasonUserInfoSubjectMismatch = "userinfo_subject_mismatch"
)

// Single sign-on 503 codes (ssoUnavailable).
const (
	AuthAuditReasonNotConfigured       = "not_configured"
	AuthAuditReasonDiscoveryFailed     = "discovery_failed"
	AuthAuditReasonTokenExchangeFailed = "token_exchange_failed"
)

// authAuditMethodList is every method, once.
var authAuditMethodList = []string{
	AuthAuditMethodPassword,
	AuthAuditMethodOIDC,
	AuthAuditMethodRefresh,
	AuthAuditMethodAPIKey,
	AuthAuditMethodSCIM,
}

// authAuditOutcomeList is every outcome, once.
var authAuditOutcomeList = []string{
	AuthAuditOutcomeSuccess,
	AuthAuditOutcomeRefused,
}

// authAuditReasonList is every reason code, once.
var authAuditReasonList = []string{
	AuthAuditReasonInvalidCredentials,
	AuthAuditReasonAccountLocked,
	AuthAuditReasonAccountInactive,
	AuthAuditReasonAccountErased,
	AuthAuditReasonPasswordChanged,
	AuthAuditReasonSSOAccount,
	AuthAuditReasonInternalError,
	AuthAuditReasonTokenInvalid,
	AuthAuditReasonUnavailable,
	AuthAuditReasonKeyInvalid,
	AuthAuditReasonKeyRevoked,
	AuthAuditReasonKeyExpired,
	AuthAuditReasonSSODisabled,
	AuthAuditReasonCustomerAccount,
	AuthAuditReasonTokenMissing,
	AuthAuditReasonTokenRevoked,
	AuthAuditReasonBindingMismatch,
	AuthAuditReasonBindingMissing,
	AuthAuditReasonClaimsInvalid,
	AuthAuditReasonEmailMissing,
	AuthAuditReasonEmailTaken,
	AuthAuditReasonGroupOverage,
	AuthAuditReasonGroupsMalformed,
	AuthAuditReasonGroupsUnavailable,
	AuthAuditReasonIDTokenInvalid,
	AuthAuditReasonIDTokenMissing,
	AuthAuditReasonIdentityMismatch,
	AuthAuditReasonIdentityTaken,
	AuthAuditReasonIssuerChanged,
	AuthAuditReasonIssuerInvalid,
	AuthAuditReasonLastAdmin,
	AuthAuditReasonNoMappingMatch,
	AuthAuditReasonNonceMismatch,
	AuthAuditReasonRoleMissing,
	AuthAuditReasonRolesMalformed,
	AuthAuditReasonStateInvalid,
	AuthAuditReasonSubjectInvalid,
	AuthAuditReasonUserInfoSubjectMismatch,
	AuthAuditReasonNotConfigured,
	AuthAuditReasonDiscoveryFailed,
	AuthAuditReasonTokenExchangeFailed,
}

// The filter values are matched exactly and case-sensitively against these
// sets.
var (
	authAuditMethods  = authAuditSet(authAuditMethodList)
	authAuditOutcomes = authAuditSet(authAuditOutcomeList)
	authAuditReasons  = authAuditSet(authAuditReasonList)
)

func authAuditSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// IsAuthAuditMethod reports whether value is one of the five methods.
func IsAuthAuditMethod(value string) bool { return authAuditMethods[value] }

// IsAuthAuditOutcome reports whether value is success or refused.
func IsAuthAuditOutcome(value string) bool { return authAuditOutcomes[value] }

// IsAuthAuditReason reports whether value is a reason code the service writes.
func IsAuthAuditReason(value string) bool { return authAuditReasons[value] }

// AuthAuditMethods returns the methods, sorted.
func AuthAuditMethods() []string { return authAuditSortedKeys(authAuditMethods) }

// AuthAuditOutcomes returns the outcomes, sorted.
func AuthAuditOutcomes() []string { return authAuditSortedKeys(authAuditOutcomes) }

// AuthAuditReasons returns the reason codes, sorted.
func AuthAuditReasons() []string { return authAuditSortedKeys(authAuditReasons) }

func authAuditSortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
