package repository

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #213: every erasure root that reaches a customer purges the form_ticket_keys
// rows of the customer and of its tickets (soft-deleted ones included); the
// lead and unlinked-submission scrubs empty idempotency_key and purge the key
// rows of the scrubbed submissions; ticketRepository.Delete purges the
// ticket's key rows. A missing table is skipped; a failing step rolls the unit
// back.

func TestFormTicketKeyErasureSQLite(t *testing.T) {
	runFormTicketKeyErasure(t, false)
}

func TestFormTicketKeyErasureMySQL(t *testing.T) {
	runFormTicketKeyErasure(t, true)
}

func TestTicketDeletePurgesFormTicketKeysSQLite(t *testing.T) {
	runTicketDeletePurgesFormTicketKeys(t, false)
}

func TestTicketDeletePurgesFormTicketKeysMySQL(t *testing.T) {
	runTicketDeletePurgesFormTicketKeys(t, true)
}

var formTicketErasureSeq uint

type formTicketErasureFixture struct {
	db   *gorm.DB
	form *models.Form
}

func newFormTicketErasureFixture(t *testing.T, db *gorm.DB) *formTicketErasureFixture {
	t.Helper()
	formTicketErasureSeq++
	form := &models.Form{Name: "Complaints", PublicID: fmt.Sprintf("fte-%d", formTicketErasureSeq), Status: models.FormStatusPublished,
		Fields:      []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		DoubleOptIn: true, CreateTicket: true}
	require.NoError(t, db.Create(form).Error)
	return &formTicketErasureFixture{db: db, form: form}
}

func (f *formTicketErasureFixture) ticket(t *testing.T, customerID uint, deleted bool) *models.Ticket {
	t.Helper()
	ticket := formTicketFixture(customerID)
	require.NoError(t, f.db.Omit("first_responded_at").Create(ticket).Error)
	if deleted {
		require.NoError(t, f.db.Delete(&models.Ticket{}, ticket.ID).Error)
	}
	return ticket
}

func (f *formTicketErasureFixture) submission(t *testing.T, email, key string, leadID *uint) *models.FormSubmission {
	t.Helper()
	submission := &models.FormSubmission{FormID: f.form.ID, Data: map[string]string{"email": email}, Email: email,
		Status: models.FormSubmissionConfirmed, LeadID: leadID, IdempotencyKey: key, TicketOutcome: models.FormTicketOutcomeCreated}
	require.NoError(t, f.db.Create(submission).Error)
	return submission
}

func (f *formTicketErasureFixture) key(t *testing.T, key string, ticketID, submissionID, customerID uint) *models.FormTicketKey {
	t.Helper()
	row := &models.FormTicketKey{FormID: f.form.ID, IdempotencyKey: key, TicketID: ticketID, SubmissionID: submissionID, CustomerID: customerID}
	require.NoError(t, f.db.Create(row).Error)
	return row
}

func keyExists(t *testing.T, db *gorm.DB, id uint) bool {
	t.Helper()
	return countRows(t, db, &models.FormTicketKey{}, "id = ?", id) == 1
}

func submissionKey(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var keys []string
	require.NoError(t, db.Unscoped().Model(&models.FormSubmission{}).Where("id = ?", id).Pluck("idempotency_key", &keys).Error)
	require.Len(t, keys, 1)
	return keys[0]
}

func runFormTicketKeyErasure(t *testing.T, useMySQL bool) {
	t.Run("customer root", func(t *testing.T) {
		db := openFormTicketRepoDB(t, useMySQL)
		f := newFormTicketErasureFixture(t, db)
		erased := createFormTicketCustomer(t, db, "erased@form-ticket.test")
		other := createFormTicketCustomer(t, db, "other@form-ticket.test")

		live := f.ticket(t, erased.ID, false)
		softDeleted := f.ticket(t, erased.ID, true)
		otherTicket := f.ticket(t, other.ID, false)

		ownSubmission := f.submission(t, "erased@form-ticket.test", "client-key-erased", nil)
		otherSubmission := f.submission(t, "other@form-ticket.test", "client-key-other", nil)

		byCustomer := f.key(t, "client-key-erased", live.ID, ownSubmission.ID, erased.ID)
		// A key row of another customer that names a soft-deleted ticket of
		// the erased one: purged through the ticket link.
		byTicket := f.key(t, "via-ticket", softDeleted.ID, otherSubmission.ID, other.ID)
		kept := f.key(t, "client-key-other", otherTicket.ID, otherSubmission.ID, other.ID)

		require.NoError(t, NewCustomerRepository(db).Delete(erased.ID))

		assert.False(t, keyExists(t, db, byCustomer.ID), "the customer's key row is purged")
		assert.False(t, keyExists(t, db, byTicket.ID), "a key row naming a (soft-deleted) ticket of the customer is purged")
		assert.True(t, keyExists(t, db, kept.ID), "another customer's key row stays")
		assert.Equal(t, "", submissionKey(t, db, ownSubmission.ID), "the erased address's unlinked submission loses its key")
		assert.Equal(t, "client-key-other", submissionKey(t, db, otherSubmission.ID))
		// Tickets themselves are business records and stay.
		assert.EqualValues(t, 2, countRows(t, db, &models.Ticket{}, "customer_id = ?", erased.ID))
	})

	t.Run("customer-role login root", func(t *testing.T) {
		db := openFormTicketRepoDB(t, useMySQL)
		f := newFormTicketErasureFixture(t, db)
		login := &models.User{Email: "login@form-ticket.test", Password: "x", FirstName: "Lo", LastName: "Gin", Role: models.RoleCustomer, IsActive: true}
		require.NoError(t, db.Create(login).Error)
		customer := createFormTicketCustomer(t, db, "login-customer@form-ticket.test")
		require.NoError(t, db.Model(customer).Update("user_id", login.ID).Error)
		ticket := f.ticket(t, customer.ID, false)
		submission := f.submission(t, "login-customer@form-ticket.test", "login-key", nil)
		row := f.key(t, "login-key", ticket.ID, submission.ID, customer.ID)

		require.NoError(t, NewUserRepository(db).Delete(login.ID))
		assert.False(t, keyExists(t, db, row.ID), "erasing the login reaches the customer and its key rows")
		assert.Equal(t, "", submissionKey(t, db, submission.ID))
	})

	t.Run("lead root", func(t *testing.T) {
		db := openFormTicketRepoDB(t, useMySQL)
		f := newFormTicketErasureFixture(t, db)
		owner := &models.User{Email: "owner@form-ticket.test", Password: "x", FirstName: "Ow", LastName: "Ner", Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(owner).Error)
		lead := &models.Lead{FirstName: "Le", LastName: "Ad", Email: "lead@form-ticket.test", OwnerID: owner.ID, Status: models.LeadStatusNew}
		require.NoError(t, db.Create(lead).Error)
		customer := createFormTicketCustomer(t, db, "unrelated@form-ticket.test")
		ticket := f.ticket(t, customer.ID, false)

		linked := f.submission(t, "lead@form-ticket.test", "linked-key", &lead.ID)
		unlinked := f.submission(t, "lead@form-ticket.test", "unlinked-key", nil)
		linkedRow := f.key(t, "linked-key", ticket.ID, linked.ID, customer.ID)
		unlinkedRow := f.key(t, "unlinked-key", ticket.ID, unlinked.ID, customer.ID)
		unrelated := f.submission(t, "unrelated@form-ticket.test", "unrelated-key", nil)
		unrelatedRow := f.key(t, "unrelated-key", ticket.ID, unrelated.ID, customer.ID)

		require.NoError(t, NewLeadRepository(db).Delete(lead.ID))
		assert.Equal(t, "", submissionKey(t, db, linked.ID), "scrubLeadFormSubmissions empties the key")
		assert.Equal(t, "", submissionKey(t, db, unlinked.ID), "scrubUnlinkedSubmissions empties the key")
		assert.False(t, keyExists(t, db, linkedRow.ID))
		assert.False(t, keyExists(t, db, unlinkedRow.ID))
		assert.Equal(t, "unrelated-key", submissionKey(t, db, unrelated.ID))
		assert.True(t, keyExists(t, db, unrelatedRow.ID), "another address's key row stays")
	})

	t.Run("a schema without form_ticket_keys skips the purge", func(t *testing.T) {
		db := openFormTicketRepoDB(t, useMySQL)
		f := newFormTicketErasureFixture(t, db)
		customer := createFormTicketCustomer(t, db, "nokeys@form-ticket.test")
		f.ticket(t, customer.ID, false)
		f.submission(t, "nokeys@form-ticket.test", "no-table-key", nil)
		require.NoError(t, db.Migrator().DropTable(&models.FormTicketKey{}))

		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))
		assert.EqualValues(t, 0, countRows(t, db, &models.Customer{}, "id = ? AND deleted_at IS NULL", customer.ID), "the erasure committed")
	})

	t.Run("a failing purge rolls the whole erasure back", func(t *testing.T) {
		db := openFormTicketRepoDB(t, useMySQL)
		f := newFormTicketErasureFixture(t, db)
		customer := createFormTicketCustomer(t, db, "rollback@form-ticket.test")
		ticket := f.ticket(t, customer.ID, false)
		submission := f.submission(t, "rollback@form-ticket.test", "rollback-key", nil)
		f.key(t, "rollback-key", ticket.ID, submission.ID, customer.ID)

		// The table exists, so the probe passes, but the purge statement fails.
		require.NoError(t, db.Exec("ALTER TABLE form_ticket_keys RENAME COLUMN customer_id TO customer_ref").Error)

		require.Error(t, NewCustomerRepository(db).Delete(customer.ID))
		var stored models.Customer
		require.NoError(t, db.First(&stored, customer.ID).Error, "the customer is still live")
		assert.Equal(t, "rollback@form-ticket.test", stored.Email, "and unchanged")
		assert.Equal(t, "rollback-key", submissionKey(t, db, submission.ID), "the submission keeps its key")
		assert.EqualValues(t, 1, countRows(t, db, &models.FormTicketKey{}, "submission_id = ?", submission.ID))
	})
}

func runTicketDeletePurgesFormTicketKeys(t *testing.T, useMySQL bool) {
	db := openFormTicketRepoDB(t, useMySQL)
	f := newFormTicketErasureFixture(t, db)
	customer := createFormTicketCustomer(t, db, "delete@form-ticket.test")
	formRepo := NewFormTicketRepository(db)

	deleted := formTicketFixture(customer.ID)
	require.NoError(t, formRepo.CreateTicketWithKey(deleted, &models.FormTicketKey{FormID: f.form.ID, IdempotencyKey: "reuse-me", SubmissionID: 1, CustomerID: customer.ID}))
	keptTicket := formTicketFixture(customer.ID)
	require.NoError(t, formRepo.CreateTicketWithKey(keptTicket, &models.FormTicketKey{FormID: f.form.ID, IdempotencyKey: "keep-me", SubmissionID: 2, CustomerID: customer.ID}))

	tickets := NewTicketRepository(db)
	require.NoError(t, tickets.Delete(deleted.ID))
	assert.EqualValues(t, 0, countRows(t, db, &models.FormTicketKey{}, "ticket_id = ?", deleted.ID), "the ticket's key rows are purged")
	assert.EqualValues(t, 1, countRows(t, db, &models.FormTicketKey{}, "ticket_id = ?", keptTicket.ID), "other tickets keep theirs")

	// The key is free again.
	again := formTicketFixture(customer.ID)
	require.NoError(t, formRepo.CreateTicketWithKey(again, &models.FormTicketKey{FormID: f.form.ID, IdempotencyKey: "reuse-me", SubmissionID: 3, CustomerID: customer.ID}))

	// Inside the delete's transaction: a failure after the purge rolls the
	// purge back with the delete.
	require.NoError(t, db.Exec("ALTER TABLE automation_runs RENAME COLUMN entity_id TO entity_ref").Error)
	require.Error(t, tickets.Delete(again.ID))
	assert.EqualValues(t, 1, countRows(t, db, &models.FormTicketKey{}, "ticket_id = ?", again.ID), "the purge rolled back")
	assert.EqualValues(t, 1, countRows(t, db, &models.Ticket{}, "id = ? AND deleted_at IS NULL", again.ID), "the ticket is still live")
	require.NoError(t, db.Exec("ALTER TABLE automation_runs RENAME COLUMN entity_ref TO entity_id").Error)

	// Inside a caller's transaction the delete is a savepoint; rolling the
	// caller back keeps the key row.
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tickets.WithTx(tx).Delete(again.ID))
		assert.EqualValues(t, 0, countRows(t, tx, &models.FormTicketKey{}, "ticket_id = ?", again.ID))
		return fmt.Errorf("caller rolls back")
	})
	require.Error(t, err)
	assert.EqualValues(t, 1, countRows(t, db, &models.FormTicketKey{}, "ticket_id = ?", again.ID))
}
