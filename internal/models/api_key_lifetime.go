package models

import "time"

// APIKeyExpiryCap is the latest expiry an API key may have when its lifetime
// starts at from: one year later (#532). A new key defaults to it and may not
// ask for later; a key stored without an expiry is treated as expiring at
// APIKeyExpiryCap(created_at).
func APIKeyExpiryCap(from time.Time) time.Time {
	return from.AddDate(1, 0, 0)
}
