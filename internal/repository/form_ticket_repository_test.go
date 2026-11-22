package repository

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #213: FormTicketRepository on the full production schema (models.MigrateDatabase,
// foreign keys on). The MySQL roots create their schema with utf8mb4_general_ci
// on purpose: that default is not the collation the tables must carry, and
// MigrateDatabase pins every table it creates to utf8mb4_unicode_ci, the
// collation the SQL migrations write. They skip without TEST_MYSQL_DSN.

var formTicketRepoSeq atomic.Uint64

// formTicketRepoDB is a file-backed SQLite database with foreign keys on and
// one connection, or a disposable MySQL schema whose default collation is
// deliberately utf8mb4_general_ci, with four connections. migrateFormTicketDB
// gives it the production schema, and MigrateDatabase pins the tables to
// utf8mb4_unicode_ci.
var formTicketRepoDB = testsupport.Group(
	testsupport.OnSQLite(testsupport.File("form-ticket.db", testsupport.BusyTimeout)),
	testsupport.OnMySQL(testsupport.MaxOpenConns(4), testsupport.SchemaCharset("utf8mb4", "utf8mb4_general_ci")),
)

// migrateFormTicketDB runs models.MigrateDatabase on db, with the global
// handle swapped for the call and restored straight away.
func migrateFormTicketDB(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	previous := models.DB
	models.DB = db
	err := models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)
	return db
}

func createFormTicketCustomer(t *testing.T, db *gorm.DB, email string) *models.Customer {
	t.Helper()
	customer := &models.Customer{FirstName: "Form", LastName: "Ticket", Email: email}
	require.NoError(t, db.Create(customer).Error)
	return customer
}

func formTicketFixture(customerID uint) *models.Ticket {
	return &models.Ticket{
		Title:       "Form: Complaints",
		Description: "Submitted through form 1, submission 1.",
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityMedium,
		CustomerID:  customerID,
	}
}

func TestFormTicketKeyRepositorySQLite(t *testing.T) {
	runFormTicketKeyRepository(t, migrateFormTicketDB(t, testsupport.Open(t, testsupport.SQLite, "form_ticket", formTicketRepoDB)))
}

func TestFormTicketKeyRepositoryMySQL(t *testing.T) {
	db := migrateFormTicketDB(t, testsupport.Open(t, testsupport.MySQL, "form_ticket", formTicketRepoDB))
	var collation string
	require.NoError(t, db.Raw(`SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'form_ticket_keys' AND COLUMN_NAME = 'idempotency_key'`).Scan(&collation).Error)
	require.Equal(t, "utf8mb4_bin", collation, "MigrateDatabase gives the key column the binary collation")
	runFormTicketKeyRepository(t, db)
}

func runFormTicketKeyRepository(t *testing.T, db *gorm.DB) {
	repo := NewFormTicketRepository(db)
	first := createFormTicketCustomer(t, db, "first@form-ticket.test")
	second := createFormTicketCustomer(t, db, "second@form-ticket.test")

	// A first key commits the ticket and the key row.
	ticket := formTicketFixture(first.ID)
	key := &models.FormTicketKey{FormID: 1, IdempotencyKey: "Order-1", SubmissionID: 10, CustomerID: first.ID}
	require.NoError(t, repo.CreateTicketWithKey(ticket, key))
	require.NotZero(t, ticket.ID)
	assert.Equal(t, ticket.ID, key.TicketID)
	var stored models.Ticket
	require.NoError(t, db.First(&stored, ticket.ID).Error)
	assert.Nil(t, stored.AssignedToID, "no assignee")
	assert.Nil(t, stored.FirstRespondedAt, "first_responded_at is not written")
	assert.Equal(t, models.TicketStatusOpen, stored.Status)
	assert.Equal(t, models.TicketPriorityMedium, stored.Priority)

	// Inside a caller's transaction, a duplicate is refused and leaves no
	// ticket row, while the caller's transaction stays usable.
	ticketsBefore := countRows(t, db, &models.Ticket{}, "1 = 1")
	err := db.Transaction(func(tx *gorm.DB) error {
		duplicate := formTicketFixture(second.ID)
		err := repo.WithTx(tx).CreateTicketWithKey(duplicate, &models.FormTicketKey{FormID: 1, IdempotencyKey: "Order-1", SubmissionID: 11, CustomerID: second.ID})
		require.True(t, errors.Is(err, ErrFormTicketKeyTaken), "duplicate key: %v", err)
		assert.NotContains(t, err.Error(), "Order-1", "the error does not quote the key")
		assert.Zero(t, duplicate.ID, "the rolled-back ticket hands out no id")
		assert.Equal(t, ticketsBefore, countRows(t, tx, &models.Ticket{}, "1 = 1"), "the savepoint rollback removed the ticket")

		row, err := repo.WithTx(tx).GetKeyForUpdate(1, "Order-1")
		require.NoError(t, err)
		assert.Equal(t, ticket.ID, row.TicketID)
		assert.Equal(t, first.ID, row.CustomerID)
		// The transaction still writes.
		return tx.Create(&models.FormTicketKey{FormID: 99, IdempotencyKey: "after", TicketID: ticket.ID, SubmissionID: 12, CustomerID: first.ID}).Error
	})
	require.NoError(t, err)
	assert.Equal(t, ticketsBefore, countRows(t, db, &models.Ticket{}, "1 = 1"), "no ticket row was left by the duplicate")
	assert.EqualValues(t, 1, countRows(t, db, &models.FormTicketKey{}, "form_id = 99"))

	// On the root handle too.
	err = repo.CreateTicketWithKey(formTicketFixture(second.ID), &models.FormTicketKey{FormID: 1, IdempotencyKey: "Order-1", SubmissionID: 13, CustomerID: second.ID})
	require.True(t, errors.Is(err, ErrFormTicketKeyTaken), "%v", err)
	assert.Equal(t, ticketsBefore, countRows(t, db, &models.Ticket{}, "1 = 1"))

	// Keys that differ only in letter case are distinct; the same key on
	// another form is distinct too.
	for _, variant := range []struct {
		form uint
		key  string
	}{{1, "order-1"}, {1, "ORDER-1"}, {2, "Order-1"}} {
		require.NoError(t, repo.CreateTicketWithKey(formTicketFixture(second.ID),
			&models.FormTicketKey{FormID: variant.form, IdempotencyKey: variant.key, SubmissionID: 20, CustomerID: second.ID}),
			"form %d key %q", variant.form, variant.key)
	}
	row, err := repo.GetKeyForUpdate(1, "order-1")
	require.NoError(t, err)
	assert.Equal(t, second.ID, row.CustomerID)
	row, err = repo.GetKeyForUpdate(1, "Order-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, row.CustomerID, "the lookup is exact")
	_, err = repo.GetKeyForUpdate(1, "missing")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	// Another failure than a duplicate rolls the ticket back too: a key row
	// naming a customer the ticket FK refuses cannot be written, so use an
	// over-long key instead (rejected by MySQL; SQLite stores it, so only
	// MySQL asserts the rollback).
	if db.Dialector.Name() == "mysql" {
		before := countRows(t, db, &models.Ticket{}, "1 = 1")
		err = repo.CreateTicketWithKey(formTicketFixture(first.ID), &models.FormTicketKey{FormID: 3, IdempotencyKey: strings.Repeat("k", 200), SubmissionID: 30, CustomerID: first.ID})
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrFormTicketKeyTaken))
		assert.Equal(t, before, countRows(t, db, &models.Ticket{}, "1 = 1"), "a failed key insert leaves no ticket")
	}

	if db.Dialector.Name() == "mysql" {
		t.Run("GetKeyForUpdate returns the committed row", func(t *testing.T) {
			// A transaction opened before the key commits still reads it
			// through the locking (current) read.
			reader := db.Begin()
			require.NoError(t, reader.Error)
			defer reader.Rollback()
			var warm int64
			require.NoError(t, reader.Model(&models.FormTicketKey{}).Count(&warm).Error, "starts the snapshot")

			require.NoError(t, repo.CreateTicketWithKey(formTicketFixture(first.ID),
				&models.FormTicketKey{FormID: 4, IdempotencyKey: "late", SubmissionID: 40, CustomerID: first.ID}))

			var snapshot int64
			require.NoError(t, reader.Model(&models.FormTicketKey{}).Where("form_id = 4").Count(&snapshot).Error)
			assert.Zero(t, snapshot, "the plain read sees the snapshot")
			row, err := repo.WithTx(reader).GetKeyForUpdate(4, "late")
			require.NoError(t, err, "the locking read sees the committed row")
			assert.Equal(t, first.ID, row.CustomerID)
		})
	}
}

// captureQueries records the SQL of every query statement on db.
func captureFormTicketQueries(t *testing.T, db *gorm.DB) func() []string {
	t.Helper()
	var mu sync.Mutex
	var statements []string
	name := fmt.Sprintf("form_ticket_capture_%d", formTicketRepoSeq.Add(1))
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		// The MySQL dialector binds LIMIT as a placeholder; render the vars so
		// the assertions see the statement the server ran.
		statements = append(statements, tx.Dialector.Explain(tx.Statement.SQL.String(), tx.Statement.Vars...))
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(statements))
		copy(out, statements)
		return out
	}
}

func TestFormTicketCustomerMatchSQLite(t *testing.T) {
	db := migrateFormTicketDB(t, testsupport.Open(t, testsupport.SQLite, "form_ticket", formTicketRepoDB))
	repo := NewFormTicketRepository(db)

	exact := createFormTicketCustomer(t, db, "Mixed.Case@Example.test")
	lower := createFormTicketCustomer(t, db, "lower@example.test")
	gone := createFormTicketCustomer(t, db, "gone@example.test")
	require.NoError(t, db.Delete(&models.Customer{}, gone.ID).Error)

	match, err := repo.LockLiveCustomerByEmail("Mixed.Case@Example.test")
	require.NoError(t, err)
	assert.Equal(t, exact.ID, match.ID, "the stored address equal to the submitted one matches")

	match, err = repo.LockLiveCustomerByEmail("LOWER@Example.TEST")
	require.NoError(t, err)
	assert.Equal(t, lower.ID, match.ID, "the stored lower-cased form of the submitted address matches")

	for _, miss := range []string{"mixed.case@example.test", "MIXED.CASE@EXAMPLE.TEST", "lower@example.test ", "gone@example.test", "", "nobody@example.test"} {
		_, err = repo.LockLiveCustomerByEmail(miss)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "%q does not match: %v", miss, err)
	}

	// Lowest id first when both spellings are stored.
	upper := createFormTicketCustomer(t, db, "Both@Example.test")
	createFormTicketCustomer(t, db, "both@example.test")
	match, err = repo.LockLiveCustomerByEmail("Both@Example.test")
	require.NoError(t, err)
	assert.Equal(t, upper.ID, match.ID)

	// A plain read on SQLite: the dialector drops the locking clause.
	captured := captureFormTicketQueries(t, db)
	_, err = repo.LockLiveCustomerByEmail("lower@example.test")
	require.NoError(t, err)
	statements := captured()
	require.NotEmpty(t, statements)
	last := strings.ToUpper(statements[len(statements)-1])
	assert.Contains(t, last, "DELETED_AT IS NULL")
	assert.Contains(t, last, "ORDER BY")
	assert.Contains(t, last, "LIMIT 1")
	assert.NotContains(t, last, "FOR UPDATE")
}

func TestFormTicketCustomerMatchMySQL(t *testing.T) {
	db := migrateFormTicketDB(t, testsupport.Open(t, testsupport.MySQL, "form_ticket", formTicketRepoDB))
	repo := NewFormTicketRepository(db)

	// The schema default differs from the collation the tables must carry,
	// so the checks below cannot pass by inheritance.
	var schemaCollation string
	require.NoError(t, db.Raw(`SELECT DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA
		WHERE SCHEMA_NAME = DATABASE()`).Scan(&schemaCollation).Error)
	require.Equal(t, "utf8mb4_general_ci", schemaCollation)

	var offTables []string
	require.NoError(t, db.Raw(`SELECT CONCAT(TABLE_NAME, ' (', COALESCE(TABLE_COLLATION, 'NULL'), ')') FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
		AND (TABLE_COLLATION IS NULL OR TABLE_COLLATION <> 'utf8mb4_unicode_ci') ORDER BY TABLE_NAME`).Scan(&offTables).Error)
	require.Empty(t, offTables, "MigrateDatabase creates every table in utf8mb4_unicode_ci")

	var offColumns []string
	require.NoError(t, db.Raw(`SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME, ' (', COLLATION_NAME, ')') FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND COLLATION_NAME IS NOT NULL
		AND COLLATION_NAME NOT IN ('utf8mb4_unicode_ci', 'utf8mb4_bin') ORDER BY TABLE_NAME, COLUMN_NAME`).Scan(&offColumns).Error)
	require.Empty(t, offColumns, "no column carries a collation other than utf8mb4_unicode_ci or the guards' utf8mb4_bin")

	var collation string
	require.NoError(t, db.Raw(`SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'customers' AND COLUMN_NAME = 'email'`).Scan(&collation).Error)
	require.Equal(t, "utf8mb4_unicode_ci", collation)

	accented := createFormTicketCustomer(t, db, "Ána@Example.test")
	padded := createFormTicketCustomer(t, db, "padded@example.test ")
	gone := createFormTicketCustomer(t, db, "gone@example.test")
	require.NoError(t, db.Delete(&models.Customer{}, gone.ID).Error)

	match, err := repo.LockLiveCustomerByEmail("ana@example.test")
	require.NoError(t, err)
	assert.Equal(t, accented.ID, match.ID, "case- and accent-insensitive")

	match, err = repo.LockLiveCustomerByEmail("padded@example.test")
	require.NoError(t, err)
	assert.Equal(t, padded.ID, match.ID, "trailing spaces are ignored")

	_, err = repo.LockLiveCustomerByEmail("gone@example.test")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "a soft-deleted customer never matches")
	_, err = repo.LockLiveCustomerByEmail("nobody@example.test")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	captured := captureFormTicketQueries(t, db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := repo.WithTx(tx).LockLiveCustomerByEmail("Ana@Example.test")
		return err
	}))
	statements := captured()
	require.NotEmpty(t, statements)
	last := strings.ToUpper(statements[len(statements)-1])
	assert.Contains(t, last, "FOR UPDATE", "a locking read on MySQL")
	assert.Contains(t, last, "DELETED_AT IS NULL")
	assert.Contains(t, last, "LIMIT 1")
}
