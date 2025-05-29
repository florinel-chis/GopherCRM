package models

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
	Title        string         `gorm:"not null;type:varchar(255)" json:"title"`
	Description  string         `gorm:"not null;type:text" json:"description"`
	Status       TicketStatus   `gorm:"not null;default:'open';type:varchar(20)" json:"status"`
	Priority     TicketPriority `gorm:"not null;default:'medium';type:varchar(20)" json:"priority"`
	CustomerID   uint           `json:"customer_id"`
	Customer     Customer       `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	AssignedToID *uint          `json:"assigned_to_id,omitempty"`
	AssignedTo   *User          `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty"`
	Resolution   string         `gorm:"type:text" json:"resolution"`
}