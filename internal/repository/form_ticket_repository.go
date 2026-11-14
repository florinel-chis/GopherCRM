package repository

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrFormTicketKeyTaken reports that the (form_id, idempotency_key) pair of a
// form ticket key is already stored (#213). CreateTicketWithKey returns it after
// rolling its savepoint back, so the ticket it inserted first is gone too.
var ErrFormTicketKeyTaken = errors.New("form ticket idempotency key already used")

// formTicketRepository backs form ticket intake (#213): the locked customer
// match of a confirmed address, the ticket insert with its key row, and the
// read of a taken key. The key table's only unique index is the identity pair,
// so a duplicate-key error on it is attributed to the key.
type formTicketRepository struct {
	db *gorm.DB
}

// NewFormTicketRepository builds the repository on the root handle; the
// confirmation binds it to its transaction with WithTx.
func NewFormTicketRepository(db *gorm.DB) FormTicketRepository {
	return &formTicketRepository{db: db}
}

func (r *formTicketRepository) WithTx(tx *gorm.DB) FormTicketRepository {
	return &formTicketRepository{db: tx}
}

// formTicketCustomerAddresses returns the values customers.email is compared
// with: the address as given and its lower-cased form, the rule the unlinked
// submission erasure uses (#214).
func formTicketCustomerAddresses(address string) []string {
	lower := strings.ToLower(address)
	if lower == address {
		return []string{address}
	}
	return []string{address, lower}
}

// LockLiveCustomerByEmail reads the live customer whose email is the address
// or its lower-cased form, lowest id first, with FOR UPDATE on MySQL/MariaDB (a
// plain read on SQLite, where the dialector drops the clause). SQLite compares
// binary, so only those two spellings match; on MySQL/MariaDB the column's
// utf8mb4_unicode_ci collation decides (case- and accent-insensitive, trailing
// spaces ignored). No match is gorm.ErrRecordNotFound.
func (r *formTicketRepository) LockLiveCustomerByEmail(address string) (*models.Customer, error) {
	if strings.TrimSpace(address) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var customer models.Customer
	err := lockingRead(r.db).Unscoped().
		Where("deleted_at IS NULL AND email IN ?", formTicketCustomerAddresses(address)).
		Order("id").Limit(1).Take(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

// formTicketSavepointSeq numbers the savepoints of CreateTicketWithKey.
var formTicketSavepointSeq atomic.Uint64

// CreateTicketWithKey inserts the ticket (first_responded_at omitted, no
// assignee, no association writes) and then the key row naming it, inside one
// savepoint of the caller's transaction (a transaction of its own on the root
// handle). A duplicate (form_id, idempotency_key) rolls the savepoint back, so
// no ticket row is left, and returns ErrFormTicketKeyTaken; any other failure
// rolls it back too and is returned as is. On success key.TicketID and
// ticket.ID are set.
func (r *formTicketRepository) CreateTicketWithKey(ticket *models.Ticket, key *models.FormTicketKey) error {
	if !inTransaction(r.db) {
		return r.db.Transaction(func(tx *gorm.DB) error {
			return (&formTicketRepository{db: tx}).CreateTicketWithKey(ticket, key)
		})
	}

	name := fmt.Sprintf("gocrm_form_ticket_%d", formTicketSavepointSeq.Add(1))
	if err := execTransactionControl(r.db, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("form ticket savepoint: %w", err)
	}

	err := r.insertTicketAndKey(ticket, key)
	if err == nil {
		if releaseErr := execTransactionControl(r.db, "RELEASE SAVEPOINT "+name); releaseErr != nil {
			err = fmt.Errorf("releasing form ticket savepoint: %w", releaseErr)
		} else {
			return nil
		}
	}

	if rbErr := execTransactionControl(r.db, "ROLLBACK TO SAVEPOINT "+name); rbErr != nil {
		return errors.Join(err, fmt.Errorf("rolling back form ticket savepoint: %w", rbErr))
	}
	// The rolled-back rows no longer exist; do not hand their ids out.
	ticket.ID = 0
	key.ID = 0
	key.TicketID = 0
	return err
}

func (r *formTicketRepository) insertTicketAndKey(ticket *models.Ticket, key *models.FormTicketKey) error {
	ticket.AssignedToID = nil
	ticket.FirstRespondedAt = nil
	if err := r.db.Omit(clause.Associations, ticketFirstResponseColumn).Create(ticket).Error; err != nil {
		return fmt.Errorf("inserting form ticket: %w", err)
	}
	key.TicketID = ticket.ID
	if err := r.db.Create(key).Error; err != nil {
		if isDuplicateKeyError(err) {
			// The driver's message quotes the duplicate key; it is not kept.
			return fmt.Errorf("form %d: %w", key.FormID, ErrFormTicketKeyTaken)
		}
		return fmt.Errorf("inserting form ticket key: %w", err)
	}
	return nil
}

// GetKeyForUpdate reads the key row of (formID, key) with FOR UPDATE on
// MySQL/MariaDB (a plain read on SQLite). A locking read is a current read
// under InnoDB, so it returns the committed row a concurrent confirmation
// inserted. No row is gorm.ErrRecordNotFound.
func (r *formTicketRepository) GetKeyForUpdate(formID uint, key string) (*models.FormTicketKey, error) {
	var row models.FormTicketKey
	if err := lockingRead(r.db).Where("form_id = ? AND idempotency_key = ?", formID, key).Take(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// purgeFormTicketKeysOfTicket hard-deletes the key rows of one ticket inside
// the ticket delete's transaction, so the key can open a new ticket. A schema
// without the table is skipped by a migrator probe.
func purgeFormTicketKeysOfTicket(tx *gorm.DB, ticketID uint) error {
	if !tx.Migrator().HasTable(&models.FormTicketKey{}) {
		return nil
	}
	if err := tx.Where("ticket_id = ?", ticketID).Delete(&models.FormTicketKey{}).Error; err != nil {
		return fmt.Errorf("purging form_ticket_keys of ticket %d: %w", ticketID, err)
	}
	return nil
}

// purgeCustomerFormTicketKeys hard-deletes, inside the erasure unit, every key
// row whose customer_id is the erased customer or whose ticket_id names one of
// its tickets, soft-deleted tickets included (#213). It runs from the customer
// plan's AfterScrub, so every root that reaches the customer reaches it. A
// schema without the table is skipped by a migrator probe; when it exists every
// failure propagates and rolls the erasure back. Errors name ids only.
func purgeCustomerFormTicketKeys(tx *gorm.DB, customerID uint) error {
	if !tx.Migrator().HasTable(&models.FormTicketKey{}) {
		return nil
	}
	var ticketIDs []uint
	if tx.Migrator().HasTable(&models.Ticket{}) {
		if err := tx.Unscoped().Model(&models.Ticket{}).
			Where("customer_id = ?", customerID).Pluck("id", &ticketIDs).Error; err != nil {
			return fmt.Errorf("purging form_ticket_keys of customer %d: %w", customerID, err)
		}
	}
	query := tx.Where("customer_id = ?", customerID)
	if len(ticketIDs) > 0 {
		query = tx.Where("customer_id = ? OR ticket_id IN ?", customerID, ticketIDs)
	}
	if err := query.Delete(&models.FormTicketKey{}).Error; err != nil {
		return fmt.Errorf("purging form_ticket_keys of customer %d: %w", customerID, err)
	}
	return nil
}

// scrubSubmissionTicketKeys empties idempotency_key on the scrubbed
// submissions and hard-deletes their key rows (#213). Each step is skipped by
// a migrator probe when its column or table is absent.
func scrubSubmissionTicketKeys(tx *gorm.DB, submissionIDs []uint) error {
	if len(submissionIDs) == 0 {
		return nil
	}
	if tx.Migrator().HasColumn(&models.FormSubmission{}, "idempotency_key") {
		if err := tx.Unscoped().Model(&models.FormSubmission{}).Where("id IN ?", submissionIDs).
			Update("idempotency_key", "").Error; err != nil {
			return fmt.Errorf("clearing idempotency keys of %d form submissions: %w", len(submissionIDs), err)
		}
	}
	if tx.Migrator().HasTable(&models.FormTicketKey{}) {
		if err := tx.Where("submission_id IN ?", submissionIDs).Delete(&models.FormTicketKey{}).Error; err != nil {
			return fmt.Errorf("purging form_ticket_keys of %d form submissions: %w", len(submissionIDs), err)
		}
	}
	return nil
}
