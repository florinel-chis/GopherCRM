package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// #278: first_responded_at is written by CreateCommentWithFirstResponse alone,
// in the transaction that inserts the comment, and only while it is NULL.
// Create and Update (Save) leave the column out, so no struct can set,
// overwrite or clear it. These roots run on a file-backed SQLite database with
// one connection and, when TEST_MYSQL_DSN is set, on a disposable MySQL schema;
// the MySQL root skips without the variable and fails when it is unreachable.

func TestTicketFirstResponseRepositorySQLite(t *testing.T) {
	runTicketFirstResponseRepository(t, openTicketFirstResponseDB(t, false))
}

func TestTicketFirstResponseRepositoryMySQL(t *testing.T) {
	runTicketFirstResponseRepository(t, openTicketFirstResponseDB(t, true))
}

func openTicketFirstResponseDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "ticket_first_response.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
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
		name := fmt.Sprintf("gocrm_ticket_first_response_%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Ticket{}, &models.TicketComment{}))
	return db
}

var errInjectedStampFailure = errors.New("injected failure after the comment insert")

func runTicketFirstResponseRepository(t *testing.T, db *gorm.DB) {
	repo := NewTicketRepository(db)

	// A test-only callback fails the stamp statement while armed. It runs after
	// the comment insert of the same transaction, so it is a failure injected
	// after the insert.
	var failStamp atomic.Bool
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:fail_first_response_stamp", func(tx *gorm.DB) {
		if failStamp.Load() && strings.Contains(tx.Statement.SQL.String(), "first_responded_at") {
			_ = tx.AddError(errInjectedStampFailure)
		}
	}))

	staff := &models.User{Email: fmt.Sprintf("staff-%d@first-response.test", time.Now().UnixNano()), Password: "x",
		FirstName: "S", LastName: "Staff", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, db.Create(staff).Error)
	customer := &models.Customer{FirstName: "C", LastName: "Customer", Email: fmt.Sprintf("customer-%d@first-response.test", time.Now().UnixNano())}
	require.NoError(t, db.Create(customer).Error)

	newTicket := func(t *testing.T, title string) *models.Ticket {
		t.Helper()
		ticket := &models.Ticket{Title: title, Description: "d", Status: models.TicketStatusOpen,
			Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &staff.ID}
		require.NoError(t, repo.Create(ticket))
		return ticket
	}
	comment := func(ticket *models.Ticket, body string) *models.TicketComment {
		return &models.TicketComment{TicketID: ticket.ID, AuthorUserID: staff.ID, Body: body}
	}
	storedStamp := func(t *testing.T, ticketID uint) *time.Time {
		t.Helper()
		var row struct{ FirstRespondedAt *time.Time }
		require.NoError(t, db.Unscoped().Model(&models.Ticket{}).Select("first_responded_at").Where("id = ?", ticketID).Scan(&row).Error)
		return row.FirstRespondedAt
	}
	storedComments := func(t *testing.T, ticketID uint) []models.TicketComment {
		t.Helper()
		var comments []models.TicketComment
		require.NoError(t, db.Unscoped().Where("ticket_id = ?", ticketID).Order("id").Find(&comments).Error)
		return comments
	}

	t.Run("the first reply stamps its own created_at", func(t *testing.T) {
		ticket := newTicket(t, "First reply")
		assert.Nil(t, storedStamp(t, ticket.ID), "a new ticket has no stamp")

		first := comment(ticket, "First answer")
		require.NoError(t, repo.CreateCommentWithFirstResponse(first))
		comments := storedComments(t, ticket.ID)
		require.Len(t, comments, 1)
		stamp := storedStamp(t, ticket.ID)
		require.NotNil(t, stamp)
		assert.True(t, comments[0].CreatedAt.Equal(*stamp), "stamp %v is the comment's created_at %v", *stamp, comments[0].CreatedAt)

		// A second call keeps the first stamp, and still inserts its comment.
		time.Sleep(5 * time.Millisecond)
		second := comment(ticket, "Second answer")
		require.NoError(t, repo.CreateCommentWithFirstResponse(second))
		comments = storedComments(t, ticket.ID)
		require.Len(t, comments, 2)
		again := storedStamp(t, ticket.ID)
		require.NotNil(t, again)
		assert.True(t, stamp.Equal(*again), "the first stamp is kept")
		assert.False(t, comments[1].CreatedAt.Equal(*again), "the later comment did not move the stamp")
	})

	t.Run("a plain comment never stamps", func(t *testing.T) {
		ticket := newTicket(t, "Plain comment")
		require.NoError(t, repo.CreateComment(comment(ticket, "Note")))
		assert.Len(t, storedComments(t, ticket.ID), 1)
		assert.Nil(t, storedStamp(t, ticket.ID))
	})

	t.Run("a soft-deleted ticket is not stamped", func(t *testing.T) {
		ticket := newTicket(t, "Deleted")
		require.NoError(t, repo.Delete(ticket.ID))
		require.NoError(t, repo.CreateCommentWithFirstResponse(comment(ticket, "Too late")))
		assert.Nil(t, storedStamp(t, ticket.ID))
	})

	t.Run("a failure after the insert rolls back the comment and the stamp", func(t *testing.T) {
		ticket := newTicket(t, "Injected failure")
		failStamp.Store(true)
		err := repo.CreateCommentWithFirstResponse(comment(ticket, "Never stored"))
		failStamp.Store(false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errInjectedStampFailure), "got %v", err)
		assert.Empty(t, storedComments(t, ticket.ID), "the comment is rolled back")
		assert.Nil(t, storedStamp(t, ticket.ID), "the stamp is rolled back")

		// The same call succeeds once the fault is gone.
		require.NoError(t, repo.CreateCommentWithFirstResponse(comment(ticket, "Stored")))
		assert.Len(t, storedComments(t, ticket.ID), 1)
		assert.NotNil(t, storedStamp(t, ticket.ID))
	})

	t.Run("inside a caller's transaction that rolls back, nothing stays", func(t *testing.T) {
		ticket := newTicket(t, "Caller rollback")
		callerFault := errors.New("caller rolls back")
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, repo.WithTx(tx).CreateCommentWithFirstResponse(comment(ticket, "Inside")))
			var row struct{ FirstRespondedAt *time.Time }
			require.NoError(t, tx.Model(&models.Ticket{}).Select("first_responded_at").Where("id = ?", ticket.ID).Scan(&row).Error)
			assert.NotNil(t, row.FirstRespondedAt, "the stamp is visible inside the transaction")
			return callerFault
		})
		require.ErrorIs(t, err, callerFault)
		assert.Empty(t, storedComments(t, ticket.ID))
		assert.Nil(t, storedStamp(t, ticket.ID))
	})

	t.Run("Update on a struct read before the stamp never overwrites or clears it", func(t *testing.T) {
		ticket := newTicket(t, "Stale save")
		stale, err := repo.GetByID(ticket.ID)
		require.NoError(t, err)
		require.Nil(t, stale.FirstRespondedAt)

		require.NoError(t, repo.CreateCommentWithFirstResponse(comment(ticket, "Reply")))
		stamp := storedStamp(t, ticket.ID)
		require.NotNil(t, stamp)

		stale.Title = "Stale save, edited"
		stale.Status = models.TicketStatusResolved
		require.NoError(t, repo.Update(stale))
		after := storedStamp(t, ticket.ID)
		require.NotNil(t, after, "a NULL from the stale struct is not written back")
		assert.True(t, stamp.Equal(*after))
		require.NotNil(t, stale.FirstRespondedAt, "the struct carries the stored value")
		assert.True(t, stamp.Equal(*stale.FirstRespondedAt))
		var row models.Ticket
		require.NoError(t, db.First(&row, ticket.ID).Error)
		assert.Equal(t, "Stale save, edited", row.Title, "the other columns are written")
		assert.Equal(t, models.TicketStatusResolved, row.Status)

		forged := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		stale.FirstRespondedAt = &forged
		require.NoError(t, repo.Update(stale))
		after = storedStamp(t, ticket.ID)
		require.NotNil(t, after)
		assert.True(t, stamp.Equal(*after), "a value set on the struct is not written")
		assert.True(t, stamp.Equal(*stale.FirstRespondedAt))
	})

	t.Run("Update of an unstamped ticket leaves it NULL", func(t *testing.T) {
		ticket := newTicket(t, "Unstamped save")
		forged := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		ticket.FirstRespondedAt = &forged
		ticket.Title = "Unstamped save, edited"
		require.NoError(t, repo.Update(ticket))
		assert.Nil(t, storedStamp(t, ticket.ID))
		assert.Nil(t, ticket.FirstRespondedAt)
	})

	t.Run("Create never writes a caller-set first_responded_at", func(t *testing.T) {
		forged := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		due := time.Date(2026, 12, 1, 10, 0, 0, 123_000_000, time.UTC)
		ticket := &models.Ticket{Title: "Forged create", Description: "d", Status: models.TicketStatusOpen,
			Priority: models.TicketPriorityMedium, CustomerID: customer.ID, FirstRespondedAt: &forged, DueAt: &due}
		require.NoError(t, repo.Create(ticket))
		assert.Nil(t, storedStamp(t, ticket.ID))
		assert.Nil(t, ticket.FirstRespondedAt)

		var row models.Ticket
		require.NoError(t, db.First(&row, ticket.ID).Error)
		require.NotNil(t, row.DueAt, "due_at is written by Create")
		assert.True(t, due.Equal(*row.DueAt))
	})
}
