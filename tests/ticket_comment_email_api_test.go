package tests

// Ticket comment mail (#219) over HTTP on SQLite: the ticket routes of
// SetupTicketRoutes behind the real RequestID, Logger, ErrorHandler and JWT
// Auth middleware, the real repositories and services (TicketService built
// with WithTicketCommentMail), a recording mailer and the full schema of
// models.MigrateDatabase with foreign keys on. Every outcome is read back from
// the database.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type ticketMailAPIMessage struct{ To, Subject, Body string }

type ticketMailAPIMailer struct {
	mu   sync.Mutex
	sent []ticketMailAPIMessage
	err  error
}

func (m *ticketMailAPIMailer) SendPasswordReset(to, resetURL, _ string) error {
	return m.Send(to, "reset", resetURL)
}

func (m *ticketMailAPIMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, ticketMailAPIMessage{To: to, Subject: subject, Body: body})
	return nil
}

func (m *ticketMailAPIMailer) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *ticketMailAPIMailer) messages() []ticketMailAPIMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ticketMailAPIMessage(nil), m.sent...)
}

type ticketMailAPIFixture struct {
	db       *gorm.DB
	router   *gin.Engine
	mailer   *ticketMailAPIMailer
	tokens   map[string]string
	users    map[string]*models.User
	customer *models.Customer
	ticket   *models.Ticket
}

func newTicketMailAPIFixture(t *testing.T) *ticketMailAPIFixture {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "ticket-mail-api.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	previousDB := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previousDB
	require.NoError(t, err)

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "ticket-comment-email-api-test-secret-0123456789", ExpiryHours: 1})
	f := &ticketMailAPIFixture{db: db, mailer: &ticketMailAPIMailer{}, tokens: map[string]string{}, users: map[string]*models.User{}}
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(db), utils.NewTransactionManager(db), f.mailer))
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))
	ticketHandler := handler.NewTicketHandler(ticketService, customerService)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupTicketRoutes(protected, ticketHandler)
	f.router = router

	for _, name := range []string{"admin", "support", "other-support", "sales", "customer"} {
		role := models.UserRole(name)
		if name == "other-support" {
			role = models.RoleSupport
		}
		user := &models.User{Email: name + "@ticket-mail-api.test", Password: "x", FirstName: "Api", LastName: name, Role: role, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		token, err := authService.GenerateJWT(user)
		require.NoError(t, err)
		f.tokens[name] = token
		f.users[name] = user
	}
	f.customer = &models.Customer{FirstName: "Api", LastName: "Customer", Email: "api-customer@ticket-mail-api.test", UserID: &f.users["customer"].ID}
	require.NoError(t, db.Create(f.customer).Error)
	f.ticket = f.newTicket(t, f.customer.ID, "API ticket title")
	return f
}

func (f *ticketMailAPIFixture) newTicket(t *testing.T, customerID uint, title string) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{Title: title, Description: "d", Status: models.TicketStatusOpen, Priority: models.TicketPriorityMedium,
		CustomerID: customerID, AssignedToID: &f.users["support"].ID}
	require.NoError(t, f.db.Create(ticket).Error)
	return ticket
}

func (f *ticketMailAPIFixture) do(t *testing.T, who, method, path string, body interface{}) (int, string, utils.APIResponse) {
	t.Helper()
	reader := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens[who])
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var envelope utils.APIResponse
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	}
	return rec.Code, rec.Body.String(), envelope
}

func (f *ticketMailAPIFixture) counts(t *testing.T, ticketID uint) (comments, deliveries int64) {
	t.Helper()
	require.NoError(t, f.db.Model(&models.TicketComment{}).Where("ticket_id = ?", ticketID).Count(&comments).Error)
	require.NoError(t, f.db.Model(&models.TicketMailDelivery{}).Where("ticket_id = ?", ticketID).Count(&deliveries).Error)
	return comments, deliveries
}

func deliveryOf(t *testing.T, envelope utils.APIResponse) map[string]interface{} {
	t.Helper()
	data, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok, "data is an object")
	delivery, ok := data["delivery"].(map[string]interface{})
	require.True(t, ok, "the 201 carries the delivery")
	return delivery
}

func TestTicketCommentEmailAPISQLite(t *testing.T) {
	f := newTicketMailAPIFixture(t)
	path := fmt.Sprintf("/tickets/%d/comments", f.ticket.ID)

	t.Run("admin flagged comment is sent", func(t *testing.T) {
		status, raw, envelope := f.do(t, "admin", http.MethodPost, path, map[string]interface{}{"body": "Admin reply", "email_customer": true})
		require.Equal(t, http.StatusCreated, status, raw)
		delivery := deliveryOf(t, envelope)
		assert.Equal(t, models.TicketMailStatusSent, delivery["status"])
		assert.NotNil(t, delivery["sent_at"])
		messages := f.mailer.messages()
		require.Len(t, messages, 1)
		assert.Equal(t, f.customer.Email, messages[0].To)
		assert.Equal(t, fmt.Sprintf("Ticket %s from GopherCRM", models.TicketNumber(f.ticket.ID)), messages[0].Subject)
		assert.Contains(t, messages[0].Body, "Admin reply")
		assert.NotContains(t, raw, f.customer.Email, "the answer never carries the address")
	})

	t.Run("assigned support flagged comment is sent", func(t *testing.T) {
		status, raw, envelope := f.do(t, "support", http.MethodPost, path, map[string]interface{}{"body": "Support reply", "email_customer": true})
		require.Equal(t, http.StatusCreated, status, raw)
		assert.Equal(t, models.TicketMailStatusSent, deliveryOf(t, envelope)["status"])
		assert.Len(t, f.mailer.messages(), 2)
	})

	t.Run("flagged internal note is 400 and writes nothing", func(t *testing.T) {
		comments, deliveries := f.counts(t, f.ticket.ID)
		status, raw, envelope := f.do(t, "admin", http.MethodPost, path, map[string]interface{}{"body": "Note", "is_internal": true, "email_customer": true})
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		details, ok := envelope.Error.Details.(map[string]interface{})
		require.True(t, ok)
		assert.Contains(t, details, "email_customer")
		afterComments, afterDeliveries := f.counts(t, f.ticket.ID)
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, deliveries, afterDeliveries)
		assert.Len(t, f.mailer.messages(), 2, "nothing is sent")
	})

	t.Run("customer-role and other callers keep 403", func(t *testing.T) {
		comments, deliveries := f.counts(t, f.ticket.ID)
		for _, who := range []string{"customer", "sales", "other-support"} {
			status, raw, _ := f.do(t, who, http.MethodPost, path, map[string]interface{}{"body": "Reply", "email_customer": true})
			assert.Equal(t, http.StatusForbidden, status, "%s: %s", who, raw)
		}
		afterComments, afterDeliveries := f.counts(t, f.ticket.ID)
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, deliveries, afterDeliveries)
	})

	t.Run("erased customer is skipped and not mailed", func(t *testing.T) {
		gone := &models.Customer{FirstName: "Gone", LastName: "Customer", Email: "gone@ticket-mail-api.test"}
		require.NoError(t, f.db.Create(gone).Error)
		ticket := f.newTicket(t, gone.ID, "Ticket of an erased customer")
		require.NoError(t, repository.NewCustomerRepository(f.db).Delete(gone.ID))
		before := len(f.mailer.messages())

		status, raw, envelope := f.do(t, "admin", http.MethodPost, fmt.Sprintf("/tickets/%d/comments", ticket.ID),
			map[string]interface{}{"body": "Reply to nobody", "email_customer": true})
		require.Equal(t, http.StatusCreated, status, raw)
		delivery := deliveryOf(t, envelope)
		assert.Equal(t, models.TicketMailStatusSkipped, delivery["status"])
		assert.Equal(t, models.TicketMailErrorCustomerUnavailable, delivery["error_code"])
		assert.Len(t, f.mailer.messages(), before, "no send")
	})

	t.Run("a failing mailer answers 201 failed and keeps the comment", func(t *testing.T) {
		f.mailer.fail(errors.New("relay unavailable"))
		t.Cleanup(func() { f.mailer.fail(nil) })
		status, raw, envelope := f.do(t, "admin", http.MethodPost, path, map[string]interface{}{"body": "Reply while the relay is down", "email_customer": true})
		require.Equal(t, http.StatusCreated, status, raw)
		delivery := deliveryOf(t, envelope)
		assert.Equal(t, models.TicketMailStatusFailed, delivery["status"])
		assert.Equal(t, models.TicketMailErrorMailFailed, delivery["error_code"])
		var kept int64
		require.NoError(t, f.db.Model(&models.TicketComment{}).Where("ticket_id = ? AND body = ?", f.ticket.ID, "Reply while the relay is down").Count(&kept).Error)
		assert.EqualValues(t, 1, kept, "the comment is kept")
	})

	t.Run("read paths never carry delivery", func(t *testing.T) {
		for _, who := range []string{"admin", "support", "customer"} {
			for _, readPath := range []string{fmt.Sprintf("/tickets/%d", f.ticket.ID), fmt.Sprintf("/tickets/%d/comments", f.ticket.ID)} {
				status, raw, _ := f.do(t, who, http.MethodGet, readPath, nil)
				require.Equal(t, http.StatusOK, status, "%s %s: %s", who, readPath, raw)
				assert.Contains(t, raw, "Admin reply", "the flagged comments are in the read")
				assert.False(t, strings.Contains(raw, `"delivery"`), "%s %s carries no delivery key", who, readPath)
			}
		}
	})
}
