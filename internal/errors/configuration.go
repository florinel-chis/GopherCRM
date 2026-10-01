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
