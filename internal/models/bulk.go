package models

import "time"

// BulkOperationStatus represents the status of a bulk operation
type BulkOperationStatus string

const (
	BulkOperationStatusPending    BulkOperationStatus = "pending"
	BulkOperationStatusProcessing BulkOperationStatus = "processing"
	BulkOperationStatusCompleted  BulkOperationStatus = "completed"
	BulkOperationStatusFailed     BulkOperationStatus = "failed"
	BulkOperationStatusPartial    BulkOperationStatus = "partial"
)

// Short aliases for BulkOperationStatus used by services
var (
	StatusPending   = BulkOperationStatusPending
	StatusProcessing = BulkOperationStatusProcessing
	StatusCompleted = BulkOperationStatusCompleted
	StatusFailed    = BulkOperationStatusFailed
	StatusPartial   = BulkOperationStatusPartial
)

// BulkOperationType represents the type of bulk operation
type BulkOperationType string

const (
	BulkOperationTypeCreate BulkOperationType = "create"
	BulkOperationTypeUpdate BulkOperationType = "update"
	BulkOperationTypeDelete BulkOperationType = "delete"
	BulkOperationTypeAction BulkOperationType = "action"
)

// Short aliases for BulkOperationType used by services
var (
	BulkCreate = BulkOperationTypeCreate
	BulkUpdate = BulkOperationTypeUpdate
	BulkDelete = BulkOperationTypeDelete
	BulkAction = BulkOperationTypeAction
)

// MaxBulkItems is the maximum number of items allowed in a single bulk operation
const MaxBulkItems = 1000

// BulkOperation represents a bulk operation record
type BulkOperation struct {
	BaseModel
	UserID        uint                `gorm:"not null" json:"user_id"`
	User          User                `gorm:"foreignKey:UserID" json:"user,omitempty"`
	ResourceType  string              `gorm:"not null;type:varchar(50)" json:"resource_type"`
	Type          BulkOperationType   `gorm:"column:type;not null;type:varchar(20)" json:"type"`
	OperationType BulkOperationType   `gorm:"-" json:"operation_type,omitempty"` // Alias for Type (not persisted)
	Status        BulkOperationStatus `gorm:"not null;default:'pending';type:varchar(20)" json:"status"`
	TotalItems    int                 `gorm:"not null" json:"total_items"`
	SuccessCount  int                 `gorm:"default:0" json:"success_count"`
	FailureCount  int                 `gorm:"default:0" json:"failure_count"`
	Items         []BulkOperationItem `gorm:"foreignKey:OperationID" json:"items,omitempty"`
}

// BulkOperationItem represents an individual item within a bulk operation
type BulkOperationItem struct {
	BaseModel
	OperationID uint                `gorm:"not null" json:"operation_id"`
	ResourceID  uint                `json:"resource_id"`
	Status      BulkOperationStatus `gorm:"not null;default:'pending';type:varchar(20)" json:"status"`
	Error       string              `gorm:"type:text" json:"error,omitempty"`
	Data        string              `gorm:"type:text" json:"data,omitempty"`
}

// BulkUpdateItem represents an item to be updated in a bulk operation
type BulkUpdateItem struct {
	ID      uint                   `json:"id"`
	Updates map[string]interface{} `json:"updates"`
}

// BulkItemError represents an error for a specific item in a bulk operation
type BulkItemError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// BulkCreateRequest represents a bulk create request
type BulkCreateRequest struct {
	Items []map[string]interface{} `json:"items"`
}

// BulkUpdateRequest represents a bulk update request
type BulkUpdateRequest struct {
	Items []BulkUpdateItem `json:"items"`
}

// BulkDeleteRequest represents a bulk delete request
type BulkDeleteRequest struct {
	IDs []uint `json:"ids"`
}

// BulkActionRequest represents a bulk action request
type BulkActionRequest struct {
	IDs        []uint                 `json:"ids"`
	Action     string                 `json:"action"`
	Params     map[string]interface{} `json:"params,omitempty"`
	Parameters map[string]interface{} `json:"parameters,omitempty"` // Alias for Params
}

// BulkResponse represents the response from a bulk operation
type BulkResponse struct {
	OperationID    uint            `json:"operation_id"`
	Status         BulkOperationStatus `json:"status,omitempty"`
	TotalItems     int             `json:"total_items"`
	ProcessedItems int             `json:"processed_items,omitempty"`
	SuccessItems   int             `json:"success_items,omitempty"`
	SuccessCount   int             `json:"success_count"`
	FailedItems    int             `json:"failed_items,omitempty"`
	FailureCount   int             `json:"failure_count"`
	ProcessingTime *time.Duration  `json:"processing_time,omitempty"`
	Errors         interface{}     `json:"errors,omitempty"` // Can be []string or []BulkItemError
}

// Bulk action type constants for each resource

// userBulkActions defines available bulk actions for users
type userBulkActions struct {
	Activate   string
	Deactivate string
	ChangeRole string
}

// UserBulkActionTypes contains the available bulk action types for users
var UserBulkActionTypes = userBulkActions{
	Activate:   "activate",
	Deactivate: "deactivate",
	ChangeRole: "change_role",
}

// leadBulkActions defines available bulk actions for leads
type leadBulkActions struct {
	UpdateStatus string
	Assign       string
	Convert      string
	UpdateSource string
}

// LeadBulkActionTypes contains the available bulk action types for leads
var LeadBulkActionTypes = leadBulkActions{
	UpdateStatus: "update_status",
	Assign:       "assign",
	Convert:      "convert",
	UpdateSource: "update_source",
}

// customerBulkActions defines available bulk actions for customers
type customerBulkActions struct {
	Activate   string
	Deactivate string
	UpdateType string
	Assign     string
}

// CustomerBulkActionTypes contains the available bulk action types for customers
var CustomerBulkActionTypes = customerBulkActions{
	Activate:   "activate",
	Deactivate: "deactivate",
	UpdateType: "update_type",
	Assign:     "assign",
}

// taskBulkActions defines available bulk actions for tasks
type taskBulkActions struct {
	UpdateStatus   string
	Assign         string
	UpdatePriority string
	SetDueDate     string
}

// TaskBulkActionTypes contains the available bulk action types for tasks
var TaskBulkActionTypes = taskBulkActions{
	UpdateStatus:   "update_status",
	Assign:         "assign",
	UpdatePriority: "update_priority",
	SetDueDate:     "set_due_date",
}

// ticketBulkActions defines available bulk actions for tickets
type ticketBulkActions struct {
	UpdateStatus   string
	Assign         string
	UpdatePriority string
	UpdateCategory string
}

// TicketBulkActionTypes contains the available bulk action types for tickets
var TicketBulkActionTypes = ticketBulkActions{
	UpdateStatus:   "update_status",
	Assign:         "assign",
	UpdatePriority: "update_priority",
	UpdateCategory: "update_category",
}
