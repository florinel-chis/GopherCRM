package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #213: FormTicketRepository on the full production schema (models.MigrateDatabase,
// foreign keys on). The MySQL roots create their schema with utf8mb4_unicode_ci,
// the collation the SQL migrations give customers.email, and skip without
// TEST_MYSQL_DSN.

var formTicketRepoSeq atomic.Uint64

func openFormTicketRepoDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("form-ticket-%d.db", formTicketRepoSeq.Add(1)))
		var err error
		db, err = gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		dsnCfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_form_ticket_%d_%d", time.Now().UnixNano(), formTicketRepoSeq.Add(1))
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

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
	runFormTicketKeyRepository(t, openFormTicketRepoDB(t, false))
}

func TestFormTicketKeyRepositoryMySQL(t *testing.T) {
	db := openFormTicketRepoDB(t, true)
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
	db := openFormTicketRepoDB(t, false)
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
	db := openFormTicketRepoDB(t, true)
	repo := NewFormTicketRepository(db)

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
