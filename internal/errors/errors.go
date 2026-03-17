package apperrors

import "fmt"

// LeadAlreadyConvertedError represents an error when trying to convert an already converted lead
type LeadAlreadyConvertedError struct {
	LeadID uint
}

func (e *LeadAlreadyConvertedError) Error() string {
	return fmt.Sprintf("lead %d already converted", e.LeadID)
}

// NewLeadAlreadyConverted creates a new LeadAlreadyConvertedError
func NewLeadAlreadyConverted(leadID uint) error {
	return &LeadAlreadyConvertedError{LeadID: leadID}
}
