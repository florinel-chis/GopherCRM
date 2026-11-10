package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Column widths of product_external_ids, in characters: the same widths as
// customer_external_ids (#212), so the shared validation rules apply.
const (
	ProductExternalIDSourceMaxLength = CustomerExternalIDSourceMaxLength
	ProductExternalIDScopeMaxLength  = CustomerExternalIDScopeMaxLength
	ProductExternalIDValueMaxLength  = CustomerExternalIDValueMaxLength
)

// ProductExternalID maps an integrator's own identity of a catalogue entry (an
// ERP article, a storefront product) to the GopherCRM product (#239): the
// system that issued it (source), an optional namespace inside that system
// (scope, "" when the source has one namespace) and the identifier itself
// (external_id). The triple is unique; the comparison is exact and
// case-sensitive on every engine (SQLite compares with BINARY, MySQL/MariaDB
// with utf8mb4_bin on the three columns).
//
// The rows have no deleted_at: a mapping is removed by a hard delete, and
// deleting the product hard-deletes its mappings in the same transaction, so
// the triple can be mapped again. products.sku stays non-unique on purpose.
//
// @Description A mapping of an external (source, scope, external_id) identity to a product. The triple is unique and compared exactly (case-sensitive).
type ProductExternalID struct {
	// Unique identifier of the mapping, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"9" readonly:"true"`
	// When the mapping was created (RFC 3339 date-time). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-12T09:30:00Z" readonly:"true"`
	// When the mapping was last written (RFC 3339 date-time). Mappings are
	// never updated in place, so it equals created_at. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-12T09:30:00Z" readonly:"true"`

	// Product the identity maps to. Read-only.
	ProductID uint `gorm:"not null;index:idx_product_external_ids_product_id" json:"product_id" example:"42" readonly:"true"`
	// Never serialised and never loaded by the API; it only declares the
	// foreign key to products.id.
	Product *Product `gorm:"foreignKey:ProductID" json:"-"`

	// System that issued the identifier: 1-64 characters, no control
	// characters, no leading or trailing whitespace. Compared exactly.
	Source string `gorm:"type:varchar(64);not null;uniqueIndex:idx_product_external_ids_identity,priority:1" json:"source" minLength:"1" maxLength:"64" example:"erp"`
	// Namespace inside the source, for example a company code: 0-128
	// characters, '' when the source has a single namespace. Same character
	// rules as source. Compared exactly.
	Scope string `gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_product_external_ids_identity,priority:2" json:"scope" maxLength:"128" example:"plant-01"`
	// The identifier in that source and scope: 1-255 characters, same
	// character rules as source. Compared exactly (case-sensitive).
	ExternalID string `gorm:"type:varchar(255);not null;uniqueIndex:idx_product_external_ids_identity,priority:3" json:"external_id" minLength:"1" maxLength:"255" example:"MAT-100042"`
}

// ensureProductExternalIDCollation gives source, scope and external_id of
// product_external_ids the binary collation on MySQL/MariaDB, exactly as
// ensureCustomerExternalIDCollation does for the customer mappings. The SQL
// migration creates the columns with utf8mb4_bin already; this guard covers a
// schema that only AutoMigrate created. It reads information_schema first and
// issues the ALTER only when a column differs, so a converted table takes no
// DDL on later starts. SQLite compares with BINARY by default and is skipped.
func ensureProductExternalIDCollation(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		return nil
	}
	var wrong int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_external_ids'
		AND COLUMN_NAME IN ('source', 'scope', 'external_id')
		AND (COLLATION_NAME IS NULL OR COLLATION_NAME <> ?)`, customerExternalIDCollation).Scan(&wrong).Error; err != nil {
		return fmt.Errorf("failed to read the collation of product_external_ids: %w", err)
	}
	if wrong == 0 {
		return nil
	}
	if err := tx.Exec(fmt.Sprintf(`ALTER TABLE product_external_ids
		MODIFY source varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL,
		MODIFY scope varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL DEFAULT '',
		MODIFY external_id varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL`,
		ProductExternalIDSourceMaxLength, customerExternalIDCollation,
		ProductExternalIDScopeMaxLength, customerExternalIDCollation,
		ProductExternalIDValueMaxLength, customerExternalIDCollation)).Error; err != nil {
		return fmt.Errorf("failed to set the collation of product_external_ids: %w", err)
	}
	return nil
}
