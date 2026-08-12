package forms

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("challenge-secret-for-unit-tests")

func TestNewChallengeFormat(t *testing.T) {
	issued := time.Unix(1770000000, 0)
	challenge := NewChallenge(testSecret, issued)

	parts := strings.Split(challenge, ".")
	if len(parts) != 2 {
		t.Fatalf("expected two dot-separated parts, got %q", challenge)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("payload is not base64url: %v", err)
	}
	if string(payload) != "1770000000" {
		t.Errorf("payload = %q, want the decimal unix timestamp", payload)
	}
	if len(parts[1]) != 64 {
		t.Errorf("signature length = %d, want 64 hex characters", len(parts[1]))
	}
}

func TestChallengeAgeRoundTrip(t *testing.T) {
	issued := time.Unix(1770000000, 0)
	challenge := NewChallenge(testSecret, issued)

	age, err := ChallengeAge(testSecret, challenge, issued.Add(42*time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if age != 42*time.Second {
		t.Errorf("age = %v, want 42s", age)
	}
}

func TestChallengeAgeIsNegativeForFutureIssue(t *testing.T) {
	issued := time.Unix(1770000000, 0)
	challenge := NewChallenge(testSecret, issued)

	age, err := ChallengeAge(testSecret, challenge, issued.Add(-10*time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if age != -10*time.Second {
		t.Errorf("age = %v, want -10s", age)
	}
}

func TestChallengeAgeTruncatesSubSecondIssueTime(t *testing.T) {
	issued := time.Unix(1770000000, 750*int64(time.Millisecond))
	challenge := NewChallenge(testSecret, issued)

	age, err := ChallengeAge(testSecret, challenge, issued.Add(time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only whole seconds survive the encoding, so the reported age carries the
	// truncated sub-second remainder.
	if age != 1750*time.Millisecond {
		t.Errorf("age = %v, want 1.75s", age)
	}
}

func TestChallengeAgeRejectsTamperedSignature(t *testing.T) {
	challenge := NewChallenge(testSecret, time.Unix(1770000000, 0))
	tampered := flipLastRune(challenge)

	if _, err := ChallengeAge(testSecret, tampered, time.Unix(1770000010, 0)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeAgeRejectsTamperedTimestamp(t *testing.T) {
	challenge := NewChallenge(testSecret, time.Unix(1770000000, 0))
	sig := challenge[strings.Index(challenge, ".")+1:]
	forged := base64.RawURLEncoding.EncodeToString([]byte("1769999000")) + "." + sig

	if _, err := ChallengeAge(testSecret, forged, time.Unix(1770000010, 0)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeAgeRejectsForeignSecret(t *testing.T) {
	challenge := NewChallenge([]byte("some other secret entirely"), time.Unix(1770000000, 0))

	if _, err := ChallengeAge(testSecret, challenge, time.Unix(1770000010, 0)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeAgeRejectsMalformedInput(t *testing.T) {
	valid := NewChallenge(testSecret, time.Unix(1770000000, 0))
	sig := valid[strings.Index(valid, ".")+1:]

	cases := map[string]string{
		"empty":                 "",
		"no separator":          strings.ReplaceAll(valid, ".", ""),
		"extra separator":       valid + ".extra",
		"empty payload":         "." + sig,
		"empty signature":       base64.RawURLEncoding.EncodeToString([]byte("1770000000")) + ".",
		"garbage":               "not-a-challenge-at-all",
		"payload not base64":    "!!!!." + sig,
		"payload not a number":  base64.RawURLEncoding.EncodeToString([]byte("yesterday")) + "." + sig,
		"signature not hex":     base64.RawURLEncoding.EncodeToString([]byte("1770000000")) + "." + strings.Repeat("z", 64),
		"signature wrong width": base64.RawURLEncoding.EncodeToString([]byte("1770000000")) + "." + sig[:32],
	}

	for name, challenge := range cases {
		t.Run(name, func(t *testing.T) {
			age, err := ChallengeAge(testSecret, challenge, time.Unix(1770000010, 0))
			if !errors.Is(err, ErrChallengeInvalid) {
				t.Fatalf("err = %v, want ErrChallengeInvalid", err)
			}
			if age != 0 {
				t.Errorf("age = %v, want zero on failure", age)
			}
		})
	}
}

func TestChallengeAgeAcceptsPaddedPayload(t *testing.T) {
	// A client or proxy may re-encode the payload with standard padding; the
	// decoder tolerates it because the signature covers the timestamp, not the
	// encoding.
	ts := "1770000000"
	padded := base64.URLEncoding.EncodeToString([]byte(ts))
	if !strings.HasSuffix(padded, "=") {
		t.Fatalf("test precondition: %q carries no padding", padded)
	}
	sig := NewChallenge(testSecret, time.Unix(1770000000, 0))
	sig = sig[strings.Index(sig, ".")+1:]

	if _, err := ChallengeAge(testSecret, padded+"."+sig, time.Unix(1770000010, 0)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func flipLastRune(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	if last == '0' {
		return s[:len(s)-1] + "1"
	}
	return s[:len(s)-1] + "0"
}
