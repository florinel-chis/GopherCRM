package errors

import "errors"

// Configuration sentinel errors. The configuration service wraps these with
// fmt.Errorf("%w", …) so the handler can classify a failure with errors.Is()
// instead of comparing error strings. Not-found is not repeated here: it uses
// the shared ErrNotFound sentinel, so IsNotFound() covers configurations too.
var (
	// ErrConfigurationReadOnly means the entry exists but the API may not change it.
	ErrConfigurationReadOnly = errors.New("configuration is read-only")

	// ErrConfigurationInvalidValue means the value fails the entry's valid_values constraint.
	ErrConfigurationInvalidValue = errors.New("invalid value for configuration")

	// ErrConfigurationSystemDelete means a system entry cannot be deleted.
	ErrConfigurationSystemDelete = errors.New("cannot delete system configuration")
)

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