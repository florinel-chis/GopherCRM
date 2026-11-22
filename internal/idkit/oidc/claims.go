package oidc

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Limits of the claims a sign-in stores.
const (
	// SubjectMaxBytes is the longest sub ValidSubject accepts.
	SubjectMaxBytes = 255
	// NameMaxBytes is the longest first or last name ProfileNames returns.
	NameMaxBytes = 100
)

// Claims are the ID-token claims a sign-in reads. They are kept raw so their
// shape can be checked.
type Claims struct {
	Groups     json.RawMessage `json:"groups"`
	Roles      json.RawMessage `json:"roles"`
	ClaimNames json.RawMessage `json:"_claim_names"`
	Email      json.RawMessage `json:"email"`
	GivenName  json.RawMessage `json:"given_name"`
	FamilyName json.RawMessage `json:"family_name"`
	Name       json.RawMessage `json:"name"`
	// OID and TID are the Entra ID object id and tenant id.
	OID json.RawMessage `json:"oid"`
	TID json.RawMessage `json:"tid"`
}

// StringArray decodes a claim that must be absent or a JSON array of
// strings. Anything else, null included, is refused.
func StringArray(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

// String is a string claim, or "" when it is absent or not a string.
func String(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// ProfileNames are the first and last name a first sign-in provisions:
// given_name and family_name as they are when either is a non-empty string;
// otherwise name split at whitespace, its first field the first name and the
// remaining fields, joined by single spaces, the last name; otherwise both
// empty. Each is cut to NameMaxBytes on a rune boundary.
func ProfileNames(given, family, name string) (first, last string) {
	if given != "" || family != "" {
		first, last = given, family
	} else if fields := strings.Fields(name); len(fields) > 0 {
		first, last = fields[0], strings.Join(fields[1:], " ")
	}
	return TruncateUTF8(first, NameMaxBytes), TruncateUTF8(last, NameMaxBytes)
}

// ValidSubject accepts a sub of 1-255 printable ASCII bytes without leading
// or trailing whitespace, so a subject that a PAD SPACE collation would
// equate with another never reaches the database.
func ValidSubject(sub string) bool {
	if sub == "" || len(sub) > SubjectMaxBytes || strings.TrimSpace(sub) != sub {
		return false
	}
	for i := 0; i < len(sub); i++ {
		if sub[i] < 0x20 || sub[i] > 0x7e {
			return false
		}
	}
	return true
}

// TruncateUTF8 cuts s to at most max bytes on a rune boundary.
func TruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
