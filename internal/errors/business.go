package errors

// Business logic error constructors

// NewEmailExists creates an email already exists error
func NewEmailExists(email string) *AppError {
	return New(CodeEmailExists, "A user with this email already exists").
		WithDetail("email", email)
}

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

// NewResourceConflict creates a resource conflict error
func NewResourceConflict(message string) *AppError {
	return New(CodeResourceConflict, message)
}

// NewCustomerNotFound creates a customer not found error
func NewCustomerNotFound(customerID uint) *AppError {
	return New(CodeNotFound, "Customer not found").
		WithDetail("resource_type", "customer").
		WithDetail("id", customerID)
}

// NewUserNotFound creates a user not found error
func NewUserNotFound(userID uint) *AppError {
	return New(CodeNotFound, "User not found").
		WithDetail("resource_type", "user").
		WithDetail("id", userID)
}

// NewLeadNotFound creates a lead not found error
func NewLeadNotFound(leadID uint) *AppError {
	return New(CodeNotFound, "Lead not found").
		WithDetail("resource_type", "lead").
		WithDetail("id", leadID)
}

// NewTaskNotFound creates a task not found error
func NewTaskNotFound(taskID uint) *AppError {
	return New(CodeNotFound, "Task not found").
		WithDetail("resource_type", "task").
		WithDetail("id", taskID)
}

// NewTicketNotFound creates a ticket not found error
func NewTicketNotFound(ticketID uint) *AppError {
	return New(CodeNotFound, "Ticket not found").
		WithDetail("resource_type", "ticket").
		WithDetail("id", ticketID)
}

// NewInvalidAssignee creates an invalid assignee error
func NewInvalidAssignee(userID uint, requiredRoles []string) *AppError {
	return New(CodeInvalidReference, "Invalid assignee").
		WithDetail("user_id", userID).
		WithDetail("required_roles", requiredRoles)
}

// NewInactiveUser creates an inactive user error
func NewInactiveUser(userID uint) *AppError {
	return New(CodeResourceConflict, "Cannot assign to inactive user").
		WithDetail("user_id", userID)
}

// NewCompletedTaskModification creates an error for trying to modify completed tasks
func NewCompletedTaskModification() *AppError {
	return New(CodeInvalidStatusTransition, "Cannot modify completed task")
}

// NewClosedTicketReopen creates an error for trying to reopen closed tickets
func NewClosedTicketReopen() *AppError {
	return New(CodeInvalidStatusTransition, "Cannot reopen closed ticket")
}

// NewTaskLeadCustomerConflict creates an error for tasks linked to both lead and customer
func NewTaskLeadCustomerConflict() *AppError {
	return New(CodeResourceConflict, "Task cannot be linked to both lead and customer")
}