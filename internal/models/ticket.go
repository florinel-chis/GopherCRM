package models

import "gorm.io/gorm"

type TicketStatus string
type TicketPriority string

const (
	TicketStatusOpen       TicketStatus = "open"
	TicketStatusInProgress TicketStatus = "in_progress"
	TicketStatusResolved   TicketStatus = "resolved"
	TicketStatusClosed     TicketStatus = "closed"

	TicketPriorityLow    TicketPriority = "low"
	TicketPriorityMedium TicketPriority = "medium"
	TicketPriorityHigh   TicketPriority = "high"
	TicketPriorityUrgent TicketPriority = "urgent"
)

type Ticket struct {
	BaseModel
	Title        string          `gorm:"not null;type:varchar(255)" json:"title"`
	Description  string          `gorm:"not null;type:text" json:"description"`
	Status       TicketStatus    `gorm:"not null;default:'open';type:varchar(20)" json:"status"`
	Priority     TicketPriority  `gorm:"not null;default:'medium';type:varchar(20)" json:"priority"`
	CustomerID   uint            `json:"customer_id"`
	Customer     Customer        `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	AssignedToID *uint           `json:"assigned_to_id,omitempty"`
	AssignedTo   *User           `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty"`
	Resolution   string          `gorm:"type:text" json:"resolution"`
	Comments     []TicketComment `gorm:"foreignKey:TicketID" json:"comments,omitempty"`
}

type TicketComment struct {
	BaseModel
	TicketID     uint   `gorm:"not null;index" json:"ticket_id"`
	AuthorUserID uint   `gorm:"not null;index;column:author_user_id" json:"author_user_id"`
	Author       *User  `gorm:"foreignKey:AuthorUserID" json:"author,omitempty"`
	Body         string `gorm:"not null;type:text" json:"body"`
	IsInternal   bool   `gorm:"not null" json:"is_internal"`

	// Derived / alias fields for API serialization compatibility with frontend
	UserID  uint   `gorm:"-" json:"user_id,omitempty"`
	User    *User  `gorm:"-" json:"user,omitempty"`
	Content string `gorm:"-" json:"content,omitempty"`
}

func (c *TicketComment) AfterFind(tx *gorm.DB) error {
	c.UserID = c.AuthorUserID
	c.User = c.Author
	c.Content = c.Body
	return nil
}

func (c *TicketComment) AfterCreate(tx *gorm.DB) error {
	c.UserID = c.AuthorUserID
	c.User = c.Author
	c.Content = c.Body
	return nil
}
