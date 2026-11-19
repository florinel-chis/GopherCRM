package models

import "time"

// The two answers a client may give through the response link of a quote
// email (#382). They are what quote_response_tokens.decision stores and what
// POST /quotes/public/response accepts.
const (
	QuoteResponseAccept  = "accept"
	QuoteResponseDecline = "decline"
)

// IsQuoteResponseDecision reports whether value is one of the two answers,
// compared exactly.
func IsQuoteResponseDecision(value string) bool {
	return value == QuoteResponseAccept || value == QuoteResponseDecline
}

// QuoteResponseToken is the single-use credential behind the client response
// link of a quote email (#382). Only the HMAC-SHA256 hash of the token is
// stored; the plain value exists in the mailed link alone. A row is spent by
// one conditional update that also requires its quote to be sent, and it
// records which link the client answered with; the history row of the
// transition has no user and source client (#400). Like quote_status_changes the table carries no foreign key and no
// soft delete, and it holds a hash, ids and timestamps only, so erasure
// keeps the rows; it stamps used_at on the unused ones of the quotes
// addressed to an erased customer, which kills a link mailed earlier (#395).
type QuoteResponseToken struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// The quote the link answers.
	QuoteID uint `gorm:"not null;index:idx_quote_response_tokens_quote_id" json:"quote_id"`
	// hex(HMAC-SHA256(token)); never serialised.
	TokenHash string `gorm:"type:varchar(255);not null;uniqueIndex:idx_quote_response_tokens_token_hash" json:"-"`
	// The start of the day after the quote's valid_until (server's local
	// date), in UTC; NULL when the quote has no valid_until.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// When the row was spent by the client or invalidated by a newer email, a
	// staff status change or the erasure of the quote's customer.
	UsedAt *time.Time `json:"used_at,omitempty"`
	// accept or decline on a row the client spent; NULL on an unused or an
	// invalidated row.
	Decision *string `gorm:"type:varchar(20)" json:"decision,omitempty"`
}

// TableName pins the table name.
func (QuoteResponseToken) TableName() string { return "quote_response_tokens" }

// QuotePublicLine is one line of the quote as the client response link shows
// it: the description and the amounts, no ids.
type QuotePublicLine struct {
	// Line description as it stands on the quote.
	Description string `json:"description" example:"Annual licence"`
	// Quantity, a decimal number with up to three decimals.
	Quantity QuoteQuantity `json:"quantity" swaggertype:"number" example:"3"`
	// Unit price in minor units.
	UnitPriceCents int64 `json:"unit_price_cents" example:"80000"`
	// Line discount in basis points (1250 = 12.5 percent).
	DiscountBP int64 `json:"discount_bp" example:"1250"`
	// Net line total after the discount, in minor units.
	LineTotalCents int64 `json:"line_total_cents" example:"210000"`
	// Tax rate in basis points (2100 = 21 percent).
	TaxRateBP int64 `json:"tax_rate_bp" example:"2100"`
	// Tax of the line, in minor units.
	TaxCents int64 `json:"tax_cents" example:"44100"`
}

// QuotePublicView is everything the unauthenticated response routes disclose
// about a quote (#382): the number, the status, the currency, the validity
// date, the lines and the five totals. It carries no id, title, notes, owner,
// customer, deal or product reference.
type QuotePublicView struct {
	// Quote number as printed in the email, for example Q-000042.
	Number string `json:"number" example:"Q-000042"`
	// Status of the quote: sent while the link is usable, accepted or
	// rejected in the answer to a decision.
	Status QuoteStatus `json:"status" example:"sent"`
	// ISO 4217 code shared by every amount.
	Currency string `json:"currency" example:"EUR"`
	// Last day the offer is valid, YYYY-MM-DD. Omitted when not set.
	ValidUntil *string `json:"valid_until,omitempty" example:"2026-12-31"`
	// The priced lines in display order; an empty array, never null.
	Lines []QuotePublicLine `json:"lines"`
	// Sum of the lines before discounts, in minor units.
	SubtotalCents int64 `json:"subtotal_cents" example:"240000"`
	// Money taken off by the line discounts, in minor units.
	DiscountCents int64 `json:"discount_cents" example:"30000"`
	// Net total after discounts and before tax, in minor units.
	TotalCents int64 `json:"total_cents" example:"210000"`
	// Sum of the lines' tax, in minor units.
	TaxCents int64 `json:"tax_cents" example:"44100"`
	// Gross total: total_cents plus tax_cents, in minor units.
	GrossTotalCents int64 `json:"gross_total_cents" example:"254100"`
}

// NewQuotePublicView builds the public view of a quote loaded with its lines.
func NewQuotePublicView(quote *Quote) *QuotePublicView {
	view := &QuotePublicView{
		Number:          quote.Number(),
		Status:          quote.Status,
		Currency:        quote.Currency,
		ValidUntil:      FormatDealDate(quote.ValidUntil),
		Lines:           make([]QuotePublicLine, 0, len(quote.Lines)),
		SubtotalCents:   quote.SubtotalCents,
		DiscountCents:   quote.DiscountCents,
		TotalCents:      quote.TotalCents,
		TaxCents:        quote.TaxCents,
		GrossTotalCents: quote.GrossTotalCents,
	}
	for _, line := range quote.Lines {
		view.Lines = append(view.Lines, QuotePublicLine{
			Description:    line.Description,
			Quantity:       line.Quantity,
			UnitPriceCents: line.UnitPriceCents,
			DiscountBP:     line.DiscountBP,
			LineTotalCents: line.LineTotalCents,
			TaxRateBP:      line.TaxRateBP,
			TaxCents:       line.TaxCents,
		})
	}
	return view
}

// QuoteRenderView is what the render page of the quote PDF loads with its
// render credential: the quote exactly as GET /quotes/:id returns it, and the
// product name the document is headed with.
type QuoteRenderView struct {
	// The quote with its lines, totals, number and summaries.
	Quote *Quote `json:"quote"`
	// The stored general.company_name, trimmed; empty when it is blank or
	// could not be read, in which case the page uses its built-in name.
	ProductName string `json:"product_name" example:"Acme CRM"`
}

// QuoteRenderViewJSON documents the JSON form of a QuoteRenderView for the API
// specification only (see QuoteJSON); it is never instantiated.
type QuoteRenderViewJSON struct {
	// The quote with its lines, totals, number and summaries, as GET
	// /quotes/{id} returns it.
	Quote QuoteJSON `json:"quote"`
	// The stored general.company_name, trimmed; empty when it is blank or
	// could not be read, in which case the page uses its built-in name.
	ProductName string `json:"product_name" example:"Acme CRM"`
}
