package models

// QuoteCreatePolicy says what the caller of QuoteService.CreateWithPolicy may
// do beyond quoting its own deals for itself. It is not persisted.
type QuoteCreatePolicy struct {
	// AnyOwner allows naming an owner other than the caller.
	AnyOwner bool
	// AnyDeal allows quoting a deal the caller does not own.
	AnyDeal bool
}
