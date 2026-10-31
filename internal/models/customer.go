package models

const CustomerWebsiteMaxLength = 255

// Customer is a person the business serves, usually created by converting a
// lead. Deleting one erases its personal data in place (GDPR Article 17).
//
// @Description A customer: a person the business serves, created directly or by converting a lead. Deleting one erases its personal data in place (GDPR Article 17).
type Customer struct {
	BaseModel
	// Given name. Required on create.
	FirstName string `gorm:"not null;type:varchar(100)" json:"first_name" example:"Ana"`
	// Family name. Required on create.
	LastName string `gorm:"not null;type:varchar(100)" json:"last_name" example:"Popescu"`
	// Email address, unique among all customers, erased ones included (409 on
	// a duplicate). Required on create.
	Email string `gorm:"uniqueIndex;not null;type:varchar(255)" json:"email" format:"email" example:"ana.popescu@example.com"`
	// Phone number, free text; "" when unknown.
	Phone string `gorm:"type:varchar(50)" json:"phone" example:"+40 721 000 000"`
	// Free-text company name as entered; independent of company_id.
	Company string `gorm:"type:varchar(200)" json:"company" example:"Acme SRL"`
	// Web site: "" or an absolute http/https URL of at most 255 characters,
	// trimmed by the server.
	Website string `gorm:"type:varchar(255);not null;default:''" json:"website" maxLength:"255" example:"https://acme.example"`
	// Job title, free text.
	Position string `gorm:"type:varchar(100)" json:"position" example:"Purchasing manager"`
	// Street address, free text.
	Address string `gorm:"type:varchar(255)" json:"address" example:"Strada Exemplu 1"`
	// City, free text.
	City string `gorm:"type:varchar(100)" json:"city" example:"Cluj-Napoca"`
	// State, county or region, free text.
	State string `gorm:"type:varchar(100)" json:"state" example:"Cluj"`
	// Country, free text (no code list is enforced).
	Country string `gorm:"type:varchar(100)" json:"country" example:"Romania"`
	// Postal code, free text.
	PostalCode string `gorm:"type:varchar(20)" json:"postal_code" example:"400000"`
	// Free-text notes. A lead conversion stores the notes sent with the
	// conversion request here.
	Notes string `gorm:"type:text" json:"notes" example:"Prefers email contact."`
	// UserID is the customer's own login. When it names a customer-role
	// account, erasure treats the two as one person (repository/erasure_cascade.go).

	// ID of the customer's own login (a user account), if any; absent when
	// none. No API operation sets it. When it names a customer-role account,
	// erasing either erases both. Read-only.
	UserID *uint `json:"user_id,omitempty" example:"15" readonly:"true"`
	// The login named by user_id, embedded only by some reads (for example
	// GET /customers/{id}); absent otherwise.
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`

	// AssignedToID is the staff account that owns this relationship — the
	// account manager, not the customer's own login (that is UserID above).
	// Nullable because a customer may sit unassigned in the pool.
	//
	// It is a foreign key to an internal account, NOT personal data about the
	// customer, so the erasure never follows or clears it: who handled the
	// account is business history, and the staff member has their own
	// independent erasure path.

	// ID of the staff account that manages the relationship; absent while
	// unassigned. Set by POST /customers/{id}/assign (admin or sales accounts
	// only) or, when leads.conversion.auto_assign_owner is on, by a lead
	// conversion to the lead's owner.
	AssignedToID *uint `json:"assigned_to_id,omitempty" example:"7"`
	// The account named by assigned_to_id. Not embedded by the customer
	// endpoints; absent unless an endpoint loads it.
	AssignedTo *User `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty"`

	// CompanyID links the customer to a curated Company record. Like
	// AssignedToID it is a business link, not personal data, so the erasure in
	// repository/customer_repository.go leaves it alone. The free-text Company
	// column above stays as entered.

	// ID of the linked company record; absent when the customer has no link.
	// Independent of the free-text company field.
	CompanyID *uint `gorm:"index" json:"company_id,omitempty" example:"3"`
	// The company named by company_id, embedded only by some reads (for
	// example GET /customers/{id}); absent otherwise.
	CompanyRecord *Company `gorm:"foreignKey:CompanyID" json:"company_record,omitempty"`

	// Tickets of the customer. Not embedded by the customer endpoints (use
	// GET /customers/{id}/tickets); absent unless an endpoint loads them.
	Tickets []Ticket `gorm:"foreignKey:CustomerID" json:"tickets,omitempty"`
}
