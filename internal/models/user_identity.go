package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Column widths of user_identities, in characters. 512 + 255 utf8mb4
// characters keep the unique (issuer, subject) index under InnoDB's 3072-byte
// key limit.
const (
	UserIdentityIssuerMaxLength  = 512
	UserIdentitySubjectMaxLength = 255
)

// Values of UserIdentity.Source (#505).
const (
	// UserIdentitySourceOIDC marks a link written by an OIDC sign-in.
	UserIdentitySourceOIDC = "oidc"
	// UserIdentitySourceSCIM marks the (issuer, externalId) link written by
	// SCIM create.
	UserIdentitySourceSCIM = "scim"
)

// UserIdentity links an OpenID Connect identity, the exact (iss, sub) pair of
// an ID token, to a user (#435). The pair is unique and compared exactly on
// every engine: SQLite compares with BINARY, MySQL/MariaDB with utf8mb4_bin on
// both columns. utf8mb4_bin is still a PAD SPACE collation, so the sign-in
// refuses a subject with leading or trailing whitespace before any lookup and
// compares the stored row byte for byte after it.
//
// Source tells who wrote the row (#505): a scim row is the (issuer,
// externalId) link written by SCIM create, an oidc row is written by a
// sign-in. An Entra ID first sign-in whose (iss, sub) is unknown finds the
// account SCIM created through the scim row whose subject is the token's oid.
//
// The rows have no deleted_at, and erasure leaves them in place: a row stays
// linked to its erased user, so that person's next sign-in is refused instead
// of provisioning a new account. The row holds the issuer URL and the opaque
// subject only, no name or address.
type UserIdentity struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// The user the identity signs in as.
	UserID uint `gorm:"not null;index:idx_user_identities_user_id" json:"user_id"`
	// Never serialised; it only declares the foreign key to users.id.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	// The iss claim, exactly as the ID token carries it.
	Issuer string `gorm:"type:varchar(512);not null;uniqueIndex:idx_user_identities_issuer_subject,priority:1" json:"issuer"`
	// The sub claim, exactly as the ID token carries it.
	Subject string `gorm:"type:varchar(255);not null;uniqueIndex:idx_user_identities_issuer_subject,priority:2" json:"subject"`
	// Who wrote the row: UserIdentitySourceOIDC or UserIdentitySourceSCIM.
	Source string `gorm:"type:varchar(8);not null;default:oidc" json:"source"`
}

// TableName pins the table name.
func (UserIdentity) TableName() string { return "user_identities" }

// userIdentityCollation is the collation issuer and subject carry on
// MySQL/MariaDB, so the unique index compares them exactly.
const userIdentityCollation = "utf8mb4_bin"

// ensureUserIdentityCollation gives issuer and subject the binary collation on
// MySQL/MariaDB, as ensureCustomerExternalIDCollation does for the customer
// mappings. MigrateDatabase's AutoMigrate creates the table as
// utf8mb4_unicode_ci (case- and accent-insensitive), under which a subject differing only in
// letter case would collide with, and be found as, another person's identity.
// The SQL migration creates the columns with utf8mb4_bin already; this guard
// covers a schema that only AutoMigrate created. It reads information_schema
// first and issues the ALTER only when a column differs. SQLite compares with
// BINARY by default and is skipped.
func ensureUserIdentityCollation(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		return nil
	}
	var wrong int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_identities'
		AND COLUMN_NAME IN ('issuer', 'subject')
		AND (COLLATION_NAME IS NULL OR COLLATION_NAME <> ?)`, userIdentityCollation).Scan(&wrong).Error; err != nil {
		return fmt.Errorf("failed to read the collation of user_identities: %w", err)
	}
	if wrong == 0 {
		return nil
	}
	if err := tx.Exec(fmt.Sprintf(`ALTER TABLE user_identities
		MODIFY issuer varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL,
		MODIFY subject varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL`,
		UserIdentityIssuerMaxLength, userIdentityCollation,
		UserIdentitySubjectMaxLength, userIdentityCollation)).Error; err != nil {
		return fmt.Errorf("failed to set the collation of user_identities: %w", err)
	}
	return nil
}
