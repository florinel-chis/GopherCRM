package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// SCIMGroupMemberValueMaxLength is the width of scim_group_members.group_value
// in characters: the longest single sign-on role mapping value
// (service.SSORoleMappingValueMax).
const SCIMGroupMemberValueMaxLength = 256

// SCIMGroupMember is one membership of a SCIM Group (#506). A group is a
// distinct groups-claim value of the single sign-on role mapping; group_value
// holds that value exactly, so the pair (group_value, user_id) is unique and
// compared exactly on every engine: SQLite compares with BINARY,
// MySQL/MariaDB with utf8mb4_bin. utf8mb4_bin is a PAD SPACE collation, but
// the role mapping validation trims every value, so two real group values
// never differ only by trailing spaces and the padding cannot merge them.
//
// The rows have no deleted_at, and erasure leaves them in place (ids and the
// group value only, no name or address): an erased or customer user is
// simply not served as a member. A row outlives a mapping edit; it is matched
// against the mapping again only by that user's next group patch.
type SCIMGroupMember struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// The groups-claim value of the mapping, exactly.
	GroupValue string `gorm:"type:varchar(256);not null;uniqueIndex:idx_scim_group_members_group_user,priority:1" json:"group_value"`
	// The member.
	UserID uint `gorm:"not null;uniqueIndex:idx_scim_group_members_group_user,priority:2;index:idx_scim_group_members_user_id" json:"user_id"`
	// Never serialised; it only declares the foreign key to users.id.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
}

// TableName pins the table name.
func (SCIMGroupMember) TableName() string { return "scim_group_members" }

// scimGroupMemberCollation is the collation group_value carries on
// MySQL/MariaDB, so the unique index compares it exactly.
const scimGroupMemberCollation = "utf8mb4_bin"

// ensureSCIMGroupMemberCollation gives group_value the binary collation on
// MySQL/MariaDB, as ensureUserIdentityCollation does for user_identities.
// AutoMigrate creates the table with the database's default collation
// (case-insensitive), under which the groups Sales and sales would share
// membership rows. The SQL migration creates the column with utf8mb4_bin
// already; this guard covers a schema that only AutoMigrate created. It reads
// information_schema first and issues the ALTER only when the column differs.
// SQLite compares with BINARY by default and is skipped.
func ensureSCIMGroupMemberCollation(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		return nil
	}
	var wrong int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'scim_group_members'
		AND COLUMN_NAME = 'group_value'
		AND (COLLATION_NAME IS NULL OR COLLATION_NAME <> ?)`, scimGroupMemberCollation).Scan(&wrong).Error; err != nil {
		return fmt.Errorf("failed to read the collation of scim_group_members: %w", err)
	}
	if wrong == 0 {
		return nil
	}
	if err := tx.Exec(fmt.Sprintf(`ALTER TABLE scim_group_members
		MODIFY group_value varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL`,
		SCIMGroupMemberValueMaxLength, scimGroupMemberCollation)).Error; err != nil {
		return fmt.Errorf("failed to set the collation of scim_group_members: %w", err)
	}
	return nil
}
