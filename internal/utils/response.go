package utils

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// APIResponse represents a unified response structure
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
	Meta    *APIMeta    `json:"meta,omitempty"`
}

// APIError represents error details
type APIError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// APIMeta represents metadata for responses
type APIMeta struct {
	RequestID  string `json:"request_id"`
	Page       int    `json:"page,omitempty"`
	PerPage    int    `json:"per_page,omitempty"`
	Total      int64  `json:"total,omitempty"`
	TotalPages int64  `json:"total_pages,omitempty"`
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

// ParsePaginationParams extracts pagination parameters from the request
func ParsePaginationParams(c *gin.Context) (page, perPage int) {
	page = 1
	perPage = 20
	
	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	
	if pp := c.Query("per_page"); pp != "" {
		if parsed, err := strconv.Atoi(pp); err == nil && parsed > 0 && parsed <= 100 {
			perPage = parsed
		}
	}
	
	return page, perPage
}

// CalculateOffset calculates the database offset from page and perPage
func CalculateOffset(page, perPage int) int {
	return (page - 1) * perPage
}

// CalculateTotalPages calculates total pages from total items and perPage
func CalculateTotalPages(total int64, perPage int) int {
	if total == 0 || perPage == 0 {
		return 0
	}
	return int((total + int64(perPage) - 1) / int64(perPage))
}