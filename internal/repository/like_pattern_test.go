package repository

import "testing"

func TestContainsPattern(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "%%"},
		{"plain", "%plain%"},
		{"a_b", "%a!_b%"},
		{"50%", "%50!%%"},
		{"a!b", "%a!!b%"},
		{`a\b`, `%a\b%`},
		{`!%_\`, `%!!!%!_\%`},
		{"__", "%!_!_%"},
	}
	for _, tc := range cases {
		if got := containsPattern(tc.in); got != tc.want {
			t.Errorf("containsPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
