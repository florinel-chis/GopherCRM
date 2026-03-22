package utils

import (
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
