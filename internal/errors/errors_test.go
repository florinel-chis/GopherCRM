package errors

import (
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// IsNotFound must recognise all three not-found identities — our two
// sentinels and gorm's — including when wrapped, and must never classify
// an unrelated failure as a miss. Handlers rely on this to choose between
// 404 and 500, so a false positive here hides real database failures.
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel ErrNotFound", ErrNotFound, true},
		{"wrapped ErrNotFound", fmt.Errorf("user 7 not found: %w", ErrNotFound), true},
		{"sentinel ErrRecordNotFound", ErrRecordNotFound, true},
		{"raw gorm.ErrRecordNotFound", gorm.ErrRecordNotFound, true},
		{"wrapped gorm.ErrRecordNotFound", fmt.Errorf("lookup: %w", gorm.ErrRecordNotFound), true},
		{"same message, different identity", errors.New("record not found"), false},
		{"unrelated failure", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
