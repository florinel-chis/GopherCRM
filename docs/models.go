package docs

// This file contains model definitions for Swagger documentation
// These are used purely for documentation purposes

// APIResponse represents a unified response structure
// @Description Unified API response structure
type APIResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
	Meta    *APIMeta    `json:"meta,omitempty"`
}

// APIError represents error details
// @Description Error details structure
type APIError struct {
	Code    string      `json:"code" example:"VALIDATION_ERROR"`
	Message string      `json:"message" example:"Invalid input data"`
	Details interface{} `json:"details,omitempty"`
}

// APIMeta represents metadata for responses
// @Description Metadata structure for API responses
type APIMeta struct {
	RequestID  string `json:"request_id" example:"123e4567-e89b-12d3-a456-426614174000"`
	Page       int    `json:"page,omitempty" example:"1"`
	PerPage    int    `json:"per_page,omitempty" example:"20"`
	Total      int64  `json:"total,omitempty" example:"100"`
	TotalPages int64  `json:"total_pages,omitempty" example:"5"`
}

// PaginatedResponse represents a paginated API response
// @Description Paginated response structure
type PaginatedResponse struct {
	APIResponse
	Data []interface{} `json:"data"`
}

// AuthResponse represents authentication response
// @Description Authentication response structure
type AuthResponse struct {
	User      interface{} `json:"user"`
	CSRFToken string      `json:"csrf_token,omitempty" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"`
}

// HealthResponse represents health check response
// @Description Health check response
type HealthResponse struct {
	Status string `json:"status" example:"healthy"`
	Time   string `json:"time" example:"2023-01-01T12:00:00Z"`
}

// BulkOperationResponse represents bulk operation response
// @Description Bulk operation response structure
type BulkOperationResponse struct {
	OperationID string `json:"operation_id" example:"op_123456"`
	Status      string `json:"status" example:"completed"`
	ProcessedCount int `json:"processed_count" example:"10"`
	SuccessCount   int `json:"success_count" example:"8"`
	FailureCount   int `json:"failure_count" example:"2"`
	Errors      []string `json:"errors,omitempty"`
}

// DashboardStats represents dashboard statistics
// @Description Dashboard statistics structure
type DashboardStats struct {
	TotalLeads     int64 `json:"total_leads" example:"150"`
	TotalCustomers int64 `json:"total_customers" example:"75"`
	TotalTickets   int64 `json:"total_tickets" example:"25"`
	TotalTasks     int64 `json:"total_tasks" example:"40"`
	OpenTickets    int64 `json:"open_tickets" example:"10"`
	PendingTasks   int64 `json:"pending_tasks" example:"15"`
}