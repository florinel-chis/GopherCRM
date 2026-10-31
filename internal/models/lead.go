package models

type LeadStatus string

const (
	LeadStatusNew         LeadStatus = "new"
	LeadStatusContacted   LeadStatus = "contacted"
	LeadStatusQualified   LeadStatus = "qualified"
	LeadStatusUnqualified LeadStatus = "unqualified"
	LeadStatusConverted   LeadStatus = "converted"
)

type LeadClassification string

const (
	LeadClassificationUnclassified LeadClassification = "unclassified"
	LeadClassificationTest         LeadClassification = "test"
	LeadClassificationSpam         LeadClassification = "spam"
	LeadClassificationLead         LeadClassification = "lead"
	LeadClassificationHotLead      LeadClassification = "hot_lead"
)

// LeadNotesMaxBytes is the size of leads.notes, a TEXT column: 65,535 bytes on
// MySQL and MariaDB, which reject a longer value (SQLite does not).
const LeadNotesMaxBytes = 65535

// Lead indexes (#109): email serves GetLatestByEmail (the form double-opt-in
// lookup; non-unique on purpose, one person may be captured twice),
// classification serves the classification filter and its count, and
// external_id serves GetByExternalID. Each lookup is an equality, so a single
// column index is enough; the handful of rows sharing one email are sorted by
// created_at, id after the index narrows to them.
type Lead struct {
	BaseModel
	// Given name. Required on create (any non-empty string); an update
	// cannot clear it. The column holds 100 characters; the API does not
	// check the length.
	FirstName string `gorm:"not null;type:varchar(100)" json:"first_name" example:"Ada"`
	// Family name. Required on create; an update cannot clear it. The column
	// holds 100 characters; the API does not check the length.
	LastName string `gorm:"not null;type:varchar(100)" json:"last_name" example:"Lovelace"`
	// Email address, validated as an address when sent. Empty when the lead
	// was created with a phone number only (a create needs an email or a
	// phone). Not unique: one person may be captured more than once. Erasure
	// replaces it with a random address in the reserved .invalid domain.
	Email string `gorm:"not null;type:varchar(255);index" json:"email" example:"ada@example.com"`
	// Phone number, free text, may be empty. The column holds 50 characters;
	// the API does not check the length.
	Phone string `gorm:"type:varchar(50)" json:"phone" example:"+44 20 7946 0000"`
	// Free-text employer name as typed or submitted by a form, independent of
	// company_id. The column holds 200 characters; the API does not check the
	// length.
	Company string `gorm:"type:varchar(200)" json:"company" example:"Analytical Engines Ltd"`
	// Job title, free text, may be empty (100-character column, not checked
	// by the API).
	Position string `gorm:"type:varchar(100)" json:"position" example:"CTO"`
	// Where the lead came from, free text (100-character column, not checked
	// by the API). Leads created by a public form carry the form's name.
	Source string `gorm:"type:varchar(100)" json:"source" example:"website"`
	// Pipeline status. Defaults to new on create. converted is set only by
	// POST /leads/{id}/convert; any other write that asks for it is refused.
	Status LeadStatus `gorm:"not null;default:'new';type:varchar(20)" json:"status" example:"new"`
	// Triage classification; unclassified when not set.
	Classification LeadClassification `gorm:"type:varchar(20);default:'unclassified';index" json:"classification" example:"lead"`
	// Identifier of the lead in an external system (imports). Omitted when
	// empty; not unique.
	ExternalID string `gorm:"type:varchar(255);index" json:"external_id,omitempty" example:"crm-legacy-1042"`
	// Free-text notes: staff text plus one block per public-form submission
	// from the lead's address (oldest form blocks are trimmed whole to stay
	// within the column). Writes through the API are refused above 65535
	// bytes (UTF-8 bytes, not characters).
	Notes string `gorm:"type:text" json:"notes" example:"Met at the trade fair; wants a demo."`
	// Staff user who owns the lead. A sales user sees and edits only the
	// leads it owns; only an admin may reassign a lead.
	OwnerID uint `json:"owner_id" example:"7"`
	// The owner's account. Always present in the JSON. GET /leads/{id}, the
	// admin GET /leads (except with the classification filter),
	// GET /leads?external_id= for admin and sales, GET /companies/{id}/leads and the dashboard fill it in with the User
	// object (see the User schema); elsewhere (create and update responses, a
	// sales user's list) it is a zero-valued placeholder (id 0, every string
	// empty, role "" included), which is not a valid User, so this property is
	// documented as a plain object. Use owner_id.
	Owner User `gorm:"foreignKey:OwnerID" json:"owner,omitempty" swaggertype:"object"`
	// Customer this lead was converted into. Set by POST /leads/{id}/convert,
	// read-only otherwise; omitted while the lead is unconverted.
	CustomerID *uint `json:"customer_id,omitempty" example:"31"`
	// The converted customer. The lead endpoints never load it, so it is
	// omitted.
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`

	// CompanyID links the lead to a curated Company record. It is a business
	// link, not personal data, so erasure leaves it in place (see
	// leadErasurePlan). The free-text Company column above is untouched by it:
	// the text is what forms and imports write, the link is what staff set.

	// Curated company the lead is linked to, independent of the free-text
	// company field. Omitted when there is no link. Erasure and conversion
	// keep it; deleting the company clears it.
	CompanyID *uint `gorm:"index" json:"company_id,omitempty" example:"12"`
	// The linked company, loaded only by GET /leads/{id} (its
	// customer_count, lead_count and deal_count are 0 there, not computed);
	// omitted elsewhere.
	CompanyRecord *Company `gorm:"foreignKey:CompanyID" json:"company_record,omitempty"`
}
