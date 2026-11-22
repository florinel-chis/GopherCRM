package oidc

import "errors"

// Errors of a sign-in, classified with errors.Is.
var (
	// ErrDisabled: single sign-on is switched off.
	ErrDisabled = errors.New("single sign-on is disabled")
	// ErrUnavailable: single sign-on is on but cannot be used right now.
	ErrUnavailable = errors.New("single sign-on is unavailable")
	// ErrSignInFailed covers every refusal of a callback.
	ErrSignInFailed = errors.New("single sign-on failed")
)

// RefusalError is a refusal with the code it is recorded under. It is
// ErrSignInFailed to errors.Is, and also the cause it wraps, if any.
type RefusalError struct {
	Code  string
	Cause error
}

func (e *RefusalError) Error() string { return "single sign-on failed: " + e.Code }

func (e *RefusalError) Is(target error) bool { return target == ErrSignInFailed }

func (e *RefusalError) Unwrap() error { return e.Cause }

// UnavailableError is an ErrUnavailable with the code it is recorded under.
// It reads and compares as ErrUnavailable.
type UnavailableError struct {
	Code string
}

func (e *UnavailableError) Error() string { return ErrUnavailable.Error() }

func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// RefusalCode is the code of the RefusalError in err's chain, "" when there
// is none.
func RefusalCode(err error) string {
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

// UnavailableCode is the code of the UnavailableError in err's chain, ""
// when there is none.
func UnavailableCode(err error) string {
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Code
	}
	return ""
}
