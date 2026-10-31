package models

import "time"

// Notification is an in-app message addressed to one staff user, created by
// the notify step of an automation rule. It is hard-deleted when the lead it
// refers to is erased and when its recipient is erased: the title and body
// may render a placeholder (a lead's name), so neither may outlive the person.
//
// @Description An in-app message for one staff user, created by the notify step of an automation rule.
type Notification struct {
	// Unique identifier, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"31" readonly:"true"`
	// When the notification was created (RFC 3339). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-10-30T08:00:00Z" readonly:"true"`
	// When the row was last written (RFC 3339), e.g. when it was marked read. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-30T08:15:00Z" readonly:"true"`

	// ID of the staff user the notification is addressed to; always the caller on these endpoints.
	UserID uint  `gorm:"not null;index" json:"user_id" example:"7"`
	User   *User `gorm:"foreignKey:UserID" json:"-"`
	// Title rendered from the notify step's title template (placeholders substituted), cut to 255 characters.
	Title string `gorm:"not null;type:varchar(255)" json:"title" example:"New lead assigned: Ada Lovelace"`
	// Body rendered from the notify step's body template; an empty string when the step has no body.
	Body string `gorm:"type:text" json:"body" example:"Please contact the lead within one business day."`
	// Type of the record the notification is about.
	EntityType string `gorm:"not null;type:varchar(20);index:idx_notifications_entity" json:"entity_type" enums:"lead,deal" example:"lead"`
	// ID of the lead or deal the notification is about.
	EntityID uint `gorm:"not null;index:idx_notifications_entity" json:"entity_id" example:"12"`
	// ID of the automation run that created the notification; omitted when there is none.
	RunID *uint `json:"run_id,omitempty" example:"88"`
	// When the caller marked it read (RFC 3339); omitted while unread.
	ReadAt *time.Time `json:"read_at,omitempty" example:"2026-10-30T08:15:00Z"`
}

func (Notification) TableName() string { return "notifications" }

// NotificationTitleMaxLength is the width of notifications.title.
const NotificationTitleMaxLength = 255
