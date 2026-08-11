package errors

import (
	"errors"
	"fmt"
	"net/http"

	"gorm.io/gorm"
)

// Sentinel errors for use with errors.Is() across service/handler boundaries.
// Services return these (wrapped with context via fmt.Errorf) instead of raw strings.
// Handlers check them with errors.Is() instead of string comparison.
var (
	// Business logic sentinel errors
	ErrDuplicateEmail          = errors.New("duplicate email")
	ErrNotFound                = errors.New("not found")
	ErrRecordNotFound          = errors.New("record not found")
	ErrLeadConverted           = errors.New("lead already converted")
	ErrAssigneeNotFound        = errors.New("assignee not found")
	ErrCustomerNotFound        = errors.New("customer not found")
	ErrLeadNotFound            = errors.New("lead not found")
	ErrForbidden               = errors.New("forbidden")
	ErrInvalidAssigneeRole     = errors.New("tickets can only be assigned to support or admin users")
	ErrInvalidCustomerAssignee = errors.New("customers can only be assigned to sales or admin users")
	ErrInactiveUser            = errors.New("cannot assign task to inactive user")
	ErrClosedTicketReopen      = errors.New("cannot reopen closed ticket")
	ErrCompletedTaskModify     = errors.New("cannot change status of completed task")
	ErrTaskLeadCustomerConflict = errors.New("task cannot be linked to both lead and customer")

	// Label errors. ErrDuplicateLabelName is the label counterpart of
	// ErrDuplicateEmail and is answered with 409; the two validation sentinels
	// are answered with 400.
	ErrDuplicateLabelName = errors.New("label with this name already exists")
	ErrInvalidLabelName   = errors.New("label name is required")
	ErrInvalidLabelColor  = errors.New("label color must be a hex value of the form #RRGGBB")
	ErrLabelNotFound      = errors.New("label not found")

	// AEO errors. The two conflict sentinels are answered with 409;
	// ErrProfileNotConfigured is the exception that is answered with 404 on
	// GET /aeo/profile (an unconfigured profile is a missing resource there)
	// and with 409 everywhere else, where it means "configure the brand first".
	// ErrNoProvidersConfigured is answered with 503, since it describes a
	// missing upstream dependency rather than a bad request.
	ErrDuplicatePrompt       = errors.New("a prompt with this text already exists")
	ErrRunInProgress         = errors.New("an AEO run is already in progress")
	ErrProfileNotConfigured  = errors.New("AEO brand profile is not configured")
	ErrNoProvidersConfigured = errors.New("no AEO providers are configured")
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

// LeadAlreadyConvertedError is a typed error for lead conversion conflicts.
// It implements Is(ErrLeadConverted) so callers can use errors.Is().
type LeadAlreadyConvertedError struct {
	LeadID uint
}

func (e *LeadAlreadyConvertedError) Error() string {
	return "lead already converted"
}

func (e *LeadAlreadyConvertedError) Is(target error) bool {
	return target == ErrLeadConverted
}

// IsNotFound checks whether an error represents a "not found" condition,
// whether it is one of our sentinel errors or a gorm.ErrRecordNotFound that
// leaked through. This helper lets callers avoid importing gorm directly.
// gorm's sentinel must be checked by identity: it shares the "record not
// found" message with ErrRecordNotFound but errors.Is compares identity,
// not text, so message equality alone would never match.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrRecordNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}
