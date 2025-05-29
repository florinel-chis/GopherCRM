package models

import "time"

type TaskStatus string
type TaskPriority string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusCancelled  TaskStatus = "cancelled"

	TaskPriorityLow    TaskPriority = "low"
	TaskPriorityMedium TaskPriority = "medium"
	TaskPriorityHigh   TaskPriority = "high"
)

type Task struct {
	BaseModel
	Title        string       `gorm:"not null;type:varchar(255)" json:"title"`
	Description  string       `gorm:"type:text" json:"description"`
	Status       TaskStatus   `gorm:"not null;default:'pending';type:varchar(20)" json:"status"`
	Priority     TaskPriority `gorm:"not null;default:'medium';type:varchar(20)" json:"priority"`
	DueDate      *time.Time   `json:"due_date,omitempty"`
	AssignedToID uint         `json:"assigned_to_id"`
	AssignedTo   User         `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty"`
	LeadID       *uint        `json:"lead_id,omitempty"`
	Lead         *Lead        `gorm:"foreignKey:LeadID" json:"lead,omitempty"`
	CustomerID   *uint        `json:"customer_id,omitempty"`
	Customer     *Customer    `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
}