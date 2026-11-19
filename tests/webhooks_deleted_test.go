package tests

// The deleted webhook events (#437) over HTTP on SQLite: the registry routes
// accept lead.deleted, customer.deleted and ticket.deleted, and DELETE
// /tickets/{id} and DELETE /leads/{id} of announced records write one deleted
// row per announcing endpoint, behind the real middleware, with the
// repositories and services wired as main wires them.

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
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWebhookDeletedAPISQLite(t *testing.T) {
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "webhooks-deleted-api.db") +
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
	webhookRepo := repository.NewWebhookRepository(db)
	erasureListener := service.NewWebhookErasureListener(webhookRepo)
	userRepo := repository.NewUserRepository(db, repository.WithUserErasureListener(erasureListener))
	customerRepo := repository.NewCustomerRepository(db, repository.WithCustomerErasureListener(erasureListener))
	leadRepo := repository.NewLeadRepository(db, repository.WithLeadErasureListener(erasureListener))
	companyRepo := repository.NewCompanyRepository(db)
	box := utils.NewSecretBox("webhooks-deleted-api-master-secret-0123456789", service.WebhookSecretContext)

	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "webhooks-deleted-api-jwt-secret-0123456789", ExpiryHours: 1})
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, txManager, service.WithLeadWebhooks(webhookRepo))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo, service.WithCustomerWebhooks(webhookRepo, txManager))
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketWebhooks(webhookRepo, txManager))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupLeadRoutes(protected, handler.NewLeadHandler(leadService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))
	handler.SetupWebhookRoutes(protected, handler.NewWebhookHandler(service.NewWebhookService(webhookRepo, txManager, box)))

	admin := &models.User{Email: "admin@webhooks-deleted-api.test", Password: "x", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
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
	deletedRows := func() int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("event_type LIKE ?", "%.deleted").Count(&n).Error)
		return n
	}
	idOf := func(envelope utils.APIResponse) uint {
		t.Helper()
		data, ok := envelope.Data.(map[string]interface{})
		require.True(t, ok)
		return uint(data["id"].(float64))
	}

	var endpointID uint
	t.Run("registry", func(t *testing.T) {
		status, envelope, raw := do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/deleted", "event_types": []string{"lead.deleted", "customer.deleted", "ticket.deleted"}})
		require.Equal(t, http.StatusCreated, status, raw)
		data := envelope.Data.(map[string]interface{})
		assert.Equal(t, []interface{}{"customer.deleted", "lead.deleted", "ticket.deleted"}, data["event_types"], "answered sorted")
		deletedOnly := idOf(envelope)

		status, envelope, raw = do(http.MethodPut, fmt.Sprintf("/webhooks/endpoints/%d", deletedOnly), map[string]interface{}{
			"event_types": []string{"ticket.deleted"}})
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, []interface{}{"ticket.deleted"}, envelope.Data.(map[string]interface{})["event_types"])

		status, _, raw = do(http.MethodGet, "/webhooks/deliveries?event_type=customer.deleted", nil)
		assert.Equal(t, http.StatusOK, status, raw)

		status, envelope, raw = do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/unknown", "event_types": []string{"lead.archived"}})
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		details, ok := envelope.Error.Details.(map[string]interface{})
		require.True(t, ok, raw)
		assert.Contains(t, details, "event_types")

		// The endpoint the deletes below are announced to: every type.
		status, envelope, raw = do(http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com/all", "event_types": models.WebhookEventTypeValues})
		require.Equal(t, http.StatusCreated, status, raw)
		endpointID = idOf(envelope)
	})
	require.NotZero(t, endpointID)

	customer := &models.Customer{FirstName: "Hook", LastName: "Customer", Email: "customer@webhooks-deleted-api.test"}
	require.NoError(t, db.Create(customer).Error)

	t.Run("DELETE /tickets/{id} of an announced ticket", func(t *testing.T) {
		status, envelope, raw := do(http.MethodPost, "/tickets", map[string]interface{}{
			"title": "Hook ticket", "description": "d", "customer_id": customer.ID, "priority": "medium"})
		require.Equal(t, http.StatusCreated, status, raw)
		ticketID := idOf(envelope)
		require.Len(t, rowsOf(models.WebhookEventTicketCreated, ticketID), 1, "announced")
		before := deletedRows()

		status, _, raw = do(http.MethodDelete, fmt.Sprintf("/tickets/%d", ticketID), nil)
		require.Equal(t, http.StatusNoContent, status, raw)
		rows := rowsOf(models.WebhookEventTicketDeleted, ticketID)
		require.Len(t, rows, 1, "one ticket.deleted row, for the announcing endpoint only")
		assert.Equal(t, endpointID, rows[0].EndpointID)
		assert.Equal(t, models.WebhookDeliveryPending, rows[0].Status)
		assert.Equal(t, before+1, deletedRows())
	})

	t.Run("DELETE /leads/{id} of an announced lead", func(t *testing.T) {
		status, envelope, raw := do(http.MethodPost, "/leads", map[string]interface{}{
			"first_name": "Hook", "last_name": "Lead", "email": "lead@webhooks-deleted-api.test", "owner_id": admin.ID})
		require.Equal(t, http.StatusCreated, status, raw)
		leadID := idOf(envelope)
		before := deletedRows()

		status, _, raw = do(http.MethodDelete, fmt.Sprintf("/leads/%d", leadID), nil)
		require.Equal(t, http.StatusNoContent, status, raw)
		rows := rowsOf(models.WebhookEventLeadDeleted, leadID)
		require.Len(t, rows, 1)
		assert.Equal(t, endpointID, rows[0].EndpointID)
		assert.Equal(t, before+1, deletedRows())
	})
}
