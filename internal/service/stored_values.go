package service

import "time"

// equalUintPtr reports whether two nullable ids hold the same value.
func equalUintPtr(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equalTimePtr reports whether two nullable instants are the same instant.
func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
