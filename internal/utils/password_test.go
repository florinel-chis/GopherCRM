package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidatePasswordComplexity(t *testing.T) {
	t.Run("valid password", func(t *testing.T) {
		err := ValidatePasswordComplexity("Str0ng!Pass")
		assert.NoError(t, err)
	})

	t.Run("valid password with various special chars", func(t *testing.T) {
		err := ValidatePasswordComplexity("Hello@World1")
		assert.NoError(t, err)
	})

	t.Run("too short", func(t *testing.T) {
		err := ValidatePasswordComplexity("Aa1!bcde9")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least 10 characters")
	})

	t.Run("missing uppercase", func(t *testing.T) {
		err := ValidatePasswordComplexity("alllower1!xx")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "uppercase")
	})

	t.Run("missing lowercase", func(t *testing.T) {
		err := ValidatePasswordComplexity("ALLUPPER1!XX")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lowercase")
	})

	t.Run("missing digit", func(t *testing.T) {
		err := ValidatePasswordComplexity("NoDigitsHere!")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "digit")
	})

	t.Run("missing special char", func(t *testing.T) {
		err := ValidatePasswordComplexity("NoSpecial123A")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "special character")
	})

	t.Run("empty password", func(t *testing.T) {
		err := ValidatePasswordComplexity("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least 10 characters")
	})
}

func TestValidateNewPassword(t *testing.T) {
	const tooLong = "password must be at most 72 bytes long"

	for _, tc := range []struct {
		name     string
		password string
		bytes    int
		want     string
	}{
		{"72 ASCII bytes pass", "Aa1!" + strings.Repeat("x", 68), 72, ""},
		{"73 ASCII bytes are refused", "Aa1!" + strings.Repeat("x", 69), 73, tooLong},
		{"72 bytes in 38 characters pass", "Aa1!" + strings.Repeat("é", 34), 72, ""},
		{"74 bytes in 39 characters are refused", "Aa1!" + strings.Repeat("é", 35), 74, tooLong},
		{"weak and too long gets the length message", strings.Repeat("a", 73), 73, tooLong},
		{"weak and short enough keeps the complexity message", "weakpassword", 12,
			"password must contain at least one uppercase letter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Len(t, tc.password, tc.bytes, "the fixture has the length the case names")
			err := ValidateNewPassword(tc.password)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tc.want)
		})
	}

	assert.Equal(t, 72, MaxPasswordBytes)
	// The complexity policy alone has no upper bound: the services still see
	// what bcrypt refuses when a caller skips the handler check.
	assert.NoError(t, ValidatePasswordComplexity("Aa1!"+strings.Repeat("x", 80)))
}
