package tests

// The updated webhook events (#408) over HTTP on SQLite: PUT /leads/{id},
// PUT /customers/{id}, PUT /tickets/{id} and the lead and ticket bulk status
// routes behind the real middleware, with the real repositories and services
// wired with the webhook outbox as main wires them, and the registry routes
// accepting the six created and updated event types.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWebhookUpdatedAPISQLite(t *testing.T) {
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "webhooks-updated-api.db") +
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

	txManager := utils.NewTransactionManager(db)
	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	companyRepo := repository.NewCompanyRepository(db)
	webhookRepo := repository.NewWebhookRepository(db)
	webhookEmitter := webhook.NewEmitter(webhookRepo)
	box := utils.NewSecretBox("webhooks-updated-api-master-secret-0123456789", service.WebhookSecretContext)

	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "webhooks-updated-api-jwt-secret-0123456789", ExpiryHours: 1})
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, txManager, service.WithLeadWebhooks(webhookEmitter))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo, txManager, service.WithCustomerWebhooks(webhookEmitter))
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketWebhooks(webhookEmitter, txManager))
	bulkService := service.NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		txManager, utils.Logger, service.WithBulkWebhooks(webhookEmitter))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupBulkStatusRoutes(protected, handler.NewBulkHandler(bulkService))
	handler.SetupLeadRoutes(protected, handler.NewLeadHandler(leadService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))
	handler.SetupWebhookRoutes(protected, handler.NewWebhookHandler(service.NewWebhookService(webhookRepo, txManager, box)))

	admin := &models.User{Email: "admin@webhooks-updated-api.test", Password: "x", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(admin).Error)
	token, err := authService.GenerateJWT(admin)
	require.NoError(t, err)

	do := func(method, path string, body interface{}) (int, utils.APIResponse, string) {
		t.Helper()
		var reader io.Reader = bytes.NewReader(nil)
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope utils.APIResponse
		if rec.Body.Len() > 0 {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		}
		return rec.Code, envelope, rec.Body.String()
	}
	rowsOf := func(eventType string, recordID uint) []models.WebhookDelivery {
		t.Helper()
		var rows []models.WebhookDelivery
		require.NoError(t, db.Where("event_type = ? AND record_id = ?", eventType, recordID).Order("id").Find(&rows).Error)
		return rows
	}
	total := func() int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Count(&n).Error)
		return n
	}
	seq := 0
	seedLead := func(status models.LeadStatus) *models.Lead {
		t.Helper()
		seq++
		lead := &models.Lead{FirstName: "Hook", LastName: "Lead", Email: fmt.Sprintf("lead-%d@webhooks-updated-api.test", seq), OwnerID: admin.ID, Status: status}
		require.NoError(t, db.Create(lead).Error)
		return lead
	}
	seedCustomer := func() *models.Customer {
		t.Helper()
		seq++
		customer := &models.Customer{FirstName: "Hook", LastName: "Customer", Email: fmt.Sprintf("customer-%d@webhooks-updated-api.test", seq)}
		require.NoError(t, db.Create(customer).Error)
		return customer
	}
	seedTicket := func(status models.TicketStatus) *models.Ticket {
		t.Helper()
		ticket := &models.Ticket{Title: "Hook ticket", Description: "d", Status: status, Priority: models.TicketPriorityMedium, CustomerID: seedCustomer().ID}
		require.NoError(t, db.Create(ticket).Error)
		return ticket
	}
	six := []string{"lead.created", "lead.updated", "customer.created", "customer.updated", "ticket.created", "ticket.updated"}
	canonical := []interface{}{"customer.created", "customer.updated", "lead.created", "lead.updated", "ticket.created", "ticket.updated"}

	var endpointID uint
	t.Run("registry", func(t *testing.T) {
		status, envelope, raw := do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/hook", "event_types": six})
		require.Equal(t, http.StatusCreated, status, raw)
		data := envelope.Data.(map[string]interface{})
		endpointID = uint(data["id"].(float64))
		assert.Equal(t, canonical, data["event_types"], "answered as the six canonical names")
		var stored string
		require.NoError(t, db.Raw("SELECT event_types FROM webhook_endpoints WHERE id = ?", endpointID).Scan(&stored).Error)
		assert.Equal(t, "customer.created,customer.updated,lead.created,lead.updated,ticket.created,ticket.updated", stored)

		// A second endpoint narrowed by PUT to one updated type, then to a
		// created type only: it must get no row from the updates below.
		status, envelope, raw = do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/other", "event_types": six})
		require.Equal(t, http.StatusCreated, status, raw)
		otherID := uint(envelope.Data.(map[string]interface{})["id"].(float64))
		status, envelope, raw = do(http.MethodPut, fmt.Sprintf("/webhooks/endpoints/%d", otherID), map[string]interface{}{
			"event_types": []string{"ticket.updated"}})
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, []interface{}{"ticket.updated"}, envelope.Data.(map[string]interface{})["event_types"])
		status, envelope, raw = do(http.MethodPut, fmt.Sprintf("/webhooks/endpoints/%d", otherID), map[string]interface{}{
			"event_types": []string{"lead.created"}})
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, []interface{}{"lead.created"}, envelope.Data.(map[string]interface{})["event_types"])

		status, envelope, raw = do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/unknown", "event_types": []string{"lead.archived"}})
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		details, ok := envelope.Error.Details.(map[string]interface{})
		require.True(t, ok, raw)
		assert.Contains(t, details, "event_types")
		status, envelope, raw = do(http.MethodPut, fmt.Sprintf("/webhooks/endpoints/%d", otherID), map[string]interface{}{
			"event_types": []string{"lead.archived"}})
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)

		status, _, raw = do(http.MethodGet, "/webhooks/deliveries?event_type=lead.updated", nil)
		assert.Equal(t, http.StatusOK, status, raw)
		status, _, _ = do(http.MethodGet, "/webhooks/deliveries?event_type=lead.archived", nil)
		assert.Equal(t, http.StatusBadRequest, status)
	})
	require.NotZero(t, endpointID)

	// assertOne checks the single pending row of one update.
	assertOne := func(t *testing.T, eventType string, recordID uint) {
		t.Helper()
		rows := rowsOf(eventType, recordID)
		require.Len(t, rows, 1, "one row, for the subscribed endpoint only")
		assert.Equal(t, endpointID, rows[0].EndpointID)
		assert.Equal(t, models.WebhookDeliveryPending, rows[0].Status)
		assert.Equal(t, 0, rows[0].Attempts)
		assert.Regexp(t, `^evt_[0-9a-f]{32}$`, rows[0].EventID)
	}

	t.Run("PUT /leads/{id}", func(t *testing.T) {
		lead := seedLead(models.LeadStatusNew)
		status, _, raw := do(http.MethodPut, fmt.Sprintf("/leads/%d", lead.ID), map[string]interface{}{"first_name": "Changed"})
		require.Equal(t, http.StatusOK, status, raw)
		assertOne(t, models.WebhookEventLeadUpdated, lead.ID)
	})

	t.Run("PUT /customers/{id}", func(t *testing.T) {
		customer := seedCustomer()
		status, _, raw := do(http.MethodPut, fmt.Sprintf("/customers/%d", customer.ID), map[string]interface{}{"first_name": "Changed"})
		require.Equal(t, http.StatusOK, status, raw)
		assertOne(t, models.WebhookEventCustomerUpdated, customer.ID)
	})

	t.Run("PUT /tickets/{id}", func(t *testing.T) {
		ticket := seedTicket(models.TicketStatusOpen)
		status, _, raw := do(http.MethodPut, fmt.Sprintf("/tickets/%d", ticket.ID), map[string]interface{}{"priority": "high"})
		require.Equal(t, http.StatusOK, status, raw)
		assertOne(t, models.WebhookEventTicketUpdated, ticket.ID)
	})

	t.Run("an unchanged update writes no row", func(t *testing.T) {
		lead := seedLead(models.LeadStatusNew)
		customer := seedCustomer()
		ticket := seedTicket(models.TicketStatusOpen)
		before := total()
		status, _, raw := do(http.MethodPut, fmt.Sprintf("/leads/%d", lead.ID), map[string]interface{}{"first_name": lead.FirstName})
		require.Equal(t, http.StatusOK, status, raw)
		status, _, raw = do(http.MethodPut, fmt.Sprintf("/customers/%d", customer.ID), map[string]interface{}{"first_name": customer.FirstName})
		require.Equal(t, http.StatusOK, status, raw)
		status, _, raw = do(http.MethodPut, fmt.Sprintf("/tickets/%d", ticket.ID), map[string]interface{}{"priority": string(ticket.Priority)})
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, before, total())
	})

	t.Run("POST /leads/bulk/status", func(t *testing.T) {
		var ids []uint
		for i := 0; i < 3; i++ {
			ids = append(ids, seedLead(models.LeadStatusNew).ID)
		}
		status, _, raw := do(http.MethodPost, "/leads/bulk/status", map[string]interface{}{"lead_ids": ids, "status": "contacted"})
		require.Equal(t, http.StatusOK, status, raw)
		events := map[string]bool{}
		for _, id := range ids {
			assertOne(t, models.WebhookEventLeadUpdated, id)
			events[rowsOf(models.WebhookEventLeadUpdated, id)[0].EventID] = true
		}
		assert.Len(t, events, 3, "one event id per lead")

		before := total()
		status, _, raw = do(http.MethodPost, "/leads/bulk/status", map[string]interface{}{"lead_ids": append(ids, 999999991), "status": "qualified"})
		require.Equal(t, http.StatusNotFound, status, raw)
		assert.Equal(t, before, total(), "a refused batch writes no row")
		var qualified int64
		require.NoError(t, db.Model(&models.Lead{}).Where("id IN ? AND status = ?", ids, models.LeadStatusQualified).Count(&qualified).Error)
		assert.Zero(t, qualified, "a refused batch changes no status")
	})

	t.Run("POST /tickets/bulk/status", func(t *testing.T) {
		var ids []uint
		for i := 0; i < 3; i++ {
			ids = append(ids, seedTicket(models.TicketStatusOpen).ID)
		}
		status, _, raw := do(http.MethodPost, "/tickets/bulk/status", map[string]interface{}{"ticket_ids": ids, "status": "resolved"})
		require.Equal(t, http.StatusOK, status, raw)
		events := map[string]bool{}
		for _, id := range ids {
			assertOne(t, models.WebhookEventTicketUpdated, id)
			events[rowsOf(models.WebhookEventTicketUpdated, id)[0].EventID] = true
		}
		assert.Len(t, events, 3, "one event id per ticket")

		before := total()
		status, _, raw = do(http.MethodPost, "/tickets/bulk/status", map[string]interface{}{"ticket_ids": append(ids, 999999991), "status": "closed"})
		require.Equal(t, http.StatusNotFound, status, raw)
		assert.Equal(t, before, total(), "a refused batch writes no row")
		var closed int64
		require.NoError(t, db.Model(&models.Ticket{}).Where("id IN ? AND status = ?", ids, models.TicketStatusClosed).Count(&closed).Error)
		assert.Zero(t, closed, "a refused batch changes no status")
	})
}
