package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Ticket outcomes a confirmed submission of a create_ticket form records
// (#213), stored in form_submissions.ticket_outcome. The empty string means the
// submission is not a ticket-form submission or is not confirmed yet.
const (
	FormTicketOutcomeCreated     = "created"
	FormTicketOutcomeDuplicate   = "duplicate"
	FormTicketOutcomeKeyConflict = "key_conflict"
	FormTicketOutcomeNoCustomer  = "no_customer"
	FormTicketOutcomeUnavailable = "unavailable"
)

// FormIdempotencyKeyMaxLength is the width of both idempotency_key columns.
const FormIdempotencyKeyMaxLength = 128

// FormServerKeyPrefix starts the server key a keyless confirmation uses
// (submission-<submission id>); a client key may not start with it.
const FormServerKeyPrefix = "submission-"

// FormTicketKey records that an idempotency key of a form opened one ticket
// (#213). The unique (form_id, idempotency_key) index is what guarantees a key
// is used for at most one ticket; the comparison is exact (BINARY on SQLite,
// utf8mb4_bin on MySQL/MariaDB). There is no deleted_at: a row is hard-deleted
// with its ticket (ticketRepository.Delete), with its customer (every erasure
// root that reaches the customer) and with its submission's erasure scrub. No
// foreign keys; every link is logical.
type FormTicketKey struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// The form the key is scoped to.
	FormID uint `gorm:"not null;uniqueIndex:idx_form_ticket_keys_identity,priority:1" json:"form_id"`
	// The client key, or submission-<submission id> for a keyless submission.
	IdempotencyKey string `gorm:"type:varchar(128);not null;uniqueIndex:idx_form_ticket_keys_identity,priority:2" json:"idempotency_key"`
	// The ticket the key opened.
	TicketID uint `gorm:"not null;index:idx_form_ticket_keys_ticket_id" json:"ticket_id"`
	// The submission whose confirmation opened it.
	SubmissionID uint `gorm:"not null;index:idx_form_ticket_keys_submission_id" json:"submission_id"`
	// The customer the ticket was opened for: tells a duplicate (same
	// customer) from a key conflict, and is the erasure purge key.
	CustomerID uint `gorm:"not null;index:idx_form_ticket_keys_customer_id" json:"customer_id"`
}

// TableName pins the table name.
func (FormTicketKey) TableName() string { return "form_ticket_keys" }

// formTicketKeyCollation is the collation form_ticket_keys.idempotency_key
// carries on MySQL/MariaDB, so the unique index compares keys exactly.
const formTicketKeyCollation = "utf8mb4_bin"

// ensureFormTicketKeyCollation gives form_ticket_keys.idempotency_key the
// binary collation on MySQL/MariaDB. AutoMigrate creates the column with the
// database default (case- and accent-insensitive), under which 'Key-A' and
// 'key-a' would collide in the unique index. The SQL migration creates it
// with utf8mb4_bin already; this guard covers a schema AutoMigrate alone
// created. It reads information_schema first and alters only when the
// collation differs. SQLite compares with BINARY by default and is skipped.
func ensureFormTicketKeyCollation(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		return nil
	}
	var wrong int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_ticket_keys'
		AND COLUMN_NAME = 'idempotency_key'
		AND (COLLATION_NAME IS NULL OR COLLATION_NAME <> ?)`, formTicketKeyCollation).Scan(&wrong).Error; err != nil {
		return fmt.Errorf("failed to read the collation of form_ticket_keys: %w", err)
	}
	if wrong == 0 {
		return nil
	}
	if err := tx.Exec(fmt.Sprintf(`ALTER TABLE form_ticket_keys
		MODIFY idempotency_key varchar(%d) CHARACTER SET utf8mb4 COLLATE %s NOT NULL`,
		FormIdempotencyKeyMaxLength, formTicketKeyCollation)).Error; err != nil {
		return fmt.Errorf("failed to set the collation of form_ticket_keys: %w", err)
	}
	return nil
}
