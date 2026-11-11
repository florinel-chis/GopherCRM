package service

// Ticket comment mail (#219). Every root runs the real service over the real
// repositories on a real database: a file-backed SQLite database with the
// production pragmas and the one-connection pool, and, for the MySQL root, a
// disposable schema on TEST_MYSQL_DSN (skipped without it, failing when the
// server is unreachable). Assertions read committed rows outside any
// transaction.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Canary values: the privacy roots assert none of them reaches a log line or
// a delivery record.
const (
	ticketMailCanaryEmail   = "cora.canary@ticket-mail.test"
	ticketMailCanaryTitle   = "TitleCanary-Zx81 printer on fire"
	ticketMailCanaryBody    = "BodyCanary-Qv27 we replaced the fuser"
	ticketMailCanaryNote    = "NoteCanary-Ip55 internal only"
	ticketMailCanaryOther   = "OtherCanary-Ww13 an earlier public reply"
	ticketMailCanaryMailErr = "550 5.1.1 <cora.canary@ticket-mail.test>: TransportCanary-Rj40 rejected"
)

var ticketMailSeq atomic.Uint64

func ticketMailGormConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Discard, DisableForeignKeyConstraintWhenMigrating: true}
}

func migrateTicketMailSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.FormSubmission{},
		&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.Ticket{}, &models.TicketComment{}, &models.TicketMailDelivery{},
	))
}

func openTicketMailSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "ticket-mail.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), ticketMailGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateTicketMailSchema(t, db)
	return db
}

func openTicketMailMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 20*time.Second, 20*time.Second
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_ticket_mail_%d_%d", time.Now().UnixNano(), ticketMailSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), ticketMailGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateTicketMailSchema(t, db)
	return db
}

// ticketMailMessage is one message the recorder was asked to deliver.
type ticketMailMessage struct {
	To, Subject, Body string
}

// ticketMailRecorder stands in for the transport: it records every call, fails
// with err when set, reports LogsOnly when logsOnly is set, and runs onSend
// inside Send.
type ticketMailRecorder struct {
	mu       sync.Mutex
	sent     []ticketMailMessage
	err      error
	logsOnly bool
	onSend   func()
}

func (m *ticketMailRecorder) SendPasswordReset(to, resetURL, _ string) error {
	return m.Send(to, "reset", resetURL)
}

func (m *ticketMailRecorder) Send(to, subject, body string) error {
	m.mu.Lock()
	m.sent = append(m.sent, ticketMailMessage{To: to, Subject: subject, Body: body})
	hook, err := m.onSend, m.err
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (m *ticketMailRecorder) LogsOnly() bool { return m.logsOnly }

func (m *ticketMailRecorder) messages() []ticketMailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ticketMailMessage(nil), m.sent...)
}

// ticketMailEnv is one ticket of one customer, assigned to a support user,
// with an internal note and an earlier public comment already on it.
type ticketMailEnv struct {
	db                                 *gorm.DB
	tm                                 *utils.TransactionManager
	deliveries                         repository.TicketMailDeliveryRepository
	admin, support, sales, customerUse *models.User
	customer                           *models.Customer
	ticket                             *models.Ticket
}

func newTicketMailEnv(t *testing.T, db *gorm.DB) *ticketMailEnv {
	t.Helper()
	// Each root may run alone (-run), so the package logger the service writes
	// to must exist; captureTicketMailLogs replaces it afterwards when needed.
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })
	tag := fmt.Sprintf("%d", ticketMailSeq.Add(1))
	e := &ticketMailEnv{db: db, tm: utils.NewTransactionManager(db), deliveries: repository.NewTicketMailDeliveryRepository(db)}
	user := func(role models.UserRole) *models.User {
		u := &models.User{Email: fmt.Sprintf("%s-%s@staff.ticket-mail.test", role, tag), Password: "x",
			FirstName: "Staff", LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u
	}
	e.admin, e.support, e.sales, e.customerUse = user(models.RoleAdmin), user(models.RoleSupport), user(models.RoleSales), user(models.RoleCustomer)
	e.customer = &models.Customer{FirstName: "Cora", LastName: "Canary", Email: "old-" + tag + "@ticket-mail.test"}
	require.NoError(t, db.Create(e.customer).Error)
	e.ticket = &models.Ticket{Title: ticketMailCanaryTitle, Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: e.customer.ID, AssignedToID: &e.support.ID}
	require.NoError(t, db.Create(e.ticket).Error)
	require.NoError(t, db.Create(&models.TicketComment{TicketID: e.ticket.ID, AuthorUserID: e.support.ID, Body: ticketMailCanaryNote, IsInternal: true}).Error)
	require.NoError(t, db.Create(&models.TicketComment{TicketID: e.ticket.ID, AuthorUserID: e.support.ID, Body: ticketMailCanaryOther}).Error)
	// The address changes after the ticket was written: the mail goes to the
	// current one, read live, and the stored value carries surrounding blanks.
	require.NoError(t, db.Model(&models.Customer{}).Where("id = ?", e.customer.ID).
		Update("email", "  "+strings.ToUpper(ticketMailCanaryEmail[:1])+ticketMailCanaryEmail[1:]+"  ").Error)
	return e
}

func (e *ticketMailEnv) service(m mailer.Mailer) TicketService {
	return NewTicketService(repository.NewTicketRepository(e.db), repository.NewCustomerRepository(e.db), repository.NewUserRepository(e.db),
		WithTicketCommentMail(e.deliveries, e.tm, m))
}

func (e *ticketMailEnv) comment(author *models.User, body string) *models.TicketComment {
	return &models.TicketComment{TicketID: e.ticket.ID, AuthorUserID: author.ID, Body: body}
}

func (e *ticketMailEnv) count(t *testing.T, model interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(model).Where(where, args...).Count(&n).Error)
	return n
}

func (e *ticketMailEnv) storedDelivery(t *testing.T, commentID uint) models.TicketMailDelivery {
	t.Helper()
	var d models.TicketMailDelivery
	require.NoError(t, e.db.Where("comment_id = ?", commentID).Take(&d).Error)
	return d
}

func (e *ticketMailEnv) firstRespondedAt(t *testing.T) *time.Time {
	t.Helper()
	var ticket models.Ticket
	require.NoError(t, e.db.First(&ticket, e.ticket.ID).Error)
	return ticket.FirstRespondedAt
}

func (e *ticketMailEnv) writes(t *testing.T) (comments, deliveries int64) {
	t.Helper()
	return e.count(t, &models.TicketComment{}, "ticket_id = ?", e.ticket.ID),
		e.count(t, &models.TicketMailDelivery{}, "ticket_id = ?", e.ticket.ID)
}

// fault registers a callback that fails the statements of one kind on table
// while armed is true.
func ticketMailFault(t *testing.T, db *gorm.DB, kind, table string, armed *atomic.Bool, sentinel error) {
	t.Helper()
	name := fmt.Sprintf("ticket_mail_test:%s:%s:%d", kind, table, ticketMailSeq.Add(1))
	fn := func(tx *gorm.DB) {
		if !armed.Load() || tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != table {
			return
		}
		_ = tx.AddError(sentinel)
	}
	switch kind {
	case "create":
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, fn))
		t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	case "query":
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, fn))
		t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	case "update":
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, fn))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	default:
		t.Fatalf("unknown callback kind %q", kind)
	}
}

// armAfterDeliveryInsert arms flag once the pending row has been inserted,
// inside the comment transaction, so a fault fires only after the commit.
func armAfterDeliveryInsert(t *testing.T, db *gorm.DB, flag *atomic.Bool) {
	t.Helper()
	name := fmt.Sprintf("ticket_mail_test:arm:%d", ticketMailSeq.Add(1))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "ticket_mail_deliveries" && tx.Error == nil {
			flag.Store(true)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
}

func TestTicketCommentEmailSubjectAndBody(t *testing.T) {
	assert.Equal(t, "Ticket T-000042 from GopherCRM", TicketCommentEmailSubject(42, DefaultMailProductName))
	assert.Equal(t, "Ticket T-000042 from Acme Ltd", TicketCommentEmailSubject(42, "Acme Ltd"))
	body := RenderTicketCommentEmail(42, "Title here", "Reply here")
	assert.Contains(t, body, "T-000042")
	assert.Contains(t, body, "Title here")
	assert.Contains(t, body, "Reply here")
}

func TestTicketCommentEmailDelivery(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	recorder := &ticketMailRecorder{}
	svc := e.service(recorder)

	for i, author := range []*models.User{e.admin, e.support} {
		body := fmt.Sprintf("%s #%d", ticketMailCanaryBody, i)
		comment := e.comment(author, body)
		require.NoError(t, svc.CreateCommentWithCustomerEmail(context.Background(), comment), "author %s", author.Role)

		messages := recorder.messages()
		require.Len(t, messages, i+1, "exactly one send per flagged comment")
		message := messages[i]
		assert.Equal(t, ticketMailCanaryEmail, strings.ToLower(message.To), "the customer's current address")
		assert.Equal(t, strings.TrimSpace(message.To), message.To, "trimmed")
		assert.Equal(t, fmt.Sprintf("Ticket %s from GopherCRM", models.TicketNumber(e.ticket.ID)), message.Subject)
		assert.Equal(t, TicketCommentEmailSubject(e.ticket.ID, DefaultMailProductName), message.Subject)
		assert.Contains(t, message.Body, models.TicketNumber(e.ticket.ID))
		assert.Contains(t, message.Body, ticketMailCanaryTitle)
		assert.Contains(t, message.Body, body)
		assert.NotContains(t, message.Body, ticketMailCanaryNote, "never an internal note")
		assert.NotContains(t, message.Body, ticketMailCanaryOther, "no other comment")
		if i == 1 {
			assert.NotContains(t, message.Body, fmt.Sprintf("%s #0", ticketMailCanaryBody), "not the earlier flagged comment either")
		}

		require.NotNil(t, comment.Delivery)
		assert.Equal(t, models.TicketMailStatusSent, comment.Delivery.Status)
		assert.Equal(t, "", comment.Delivery.ErrorCode)
		require.NotNil(t, comment.Delivery.SentAt)
		assert.Equal(t, comment.ID, comment.Delivery.CommentID)
		assert.Equal(t, e.ticket.ID, comment.Delivery.TicketID)
		assert.Equal(t, e.customer.ID, comment.Delivery.CustomerID)

		stored := e.storedDelivery(t, comment.ID)
		assert.Equal(t, comment.Delivery.ID, stored.ID)
		assert.Equal(t, models.TicketMailStatusSent, stored.Status)
		assert.Equal(t, "", stored.ErrorCode)
		require.NotNil(t, stored.SentAt)
		assert.WithinDuration(t, *comment.Delivery.SentAt, *stored.SentAt, time.Second)
		assert.EqualValues(t, 1, e.count(t, &models.TicketComment{}, "id = ?", comment.ID), "the comment is committed")
	}
	assert.NotNil(t, e.firstRespondedAt(t), "the first flagged staff reply stamps first_responded_at")
}

func TestTicketCommentEmailSkipReasons(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *ticketMailEnv)
		nilMail bool
		code    string
	}{
		{"erased customer", func(t *testing.T, e *ticketMailEnv) {
			require.NoError(t, repository.NewCustomerRepository(e.db).Delete(e.customer.ID))
		}, false, models.TicketMailErrorCustomerUnavailable},
		{"soft-deleted customer", func(t *testing.T, e *ticketMailEnv) {
			require.NoError(t, e.db.Delete(&models.Customer{}, e.customer.ID).Error)
		}, false, models.TicketMailErrorCustomerUnavailable},
		{"missing customer", func(t *testing.T, e *ticketMailEnv) {
			require.NoError(t, e.db.Model(&models.Ticket{}).Where("id = ?", e.ticket.ID).Update("customer_id", 99999999).Error)
		}, false, models.TicketMailErrorCustomerUnavailable},
		{"blank address", func(t *testing.T, e *ticketMailEnv) {
			require.NoError(t, e.db.Model(&models.Customer{}).Where("id = ?", e.customer.ID).Update("email", "   ").Error)
		}, false, models.TicketMailErrorNoRecipient},
		{"no mailer, decided before the customer read", func(t *testing.T, e *ticketMailEnv) {
			// A customer read after the commit would fail; no_mailer is
			// decided first, so the outcome is not lookup_failed.
			armed := &atomic.Bool{}
			ticketMailFault(t, e.db, "query", "customers", armed, errors.New("customer read must not happen"))
			armAfterDeliveryInsert(t, e.db, armed)
		}, true, models.TicketMailErrorNoMailer},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := newTicketMailEnv(t, openTicketMailSQLite(t))
			tc.prepare(t, e)
			recorder := &ticketMailRecorder{}
			var svc TicketService
			if tc.nilMail {
				svc = e.service(nil)
			} else {
				svc = e.service(recorder)
			}
			comment := e.comment(e.admin, ticketMailCanaryBody)
			require.NoError(t, svc.CreateCommentWithCustomerEmail(context.Background(), comment))

			assert.Empty(t, recorder.messages(), "nothing is sent")
			require.NotNil(t, comment.Delivery)
			assert.Equal(t, models.TicketMailStatusSkipped, comment.Delivery.Status)
			assert.Equal(t, tc.code, comment.Delivery.ErrorCode)
			assert.Nil(t, comment.Delivery.SentAt)
			stored := e.storedDelivery(t, comment.ID)
			assert.Equal(t, models.TicketMailStatusSkipped, stored.Status)
			assert.Equal(t, tc.code, stored.ErrorCode)
			assert.EqualValues(t, 1, e.count(t, &models.TicketComment{}, "id = ?", comment.ID), "the comment is committed")
			assert.NotNil(t, e.firstRespondedAt(t), "first_responded_at is committed")
		})
	}
}

func TestTicketCommentEmailFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"deadline", fmt.Errorf("smtp exchange: %w", os.ErrDeadlineExceeded), models.TicketMailErrorTimeout},
		{"invalid address", fmt.Errorf("recipient: %w", mailer.ErrInvalidAddress), models.TicketMailErrorInvalidRecipient},
		{"invalid header", fmt.Errorf("subject: %w", mailer.ErrInvalidHeader), models.TicketMailErrorInvalidRecipient},
		{"other", errors.New(ticketMailCanaryMailErr), models.TicketMailErrorMailFailed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := newTicketMailEnv(t, openTicketMailSQLite(t))
			recorder := &ticketMailRecorder{err: tc.err}
			comment := e.comment(e.support, ticketMailCanaryBody)
			require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment), "the comment stands whatever the mail did")

			assert.Len(t, recorder.messages(), 1, "Send is called once, never again")
			require.NotNil(t, comment.Delivery)
			assert.Equal(t, models.TicketMailStatusFailed, comment.Delivery.Status)
			assert.Equal(t, tc.code, comment.Delivery.ErrorCode)
			assert.Nil(t, comment.Delivery.SentAt)
			stored := e.storedDelivery(t, comment.ID)
			assert.Equal(t, models.TicketMailStatusFailed, stored.Status)
			assert.Equal(t, tc.code, stored.ErrorCode)
			assert.EqualValues(t, 1, e.count(t, &models.TicketComment{}, "id = ?", comment.ID))
			assert.NotNil(t, e.firstRespondedAt(t))
		})
	}

	t.Run("lookup failed", func(t *testing.T) {
		e := newTicketMailEnv(t, openTicketMailSQLite(t))
		armed := &atomic.Bool{}
		ticketMailFault(t, e.db, "query", "customers", armed, errors.New("injected customer read failure"))
		armAfterDeliveryInsert(t, e.db, armed)
		recorder := &ticketMailRecorder{}
		comment := e.comment(e.admin, ticketMailCanaryBody)
		require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))
		armed.Store(false)

		assert.Empty(t, recorder.messages(), "nothing is sent")
		assert.Equal(t, models.TicketMailStatusFailed, comment.Delivery.Status)
		assert.Equal(t, models.TicketMailErrorLookupFailed, comment.Delivery.ErrorCode)
		stored := e.storedDelivery(t, comment.ID)
		assert.Equal(t, models.TicketMailErrorLookupFailed, stored.ErrorCode)
		assert.NotNil(t, e.firstRespondedAt(t))
	})
}

func TestTicketCommentEmailLogOnly(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	recorder := &ticketMailRecorder{logsOnly: true}
	comment := e.comment(e.admin, ticketMailCanaryBody)
	require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))

	assert.Len(t, recorder.messages(), 1, "the log-only mailer is called exactly once")
	assert.Equal(t, models.TicketMailStatusLogOnly, comment.Delivery.Status)
	assert.Nil(t, comment.Delivery.SentAt)
	assert.Equal(t, "", comment.Delivery.ErrorCode)
	stored := e.storedDelivery(t, comment.ID)
	assert.Equal(t, models.TicketMailStatusLogOnly, stored.Status)
	assert.Nil(t, stored.SentAt)
	assert.Equal(t, "", stored.ErrorCode)

	// The development LogMailer (no SMTP_HOST) is such a mailer.
	reporter, ok := interface{}(&mailer.LogMailer{}).(mailer.LogOnlyReporter)
	require.True(t, ok)
	assert.True(t, reporter.LogsOnly())
}

func TestTicketCommentEmailAtomicitySQLite(t *testing.T) {
	runTicketCommentEmailAtomicity(t, openTicketMailSQLite)
}

func TestTicketCommentEmailAtomicityMySQL(t *testing.T) {
	runTicketCommentEmailAtomicity(t, openTicketMailMySQL)
}

func runTicketCommentEmailAtomicity(t *testing.T, open func(t *testing.T) *gorm.DB) {
	t.Run("delivery insert failure rolls everything back", func(t *testing.T) {
		e := newTicketMailEnv(t, open(t))
		armed := &atomic.Bool{}
		armed.Store(true)
		sentinel := errors.New("injected delivery insert failure")
		ticketMailFault(t, e.db, "create", "ticket_mail_deliveries", armed, sentinel)
		commentsBefore, deliveriesBefore := e.writes(t)
		recorder := &ticketMailRecorder{}

		err := e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), e.comment(e.admin, ticketMailCanaryBody))
		require.ErrorIs(t, err, sentinel)
		armed.Store(false)
		comments, deliveries := e.writes(t)
		assert.Equal(t, commentsBefore, comments, "the comment rolled back")
		assert.Equal(t, deliveriesBefore, deliveries)
		assert.Nil(t, e.firstRespondedAt(t), "the first_responded_at stamp rolled back")
		assert.Empty(t, recorder.messages(), "nothing is sent")
	})

	t.Run("cancelled context writes and sends nothing", func(t *testing.T) {
		e := newTicketMailEnv(t, open(t))
		commentsBefore, deliveriesBefore := e.writes(t)
		recorder := &ticketMailRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := e.service(recorder).CreateCommentWithCustomerEmail(ctx, e.comment(e.admin, ticketMailCanaryBody))
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		comments, deliveries := e.writes(t)
		assert.Equal(t, commentsBefore, comments)
		assert.Equal(t, deliveriesBefore, deliveries)
		assert.Nil(t, e.firstRespondedAt(t))
		assert.Empty(t, recorder.messages())
	})

	t.Run("send runs after the commit with no transaction held", func(t *testing.T) {
		e := newTicketMailEnv(t, open(t))
		recorder := &ticketMailRecorder{}
		var seenPending, seenComment int64
		var probeErr error
		recorder.onSend = func() {
			// On SQLite's single connection this read would wait for a held
			// transaction until the bound expires; on MySQL another connection
			// sees only committed rows.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			probeErr = e.db.WithContext(ctx).Model(&models.TicketMailDelivery{}).
				Where("ticket_id = ? AND status = ?", e.ticket.ID, models.TicketMailStatusPending).Count(&seenPending).Error
			if probeErr == nil {
				probeErr = e.db.WithContext(ctx).Model(&models.TicketComment{}).
					Where("ticket_id = ? AND body = ?", e.ticket.ID, ticketMailCanaryBody).Count(&seenComment).Error
			}
		}
		comment := e.comment(e.admin, ticketMailCanaryBody)
		require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))
		require.NoError(t, probeErr, "the probe inside Send ran without waiting for a transaction")
		assert.EqualValues(t, 1, seenPending, "the pending row was committed before the send")
		assert.EqualValues(t, 1, seenComment, "the comment was committed before the send")
		assert.Equal(t, models.TicketMailStatusSent, e.storedDelivery(t, comment.ID).Status)
	})

	t.Run("the stamp is conditional on pending", func(t *testing.T) {
		e := newTicketMailEnv(t, open(t))
		recorder := &ticketMailRecorder{}
		comment := e.comment(e.admin, ticketMailCanaryBody)
		// Another writer stamps the row while the mail is in flight: the
		// service's stamp matches nothing and does not overwrite it.
		recorder.onSend = func() {
			require.NoError(t, e.db.Model(&models.TicketMailDelivery{}).Where("ticket_id = ? AND status = ?", e.ticket.ID, models.TicketMailStatusPending).
				Updates(map[string]interface{}{"status": models.TicketMailStatusSkipped, "error_code": models.TicketMailErrorNoMailer}).Error)
		}
		require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))
		stored := e.storedDelivery(t, comment.ID)
		assert.Equal(t, models.TicketMailStatusSkipped, stored.Status, "a stamp never overwrites a finished row")
		assert.Equal(t, models.TicketMailStatusPending, comment.Delivery.Status, "the answer shows what the service could not record")
	})

	t.Run("a failed stamp leaves pending and returns nil", func(t *testing.T) {
		e := newTicketMailEnv(t, open(t))
		armed := &atomic.Bool{}
		armed.Store(true)
		ticketMailFault(t, e.db, "update", "ticket_mail_deliveries", armed, errors.New("injected stamp failure"))
		recorder := &ticketMailRecorder{}
		comment := e.comment(e.admin, ticketMailCanaryBody)

		require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))
		armed.Store(false)
		assert.Len(t, recorder.messages(), 1)
		require.NotNil(t, comment.Delivery)
		assert.Equal(t, models.TicketMailStatusPending, comment.Delivery.Status, "the returned delivery shows the stored pending")
		assert.Equal(t, models.TicketMailStatusPending, e.storedDelivery(t, comment.ID).Status)
		assert.EqualValues(t, 1, e.count(t, &models.TicketComment{}, "id = ?", comment.ID))
	})
}

func TestTicketCommentEmailRefusedWhenUnwired(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	recorder := &ticketMailRecorder{}
	ticketRepo := repository.NewTicketRepository(e.db)
	customerRepo := repository.NewCustomerRepository(e.db)
	userRepo := repository.NewUserRepository(e.db)
	services := map[string]TicketService{
		"no option":              NewTicketService(ticketRepo, customerRepo, userRepo),
		"no delivery repository": NewTicketService(ticketRepo, customerRepo, userRepo, WithTicketCommentMail(nil, e.tm, recorder)),
		"no transaction manager": NewTicketService(ticketRepo, customerRepo, userRepo, WithTicketCommentMail(e.deliveries, nil, recorder)),
	}
	commentsBefore, deliveriesBefore := e.writes(t)
	for name, svc := range services {
		err := svc.CreateCommentWithCustomerEmail(context.Background(), e.comment(e.admin, ticketMailCanaryBody))
		assert.ErrorIs(t, err, ErrTicketCommentMailUnavailable, name)
	}
	comments, deliveries := e.writes(t)
	assert.Equal(t, commentsBefore, comments, "nothing is written")
	assert.Equal(t, deliveriesBefore, deliveries)
	assert.Empty(t, recorder.messages())
}

func TestTicketCommentEmailTransactionRootRequired(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	recorder := &ticketMailRecorder{}
	svc := e.service(recorder)
	commentsBefore, deliveriesBefore := e.writes(t)

	var statements atomic.Int32
	counting := &atomic.Bool{}
	name := "ticket_mail_test:count_statements"
	count := func(tx *gorm.DB) {
		if counting.Load() {
			statements.Add(1)
		}
	}
	require.NoError(t, e.db.Callback().Query().Before("gorm:query").Register(name, count))
	require.NoError(t, e.db.Callback().Create().Before("gorm:create").Register(name, count))
	require.NoError(t, e.db.Callback().Update().Before("gorm:update").Register(name, count))
	t.Cleanup(func() {
		_ = e.db.Callback().Query().Remove(name)
		_ = e.db.Callback().Create().Remove(name)
		_ = e.db.Callback().Update().Remove(name)
	})

	var inner error
	err := e.tm.WithTransaction(context.Background(), func(ctx context.Context) error {
		counting.Store(true)
		inner = svc.CreateCommentWithCustomerEmail(ctx, e.comment(e.admin, ticketMailCanaryBody))
		counting.Store(false)
		return nil
	})
	require.NoError(t, err)
	assert.ErrorIs(t, inner, utils.ErrTransactionRootRequired)
	assert.Zero(t, statements.Load(), "refused before any read or write")
	comments, deliveries := e.writes(t)
	assert.Equal(t, commentsBefore, comments)
	assert.Equal(t, deliveriesBefore, deliveries)
	assert.Empty(t, recorder.messages())
}

func TestTicketCommentEmailServiceRefusesInternalOrNonStaff(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	recorder := &ticketMailRecorder{}
	svc := e.service(recorder)
	commentsBefore, deliveriesBefore := e.writes(t)

	internal := e.comment(e.admin, ticketMailCanaryBody)
	internal.IsInternal = true
	assert.ErrorIs(t, svc.CreateCommentWithCustomerEmail(context.Background(), internal), ErrTicketCommentEmailNotAllowed, "an internal note")
	assert.ErrorIs(t, svc.CreateCommentWithCustomerEmail(context.Background(), e.comment(e.sales, ticketMailCanaryBody)),
		ErrTicketCommentEmailNotAllowed, "a sales author")
	assert.ErrorIs(t, svc.CreateCommentWithCustomerEmail(context.Background(), e.comment(e.customerUse, ticketMailCanaryBody)),
		ErrTicketCommentEmailNotAllowed, "a customer-role author")

	comments, deliveries := e.writes(t)
	assert.Equal(t, commentsBefore, comments, "nothing is written")
	assert.Equal(t, deliveriesBefore, deliveries)
	assert.Nil(t, e.firstRespondedAt(t))
	assert.Empty(t, recorder.messages(), "nothing is sent")
}

func captureTicketMailLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := utils.Logger
	l := logrus.New()
	var output bytes.Buffer
	l.SetOutput(&output)
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetLevel(logrus.DebugLevel)
	utils.Logger = l
	t.Cleanup(func() { utils.Logger = old })
	return &output
}

func TestTicketCommentEmailLogsOmitPersonalData(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	output := captureTicketMailLogs(t)

	// sent
	require.NoError(t, e.service(&ticketMailRecorder{}).CreateCommentWithCustomerEmail(context.Background(), e.comment(e.admin, ticketMailCanaryBody+" sent")))
	// failed, with a transport error that quotes the address
	require.NoError(t, e.service(&ticketMailRecorder{err: errors.New(ticketMailCanaryMailErr)}).
		CreateCommentWithCustomerEmail(context.Background(), e.comment(e.admin, ticketMailCanaryBody+" failed")))
	// a stamp that fails
	armed := &atomic.Bool{}
	armed.Store(true)
	ticketMailFault(t, e.db, "update", "ticket_mail_deliveries", armed, errors.New("injected stamp failure"))
	require.NoError(t, e.service(&ticketMailRecorder{}).CreateCommentWithCustomerEmail(context.Background(), e.comment(e.support, ticketMailCanaryBody+" unstamped")))
	armed.Store(false)
	// refused
	internal := e.comment(e.admin, ticketMailCanaryBody+" internal")
	internal.IsInternal = true
	require.Error(t, e.service(&ticketMailRecorder{}).CreateCommentWithCustomerEmail(context.Background(), internal))

	logs := output.String()
	require.Contains(t, logs, "Ticket comment mail recorded", "the capture saw the path's lines")
	require.Contains(t, logs, "Ticket comment mail failed")
	require.Contains(t, logs, "Ticket comment mail outcome could not be recorded")
	lower := strings.ToLower(logs)
	for _, forbidden := range []string{
		ticketMailCanaryEmail, ticketMailCanaryTitle, ticketMailCanaryBody, "TransportCanary-Rj40",
		"from gophercrm", "550 5.1.1",
	} {
		assert.NotContains(t, lower, strings.ToLower(forbidden), "a log line carries %q", forbidden)
	}

	// Every line of the mail path carries only the permitted fields.
	allowed := map[string]bool{"level": true, "msg": true, "time": true, "service": true, "method": true,
		"ticket_id": true, "comment_id": true, "delivery_id": true, "status": true, "error_code": true}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["method"] != "CreateCommentWithCustomerEmail" {
			continue
		}
		for key := range entry {
			assert.True(t, allowed[key], "field %q in %s", key, line)
		}
	}
}

func TestTicketCommentEmailRecordHoldsNoAddress(t *testing.T) {
	e := newTicketMailEnv(t, openTicketMailSQLite(t))
	needles := []string{ticketMailCanaryEmail, ticketMailCanaryTitle, ticketMailCanaryBody, "from gophercrm", "transportcanary"}

	for _, recorder := range []*ticketMailRecorder{{}, {err: errors.New(ticketMailCanaryMailErr)}, {logsOnly: true}} {
		comment := e.comment(e.admin, ticketMailCanaryBody)
		require.NoError(t, e.service(recorder).CreateCommentWithCustomerEmail(context.Background(), comment))
		raw, err := json.Marshal(comment.Delivery)
		require.NoError(t, err)
		for _, needle := range needles {
			assert.NotContains(t, strings.ToLower(string(raw)), strings.ToLower(needle), "the delivery JSON holds %q", needle)
		}
	}

	var rows []map[string]interface{}
	require.NoError(t, e.db.Table("ticket_mail_deliveries").Find(&rows).Error)
	require.Len(t, rows, 3)
	for _, row := range rows {
		for column, value := range row {
			if value == nil {
				continue
			}
			text := strings.ToLower(fmt.Sprintf("%v", value))
			if b, ok := value.([]byte); ok {
				text = strings.ToLower(string(b))
			}
			for _, needle := range needles {
				assert.NotContains(t, text, strings.ToLower(needle), "ticket_mail_deliveries.%s holds %q", column, needle)
			}
		}
	}
}
