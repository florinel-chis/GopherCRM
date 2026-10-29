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
	ErrDuplicateEmail           = errors.New("duplicate email")
	ErrNotFound                 = errors.New("not found")
	ErrRecordNotFound           = errors.New("record not found")
	ErrLeadConverted            = errors.New("lead already converted")
	ErrLeadMissingEmail         = errors.New("lead has no email address")
	ErrAssigneeNotFound         = errors.New("assignee not found")
	ErrCustomerNotFound         = errors.New("customer not found")
	ErrLeadNotFound             = errors.New("lead not found")
	ErrForbidden                = errors.New("forbidden")
	ErrInvalidAssigneeRole      = errors.New("tickets can only be assigned to support or admin users")
	ErrInvalidCustomerAssignee  = errors.New("customers can only be assigned to sales or admin users")
	ErrInactiveUser             = errors.New("cannot assign task to inactive user")
	ErrClosedTicketReopen       = errors.New("cannot reopen closed ticket")
	ErrCompletedTaskModify      = errors.New("cannot change status of completed task")
	ErrTaskLeadCustomerConflict = errors.New("task cannot be linked to both lead and customer")

	// ErrValidation is the generic validation sentinel: the request was
	// understood but describes something the domain refuses. Services wrap
	// their own validation failures in it — including the typed ones, such as
	// models.ErrInvalidFormDefinition — so a handler can answer 400 without
	// knowing every rule that could have failed.
	ErrValidation = errors.New("validation failed")

	// Label errors. ErrDuplicateLabelName is the label counterpart of
	// ErrDuplicateEmail and is answered with 409; the two validation sentinels
	// are answered with 400.
	ErrDuplicateLabelName = errors.New("label with this name already exists")
	ErrInvalidLabelName   = errors.New("label name is required")
	ErrInvalidLabelColor  = errors.New("label color must be a hex value of the form #RRGGBB")
	ErrLabelNotFound      = errors.New("label not found")

	// Company errors. ErrDuplicateCompanyDomain is the company counterpart of
	// ErrDuplicateEmail and is answered with 409. ErrCompanyNotFound is what a
	// lead or customer create/update gets back for a company_id that matches no
	// live company; the handlers answer it with 400 INVALID_REFERENCE, because
	// the bad reference is in the body, not at the requested path.
	ErrDuplicateCompanyDomain = errors.New("a company with this domain already exists")
	ErrCompanyNotFound        = errors.New("company not found")

	// Product and quote errors. The *NotFound sentinels follow the company
	// rule: at the requested path they are a 404, inside a request body (a
	// quote naming a deal, customer or product) they are 400 INVALID_REFERENCE.
	ErrProductNotFound   = errors.New("product not found")
	ErrQuoteNotFound     = errors.New("quote not found")
	ErrQuoteLineNotFound = errors.New("quote line not found")
	ErrDealNotFound      = errors.New("deal not found")
	// ErrDealClosed refuses a quote on a deal that is won or lost (409).
	ErrDealClosed = errors.New("deal is closed")
	// ErrQuoteLocked is every write to a quote that is not a draft, and a
	// sales user's delete of a non-draft quote (409).
	ErrQuoteLocked = errors.New("quote can only be changed while it is a draft")
	// ErrQuoteCurrencyLocked refuses a currency change once the quote has
	// lines priced in the old one (409).
	ErrQuoteCurrencyLocked = errors.New("quote currency cannot change once it has lines")
	// ErrQuoteCurrencyMismatch refuses a product priced in another currency
	// than the quote's (400 VALIDATION_ERROR).
	ErrQuoteCurrencyMismatch = errors.New("product currency does not match the quote currency")
	// ErrInvalidQuoteTransition is a disallowed status pair, the same status
	// again, or a transition that lost its race (400 INVALID_STATUS_TRANSITION).
	ErrInvalidQuoteTransition = errors.New("invalid quote status transition")
	// ErrQuoteHasNoLines refuses draft→sent for a quote without lines (409).
	ErrQuoteHasNoLines = errors.New("quote has no lines")
	// ErrQuoteExpired refuses acceptance after valid_until (409).
	ErrQuoteExpired = errors.New("quote is past its valid_until date")
	// ErrQuoteNotSent refuses to email a quote that is neither sent nor
	// accepted (409).
	ErrQuoteNotSent = errors.New("quote must be sent or accepted to be emailed")
	// ErrQuoteNoRecipient refuses to email a quote without a live customer (409).
	ErrQuoteNoRecipient = errors.New("quote has no customer to email")
	// ErrQuoteChangedDuringSend is a revision to draft that committed between
	// the read and the stamp of an email (409); the mail went out but the
	// quote is not marked as emailed.
	ErrQuoteChangedDuringSend = errors.New("quote was revised while the email was being sent")
	// ErrMailDelivery wraps a mailer failure (502 MAIL_DELIVERY_FAILED).
	ErrMailDelivery = errors.New("mail delivery failed")

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
	// ErrGenerationProviderNotConfigured is the generation-specific variant:
	// prompt generation runs on one named engine (configurable, Anthropic by
	// default), so other configured providers do not help. The service wraps
	// this with a message naming the selected engine. Also answered with 503.
	ErrGenerationProviderNotConfigured = errors.New("the prompt generation engine has no API key configured")

	// Follow-up automation errors. ErrAutomationRuleNotFound and
	// ErrAutomationRunNotFound are 404 at the requested path; a sales caller
	// asking for a run on a record it does not own gets the same not-found.
	// ErrAutomationRunFinished refuses a cancel of a completed, failed or
	// cancelled run (409 INVALID_STATUS_TRANSITION). Rule validation failures
	// unwrap to ErrValidation (400 INVALID_INPUT) and an unknown or inactive
	// user named by a step to ErrAssigneeNotFound (400 INVALID_REFERENCE).
	ErrAutomationRuleNotFound = errors.New("automation rule not found")
	ErrAutomationRunNotFound  = errors.New("automation run not found")
	ErrAutomationRunFinished  = errors.New("automation run is already finished")
	// ErrNotificationNotFound is a notification that does not exist or that
	// belongs to another user: both are 404.
	ErrNotificationNotFound = errors.New("notification not found")
)

// Error codes
const (
	// Auth errors
	CodeInsufficientPermissions = "INSUFFICIENT_PERMISSIONS"

	// Validation errors
	CodeInvalidInput     = "INVALID_INPUT"
	CodeInvalidReference = "INVALID_REFERENCE"

	// Business logic errors
	CodeInvalidStatusTransition = "INVALID_STATUS_TRANSITION"

	// Repository errors
	CodeNotFound = "NOT_FOUND"

	// General errors
	CodeInternal = "INTERNAL_ERROR"
)

// httpStatusMap maps error codes to HTTP status codes
var httpStatusMap = map[string]int{
	CodeInsufficientPermissions: http.StatusForbidden,
	CodeInvalidInput:            http.StatusBadRequest,
	CodeInvalidReference:        http.StatusBadRequest,
	CodeInvalidStatusTransition: http.StatusBadRequest,
	CodeNotFound:                http.StatusNotFound,
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
