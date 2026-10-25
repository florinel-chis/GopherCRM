package models

// Product is one entry of the catalogue a quote line is priced from. The
// price is an integer amount in minor units next to an ISO 4217 code, the
// deal money convention; a quote copies the name and the price onto its line
// when the line is added, so a later edit here never changes an existing
// quote (see QuoteLine).
//
// A product holds no personal data, so the right-to-erasure machinery does
// not touch this table. Deleting a product is a soft delete: quote lines keep
// referencing the row through product_id and keep their copied values.
type Product struct {
	BaseModel
	Name string `gorm:"not null;type:varchar(200)" json:"name"`
	// SKU is an optional stock-keeping code. It is indexed for the search and
	// deliberately not unique: a unique index over a soft-deleted table would
	// either block the reuse of a code or, scoped by deleted_at, stop meaning
	// anything (see the gotchas in docs/DEVELOPMENT.md).
	SKU         string `gorm:"column:sku;type:varchar(64);index" json:"sku"`
	Description string `gorm:"type:text" json:"description"`
	// UnitPriceCents carries no column default on purpose: a GORM default
	// would replace an explicit 0 on create, and a free item is stored at 0.
	UnitPriceCents int64  `gorm:"not null" json:"unit_price_cents"`
	Currency       string `gorm:"not null;type:char(3)" json:"currency"`
	// IsActive has no column default either: GORM would overwrite an explicit
	// false with it. The service sets true when the request leaves it out.
	IsActive bool `gorm:"not null" json:"is_active"`
}

// Column widths and bounds of the products table. The handler and the service
// reject a value that would not fit before it reaches the database;
// product_test.go holds each constant to the width the model declares.
const (
	ProductNameMaxLength  = 200
	ProductSKUMaxLength   = 64
	ProductCurrencyLength = 3
	// ProductDescriptionMaxBytes is the size of products.description, a TEXT
	// column, bounded the same way as leads.notes.
	ProductDescriptionMaxBytes = LeadNotesMaxBytes
)
