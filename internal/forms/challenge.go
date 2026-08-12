// Package forms carries the stateless building blocks of the public form
// submission pipeline: the signed time-trap challenge and the captcha verifier.
package forms

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrChallengeInvalid reports a challenge that was not issued by this server:
// malformed, truncated, re-signed with a different key or edited in flight.
// Callers translate it into a 400 — it is a client fault, not a spam signal.
var ErrChallengeInvalid = errors.New("challenge invalid")

// challengeSeparator splits the encoded timestamp from its signature.
const challengeSeparator = "."

// NewChallenge issues a challenge that pins the moment a form definition was
// handed out. It carries no secret material of its own: the payload is the
// issue time in plain sight and the signature only makes it unforgeable, so a
// client can neither backdate nor postdate its submission.
func NewChallenge(secret []byte, now time.Time) string {
	issuedAt := strconv.FormatInt(now.Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(issuedAt)) +
		challengeSeparator +
		hex.EncodeToString(signChallenge(secret, issuedAt))
}

// ChallengeAge returns how long ago the challenge was issued. The result is
// negative when the challenge is dated in the future, which happens with clock
// skew between processes; deciding what is too young or too old belongs to the
// caller, which owns the thresholds.
func ChallengeAge(secret []byte, challenge string, now time.Time) (time.Duration, error) {
	encodedTime, signature, found := strings.Cut(challenge, challengeSeparator)
	if !found || encodedTime == "" || signature == "" || strings.Contains(signature, challengeSeparator) {
		return 0, ErrChallengeInvalid
	}

	// Accept both padded and unpadded base64url: the signature covers the
	// timestamp itself, so the transport encoding is free to vary.
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encodedTime, "="))
	if err != nil {
		return 0, ErrChallengeInvalid
	}
	issuedAt := string(payload)
	unix, err := strconv.ParseInt(issuedAt, 10, 64)
	if err != nil {
		return 0, ErrChallengeInvalid
	}

	presented, err := hex.DecodeString(signature)
	if err != nil {
		return 0, ErrChallengeInvalid
	}
	if !hmac.Equal(presented, signChallenge(secret, issuedAt)) {
		return 0, ErrChallengeInvalid
	}

	return now.Sub(time.Unix(unix, 0)), nil
}

func signChallenge(secret []byte, issuedAt string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(issuedAt))
	return mac.Sum(nil)
}
