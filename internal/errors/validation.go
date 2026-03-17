package errors

// Validation error constructors

// NewValidationFailed creates a validation failed error with field details
func NewValidationFailed(field, message string) *AppError {
	return New(CodeValidationFailed, "Validation failed").
		WithDetail("field", field).
		WithDetail("message", message)
}

// NewRequiredField creates a required field error
func NewRequiredField(field string) *AppError {
	return New(CodeRequiredField, "Required field is missing").
		WithDetail("field", field)
}

// NewInvalidFormat creates an invalid format error
func NewInvalidFormat(field, expectedFormat string) *AppError {
	return New(CodeInvalidFormat, "Invalid format").
		WithDetail("field", field).
		WithDetail("expected_format", expectedFormat)
}

// NewInvalidInput creates an invalid input error
func NewInvalidInput(message string) *AppError {
	return New(CodeInvalidInput, message)
}

// NewInvalidReference creates an invalid reference error
func NewInvalidReference(resourceType string, id interface{}) *AppError {
	return New(CodeInvalidReference, "Referenced resource not found").
		WithDetail("resource_type", resourceType).
		WithDetail("id", id)
}