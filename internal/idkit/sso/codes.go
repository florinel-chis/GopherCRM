package sso

import (
	"errors"

	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
)

// Refusal codes the sign-in decides. A refusal is an idkitoidc.RefusalError
// with one of these codes (ErrSignInFailed to errors.Is); the code is the
// reason of the audit event and the error_code of the log line. The host's
// AccountStore adds its own codes (account_erased, account_inactive,
// customer_login_role, role_missing, last_admin, email_missing, email_taken).
const (
	CodeStateInvalid            = "state_invalid"
	CodeBindingMissing          = "binding_missing"
	CodeBindingMismatch         = "binding_mismatch"
	CodeIssuerChanged           = "issuer_changed"
	CodeIDTokenMissing          = "id_token_missing"
	CodeIDTokenInvalid          = "id_token_invalid"
	CodeNonceMismatch           = "nonce_mismatch"
	CodeClaimsInvalid           = "claims_invalid"
	CodeGroupOverage            = "group_overage"
	CodeGroupsMalformed         = "groups_malformed"
	CodeRolesMalformed          = "roles_malformed"
	CodeSubjectInvalid          = "subject_invalid"
	CodeIssuerInvalid           = "issuer_invalid"
	CodeGroupsUnavailable       = "groups_unavailable"
	CodeUserInfoSubjectMismatch = "userinfo_subject_mismatch"
	CodeNoMappingMatch          = "no_mapping_match"
	CodeIdentityMismatch        = "identity_mismatch"
	CodeIdentityTaken           = "identity_taken"
)

// Unavailable codes: an idkitoidc.UnavailableError with one of these codes
// reads and compares as ErrUnavailable.
const (
	CodeNotConfigured       = "not_configured"
	CodeDiscoveryFailed     = "discovery_failed"
	CodeTokenExchangeFailed = "token_exchange_failed"
)

// The method, outcomes and the two reasons of an audit event that are not a
// refusal or unavailable code.
const (
	auditMethodOIDC     = "oidc"
	auditOutcomeSuccess = "success"
	auditOutcomeRefused = "refused"
	reasonSSODisabled   = "sso_disabled"
	reasonInternalError = "internal_error"
)

// refuse and refuseWith build a refusal with its code: ErrSignInFailed to
// errors.Is, and also the cause it wraps, if any.
func refuse(code string) error { return &idkitoidc.RefusalError{Code: code} }

func refuseWith(code string, cause error) error {
	return &idkitoidc.RefusalError{Code: code, Cause: cause}
}

// unavailable builds an ErrUnavailable with its code.
func unavailable(code string) error { return &idkitoidc.UnavailableError{Code: code} }

// accountRefusal is a refusal that concerns an existing account. It reads
// and compares as the refusal it wraps.
type accountRefusal struct {
	err       error
	accountID string
}

func (e *accountRefusal) Error() string { return e.err.Error() }

func (e *accountRefusal) Unwrap() error { return e.err }

// RefuseAccount attaches the id of the account a refusal concerns, for the
// audit event. err is returned as it is when it is not a refusal (no
// idkitoidc.RefusalError in its chain) or accountID is "".
func RefuseAccount(err error, accountID string) error {
	var refusal *idkitoidc.RefusalError
	if accountID == "" || !errors.As(err, &refusal) {
		return err
	}
	return &accountRefusal{err: err, accountID: accountID}
}

// RefusedAccount is the account id RefuseAccount attached to err, or "".
func RefusedAccount(err error) string {
	var account *accountRefusal
	if errors.As(err, &account) {
		return account.accountID
	}
	return ""
}

// auditReason is the reason of the audit event of a failed callback and the
// account it concerns ("" when unknown): a refusal's or an unavailable
// error's code, sso_disabled for ErrDisabled, internal_error for anything
// else.
func auditReason(err error) (reason, accountID string) {
	var refusal *idkitoidc.RefusalError
	if errors.As(err, &refusal) {
		return refusal.Code, RefusedAccount(err)
	}
	var notAvailable *idkitoidc.UnavailableError
	if errors.As(err, &notAvailable) {
		return notAvailable.Code, ""
	}
	if errors.Is(err, idkitoidc.ErrDisabled) {
		return reasonSSODisabled, ""
	}
	return reasonInternalError, ""
}
