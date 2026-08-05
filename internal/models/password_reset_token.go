package models

import "time"

// PasswordResetToken is a single-use, short-lived token that authorizes a
// password reset. Only the SHA256/HMAC hash of the opaque token is stored;
// the raw value exists solely in the reset link e-mailed to the account owner.
// A token is spendable only while UsedAt is nil and ExpiresAt is in the future.
type PasswordResetToken struct {
	BaseModel
	UserID    uint       `gorm:"not null" json:"user_id"`
	User      User       `gorm:"foreignKey:UserID" json:"-"`
	TokenHash string     `gorm:"not null;uniqueIndex;type:varchar(255)" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}
