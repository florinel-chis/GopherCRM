package tests

// Ticket automation (#242) over HTTP on SQLite: the production route setup
// (SetupTicketRoutes, SetupAutomationRoutes, SetupNotificationRoutes) behind
// the real RequestID, ErrorHandler and JWT Auth middleware, the services wired
// as cmd/main.go wires them (TicketService with WithTicketAutomationEvents)
// and the full schema of models.MigrateDatabase with foreign keys on. The
// worker is the real engine, ticked by the test.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

type ticketAutomationAPI struct {
	t      *testing.T
	db     *gorm.DB
	router *gin.Engine
	engine *service.AutomationEngine
	tokens map[string]string
	users  map[string]*models.User
}

func newTicketAutomationAPI(t *testing.T) *ticketAutomationAPI {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "ticket-automation-api.db") +
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
	automationRepo := repository.NewAutomationRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "ticket-automation-api-test-secret-0123456789", ExpiryHours: 1})
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(db), txManager, nil),
		service.WithTicketAutomationEvents(automationRepo, txManager))
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))
	engine := service.NewAutomationEngine(automationRepo, notificationRepo, repository.NewTaskRepository(db), userRepo, nil, txManager)
	automationService := service.NewAutomationService(automationRepo, userRepo, engine, txManager)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))
	handler.SetupAutomationRoutes(protected, handler.NewAutomationHandler(automationService))
	handler.SetupNotificationRoutes(protected, handler.NewNotificationHandler(service.NewNotificationService(notificationRepo)))

	api := &ticketAutomationAPI{t: t, db: db, router: router, engine: engine, tokens: map[string]string{}, users: map[string]*models.User{}}
	for _, name := range []string{"admin", "support", "sales"} {
		user := &models.User{Email: name + "@ticket-automation-api.test", Password: "x", FirstName: "Api", LastName: name,
			Role: models.UserRole(name), IsActive: true}
		require.NoError(t, db.Create(user).Error)
		token, err := authService.GenerateJWT(user)
		require.NoError(t, err)
		api.tokens[name] = token
		api.users[name] = user
	}
	return api
}

func (a *ticketAutomationAPI) do(who, method, path string, body interface{}) (int, utils.APIResponse) {
	a.t.Helper()
	reader := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(a.t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.tokens[who])
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	var response utils.APIResponse
	if rec.Body.Len() > 0 {
		require.NoError(a.t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	}
	return rec.Code, response
}

func (a *ticketAutomationAPI) into(response utils.APIResponse, dst interface{}) {
	a.t.Helper()
	raw, err := json.Marshal(response.Data)
	require.NoError(a.t, err)
	require.NoError(a.t, json.Unmarshal(raw, dst))
}

func TestAutomationTicketAPISQLite(t *testing.T) {
	api := newTicketAutomationAPI(t)
	customer := &models.Customer{FirstName: "Api", LastName: "Customer", Email: "customer@ticket-automation-api.test"}
	require.NoError(t, api.db.Create(customer).Error)
	ticket := &models.Ticket{Title: "Printer jammed", Description: "d", Status: models.TicketStatusInProgress,
		Priority: models.TicketPriorityHigh, CustomerID: customer.ID, AssignedToID: &api.users["support"].ID}
	require.NoError(t, api.db.Create(ticket).Error)

	// An admin creates a ticket_status_changed rule.
	body := map[string]interface{}{"name": "Resolved ticket follow-up", "entity_type": "ticket", "trigger_type": "ticket_status_changed"}
	body["trigger"] = map[string]interface{}{"to": "resolved"}
	body["enabled"] = true
	body["steps"] = []map[string]interface{}{
		{"step_type": "notify", "config": map[string]interface{}{"title": "Ticket {{ticket.number}} is {{ticket.status}}", "recipient": "owner"}},
		{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 60}},
		{"step_type": "notify", "config": map[string]interface{}{"title": "Ticket {{ticket.number}} follow-up", "recipient": "owner"}},
	}
	code, response := api.do("admin", http.MethodPost, "/automation/rules", body)
	require.Equal(t, http.StatusCreated, code, "%+v", response.Error)
	var rule models.AutomationRule
	api.into(response, &rule)
	assert.Equal(t, models.AutomationEntityTicket, rule.EntityType)

	// The assigned support user resolves the ticket.
	code, response = api.do("support", http.MethodPut, fmt.Sprintf("/tickets/%d", ticket.ID), map[string]interface{}{"status": "resolved"})
	require.Equal(t, http.StatusOK, code, "%+v", response.Error)

	// The worker runs the rule.
	require.NoError(t, api.engine.Tick(context.Background()))

	t.Run("the assignee sees the notification", func(t *testing.T) {
		code, response := api.do("support", http.MethodGet, "/notifications", nil)
		require.Equal(t, http.StatusOK, code)
		var notifications []models.Notification
		api.into(response, &notifications)
		require.Len(t, notifications, 1)
		assert.Equal(t, models.AutomationEntityTicket, notifications[0].EntityType)
		assert.Equal(t, ticket.ID, notifications[0].EntityID)
		assert.Equal(t, fmt.Sprintf("Ticket %s is resolved", models.TicketNumber(ticket.ID)), notifications[0].Title)
	})

	var runID uint
	t.Run("the admin sees the ticket run", func(t *testing.T) {
		code, response := api.do("admin", http.MethodGet, "/automation/runs?entity_type=ticket", nil)
		require.Equal(t, http.StatusOK, code, "%+v", response.Error)
		var runs []models.AutomationRun
		api.into(response, &runs)
		require.Len(t, runs, 1)
		assert.Equal(t, models.AutomationEntityTicket, runs[0].EntityType)
		assert.Equal(t, ticket.ID, runs[0].EntityID)
		assert.Equal(t, models.AutomationRunWaiting, runs[0].Status)
		assert.Equal(t, rule.ID, runs[0].RuleID)
		runID = runs[0].ID

		code, _ = api.do("admin", http.MethodGet, fmt.Sprintf("/automation/runs/%d", runID), nil)
		assert.Equal(t, http.StatusOK, code)
	})

	t.Run("sales sees no ticket run and support has no run access", func(t *testing.T) {
		require.NotZero(t, runID)
		code, response := api.do("sales", http.MethodGet, "/automation/runs", nil)
		require.Equal(t, http.StatusOK, code)
		var runs []models.AutomationRun
		api.into(response, &runs)
		assert.Empty(t, runs)
		code, response = api.do("sales", http.MethodGet, "/automation/runs?entity_type=ticket", nil)
		require.Equal(t, http.StatusOK, code)
		api.into(response, &runs)
		assert.Empty(t, runs)
		code, _ = api.do("sales", http.MethodGet, fmt.Sprintf("/automation/runs/%d", runID), nil)
		assert.Equal(t, http.StatusNotFound, code)

		code, _ = api.do("support", http.MethodGet, "/automation/runs", nil)
		assert.Equal(t, http.StatusForbidden, code)
		code, _ = api.do("support", http.MethodGet, fmt.Sprintf("/automation/runs/%d", runID), nil)
		assert.Equal(t, http.StatusForbidden, code)
		code, _ = api.do("support", http.MethodPost, fmt.Sprintf("/automation/runs/%d/cancel", runID), nil)
		assert.Equal(t, http.StatusForbidden, code)
	})

	t.Run("deleting the ticket cancels the waiting run", func(t *testing.T) {
		require.NotZero(t, runID)
		code, response := api.do("admin", http.MethodDelete, fmt.Sprintf("/tickets/%d", ticket.ID), nil)
		require.Equal(t, http.StatusNoContent, code, "%+v", response.Error)

		var run models.AutomationRun
		require.NoError(t, api.db.First(&run, runID).Error)
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
		assert.NotNil(t, run.FinishedAt)

		code, response = api.do("support", http.MethodGet, "/notifications", nil)
		require.Equal(t, http.StatusOK, code)
		var notifications []models.Notification
		api.into(response, &notifications)
		assert.Empty(t, notifications, "the ticket's notifications are purged with it")

		// The wait passing later resumes nothing.
		api.engine.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
		require.NoError(t, api.engine.Tick(context.Background()))
		require.NoError(t, api.db.First(&run, runID).Error)
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
	})
}
