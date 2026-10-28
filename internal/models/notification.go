package models

import "time"

// Notification is an in-app message addressed to one staff user, created by
// the notify step of an automation rule. It is hard-deleted when the lead it
// refers to is erased and when its recipient is erased: the title and body
// may render a placeholder (a lead's name), so neither may outlive the person.
type Notification struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	UserID     uint       `gorm:"not null;index" json:"user_id"`
	User       *User      `gorm:"foreignKey:UserID" json:"-"`
	Title      string     `gorm:"not null;type:varchar(255)" json:"title"`
	Body       string     `gorm:"type:text" json:"body"`
	EntityType string     `gorm:"not null;type:varchar(20);index:idx_notifications_entity" json:"entity_type"`
	EntityID   uint       `gorm:"not null;index:idx_notifications_entity" json:"entity_id"`
	RunID      *uint      `json:"run_id,omitempty"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
}

func (Notification) TableName() string { return "notifications" }

// NotificationTitleMaxLength is the width of notifications.title.
const NotificationTitleMaxLength = 255
