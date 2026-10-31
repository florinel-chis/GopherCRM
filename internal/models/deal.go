package models

import (
	"encoding/json"
	"time"
)

// DealStage is where a deal stands in the pipeline. The five values are fixed;
// configurable stages are out of scope for this round.
type DealStage string

const (
	DealStageQualification DealStage = "qualification"
	DealStageProposal      DealStage = "proposal"
	DealStageNegotiation   DealStage = "negotiation"
	DealStageWon           DealStage = "won"
	DealStageLost          DealStage = "lost"
)

// DealStages lists every stage in pipeline order. The handler's `oneof`
// binding tag repeats the same values; TestDealStageBindingTagsMatchTheModel
// in internal/handler/deal_handler_test.go holds the two together.
var DealStages = []DealStage{
	DealStageQualification,
	DealStageProposal,
	DealStageNegotiation,
	DealStageWon,
	DealStageLost,
}

// IsValid reports whether s is one of DealStages.
func (s DealStage) IsValid() bool {
	for _, stage := range DealStages {
		if s == stage {
			return true
		}
	}
	return false
}

// IsClosed reports whether the stage ends the deal: won or lost. Everything
// else is an open stage, which is what the `open=true` list filter selects.
func (s DealStage) IsClosed() bool {
	return s == DealStageWon || s == DealStageLost
}

// DefaultProbability is the probability a deal takes when it enters the stage
// without an explicit one: 10/40/70 for the open stages, and the two closed
// stages are fixed at 100 and 0 whatever the caller sends.
func (s DealStage) DefaultProbability() int {
	switch s {
	case DealStageQualification:
		return 10
	case DealStageProposal:
		return 40
	case DealStageNegotiation:
		return 70
	case DealStageWon:
		return 100
	default:
		return 0
	}
}

// DealDefaultCurrency is the shipped value of the `deals.default_currency`
// configuration key, and what the service falls back to when the stored value
// is not a three-letter code.
const DealDefaultCurrency = "EUR"

// Deal is one sales opportunity, optionally attached to a company, a customer
// and the lead it grew from, and always owned by a staff account.
//
// Money is an integer amount in minor units (cents) next to an ISO 4217 code:
// exact on MySQL, MariaDB and SQLite alike, with no decimal dependency. Sums
// are only ever meaningful per currency.
//
// A deal holds no personal data by design: the title and notes are free text
// and may be typed to contain a name, the same accepted limitation as a
// ticket subject. The links (company_id, customer_id, lead_id, owner_id) are
// business links like assigned_to_id, so erasing the person behind any of them
// leaves the deal and its history untouched.
type Deal struct {
	BaseModel
	// Deal title, trimmed, 1 to 200 characters. Required.
	Title string `gorm:"not null;type:varchar(200)" json:"title" example:"Acme rollout 2027"`
	// Pipeline stage. Defaults to qualification on create. Every change is
	// recorded in the stage history (GET /deals/{id}/history).
	Stage DealStage `gorm:"not null;default:'qualification';type:varchar(20)" json:"stage" example:"proposal"`
	// Deal value in minor units of currency (cents for EUR), an integer from
	// 0 to 1000000000000 (10^12). Sums are meaningful per currency only.
	AmountCents int64 `gorm:"not null;default:0" json:"amount_cents" minimum:"0" maximum:"1000000000000" example:"1250000"`
	// ISO 4217 code: exactly three upper-case ASCII letters. Defaults to the
	// deals.default_currency setting (EUR as shipped) on create.
	Currency string `gorm:"not null;type:char(3)" json:"currency" minLength:"3" maxLength:"3" example:"EUR"`

	// Probability carries no column default on purpose: a GORM default would
	// replace an explicit 0 on create, and a lost deal is stored at 0.

	// Win probability in percent, 0 to 100. Takes the stage's default when a
	// stage is entered without an explicit value (qualification 10, proposal
	// 40, negotiation 70); won is always 100 and lost always 0.
	Probability int `gorm:"not null" json:"probability" minimum:"0" maximum:"100" example:"40"`

	// ExpectedCloseDate is a calendar date. It is stored in a DATE column as
	// midnight in the process's local zone (the MySQL DSN uses loc=Local, so
	// that is the value that round-trips exactly on every engine) and exposed
	// as YYYY-MM-DD; see MarshalJSON.

	// Expected close date as a calendar date, YYYY-MM-DD (no time, no zone).
	// Omitted when not set.
	ExpectedCloseDate *time.Time `gorm:"type:date" json:"expected_close_date,omitempty" swaggertype:"string" format:"date" example:"2026-12-31"`
	// When the deal entered won or lost (UTC, RFC 3339). Set by the server on
	// entering a closed stage, cleared on returning to an open one; omitted
	// while the deal is open. Read-only.
	ClosedAt *time.Time `json:"closed_at,omitempty" readonly:"true" example:"2026-10-15T09:30:00Z"`
	// Why the deal was lost, at most 255 characters. Kept only while the
	// stage is lost; empty otherwise.
	LostReason string `gorm:"type:varchar(255)" json:"lost_reason" example:""`
	// Where the deal came from, free text, at most 100 characters.
	Source string `gorm:"type:varchar(100)" json:"source" example:"referral"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes, not characters).
	Notes string `gorm:"type:text" json:"notes" example:"Decision expected after the board meeting."`

	// Linked company. Omitted when there is none; cleared when the company
	// is deleted.
	CompanyID *uint `gorm:"index" json:"company_id,omitempty" example:"12"`
	// The linked company, loaded by the deal endpoints (its customer_count,
	// lead_count and deal_count are 0 here, not computed). Omitted when there
	// is no link or the company no longer exists.
	Company *Company `gorm:"foreignKey:CompanyID" json:"company,omitempty"`
	// Linked customer. Omitted when there is none. Erasing the customer
	// keeps the link.
	CustomerID *uint `gorm:"index" json:"customer_id,omitempty" example:"31"`
	// The linked customer, loaded by the deal endpoints; omitted when there
	// is no link or the customer was erased (soft-deleted).
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	// Linked lead. Omitted when there is none AND whenever the caller may not
	// see that lead (admins see every lead, sales users only the leads they
	// own); decided on every read.
	LeadID *uint `gorm:"index" json:"lead_id,omitempty" example:"55"`
	// The linked lead, under the same visibility rule as lead_id.
	Lead *Lead `gorm:"foreignKey:LeadID" json:"lead,omitempty"`

	// OwnerID is the staff account working the deal. Never null: it defaults
	// to the caller, and only an admin may name somebody else.

	// Staff user working the deal. Never null: defaults to the caller; only
	// an admin may name somebody else. Sales users see only their own deals.
	OwnerID uint `gorm:"not null;index" json:"owner_id" example:"7"`
	// The owner's user record, loaded by the deal endpoints.
	Owner *User `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
}

// MarshalJSON renders expected_close_date as YYYY-MM-DD instead of the RFC 3339
// timestamp time.Time would produce. The date is formatted in its own location,
// which is the calendar date that was stored whichever engine read it back.
func (d Deal) MarshalJSON() ([]byte, error) {
	type plain Deal
	return json.Marshal(struct {
		plain
		ExpectedCloseDate *string `json:"expected_close_date,omitempty"`
	}{
		plain:             plain(d),
		ExpectedCloseDate: FormatDealDate(d.ExpectedCloseDate),
	})
}

// DealDateLayout is the wire format of expected_close_date.
const DealDateLayout = "2006-01-02"

// FormatDealDate renders a date pointer as YYYY-MM-DD, or nil for nil.
func FormatDealDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(DealDateLayout)
	return &s
}

// ParseDealDate turns YYYY-MM-DD into midnight of that day in the local zone,
// the form ExpectedCloseDate is stored in.
func ParseDealDate(s string) (time.Time, error) {
	return time.ParseInLocation(DealDateLayout, s, time.Local)
}

// DealStageChange is one row of a deal's stage history: written in the same
// transaction as the create (from_stage NULL) or the stage change, never
// updated, never deleted with the deal (a soft-deleted deal keeps its history).
// The column names are from_stage and to_stage because `from` and `to` are
// reserved words; `stage` is not, on any of the three engines.
type DealStageChange struct {
	// History row id, increasing in write order.
	ID uint `gorm:"primarykey" json:"id" example:"301"`
	// The deal this row belongs to.
	DealID uint `gorm:"not null;index" json:"deal_id" example:"88"`
	// Stage before the change; null on the first row, written when the deal
	// was created.
	FromStage *DealStage `gorm:"type:varchar(20)" json:"from_stage" extensions:"x-nullable" example:"qualification"`
	// Stage after the change.
	ToStage DealStage `gorm:"not null;type:varchar(20)" json:"to_stage" example:"proposal"`
	// User who made the change.
	ChangedByID uint `gorm:"not null" json:"changed_by_id" example:"7"`
	// The acting user's full record (including email), loaded by GET
	// /deals/{id}/history; omitted when that account no longer exists.
	ChangedBy *User `gorm:"foreignKey:ChangedByID" json:"changed_by,omitempty"`
	// When the change was written (UTC, RFC 3339).
	ChangedAt time.Time `gorm:"not null" json:"changed_at" example:"2026-10-01T08:00:00Z"`
}

// Column widths of the deals table. The handler rejects a value that would not
// fit before it reaches the database; deal_test.go holds each constant to the
// width the model declares.
const (
	DealTitleMaxLength      = 200
	DealStageMaxLength      = 20
	DealCurrencyLength      = 3
	DealLostReasonMaxLength = 255
	DealSourceMaxLength     = 100
	// DealNotesMaxBytes is the size of deals.notes, a TEXT column, bounded
	// the same way as leads.notes.
	DealNotesMaxBytes = LeadNotesMaxBytes
)

// DealAmountCentsMax is the largest amount_cents a deal may carry: 10^12
// cents, i.e. 10,000,000,000.00 in major units. It keeps the pipeline
// aggregates inside a BIGINT (2^63 - 1, about 9.22e18): a maximal deal at
// probability 100 contributes 10^14 to SUM(amount_cents * probability), so
// about 92,000 such deals in one stage and currency would be needed to
// overflow it, and the plain SUM(amount_cents) needs 9.2 million. The bound is
// also far below 2^53, so a JavaScript number holds every amount exactly.
const DealAmountCentsMax int64 = 1_000_000_000_000
