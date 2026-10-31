package models

import "gorm.io/gorm"

// TicketStatus is the workflow status of a ticket. A new ticket always starts
// as open; a closed ticket cannot be moved to any other status.
//
// @Description Workflow status of a ticket. A new ticket always starts as open; a closed ticket cannot be moved to any other status.
type TicketStatus string

// TicketPriority is the priority of a ticket; medium when not given on create.
//
// @Description Priority of a ticket; medium when not given on create.
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

// @Description A support request raised for a customer and handled by a support or admin user.
type Ticket struct {
	BaseModel
	// Short summary of the ticket. Required on create. The column is 255 characters wide; the API does not check the length before the write.
	Title string `gorm:"not null;type:varchar(255)" json:"title" example:"Printer offline after firmware update"`
	// Full description of the problem. Required on create.
	Description string `gorm:"not null;type:text" json:"description" example:"Since this morning the office printer no longer appears on the network."`
	// Workflow status. New tickets always start as open; a closed ticket cannot move to another status.
	Status TicketStatus `gorm:"not null;default:'open';type:varchar(20)" json:"status" example:"open"`
	// Priority; medium when not given on create.
	Priority TicketPriority `gorm:"not null;default:'medium';type:varchar(20)" json:"priority" example:"high"`
	// ID of the customer the ticket belongs to. Set on create and never changed by PUT /tickets/{id}.
	CustomerID uint `json:"customer_id" example:"3"`
	// The customer record. Always present in the JSON. GET /tickets, GET /tickets/{id} and PUT /tickets/{id} fill it in with the Customer object (see the Customer schema); POST /tickets, GET /tickets/my and GET /customers/{id}/tickets return a zero-valued placeholder instead (id 0, every string empty, email "" included), which is not a valid Customer, so this property is documented as a plain object. Use customer_id.
	Customer Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty" swaggertype:"object"`
	// ID of the assigned support or admin user. POST /tickets assigns the caller when it is not given. Omitted when the ticket is unassigned.
	AssignedToID *uint `json:"assigned_to_id,omitempty" example:"5"`
	// The assignee's user record, loaded by the same endpoints that fill in customer; omitted elsewhere and when the ticket is unassigned.
	AssignedTo *User `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty"`
	// How the ticket was resolved; an empty string until someone sets it. PUT /tickets/{id} cannot clear it.
	Resolution string `gorm:"type:text" json:"resolution" example:"Rolled the printer back to the previous firmware."`
	// The ticket's comments, oldest first, each with its author. Only loaded by GET /tickets/{id} and PUT /tickets/{id}; omitted elsewhere. Customer callers never receive internal notes here.
	Comments []TicketComment `gorm:"foreignKey:TicketID" json:"comments,omitempty"`
}

// @Description A comment on a ticket, or an internal note visible to staff only.
type TicketComment struct {
	BaseModel
	// ID of the ticket the comment belongs to.
	TicketID uint `gorm:"not null;index" json:"ticket_id" example:"18"`
	// ID of the staff user who wrote the comment (the caller of POST /tickets/{id}/comments).
	AuthorUserID uint `gorm:"not null;index;column:author_user_id" json:"author_user_id" example:"5"`
	// The author's user record. Omitted when the author's account has since been deleted.
	Author *User `gorm:"foreignKey:AuthorUserID" json:"author,omitempty"`
	// The comment text, trimmed of surrounding whitespace.
	Body string `gorm:"not null;type:text" json:"body" example:"Rolled back the firmware; waiting for the customer to confirm."`
	// True for an internal note: visible to staff only, never returned to customer callers.
	IsInternal bool `gorm:"not null" json:"is_internal" example:"false"`

	// Derived / alias fields for API serialization compatibility with frontend

	// Read-only alias of author_user_id, kept for older clients.
	UserID uint `gorm:"-" json:"user_id,omitempty" example:"5" readonly:"true"`
	// Read-only alias of author, kept for older clients.
	User *User `gorm:"-" json:"user,omitempty" readonly:"true"`
	// Read-only alias of body, kept for older clients.
	Content string `gorm:"-" json:"content,omitempty" example:"Rolled back the firmware; waiting for the customer to confirm." readonly:"true"`
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
