package models

// BulkOperationStatus represents the status of a bulk operation
type BulkOperationStatus string

const (
	BulkOperationStatusPending    BulkOperationStatus = "pending"
	BulkOperationStatusProcessing BulkOperationStatus = "processing"
	BulkOperationStatusCompleted  BulkOperationStatus = "completed"
	BulkOperationStatusFailed     BulkOperationStatus = "failed"
)

// BulkOperationType represents the type of bulk operation
type BulkOperationType string

const (
	BulkOperationTypeCreate BulkOperationType = "create"
	BulkOperationTypeUpdate BulkOperationType = "update"
	BulkOperationTypeDelete BulkOperationType = "delete"
)

// BulkOperation represents a bulk operation record
type BulkOperation struct {
	BaseModel
	UserID       uint                `gorm:"not null" json:"user_id"`
	User         User                `gorm:"foreignKey:UserID" json:"user,omitempty"`
	ResourceType string              `gorm:"not null;type:varchar(50)" json:"resource_type"`
	Type         BulkOperationType   `gorm:"not null;type:varchar(20)" json:"type"`
	Status       BulkOperationStatus `gorm:"not null;default:'pending';type:varchar(20)" json:"status"`
	TotalItems   int                 `gorm:"not null" json:"total_items"`
	SuccessCount int                 `gorm:"default:0" json:"success_count"`
	FailureCount int                 `gorm:"default:0" json:"failure_count"`
	Items        []BulkOperationItem `gorm:"foreignKey:OperationID" json:"items,omitempty"`
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
	IDs    []uint                 `json:"ids"`
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// BulkResponse represents the response from a bulk operation
type BulkResponse struct {
	OperationID  uint     `json:"operation_id"`
	TotalItems   int      `json:"total_items"`
	SuccessCount int      `json:"success_count"`
	FailureCount int      `json:"failure_count"`
	Errors       []string `json:"errors,omitempty"`
}
