package repository

import "gorm.io/gorm"

// ErasedRecords names the leads and the customers one erasure unit erased:
// those whose row the unit locked and found live (deleted_at NULL). A row
// that was already soft-deleted when it was locked is scrubbed again but is
// not listed. Both slices are in ascending id order.
type ErasedRecords struct {
	LeadIDs     []uint
	CustomerIDs []uint
}

// ErasureListener is told, at the very end of an erasure unit and with the
// unit's transaction, which leads and customers the unit erased (#437). An
// error it returns fails the unit, which rolls everything back. A unit that is
// replayed or restarted calls it again inside each attempt, so only the
// attempt that commits keeps what it wrote. It is not called when the unit
// erased no lead and no customer, nor when the unit was refused before its
// writes (ErrConvertedLeadNeedsAdmin, ErrErasureGraphChanged).
//
// It is a production option: cmd/main.go wires the outbound webhook listener
// (webhook.NewErasureListener) on the user, customer and lead
// repositories, and WithTx copies it, so every erasure root reaches it.
type ErasureListener func(tx *gorm.DB, erased ErasedRecords) error

// WithLeadErasureListener sets the erasure listener of NewLeadRepository.
func WithLeadErasureListener(listener ErasureListener) LeadRepositoryOption {
	return func(r *leadRepository) { r.erasureListener = listener }
}

// WithCustomerErasureListener sets the erasure listener of
// NewCustomerRepository.
func WithCustomerErasureListener(listener ErasureListener) CustomerRepositoryOption {
	return func(r *customerRepository) { r.erasureListener = listener }
}

// WithUserErasureListener sets the erasure listener of NewUserRepository.
func WithUserErasureListener(listener ErasureListener) UserRepositoryOption {
	return func(r *userRepository) { r.erasureListener = listener }
}
