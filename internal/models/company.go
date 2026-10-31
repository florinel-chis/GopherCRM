package models

// Company is an organisation that leads and customers can be linked to: the
// curated counterpart of the free-text `company` column both of them carry.
// The text stays the contract of the public forms and of imports; the link is
// what staff set by hand, and the UI shows the linked company when it is set
// and the text otherwise.
//
// A company holds no personal data by design. Notes are free text and may be
// typed to contain a name, the same accepted limitation as a ticket subject,
// so the right-to-erasure machinery does not touch this table: erasing a lead
// or a customer leaves its company_id in place, as a business link like
// assigned_to_id, and leaves the company itself untouched.
//
// Deleting a company is a plain soft delete. Because the soft-deleted row keeps
// its domain, uniqueness of the domain is checked in the service among live
// rows rather than by a unique index — the same reasoning
// as the label name, minus the hard delete, which labels can afford because
// nothing references them by foreign key.
//
// Domain is stored normalised to lower case (service.NormalizeCompanyDomain)
// and carries a NON-unique index (idx_companies_domain) for that live-row
// lookup, which compares the column with plain equality so the index applies
// (#109).
type Company struct {
	BaseModel
	// Company name, trimmed, 1 to 200 characters. Required.
	Name string `gorm:"not null;type:varchar(200)" json:"name" example:"Acme Corporation"`
	// Normalised domain: lower case, without scheme, path, query, a leading
	// "www." or a trailing dot. Empty when not set. Unique among live
	// (not deleted) companies.
	Domain string `gorm:"type:varchar(255);index" json:"domain" example:"acme.com"`
	// Website as entered (trimmed); an http or https URL, or empty.
	Website string `gorm:"type:varchar(255)" json:"website" example:"https://www.acme.com"`
	// Industry, free text, at most 100 characters; may be empty.
	Industry string `gorm:"type:varchar(100)" json:"industry" example:"Manufacturing"`
	// Head-count band, or empty when unknown.
	EmployeeRange string `gorm:"type:varchar(20)" json:"employee_range" enums:"1-10,11-50,51-200,201-500,501-1000,1000+," example:"51-200"`
	// Switchboard phone number, free text, at most 50 characters.
	Phone string `gorm:"type:varchar(50)" json:"phone" example:"+1 555 0100"`
	// Street address, at most 255 characters.
	Address string `gorm:"type:varchar(255)" json:"address" example:"1 Industrial Way"`
	// City, at most 100 characters.
	City string `gorm:"type:varchar(100)" json:"city" example:"Springfield"`
	// State or region, at most 100 characters.
	State string `gorm:"type:varchar(100)" json:"state" example:"IL"`
	// Country, free text, at most 100 characters.
	Country string `gorm:"type:varchar(100)" json:"country" example:"USA"`
	// Postal code, at most 20 characters.
	PostalCode string `gorm:"type:varchar(20)" json:"postal_code" example:"62701"`
	// Free-text notes, at most 65535 bytes (UTF-8 bytes, not characters).
	Notes string `gorm:"type:text" json:"notes" example:"Key account; renewal in Q3."`

	// OwnerID is the account manager: a staff account, not personal data about
	// the company. Nullable because a company may sit unowned.

	// Account manager (a user id). Omitted when the company is unowned.
	OwnerID *uint `gorm:"index" json:"owner_id,omitempty" example:"7"`
	// The account manager's user record, loaded by the company endpoints;
	// omitted when unowned or when the owner account no longer exists.
	Owner *User `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`

	// CustomerCount, LeadCount and DealCount are how many live customers, leads
	// and deals point at the company. They are computed by the repository, not
	// stored: `gorm:"-"` keeps them out of the schema and of every generated
	// statement.

	// Number of live customers linked to the company. Computed by the company
	// endpoints; 0 where the company is embedded in another record (a lead's
	// company_record, a deal's company). Read-only.
	CustomerCount int64 `gorm:"-" json:"customer_count" readonly:"true" example:"4"`
	// Number of live leads linked to the company, whoever owns them. Computed
	// like customer_count. Read-only.
	LeadCount int64 `gorm:"-" json:"lead_count" readonly:"true" example:"9"`
	// Number of live deals linked to the company, whoever owns them. Computed
	// like customer_count. Read-only.
	DealCount int64 `gorm:"-" json:"deal_count" readonly:"true" example:"2"`
}

// Column widths of the companies table. The handler rejects a value that would
// not fit before it reaches the database, which would otherwise answer with a
// driver error on MySQL and MariaDB (SQLite does not enforce varchar widths).
// company_column_limits_test.go holds each constant to the width the model
// declares, so the two cannot drift apart.
const (
	CompanyNameMaxLength          = 200
	CompanyDomainMaxLength        = 255
	CompanyWebsiteMaxLength       = 255
	CompanyIndustryMaxLength      = 100
	CompanyEmployeeRangeMaxLength = 20
	CompanyPhoneMaxLength         = 50
	CompanyAddressMaxLength       = 255
	CompanyCityMaxLength          = 100
	CompanyStateMaxLength         = 100
	CompanyCountryMaxLength       = 100
	CompanyPostalCodeMaxLength    = 20
	// CompanyNotesMaxBytes is the size of companies.notes, a TEXT column,
	// bounded the same way as leads.notes.
	CompanyNotesMaxBytes = LeadNotesMaxBytes
)

// CompanyEmployeeRanges are the admitted values of employee_range, in order of
// size. The handler's binding tag lists the same values.
var CompanyEmployeeRanges = []string{"1-10", "11-50", "51-200", "201-500", "501-1000", "1000+"}
