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
// rows with LOWER(domain) rather than by a unique index — the same reasoning
// as the label name, minus the hard delete, which labels can afford because
// nothing references them by foreign key.
type Company struct {
	BaseModel
	Name          string `gorm:"not null;type:varchar(200)" json:"name"`
	Domain        string `gorm:"type:varchar(255)" json:"domain"`
	Website       string `gorm:"type:varchar(255)" json:"website"`
	Industry      string `gorm:"type:varchar(100)" json:"industry"`
	EmployeeRange string `gorm:"type:varchar(20)" json:"employee_range"`
	Phone         string `gorm:"type:varchar(50)" json:"phone"`
	Address       string `gorm:"type:varchar(255)" json:"address"`
	City          string `gorm:"type:varchar(100)" json:"city"`
	State         string `gorm:"type:varchar(100)" json:"state"`
	Country       string `gorm:"type:varchar(100)" json:"country"`
	PostalCode    string `gorm:"type:varchar(20)" json:"postal_code"`
	Notes         string `gorm:"type:text" json:"notes"`

	// OwnerID is the account manager: a staff account, not personal data about
	// the company. Nullable because a company may sit unowned.
	OwnerID *uint `gorm:"index" json:"owner_id,omitempty"`
	Owner   *User `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`

	// CustomerCount, LeadCount and DealCount are how many live customers, leads
	// and deals point at the company. They are computed by the repository, not
	// stored: `gorm:"-"` keeps them out of the schema and of every generated
	// statement.
	CustomerCount int64 `gorm:"-" json:"customer_count"`
	LeadCount     int64 `gorm:"-" json:"lead_count"`
	DealCount     int64 `gorm:"-" json:"deal_count"`
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
