package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
)

// Access-token revocation (#236). Every access JWT carries the owner's
// users.token_version as the integer claim `token_version`. Logout, a password
// change and a password-reset confirmation increment the stored counter, so
// every JWT signed before them no longer matches and ValidateToken refuses it
// with ErrAccessTokenRevoked. middleware.Auth answers that like any other
// rejected credential: 401 "Invalid credentials". Refresh rotation and login
// leave the counter alone.
//
// These declarations live here, not in the sentinel block of auth_service.go,
// so that the line citations of that file in the documentation stay valid.

// ErrAccessTokenRevoked rejects an access JWT whose token_version claim is not
// the owner's current users.token_version: the session it belonged to was
// ended by a logout, a password change or a password reset.
var ErrAccessTokenRevoked = errors.New("access token has been revoked")

// errInvalidTokenVersionClaim rejects a token_version claim that is present
// but not a non-negative integral JSON number of at most 2^53-1.
var errInvalidTokenVersionClaim = errors.New("invalid token_version in token claims")

// tokenVersionClaim is the name of the access JWT claim that carries the
// owner's users.token_version.
const tokenVersionClaim = "token_version"

// maxTokenVersionClaim is the largest token_version a claim may carry:
// 2^53-1, the largest integer every JSON implementation represents exactly.
// A float64 holds it exactly, so the comparison below is exact.
const maxTokenVersionClaim = 1<<53 - 1

// accessTokenVersion reads the token_version claim of a verified access JWT.
// An absent claim reads as 0, the value every account starts with, so a token
// signed before the claim existed stays valid until the first increment of
// its owner. Any other shape is rejected before a conversion to uint is
// attempted: a value that is not a JSON number (a string, a boolean, null, an
// object), a non-integral or negative number, and one above 2^53-1.
func accessTokenVersion(claims jwt.MapClaims) (uint, error) {
	raw, present := claims[tokenVersionClaim]
	if !present {
		return 0, nil
	}
	switch value := raw.(type) {
	case float64:
		return tokenVersionFromFloat(value)
	case json.Number:
		// Only produced by a parser configured with WithJSONNumber; handled so
		// such a configuration cannot bypass the range checks.
		if n, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			if n < 0 || n > maxTokenVersionClaim {
				return 0, errInvalidTokenVersionClaim
			}
			return uint(n), nil
		}
		f, err := value.Float64()
		if err != nil {
			return 0, errInvalidTokenVersionClaim
		}
		return tokenVersionFromFloat(f)
	default:
		return 0, errInvalidTokenVersionClaim
	}
}

func tokenVersionFromFloat(value float64) (uint, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) ||
		value < 0 || value > maxTokenVersionClaim {
		return 0, errInvalidTokenVersionClaim
	}
	return uint(value), nil
}

// checkAccessTokenVersion compares the claim with the owner's stored counter.
func checkAccessTokenVersion(claimed, stored uint) error {
	if claimed != stored {
		return fmt.Errorf("token_version is no longer current: %w", ErrAccessTokenRevoked)
	}
	return nil
}
