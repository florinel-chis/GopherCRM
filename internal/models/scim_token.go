package models

import "time"

// SCIMTokenNameMaxLength is the width of scim_tokens.name.
const SCIMTokenNameMaxLength = 100

// SCIMToken is a bearer token an identity provider uses on /scim/v2 (#468).
// Only the HMAC-SHA256 of the token is stored (keyed like API keys by
// API_KEY_SECRET, else JWT_SECRET); the plaintext is shown once, on creation.
// A token is revoked by stamping RevokedAt and is never deleted. No foreign
// key: a token belongs to the installation, not to a user.
type SCIMToken struct {
	ID uint `gorm:"primarykey" json:"id"`
	// A label the administrator chose, such as the identity provider's name.
	Name string `gorm:"type:varchar(100);not null" json:"name"`
	// utils.HashAPIKeyHMAC of the token ("hmac$<hex>"). Never returned.
	TokenHash string `gorm:"type:varchar(128);not null;uniqueIndex:idx_scim_tokens_token_hash" json:"-"`
	// Written explicitly in UTC.
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	// The last successful authentication with the token, best-effort.
	LastUsedAt *time.Time `json:"last_used_at"`
	// When the token was revoked; NULL while it is usable.
	RevokedAt *time.Time `json:"revoked_at"`
}

// TableName pins the table name.
func (SCIMToken) TableName() string { return "scim_tokens" }
