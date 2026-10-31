package models

import (
	"time"

	"gorm.io/gorm"
)

// BaseModel carries the identity and bookkeeping columns every persisted
// entity shares. The field comments and swag tags below are the schema
// descriptions of these four properties on every model that embeds it.
type BaseModel struct {
	// Unique identifier, assigned by the server on creation. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"42" readonly:"true"`
	// When the record was created (RFC 3339 date-time), set by the server. Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-10-01T09:30:00Z" readonly:"true"`
	// When the record was last written (RFC 3339 date-time), set by the server on every update. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-02T14:05:00Z" readonly:"true"`
	// Soft-deletion time (RFC 3339 date-time) or null. Always present in the JSON. Reads exclude soft-deleted rows, so records returned by the API carry null here. Read-only.
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty" swaggertype:"string" format:"date-time" extensions:"x-nullable" readonly:"true"`
}
