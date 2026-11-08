package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Column widths of customer_external_ids, in characters. The service's
// validation limits are these constants (service.CustomerExternalID*MaxLength),
// and a test parses the GORM tags below to keep the two equal.
const (
	CustomerExternalIDSourceMaxLength = 64
	CustomerExternalIDScopeMaxLength  = 128
	CustomerExternalIDValueMaxLength  = 255
)

// CustomerExternalID maps an integrator's own identity of a customer to the
// GopherCRM customer (#212): the system that issued it (source), an optional
// namespace inside that system such as a storefront site (scope, "" when the
// source has one namespace) and the identifier itself (external_id). The triple
// is unique; the comparison is exact and case-sensitive on every engine (SQLite
// compares with BINARY, MySQL/MariaDB with utf8mb4_bin on the three columns).
//
// The rows have no deleted_at: a mapping is removed by a hard delete, and the
// erasure of the customer hard-deletes its mappings in the same unit.
//
// @Description A mapping of an external (source, scope, external_id) identity to a customer. The triple is unique and compared exactly (case-sensitive).
type CustomerExternalID struct {
	// Unique identifier of the mapping, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"9" readonly:"true"`
	// When the mapping was created (RFC 3339 date-time). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-11T09:30:00Z" readonly:"true"`
	// When the mapping was last written (RFC 3339 date-time). Mappings are
	// never updated in place, so it equals created_at. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-11T09:30:00Z" readonly:"true"`

	// Customer the identity maps to. Read-only.
	CustomerID uint `gorm:"not null;index:idx_customer_external_ids_customer_id" json:"customer_id" example:"42" readonly:"true"`
	// Never serialised and never loaded by the API; it only declares the
	// foreign key to customers.id.
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"-"`

	// System that issued the identifier: 1-64 characters, no control
	// characters, no leading or trailing whitespace. Compared exactly.
	Source string `gorm:"type:varchar(64);not null;uniqueIndex:idx_customer_external_ids_identity,priority:1" json:"source" minLength:"1" maxLength:"64" example:"shopify"`
	// Namespace inside the source, for example a storefront site: 0-128
	// characters, '' when the source has a single namespace. Same character
	// rules as source. Compared exactly.
	Scope string `gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_customer_external_ids_identity,priority:2" json:"scope" maxLength:"128" example:"store-eu"`
	// The identifier in that source and scope: 1-255 characters, same
	// character rules as source. Compared exactly (case-sensitive).
	ExternalID string `gorm:"type:varchar(255);not null;uniqueIndex:idx_customer_external_ids_identity,priority:3" json:"external_id" minLength:"1" maxLength:"255" example:"gid-1001"`
}

// customerExternalIDCollation is the collation the three identity columns
// carry on MySQL/MariaDB, so the unique index compares them exactly.
const customerExternalIDCollation = "utf8mb4_bin"

// ensureCustomerExternalIDCollation gives source, scope and external_id the
// binary collation on MySQL/MariaDB. AutoMigrate creates the table with the
// database's default collation (case- and accent-insensitive), under which the
// unique index would refuse ('Shop-A', "", 'X1') beside ('shop-a', "", 'x1')
// and a lookup would match either. The SQL migration creates the columns with
// utf8mb4_bin already; this guard covers a schema that only AutoMigrate
// created. It reads information_schema first and issues the ALTER only when a
// column differs, so a converted table takes no DDL on later starts. SQLite
// compares with BINARY by default and is skipped.
func ensureCustomerExternalIDCollation(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		return nil
	}
	var wrong int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_external_ids'
		AND COLUMN_NAME IN ('source', 'scope', 'external_id')
		AND (COLLATION_NAME IS NULL OR COLLATION_NAME <> ?)`, customerExternalIDCollation).Scan(&wrong).Error; err != nil {
		return fmt.Errorf("failed to read the collation of customer_external_ids: %w", err)
	}
	if wrong == 0 {
		return nil
	}
	if err := tx.Exec(fmt.Sprintf(`ALTER TABLE customer_external_ids
		MODIFY source varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL,
		MODIFY scope varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL DEFAULT '',
		MODIFY external_id varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL`,
		CustomerExternalIDSourceMaxLength, customerExternalIDCollation,
		CustomerExternalIDScopeMaxLength, customerExternalIDCollation,
		CustomerExternalIDValueMaxLength, customerExternalIDCollation)).Error; err != nil {
		return fmt.Errorf("failed to set the collation of customer_external_ids: %w", err)
	}
	return nil
}
