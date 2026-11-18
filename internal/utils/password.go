package utils

import (
	"fmt"
	"unicode"
)

// MaxPasswordBytes is the longest password bcrypt can hash, in bytes.
const MaxPasswordBytes = 72

// ValidateNewPassword is the check of every route that accepts a new
// password: at most MaxPasswordBytes bytes (bytes, not characters), then the
// complexity policy. The length comes first, so a password that is both too
// long and too weak is answered with the length.
func ValidateNewPassword(password string) error {
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes long", MaxPasswordBytes)
	}
	return ValidatePasswordComplexity(password)
}

// ValidatePasswordComplexity checks that a password meets minimum complexity requirements:
// - At least 10 characters long
// - At least 1 uppercase letter
// - At least 1 lowercase letter
// - At least 1 digit
// - At least 1 special character
func ValidatePasswordComplexity(password string) error {
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters long")
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSpecial = true
		}
	}

	if !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	if !hasSpecial {
		return fmt.Errorf("password must contain at least one special character")
	}

	return nil
}
