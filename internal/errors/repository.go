package errors

// Repository error constructors

// NewNotFound creates a not found error
func NewNotFound(resourceType string, id interface{}) *AppError {
	return New(CodeNotFound, "Resource not found").
		WithDetail("resource_type", resourceType).
		WithDetail("id", id)
}

// NewDuplicateKey creates a duplicate key error
func NewDuplicateKey(resourceType, field string, value interface{}) *AppError {
	return New(CodeDuplicateKey, "Resource with this value already exists").
		WithDetail("resource_type", resourceType).
		WithDetail("field", field).
		WithDetail("value", value)
}

// NewConstraintViolation creates a constraint violation error
func NewConstraintViolation(constraint string) *AppError {
	return New(CodeConstraintViolation, "Database constraint violation").
		WithDetail("constraint", constraint)
}

// NewDatabaseError creates a database error
func NewDatabaseError(operation string, cause error) *AppError {
	return Wrap(cause, CodeDatabaseError, "Database operation failed").
		WithDetail("operation", operation)
}