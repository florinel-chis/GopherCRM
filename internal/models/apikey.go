package models

import "time"

// APIKey is a personal API key, presented as `Authorization: ApiKey <key>`.
// Only an HMAC of the key is stored; the plaintext is shown once, at creation.
//
// @Description A personal API key, presented as `Authorization: ApiKey <key>`. Only a hash of the key is stored: the plaintext is returned once, by POST /api-keys, and never again.
type APIKey struct {
	BaseModel
	// Owner-chosen label, 3 to 100 characters.
	Name    string `gorm:"not null;type:varchar(100)" json:"name" example:"CI pipeline"`
	KeyHash string `gorm:"uniqueIndex;not null;size:128" json:"-"`
	// First 8 hexadecimal characters of the key's random part (the characters
	// after "gcrm_"), so a key can be recognised without exposing it. Read-only.
	Prefix string `gorm:"not null;type:varchar(8)" json:"prefix" example:"3f9a1c2b" readonly:"true"`
	// ID of the owning user, always the account that created the key. Read-only.
	UserID uint `json:"user_id" example:"7" readonly:"true"`
	// Not populated by the API-key endpoints: always serialised as an empty
	// placeholder user object (id 0, every string empty, role "" included),
	// so it is documented as a plain object rather than a User. Use user_id.
	User User `gorm:"foreignKey:UserID" json:"user,omitempty" swaggertype:"object"`
	// Time the key last authenticated a request (RFC 3339), updated on a best-
	// effort basis. Absent when the key has never been used. Read-only.
	LastUsedAt *time.Time `json:"last_used_at,omitempty" example:"2026-10-30T11:00:00Z" readonly:"true"`
	// Expiry time (RFC 3339); requests with the key are rejected from this
	// instant on. Absent when the key never expires. Set only at creation.
	ExpiresAt *time.Time `json:"expires_at,omitempty" example:"2026-12-31T23:59:59Z"`
	// False once the key is revoked (DELETE /api-keys/{id} or PUT with
	// is_active=false); the owner can set it back to true. An inactive key is
	// rejected at authentication.
	IsActive bool `gorm:"default:true" json:"is_active" example:"true"`
}
