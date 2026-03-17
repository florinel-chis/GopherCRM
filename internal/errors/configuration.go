package errors

// Configuration error constructors

// NewConfigNotFound creates a configuration not found error
func NewConfigNotFound(key string) *AppError {
	return New(CodeConfigNotFound, "Configuration not found").
		WithDetail("key", key)
}

// NewConfigReadOnly creates a read-only configuration error
func NewConfigReadOnly(key string) *AppError {
	return New(CodeConfigReadOnly, "Configuration is read-only").
		WithDetail("key", key)
}

// NewInvalidConfigValue creates an invalid configuration value error
func NewInvalidConfigValue(key string, value interface{}) *AppError {
	return New(CodeInvalidConfigValue, "Invalid value for configuration").
		WithDetail("key", key).
		WithDetail("value", value)
}

// NewConfigTypeMismatch creates a configuration type mismatch error
func NewConfigTypeMismatch(key, expected, actual string) *AppError {
	return New(CodeConfigTypeMismatch, "Configuration type mismatch").
		WithDetail("key", key).
		WithDetail("expected_type", expected).
		WithDetail("actual_type", actual)
}

// NewSystemConfigDeletion creates an error for trying to delete system configurations
func NewSystemConfigDeletion(key string) *AppError {
	return New(CodeConfigReadOnly, "Cannot delete system configuration").
		WithDetail("key", key)
}