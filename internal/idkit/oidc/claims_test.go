package oidc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOIDCSubjectAndClaimRules(t *testing.T) {
	for _, sub := range []string{"abc", "00u1a2b3c4", strings.Repeat("a", 255)} {
		assert.True(t, ValidSubject(sub), sub)
	}
	for _, sub := range []string{"", "abc ", " abc", "a\tb", "abé", strings.Repeat("a", 256), "a\nb"} {
		assert.False(t, ValidSubject(sub), "%q", sub)
	}
	for raw, ok := range map[string]bool{``: true, `["a","b"]`: true, `[]`: true, `"a"`: false, `null`: false, `[1]`: false, `{}`: false} {
		_, got := StringArray(json.RawMessage(raw))
		assert.Equal(t, ok, got, raw)
	}
	assert.Equal(t, "ab", TruncateUTF8("abé", 3), "cut on a rune boundary")
	assert.Equal(t, "abé", TruncateUTF8("abé", 4))
}

func TestStrictStringArray(t *testing.T) {
	for raw, want := range map[string][]string{`["a","b"]`: {"a", "b"}, `[]`: {}} {
		got, ok := StrictStringArray(json.RawMessage(raw))
		assert.True(t, ok, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{``, `null`, `[null]`, `[1]`, `{}`, `"a"`} {
		got, ok := StrictStringArray(json.RawMessage(raw))
		assert.False(t, ok, "%q", raw)
		assert.Nil(t, got, "%q", raw)
	}
}

func TestOIDCProfileNames(t *testing.T) {
	long := strings.Repeat("é", 60) // 120 bytes
	cases := []struct {
		name                string
		given, family, full string
		first, last         string
	}{
		{"given_only", "Ada", "", "", "Ada", ""},
		{"family_only", "", "Lovelace", "", "", "Lovelace"},
		{"both", "Ada", "Lovelace", "", "Ada", "Lovelace"},
		{"name_one_word", "", "", "Cher", "Cher", ""},
		{"name_extra_whitespace", "", "", "  Ada \t Augusta\n  King  ", "Ada", "Augusta King"},
		{"multibyte_truncation", "", "", long + " " + long, strings.Repeat("é", 50), strings.Repeat("é", 50)},
		{"given_wins_over_name", "Ada", "", "Someone Else", "Ada", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last := ProfileNames(tc.given, tc.family, tc.full)
			assert.Equal(t, tc.first, first)
			assert.Equal(t, tc.last, last)
			assert.LessOrEqual(t, len(first), NameMaxBytes)
			assert.LessOrEqual(t, len(last), NameMaxBytes)
		})
	}
}
