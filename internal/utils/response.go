package utils

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// APIResponse represents a unified response structure
//
// @Description Envelope of every JSON response (the CSV export and the public form pages are the exceptions). success tells the outcome; data carries the payload on success, error the failure on error; meta is always present.
type APIResponse struct {
	// True for a 2xx response, false when error is set.
	Success bool `json:"success" example:"true"`
	// Operation-specific payload on success (each operation documents its
	// shape). Absent on errors and on successes without a payload.
	Data interface{} `json:"data,omitempty"`
	// Failure details; present only when success is false.
	Error *APIError `json:"error,omitempty"`
	// Request metadata, present on every enveloped response, with pagination
	// fields on paginated lists.
	Meta *APIMeta `json:"meta,omitempty"`
}

// APIError represents error details
//
// @Description Error object of a failed request. code is the stable value to branch on; message is for people and may change.
type APIError struct {
	// Stable machine-readable error code. Shared codes: VALIDATION_ERROR,
	// BAD_REQUEST, UNAUTHORIZED, FORBIDDEN, NOT_FOUND, CONFLICT,
	// TOO_MANY_REQUESTS, INTERNAL_ERROR, MAIL_DELIVERY_FAILED; operations
	// document their own additional codes (for example INVALID_REFERENCE,
	// SESSION_STALE, SESSION_REFRESH_UNAVAILABLE).
	Code string `json:"code" example:"VALIDATION_ERROR"`
	// Human-readable explanation. Not stable: do not parse it.
	Message string `json:"message" example:"Validation failed"`
	// Optional context; its JSON type depends on the code and the operation,
	// so the schema leaves it open. Usually an object: a binding failure
	// (VALIDATION_ERROR) maps each failed field to a message, keyed by the
	// server-side Go field name (for example "Email", "FirstName"), not the
	// JSON name; TOO_MANY_REQUESTS carries {"retry_after": "60s"}. Some
	// operations send another shape (for example a plain string on POST
	// /aeo/prompts/{id}/run with an invalid id) and document it; many errors
	// omit it.
	Details interface{} `json:"details,omitempty"`
}

// APIMeta represents metadata for responses
//
// @Description Response metadata. request_id is always present; the pagination fields appear only on paginated list responses and, like every zero value here, are omitted when 0.
type APIMeta struct {
	// Correlation id of the request: the client's X-Request-ID header when
	// sent, otherwise a server-generated UUID. Also returned in the
	// X-Request-ID response header.
	RequestID string `json:"request_id" example:"3b1f8c2e-6d4a-4f0e-9a51-2c7d9e8b1a40"`
	// 1-based page number of this response (paginated lists only; omitted when 0).
	Page int `json:"page,omitempty" example:"1"`
	// Page size used for this response (paginated lists only; omitted when 0).
	PerPage int `json:"per_page,omitempty" example:"20"`
	// Total number of matching records (paginated lists only; omitted when
	// 0, so an empty result carries no total).
	Total int64 `json:"total,omitempty" example:"57"`
	// Number of pages at per_page (paginated lists only; omitted when 0).
	TotalPages int64 `json:"total_pages,omitempty" example:"3"`
}

// Common error codes
const (
	ErrCodeValidation      = "VALIDATION_ERROR"
	ErrCodeUnauthorized    = "UNAUTHORIZED"
	ErrCodeForbidden       = "FORBIDDEN"
	ErrCodeNotFound        = "NOT_FOUND"
	ErrCodeConflict        = "CONFLICT"
	ErrCodeInternal        = "INTERNAL_ERROR"
	ErrCodeBadRequest      = "BAD_REQUEST"
	ErrCodeTooManyRequests = "TOO_MANY_REQUESTS"
	// ErrCodeMailDeliveryFailed is answered with 502 when the mailer refused
	// or failed to deliver a message the request asked for.
	ErrCodeMailDeliveryFailed = "MAIL_DELIVERY_FAILED"
)

// RespondSuccess sends a successful response
func RespondSuccess(c *gin.Context, statusCode int, data interface{}) {
	response := APIResponse{
		Success: true,
		Data:    data,
		Meta: &APIMeta{
			RequestID: c.GetString("request_id"),
		},
	}
	c.JSON(statusCode, response)
}

// RespondSuccessWithMeta sends a successful response with metadata
func RespondSuccessWithMeta(c *gin.Context, statusCode int, data interface{}, meta *APIMeta) {
	if meta == nil {
		meta = &APIMeta{}
	}
	meta.RequestID = c.GetString("request_id")

	response := APIResponse{
		Success: true,
		Data:    data,
		Meta:    meta,
	}
	c.JSON(statusCode, response)
}

// RespondError sends an error response
func RespondError(c *gin.Context, statusCode int, errorCode, message string, details interface{}) {
	response := APIResponse{
		Success: false,
		Error: &APIError{
			Code:    errorCode,
			Message: message,
			Details: details,
		},
		Meta: &APIMeta{
			RequestID: c.GetString("request_id"),
		},
	}
	c.JSON(statusCode, response)
}

// RespondValidationError sends a validation error response
func RespondValidationError(c *gin.Context, errors interface{}) {
	RespondError(c, http.StatusBadRequest, ErrCodeValidation, "Validation failed", errors)
}

// RespondUnauthorized sends an unauthorized error response
func RespondUnauthorized(c *gin.Context, message string) {
	if message == "" {
		message = "Authentication required"
	}
	RespondError(c, http.StatusUnauthorized, ErrCodeUnauthorized, message, nil)
}

// RespondForbidden sends a forbidden error response
func RespondForbidden(c *gin.Context, message string) {
	if message == "" {
		message = "Access denied"
	}
	RespondError(c, http.StatusForbidden, ErrCodeForbidden, message, nil)
}

// RespondNotFound sends a not found error response
func RespondNotFound(c *gin.Context, resource string) {
	message := "Resource not found"
	if resource != "" {
		message = resource
	}
	RespondError(c, http.StatusNotFound, ErrCodeNotFound, message, nil)
}

// RespondConflict sends a conflict error response
func RespondConflict(c *gin.Context, message string) {
	if message == "" {
		message = "Resource conflict"
	}
	RespondError(c, http.StatusConflict, ErrCodeConflict, message, nil)
}

// RespondInternalError sends an internal server error response
func RespondInternalError(c *gin.Context) {
	RespondError(c, http.StatusInternalServerError, ErrCodeInternal, "An unexpected error occurred", nil)
}

// RespondBadRequest sends a bad request error response
func RespondBadRequest(c *gin.Context, message string) {
	if message == "" {
		message = "Bad request"
	}
	RespondError(c, http.StatusBadRequest, ErrCodeBadRequest, message, nil)
}

// ParseOffsetLimit extracts offset/limit pagination parameters from the request,
// clamping them to a usable range. limit is never returned as 0, so callers can
// safely divide by it when building pagination metadata.
func ParseOffsetLimit(c *gin.Context) (offset, limit int) {
	offset = 0
	limit = 20

	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed > 0 {
			offset = parsed
		}
	}

	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}

	return offset, limit
}
