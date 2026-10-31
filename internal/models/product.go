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
	// Product name, trimmed, 1 to 200 characters. Copied onto a quote line
	// as its description when the line is added.
	Name string `gorm:"not null;type:varchar(200)" json:"name" example:"Annual support plan"`

	// SKU is an optional stock-keeping code. It is indexed for the search and
	// deliberately not unique: a unique index over a soft-deleted table would
	// either block the reuse of a code or, scoped by deleted_at, stop meaning
	// anything (see the gotchas in docs/DEVELOPMENT.md).

	// Optional stock-keeping code, trimmed, at most 64 characters; empty when
	// not set. Not unique.
	SKU string `gorm:"column:sku;type:varchar(64);index" json:"sku" example:"SUP-ANNUAL"`
	// Free-text description, at most 65535 bytes (UTF-8 bytes, not
	// characters). Not copied onto quote lines.
	Description string `gorm:"type:text" json:"description" example:"Business-hours support for twelve months."`

	// UnitPriceCents carries no column default on purpose: a GORM default
	// would replace an explicit 0 on create, and a free item is stored at 0.

	// Price of one unit in minor units of currency (cents for EUR), 0 to
	// 1000000000000 (10^12). 0 is a free item. Copied onto a quote line when
	// the line is added.
	UnitPriceCents int64 `gorm:"not null" json:"unit_price_cents" minimum:"0" maximum:"1000000000000" example:"120000"`
	// ISO 4217 code: exactly three upper-case ASCII letters. Defaults to the
	// deals.default_currency setting (EUR as shipped). A product can be added
	// only to quotes in the same currency.
	Currency string `gorm:"not null;type:char(3)" json:"currency" minLength:"3" maxLength:"3" example:"EUR"`

	// IsActive has no column default either: GORM would overwrite an explicit
	// false with it. The service sets true when the request leaves it out.

	// Whether the product can be added to quote lines. Defaults to true on
	// create; existing lines of an inactive product are kept.
	IsActive bool `gorm:"not null" json:"is_active" example:"true"`
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
