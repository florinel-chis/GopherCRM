package tests

// Form ticket intake over HTTP (issue #213): the real routes behind the real
// request-id, error, JWT and role middleware, the real services and
// repositories on the full SQLite schema, and a mailer that captures the
// confirmation links.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/forms"
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

const formTicketAPISecret = "form-ticket-intake-api-regression-secret-32"

// formTicketAPIMailer captures every message so the test can read the
// confirmation links.
type formTicketAPIMailer struct {
	mu   sync.Mutex
	sent []struct{ to, body string }
}

func (m *formTicketAPIMailer) SendPasswordReset(string, string, string) error { return nil }

func (m *formTicketAPIMailer) Send(to, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct{ to, body string }{to, body})
	return nil
}

// lastToken returns the token of the newest confirmation link mailed to to.
func (m *formTicketAPIMailer) lastToken(t *testing.T, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].to != to {
			continue
		}
		index := strings.Index(m.sent[i].body, "/forms/public/confirm?token=")
		if index < 0 {
			continue
		}
		start := strings.LastIndex(m.sent[i].body[:index], "http")
		link := m.sent[i].body[start:]
		if end := strings.IndexAny(link, " \n\t"); end >= 0 {
			link = link[:end]
		}
		parsed, err := url.Parse(link)
		require.NoError(t, err)
		token := parsed.Query().Get("token")
		require.NotEmpty(t, token)
		return token
	}
	t.Fatalf("no confirmation link was mailed to %s", to)
	return ""
}

func TestFormTicketIntakeAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formTicketAPISecret)
	// The public routes sit on the strict tier; this root makes more than its
	// burst of public calls from one address.
	t.Setenv("DISABLE_RATE_LIMIT", "true")

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:form_ticket_api_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano())),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db), config.JWTConfig{Secret: formTicketAPISecret, ExpiryHours: 1})
	mail := &formTicketAPIMailer{}
	formsCfg := config.FormsConfig{PublicBaseURL: "https://crm.example.test"}
	automationRepo := repository.NewAutomationRepository(db)
	formService := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db), userRepo, mail,
		txManager, formsCfg, "/api/v1", service.WithFormAutomationEvents(automationRepo),
		service.WithFormTicketIntake(repository.NewFormTicketRepository(db), txManager))
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	api := router.Group("/api/v1")
	handler.SetupFormPublicRoutes(api, handler.NewFormPublicHandler(formService, formsCfg, "/api/v1"))
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupFormRoutes(protected, handler.NewFormHandler(formService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))

	admin := &models.User{Email: "admin@form-ticket-api.test", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, service.NewUserService(userRepo).Register(admin, "password123"))
	bearer, err := authService.Login(admin.Email, "password123")
	require.NoError(t, err)

	authed := func(t *testing.T, method, path string, body interface{}) (int, map[string]interface{}) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(encoded)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, reader)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		return rec.Code, envelope
	}
	submit := func(t *testing.T, publicID, email string, key interface{}) (int, string) {
		t.Helper()
		body := map[string]interface{}{
			"values":    map[string]string{"email": email, "complaint": "My order arrived broken."},
			"challenge": forms.NewChallenge([]byte(formTicketAPISecret), time.Now().Add(-30*time.Second)),
		}
		if key != nil {
			body["idempotency_key"] = key
		}
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/"+publicID+"/submissions", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		// The same request id, so two answers can be compared byte for byte.
		req.Header.Set(middleware.RequestIDHeader, "form-ticket-api")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	confirm := func(t *testing.T, token string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/confirm", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}
	latestSubmissionID := func(t *testing.T, formID uint, email string) uint {
		t.Helper()
		var submission models.FormSubmission
		require.NoError(t, db.Where("form_id = ? AND email = ?", formID, email).Order("id desc").First(&submission).Error)
		return submission.ID
	}
	submissionView := func(t *testing.T, id uint) map[string]interface{} {
		t.Helper()
		status, envelope := authed(t, http.MethodGet, fmt.Sprintf("/forms/submissions/%d", id), nil)
		require.Equal(t, http.StatusOK, status)
		return envelope["data"].(map[string]interface{})
	}

	// An admin creates a create_ticket double opt-in form.
	status, envelope := authed(t, http.MethodPost, "/forms", map[string]interface{}{
		"name":   "Complaints API",
		"status": "published",
		"fields": []map[string]interface{}{
			{"name": "email", "label": "Email", "type": "email", "required": true},
			{"name": "complaint", "label": "Complaint", "type": "textarea", "required": true},
		},
		"double_opt_in": true,
		"create_ticket": true,
	})
	require.Equal(t, http.StatusCreated, status, "%v", envelope)
	formData := envelope["data"].(map[string]interface{})
	assert.Equal(t, true, formData["create_ticket"])
	assert.Equal(t, false, formData["create_lead"])
	formID := uint(formData["id"].(float64))
	publicID := formData["public_id"].(string)

	known := &models.Customer{FirstName: "Known", LastName: "Customer", Email: "known@form-ticket-api.test"}
	require.NoError(t, db.Create(known).Error)

	t.Run("submit-confirm-submit-confirm with one key yields one ticket", func(t *testing.T) {
		firstStatus, firstBody := submit(t, publicID, known.Email, "K-api-1")
		require.Equal(t, http.StatusOK, firstStatus, firstBody)
		firstID := latestSubmissionID(t, formID, known.Email)
		firstPage := confirm(t, mail.lastToken(t, known.Email))

		secondStatus, secondBody := submit(t, publicID, known.Email, "K-api-1")
		require.Equal(t, http.StatusOK, secondStatus, secondBody)
		secondID := latestSubmissionID(t, formID, known.Email)
		require.NotEqual(t, firstID, secondID)
		secondPage := confirm(t, mail.lastToken(t, known.Email))

		assert.Equal(t, firstBody, secondBody, "the two submit answers are identical")
		assert.Equal(t, firstPage, secondPage, "the two confirmation pages are identical")
		assert.Contains(t, firstPage, "confirmed", "the confirmation succeeded")

		var tickets []models.Ticket
		require.NoError(t, db.Where("customer_id = ?", known.ID).Find(&tickets).Error)
		require.Len(t, tickets, 1, "exactly one ticket")
		status, envelope := authed(t, http.MethodGet, fmt.Sprintf("/tickets/%d", tickets[0].ID), nil)
		require.Equal(t, http.StatusOK, status, "%v", envelope)
		ticketData := envelope["data"].(map[string]interface{})
		assert.Equal(t, "Form: Complaints API", ticketData["title"])
		assert.Equal(t, "open", ticketData["status"])
		assert.Equal(t, "medium", ticketData["priority"])

		first := submissionView(t, firstID)
		assert.Equal(t, models.FormTicketOutcomeCreated, first["ticket_outcome"])
		assert.EqualValues(t, tickets[0].ID, first["ticket_id"])
		assert.Equal(t, "K-api-1", first["idempotency_key"])
		second := submissionView(t, secondID)
		assert.Equal(t, models.FormTicketOutcomeDuplicate, second["ticket_outcome"])
		assert.EqualValues(t, tickets[0].ID, second["ticket_id"])
	})

	t.Run("a re-submission before confirmation invalidates the first link", func(t *testing.T) {
		other := &models.Customer{FirstName: "Early", LastName: "Resubmitter", Email: "early@form-ticket-api.test"}
		require.NoError(t, db.Create(other).Error)

		status, body := submit(t, publicID, other.Email, "K-api-2")
		require.Equal(t, http.StatusOK, status, body)
		firstToken := mail.lastToken(t, other.Email)
		status, body = submit(t, publicID, other.Email, "K-api-2")
		require.Equal(t, http.StatusOK, status, body)
		secondToken := mail.lastToken(t, other.Email)
		require.NotEqual(t, firstToken, secondToken)

		invalid := confirm(t, firstToken)
		assert.Contains(t, invalid, "no longer valid", "the first link is invalid")
		var count int64
		require.NoError(t, db.Model(&models.Ticket{}).Where("customer_id = ?", other.ID).Count(&count).Error)
		assert.Zero(t, count)

		valid := confirm(t, secondToken)
		assert.NotContains(t, valid, "no longer valid")
		require.NoError(t, db.Model(&models.Ticket{}).Where("customer_id = ?", other.ID).Count(&count).Error)
		assert.EqualValues(t, 1, count, "one ticket once the second link is confirmed")
	})

	t.Run("an unknown address records no_customer", func(t *testing.T) {
		status, body := submit(t, publicID, "stranger@form-ticket-api.test", nil)
		require.Equal(t, http.StatusOK, status, body)
		id := latestSubmissionID(t, formID, "stranger@form-ticket-api.test")
		page := confirm(t, mail.lastToken(t, "stranger@form-ticket-api.test"))
		assert.NotContains(t, page, "no longer valid")
		view := submissionView(t, id)
		assert.Equal(t, models.FormTicketOutcomeNoCustomer, view["ticket_outcome"])
		assert.Nil(t, view["ticket_id"])
		assert.Equal(t, "", view["idempotency_key"])
	})

	t.Run("an invalid idempotency_key answers 400", func(t *testing.T) {
		var before int64
		require.NoError(t, db.Model(&models.FormSubmission{}).Count(&before).Error)
		status, body := submit(t, publicID, known.Email, "not valid!")
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Contains(t, body, `"VALIDATION_ERROR"`)
		assert.Contains(t, body, `"idempotency_key"`)
		assert.NotContains(t, body, "not valid!")
		var after int64
		require.NoError(t, db.Model(&models.FormSubmission{}).Count(&after).Error)
		assert.Equal(t, before, after, "nothing stored")
	})
}
