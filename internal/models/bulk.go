package models

// BulkOperationStatus represents the status of a bulk operation.
//
// The bulk status endpoints write only pending, completed and failed.
// processing and partial are kept because rows written by the removed generic
// bulk subsystem may still hold them.
type BulkOperationStatus string

const (
	BulkOperationStatusPending    BulkOperationStatus = "pending"
	BulkOperationStatusProcessing BulkOperationStatus = "processing"
	BulkOperationStatusCompleted  BulkOperationStatus = "completed"
	BulkOperationStatusFailed     BulkOperationStatus = "failed"
	BulkOperationStatusPartial    BulkOperationStatus = "partial"
)

// Short aliases for BulkOperationStatus
var (
	StatusPending    = BulkOperationStatusPending
	StatusProcessing = BulkOperationStatusProcessing
	StatusCompleted  = BulkOperationStatusCompleted
	StatusFailed     = BulkOperationStatusFailed
	StatusPartial    = BulkOperationStatusPartial
)

// BulkOperationType represents the type of bulk operation.
//
// Only BulkOperationTypeAction is written now (every bulk status update is an
// action). create, update and delete name the values that rows written by the
// removed generic bulk subsystem may still hold.
type BulkOperationType string

const (
	BulkOperationTypeCreate BulkOperationType = "create"
	BulkOperationTypeUpdate BulkOperationType = "update"
	BulkOperationTypeDelete BulkOperationType = "delete"
	BulkOperationTypeAction BulkOperationType = "action"
)

// BulkAction is the short name of the one operation type still written.
var BulkAction = BulkOperationTypeAction

// MaxBulkStatusItems is the maximum number of records a single bulk status
// update may touch. A status update is all-or-nothing, so every listed row is
// read, authorized and written inside one transaction, and the cap bounds how
// long that transaction can hold its locks.
const MaxBulkStatusItems = 100

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

// BulkStatusUpdateResult is what a bulk status update reports back. There are
// no per-item results to report: the operation either updates every listed
// record or updates none of them, so a single count says everything there is to
// say about a success, and a failure is carried by the error instead.
type BulkStatusUpdateResult struct {
	Updated int `json:"updated"`
}
