package models

import "time"

// RefreshToken represents a refresh token for JWT authentication
type RefreshToken struct {
	BaseModel
	UserID    uint      `gorm:"not null" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	TokenHash string    `gorm:"not null;uniqueIndex;type:varchar(255)" json:"-"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	Revoked   bool      `gorm:"default:false" json:"revoked"`
}
