package errors

import (
	"fmt"
	"net/http"
)

// Error codes
const (
	// Auth errors
	CodeInvalidCredentials      = "INVALID_CREDENTIALS"
	CodeAccountDisabled         = "ACCOUNT_DISABLED"
	CodeInvalidToken            = "INVALID_TOKEN"
	CodeTokenExpired            = "TOKEN_EXPIRED"
	CodeInvalidAPIKey           = "INVALID_API_KEY"
	CodeAPIKeyExpired           = "API_KEY_EXPIRED"
	CodeUnauthorized            = "UNAUTHORIZED"
	CodeInsufficientPermissions = "INSUFFICIENT_PERMISSIONS"

	// Validation errors
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeRequiredField    = "REQUIRED_FIELD"
	CodeInvalidFormat    = "INVALID_FORMAT"
	CodeInvalidInput     = "INVALID_INPUT"
	CodeInvalidReference = "INVALID_REFERENCE"

	// Business logic errors
	CodeEmailExists              = "EMAIL_EXISTS"
	CodeLeadAlreadyConverted     = "LEAD_ALREADY_CONVERTED"
	CodeInvalidStatusTransition  = "INVALID_STATUS_TRANSITION"
	CodeResourceConflict         = "RESOURCE_CONFLICT"

	// Repository errors
	CodeNotFound            = "NOT_FOUND"
	CodeDuplicateKey        = "DUPLICATE_KEY"
	CodeConstraintViolation = "CONSTRAINT_VIOLATION"
	CodeDatabaseError       = "DATABASE_ERROR"

	// Configuration errors
	CodeConfigNotFound      = "CONFIG_NOT_FOUND"
	CodeConfigReadOnly      = "CONFIG_READ_ONLY"
	CodeInvalidConfigValue  = "INVALID_CONFIG_VALUE"
	CodeConfigTypeMismatch  = "CONFIG_TYPE_MISMATCH"

	// General errors
	CodeInternal = "INTERNAL_ERROR"
)

// httpStatusMap maps error codes to HTTP status codes
var httpStatusMap = map[string]int{
	CodeInvalidCredentials:      http.StatusUnauthorized,
	CodeAccountDisabled:         http.StatusForbidden,
	CodeInvalidToken:            http.StatusUnauthorized,
	CodeTokenExpired:            http.StatusUnauthorized,
	CodeInvalidAPIKey:           http.StatusUnauthorized,
	CodeAPIKeyExpired:           http.StatusUnauthorized,
	CodeUnauthorized:            http.StatusUnauthorized,
	CodeInsufficientPermissions: http.StatusForbidden,
	CodeValidationFailed:        http.StatusBadRequest,
	CodeRequiredField:           http.StatusBadRequest,
	CodeInvalidFormat:           http.StatusBadRequest,
	CodeInvalidInput:            http.StatusBadRequest,
	CodeInvalidReference:        http.StatusBadRequest,
	CodeEmailExists:             http.StatusConflict,
	CodeLeadAlreadyConverted:    http.StatusConflict,
	CodeInvalidStatusTransition: http.StatusBadRequest,
	CodeResourceConflict:        http.StatusConflict,
	CodeNotFound:                http.StatusNotFound,
	CodeDuplicateKey:            http.StatusConflict,
	CodeConstraintViolation:     http.StatusConflict,
	CodeDatabaseError:           http.StatusInternalServerError,
	CodeConfigNotFound:          http.StatusNotFound,
	CodeConfigReadOnly:          http.StatusForbidden,
	CodeInvalidConfigValue:      http.StatusBadRequest,
	CodeConfigTypeMismatch:      http.StatusBadRequest,
	CodeInternal:                http.StatusInternalServerError,
}

// AppError is the structured application error type
type AppError struct {
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	HTTPStatus int                    `json:"http_status"`
	Details    map[string]interface{} `json:"details,omitempty"`
	Cause      error                  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Cause
}

// WithDetail adds a detail key-value pair to the error
func (e *AppError) WithDetail(key string, value interface{}) *AppError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// New creates a new AppError with the given code and message
func New(code, message string) *AppError {
	status, ok := httpStatusMap[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &AppError{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}
}

// Wrap wraps an existing error with an AppError
func Wrap(cause error, code, message string) *AppError {
	appErr := New(code, message)
	appErr.Cause = cause
	return appErr
}

// AsAppError attempts to extract an AppError from an error
func AsAppError(err error) (*AppError, bool) {
	if err == nil {
		return nil, false
	}
	if appErr, ok := err.(*AppError); ok {
		return appErr, true
	}
	return nil, false
}

// LeadAlreadyConvertedError is a simple error type for backward compatibility
// with string-based error checks in handlers (err.Error() == "lead already converted")
type LeadAlreadyConvertedError struct {
	LeadID uint
}

func (e *LeadAlreadyConvertedError) Error() string {
	return "lead already converted"
}
