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
// binding tag repeats the same values; deal_stage_tag_test.go in package
// handler holds the two together.
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
	Title       string    `gorm:"not null;type:varchar(200)" json:"title"`
	Stage       DealStage `gorm:"not null;default:'qualification';type:varchar(20)" json:"stage"`
	AmountCents int64     `gorm:"not null;default:0" json:"amount_cents"`
	Currency    string    `gorm:"not null;type:char(3)" json:"currency"`
	// Probability carries no column default on purpose: a GORM default would
	// replace an explicit 0 on create, and a lost deal is stored at 0.
	Probability int `gorm:"not null" json:"probability"`
	// ExpectedCloseDate is a calendar date. It is stored in a DATE column as
	// midnight in the process's local zone (the MySQL DSN uses loc=Local, so
	// that is the value that round-trips exactly on every engine) and exposed
	// as YYYY-MM-DD; see MarshalJSON.
	ExpectedCloseDate *time.Time `gorm:"type:date" json:"expected_close_date,omitempty" swaggertype:"string" format:"date" example:"2026-12-31"`
	// ClosedAt is set when the stage becomes won or lost and cleared when it
	// leaves them.
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// LostReason is kept only while the stage is lost.
	LostReason string `gorm:"type:varchar(255)" json:"lost_reason"`
	Source     string `gorm:"type:varchar(100)" json:"source"`
	Notes      string `gorm:"type:text" json:"notes"`

	CompanyID  *uint     `gorm:"index" json:"company_id,omitempty"`
	Company    *Company  `gorm:"foreignKey:CompanyID" json:"company,omitempty"`
	CustomerID *uint     `gorm:"index" json:"customer_id,omitempty"`
	Customer   *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	LeadID     *uint     `gorm:"index" json:"lead_id,omitempty"`
	Lead       *Lead     `gorm:"foreignKey:LeadID" json:"lead,omitempty"`

	// OwnerID is the staff account working the deal. Never null: it defaults
	// to the caller, and only an admin may name somebody else.
	OwnerID uint  `gorm:"not null;index" json:"owner_id"`
	Owner   *User `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
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
	ID          uint       `gorm:"primarykey" json:"id"`
	DealID      uint       `gorm:"not null;index" json:"deal_id"`
	FromStage   *DealStage `gorm:"type:varchar(20)" json:"from_stage"`
	ToStage     DealStage  `gorm:"not null;type:varchar(20)" json:"to_stage"`
	ChangedByID uint       `gorm:"not null" json:"changed_by_id"`
	ChangedBy   *User      `gorm:"foreignKey:ChangedByID" json:"changed_by,omitempty"`
	ChangedAt   time.Time  `gorm:"not null" json:"changed_at"`
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
