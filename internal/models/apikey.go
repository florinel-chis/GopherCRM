package models

import "time"

type APIKey struct {
	BaseModel
	Name        string     `gorm:"not null;type:varchar(100)" json:"name"`
	KeyHash     string     `gorm:"uniqueIndex;not null;type:varchar(64)" json:"-"`
	Prefix      string     `gorm:"not null;type:varchar(8)" json:"prefix"`
	UserID      uint       `json:"user_id"`
	User        User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	IsActive    bool       `gorm:"default:true" json:"is_active"`
}