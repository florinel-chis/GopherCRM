package models

import (
	"encoding/json"
	"fmt"
	"time"
)

// QuoteStatus is where a quote stands in its lifecycle. The five values are
// fixed; see QuoteTransitions for the moves between them.
type QuoteStatus string

const (
	QuoteStatusDraft    QuoteStatus = "draft"
	QuoteStatusSent     QuoteStatus = "sent"
	QuoteStatusAccepted QuoteStatus = "accepted"
	QuoteStatusRejected QuoteStatus = "rejected"
	QuoteStatusExpired  QuoteStatus = "expired"
)

// QuoteStatuses lists every status in lifecycle order. The handler's `oneof`
// binding tag repeats the same values; TestQuoteStatusBindingTagsMatchTheModel
// in internal/handler/quote_handler_test.go holds the two together.
var QuoteStatuses = []QuoteStatus{
	QuoteStatusDraft,
	QuoteStatusSent,
	QuoteStatusAccepted,
	QuoteStatusRejected,
	QuoteStatusExpired,
}

// quoteTransitions are the only allowed moves: a draft is sent; a sent quote
// is accepted, rejected or expired, or taken back to draft to be revised. The
// three closing statuses are final, and the same status again is not a move.
var quoteTransitions = map[QuoteStatus][]QuoteStatus{
	QuoteStatusDraft: {QuoteStatusSent},
	QuoteStatusSent:  {QuoteStatusAccepted, QuoteStatusRejected, QuoteStatusExpired, QuoteStatusDraft},
}

// IsValid reports whether s is one of QuoteStatuses.
func (s QuoteStatus) IsValid() bool {
	for _, status := range QuoteStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// IsClosed reports whether the status ends the quote: accepted, rejected or
// expired.
func (s QuoteStatus) IsClosed() bool {
	return s == QuoteStatusAccepted || s == QuoteStatusRejected || s == QuoteStatusExpired
}

// CanTransitionTo reports whether the move from s to `to` is one of the
// allowed transitions. The same status again is never allowed.
func (s QuoteStatus) CanTransitionTo(to QuoteStatus) bool {
	for _, allowed := range quoteTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Quote is a priced offer to a customer, optionally grown from a deal, always
// owned by a staff account. It carries its lines and server-computed totals;
// everything, lines included, is editable only while the status is draft.
//
// Money is integer minor units next to one ISO 4217 code shared by every
// line. A quote copies no personal data: the recipient is read from the
// linked customer at send time, so erasing the customer leaves the quote and
// its history untouched (the links are business links, like a deal's).
type Quote struct {
	BaseModel
	Title  string      `gorm:"not null;type:varchar(200)" json:"title"`
	Status QuoteStatus `gorm:"not null;default:'draft';type:varchar(20);index" json:"status"`
	// Currency is immutable once the quote has lines: every line is priced in
	// it and the totals are only meaningful within it.
	Currency string `gorm:"not null;type:char(3)" json:"currency"`
	// The totals carry no column default so an explicit 0 is kept; they are
	// recomputed from the lines by the service on every line change and never
	// taken from a request.
	SubtotalCents int64 `gorm:"not null" json:"subtotal_cents"`
	DiscountCents int64 `gorm:"not null" json:"discount_cents"`
	TotalCents    int64 `gorm:"not null" json:"total_cents"`
	// ValidUntil is a calendar date, stored as local midnight in a DATE
	// column and exposed as YYYY-MM-DD, the deal date convention. Acceptance
	// is refused once it lies in the past; expiry itself is a manual status.
	ValidUntil *time.Time `gorm:"type:date" json:"valid_until,omitempty" swaggertype:"string" format:"date" example:"2026-12-31"`
	Notes      string     `gorm:"type:text" json:"notes"`
	// SentAt is set on draft→sent and cleared on sent→draft; ClosedAt is set
	// on accepted, rejected and expired. LastEmailedAt is stamped by the
	// conditional update that follows a successful email. All three in UTC.
	SentAt        *time.Time `json:"sent_at,omitempty"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	LastEmailedAt *time.Time `json:"last_emailed_at,omitempty"`

	DealID     *uint     `gorm:"index" json:"deal_id,omitempty"`
	Deal       *Deal     `gorm:"foreignKey:DealID" json:"-"`
	CustomerID *uint     `gorm:"index" json:"customer_id,omitempty"`
	Customer   *Customer `gorm:"foreignKey:CustomerID" json:"-"`

	// OwnerID defaults to the caller; only an admin may name somebody else.
	OwnerID uint  `gorm:"not null;index" json:"owner_id"`
	Owner   *User `gorm:"foreignKey:OwnerID" json:"-"`

	// Lines is the only declared side of the quote/line association, so
	// AutoMigrate creates exactly one constraint (fk_quotes_lines) on
	// quote_lines.quote_id.
	Lines []QuoteLine `gorm:"foreignKey:QuoteID" json:"lines"`
}

// QuoteCustomerSummary is what a quote discloses about its customer: the
// display fields, never the contact details.
type QuoteCustomerSummary struct {
	ID        uint   `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company"`
}

// QuoteDealSummary is what a quote discloses about its deal.
type QuoteDealSummary struct {
	ID    uint      `json:"id"`
	Title string    `json:"title"`
	Stage DealStage `json:"stage"`
}

// QuoteOwnerSummary is what a quote discloses about its owner.
type QuoteOwnerSummary struct {
	ID        uint   `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// QuoteNumber renders the human-readable number of a quote: Q- followed by the
// id zero-padded to six digits. It is also the quote number of the email
// subject.
func QuoteNumber(id uint) string {
	return fmt.Sprintf("Q-%06d", id)
}

// Number is QuoteNumber of this quote's id.
func (q Quote) Number() string {
	return QuoteNumber(q.ID)
}

// MarshalJSON adds number, renders valid_until as YYYY-MM-DD and replaces the
// preloaded associations by their summaries, so the response never carries a
// customer's contact details or a user's email.
func (q Quote) MarshalJSON() ([]byte, error) {
	type plain Quote
	out := struct {
		plain
		Number     string                `json:"number"`
		ValidUntil *string               `json:"valid_until,omitempty"`
		Customer   *QuoteCustomerSummary `json:"customer,omitempty"`
		Deal       *QuoteDealSummary     `json:"deal,omitempty"`
		Owner      *QuoteOwnerSummary    `json:"owner,omitempty"`
	}{
		plain:      plain(q),
		Number:     q.Number(),
		ValidUntil: FormatDealDate(q.ValidUntil),
	}
	if out.plain.Lines == nil {
		out.plain.Lines = []QuoteLine{}
	}
	if q.Customer != nil {
		out.Customer = &QuoteCustomerSummary{ID: q.Customer.ID, FirstName: q.Customer.FirstName, LastName: q.Customer.LastName, Company: q.Customer.Company}
	}
	if q.Deal != nil {
		out.Deal = &QuoteDealSummary{ID: q.Deal.ID, Title: q.Deal.Title, Stage: q.Deal.Stage}
	}
	if q.Owner != nil {
		out.Owner = &QuoteOwnerSummary{ID: q.Owner.ID, FirstName: q.Owner.FirstName, LastName: q.Owner.LastName}
	}
	return json.Marshal(out)
}

// QuoteLine is one priced row of a quote. It copies the product's name and
// price when it is added, so the quote reads the same whatever happens to the
// catalogue afterwards; both copies are editable while the quote is a draft.
// The discount is a percentage in basis points (12.5% = 1250) applied per
// line, and line_total_cents is the rounded result of ComputeQuoteLineTotal.
//
// Product is the only declared side of the line/product association, so
// AutoMigrate creates exactly one constraint (fk_quote_lines_product). The
// quote side is declared on Quote.Lines.
type QuoteLine struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	QuoteID   uint      `gorm:"not null;index" json:"quote_id"`
	ProductID uint      `gorm:"not null;index" json:"product_id"`
	Product   *Product  `gorm:"foreignKey:ProductID" json:"product,omitempty"`
	// Position is the 1-based display order, assigned max+1 on insert. No
	// column default, like every bound column below: an explicit 0 would be
	// replaced on create.
	Position       int64  `gorm:"not null" json:"position"`
	Description    string `gorm:"not null;type:varchar(255)" json:"description"`
	Quantity       int64  `gorm:"not null" json:"quantity"`
	UnitPriceCents int64  `gorm:"not null" json:"unit_price_cents"`
	DiscountBP     int64  `gorm:"column:discount_bp;not null" json:"discount_bp"`
	LineTotalCents int64  `gorm:"not null" json:"line_total_cents"`
}

// QuoteLineInput is a line to add: the product it is priced from, the
// quantity, the per-line discount in basis points and the optional overrides
// of the copied price and description. It is the service's input type, kept
// here so the shared mocks can name it.
type QuoteLineInput struct {
	ProductID      uint
	Quantity       int64
	DiscountBP     int64
	UnitPriceCents *int64
	Description    *string
}

// QuoteLinePatch is a partial edit of a line: every nil field keeps the
// stored value.
type QuoteLinePatch struct {
	Quantity       *int64
	DiscountBP     *int64
	UnitPriceCents *int64
	Description    *string
	Position       *int64
}

// QuoteStatusChange is one row of a quote's status history: written in the
// same transaction as the create (from_status NULL) or the transition, never
// updated, never deleted with the quote. Like deal_stage_changes it carries no
// foreign key to its quote; its id is also the monotonic marker the email
// stamp compares against to detect a revision that committed during the send.
type QuoteStatusChange struct {
	ID          uint         `gorm:"primarykey" json:"id"`
	QuoteID     uint         `gorm:"not null;index" json:"quote_id"`
	FromStatus  *QuoteStatus `gorm:"type:varchar(20)" json:"from_status"`
	ToStatus    QuoteStatus  `gorm:"not null;type:varchar(20)" json:"to_status"`
	ChangedByID uint         `gorm:"not null" json:"changed_by_id"`
	ChangedBy   *User        `gorm:"foreignKey:ChangedByID" json:"-"`
	ChangedAt   time.Time    `gorm:"not null" json:"changed_at"`
}

// MarshalJSON replaces the preloaded user by a summary without the email.
func (c QuoteStatusChange) MarshalJSON() ([]byte, error) {
	type plain QuoteStatusChange
	out := struct {
		plain
		ChangedBy *QuoteOwnerSummary `json:"changed_by,omitempty"`
	}{plain: plain(c)}
	if c.ChangedBy != nil {
		out.ChangedBy = &QuoteOwnerSummary{ID: c.ChangedBy.ID, FirstName: c.ChangedBy.FirstName, LastName: c.ChangedBy.LastName}
	}
	return json.Marshal(out)
}

// Column widths of the quotes and quote_lines tables. The handler and the
// service reject a value that would not fit before it reaches the database;
// quote_test.go holds each constant to the width the model declares.
const (
	QuoteTitleMaxLength           = 200
	QuoteStatusMaxLength          = 20
	QuoteCurrencyLength           = 3
	QuoteLineDescriptionMaxLength = 255
	// QuoteNotesMaxBytes is the size of quotes.notes, a TEXT column, bounded
	// the same way as leads.notes.
	QuoteNotesMaxBytes = LeadNotesMaxBytes
)

// Bounds of the quote arithmetic. QuoteAmountCentsMax is the deal bound: a
// line's gross (quantity × unit price) and a quote's total may not exceed it,
// so nothing the service multiplies by a discount can leave an int64 (10^12 ×
// 10^4 is 10^16, far below 2^63). The gross is checked before any discount
// arithmetic for exactly that reason: a maximal quantity times a maximal unit
// price is 10^18, still inside an int64, but times a discount it would not be.
const (
	QuoteAmountCentsMax  int64 = DealAmountCentsMax
	QuoteLineQuantityMax int64 = 1_000_000
	QuoteDiscountBPMax   int64 = 10000
	QuoteLinesMax              = 100
)

// QuoteDiscountBPScale is 100% in basis points.
const QuoteDiscountBPScale int64 = 10000

// ComputeQuoteLineTotal returns a line's gross (quantity × unit price) and its
// total after the per-line discount: gross × (10000 − discount_bp) / 10000,
// rounded half up on minor units. The caller holds gross within
// QuoteAmountCentsMax before calling, so the multiplication cannot overflow.
func ComputeQuoteLineTotal(quantity, unitPriceCents, discountBP int64) (gross, lineTotal int64) {
	gross = quantity * unitPriceCents
	lineTotal = (gross*(QuoteDiscountBPScale-discountBP) + QuoteDiscountBPScale/2) / QuoteDiscountBPScale
	return gross, lineTotal
}

// ComputeQuoteTotals sums the lines: subtotal is Σ gross, total is Σ
// line_total_cents and discount is the difference. The stored line totals are
// trusted; a line is recomputed only when it is written.
func ComputeQuoteTotals(lines []QuoteLine) (subtotal, discount, total int64) {
	for _, line := range lines {
		subtotal += line.Quantity * line.UnitPriceCents
		total += line.LineTotalCents
	}
	return subtotal, subtotal - total, total
}
