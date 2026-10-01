package errors

// Business logic error constructors

// NewLeadAlreadyConverted creates a lead already converted error
// Returns a simple error type for backward compatibility with string-based error checks in handlers
func NewLeadAlreadyConverted(leadID uint) error {
	return &LeadAlreadyConvertedError{LeadID: leadID}
}

// NewInvalidStatusTransition creates an invalid status transition error
func NewInvalidStatusTransition(fromStatus, toStatus string) *AppError {
	return New(CodeInvalidStatusTransition, "Invalid status transition").
		WithDetail("from_status", fromStatus).
		WithDetail("to_status", toStatus)
}

// NewCompletedTaskModification creates an error for trying to modify completed tasks
func NewCompletedTaskModification() *AppError {
	return New(CodeInvalidStatusTransition, "Cannot modify completed task")
}

// NewClosedTicketReopen creates an error for trying to reopen closed tickets
func NewClosedTicketReopen() *AppError {
	return New(CodeInvalidStatusTransition, "Cannot reopen closed ticket")
}
