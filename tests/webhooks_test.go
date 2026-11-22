package tests

// Outbound webhooks (#345) over HTTP on SQLite: the lead, customer, ticket and
// webhook routes behind the real RequestID, Logger, ErrorHandler, Auth and
// RequireRole middleware, the real repositories and services, the full schema
// of models.MigrateDatabase, and a WebhookRuntime whose injected egress guard
// reaches an httptest TLS server.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/egress"
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

// failingWebhookRepository wraps the real repository; while armed, its
// enqueue writes the rows and then returns the injected error, after the
// customer insert of the same transaction.
type failingWebhookRepository struct {
	repository.WebhookRepository
	armed *atomic.Bool
	err   error
}

func (f *failingWebhookRepository) WithTx(tx *gorm.DB) repository.WebhookRepository {
	return &failingWebhookRepository{WebhookRepository: f.WebhookRepository.WithTx(tx), armed: f.armed, err: f.err}
}

func (f *failingWebhookRepository) EnqueueDeliveries(ctx context.Context, rows []models.WebhookDelivery) error {
	if err := f.WebhookRepository.EnqueueDeliveries(ctx, rows); err != nil {
		return err
	}
	if f.armed.Load() {
		return f.err
	}
	return nil
}

type webhookAPIRequest struct {
	header http.Header
	body   []byte
}

type webhookAPIConsumer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []webhookAPIRequest
}

func (c *webhookAPIConsumer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.requests = append(c.requests, webhookAPIRequest{header: r.Header.Clone(), body: body})
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *webhookAPIConsumer) received() []webhookAPIRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]webhookAPIRequest(nil), c.requests...)
}

type apiResolver map[string][]netip.Addr

func (r apiResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addrs, ok := r[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// verifyWebhook is a Standard Webhooks check written for this test.
func verifyWebhook(secret string, header http.Header, body []byte) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(header.Get("webhook-id") + "." + header.Get("webhook-timestamp") + "." + string(body)))
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header.Get("webhook-signature")))
}

func TestWebhookAPISQLite(t *testing.T) {
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "webhooks-api.db") +
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
	box := utils.NewSecretBox("webhooks-api-test-master-secret-0123456789", webhook.WebhookSecretContext)
	failing := &failingWebhookRepository{WebhookRepository: webhookRepo, armed: &atomic.Bool{}, err: errors.New("injected outbox failure")}

	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "webhooks-api-test-jwt-secret-0123456789", ExpiryHours: 1})
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, txManager, service.WithLeadWebhooks(webhook.NewEmitter(webhookRepo)))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo, txManager, service.WithCustomerWebhooks(webhook.NewEmitter(failing)))
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketWebhooks(webhook.NewEmitter(webhookRepo), txManager))

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
	handler.SetupWebhookRoutes(protected, handler.NewWebhookHandler(webhook.NewWebhookService(webhookRepo, txManager, box)))

	tokens := map[string]string{}
	users := map[string]*models.User{}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport} {
		user := &models.User{Email: string(role) + "@webhooks-api.test", Password: "x", FirstName: "Api", LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		token, err := authService.GenerateJWT(user)
		require.NoError(t, err)
		tokens[string(role)] = token
		users[string(role)] = user
	}
	do := func(who, method, path string, body interface{}) (int, utils.APIResponse, string) {
		t.Helper()
		var reader io.Reader = bytes.NewReader(nil)
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokens[who])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope utils.APIResponse
		if rec.Body.Len() > 0 {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		}
		return rec.Code, envelope, rec.Body.String()
	}

	consumer := &webhookAPIConsumer{}
	consumer.server = httptest.NewUnstartedServer(http.HandlerFunc(consumer.serve))
	consumer.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	consumer.server.StartTLS()
	t.Cleanup(consumer.server.Close)
	_, port, err := net.SplitHostPort(consumer.server.Listener.Addr().String())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(consumer.server.Certificate())
	guard := egress.New(
		egress.WithResolver(apiResolver{"example.com": {netip.MustParseAddr("93.184.216.34")}}),
		egress.WithDialer(func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", consumer.server.Listener.Addr().String())
		}),
		egress.WithRootCAs(roots))
	runtime := webhook.NewWebhookRuntime(webhookRepo, guard, box)

	// An admin registers an endpoint and sees the secret once.
	status, envelope, raw := do("admin", http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
		"url": "https://example.com:" + port + "/hook", "event_types": []string{"lead.created", "customer.created", "ticket.created"},
	})
	require.Equal(t, http.StatusCreated, status, raw)
	data := envelope.Data.(map[string]interface{})
	secret, _ := data["secret"].(string)
	require.True(t, strings.HasPrefix(secret, "whsec_"), raw)
	endpointID := uint(data["id"].(float64))
	assert.Equal(t, true, data["is_active"])

	status, _, raw = do("admin", http.MethodGet, fmt.Sprintf("/webhooks/endpoints/%d", endpointID), nil)
	require.Equal(t, http.StatusOK, status)
	assert.NotContains(t, raw, secret)
	// No secret key and no sealed column; previous_secret_expires_at (#503)
	// is the one field whose name says secret.
	assert.NotContains(t, raw, `"secret"`)
	assert.NotContains(t, raw, "secret_sealed")
	status, _, raw = do("admin", http.MethodGet, "/webhooks/endpoints", nil)
	require.Equal(t, http.StatusOK, status)
	assert.NotContains(t, raw, secret)
	status, _, _ = do("sales", http.MethodGet, "/webhooks/endpoints", nil)
	assert.Equal(t, http.StatusForbidden, status, "sales is refused")

	// A lead, a customer and a ticket.
	status, _, raw = do("admin", http.MethodPost, "/leads", map[string]interface{}{
		"first_name": "Hook", "last_name": "Lead", "email": "hook.lead@webhooks-api.test", "owner_id": users["admin"].ID})
	require.Equal(t, http.StatusCreated, status, raw)
	status, _, raw = do("admin", http.MethodPost, "/customers", map[string]interface{}{
		"first_name": "Hook", "last_name": "Customer", "email": "hook.customer@webhooks-api.test"})
	require.Equal(t, http.StatusCreated, status, raw)
	var customer models.Customer
	require.NoError(t, db.Where("email = ?", "hook.customer@webhooks-api.test").Take(&customer).Error)
	status, _, raw = do("support", http.MethodPost, "/tickets", map[string]interface{}{
		"title": "Hook ticket", "description": "d", "customer_id": customer.ID})
	require.Equal(t, http.StatusCreated, status, raw)

	var pending int64
	require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("status = ?", models.WebhookDeliveryPending).Count(&pending).Error)
	assert.EqualValues(t, 3, pending, "one row per created record")

	result, err := runtime.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, result.Succeeded)
	received := consumer.received()
	require.Len(t, received, 3)
	types := map[string]bool{}
	for _, request := range received {
		assert.True(t, verifyWebhook(secret, request.header, request.body), "a valid signature")
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(request.body, &body))
		assert.Len(t, body, 4)
		types[body["type"].(string)] = true
		assert.NotContains(t, string(request.body), "webhooks-api.test", "no personal data leaves")
		assert.NotContains(t, string(request.body), "Hook")
	}
	assert.Equal(t, map[string]bool{"lead.created": true, "customer.created": true, "ticket.created": true}, types)

	// A customer create rolled back after its insert: 500, nothing stored,
	// nothing sent.
	failing.armed.Store(true)
	status, envelope, raw = do("admin", http.MethodPost, "/customers", map[string]interface{}{
		"first_name": "Rolled", "last_name": "Back", "email": "rolled.back@webhooks-api.test"})
	failing.armed.Store(false)
	assert.Equal(t, http.StatusInternalServerError, status, raw)
	require.NotNil(t, envelope.Error)
	var customers int64
	require.NoError(t, db.Unscoped().Model(&models.Customer{}).Where("email = ?", "rolled.back@webhooks-api.test").Count(&customers).Error)
	assert.Zero(t, customers, "no customer")
	require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("status = ?", models.WebhookDeliveryPending).Count(&pending).Error)
	assert.Zero(t, pending, "no delivery row")
	result, err = runtime.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, result.Attempted)
	assert.Len(t, consumer.received(), 3, "the runtime sends nothing")

	// The delivery log lists the outcomes, newest first, without lease_until.
	status, envelope, raw = do("admin", http.MethodGet, fmt.Sprintf("/webhooks/deliveries?endpoint_id=%d&status=succeeded", endpointID), nil)
	require.Equal(t, http.StatusOK, status, raw)
	rows := envelope.Data.([]interface{})
	require.Len(t, rows, 3)
	first := rows[0].(map[string]interface{})
	second := rows[1].(map[string]interface{})
	assert.Greater(t, first["id"].(float64), second["id"].(float64))
	assert.Equal(t, "succeeded", first["status"])
	assert.EqualValues(t, 1, first["attempts"])
	assert.EqualValues(t, 200, first["last_http_status"])
	assert.NotContains(t, raw, "lease_until")
	status, _, _ = do("support", http.MethodGet, "/webhooks/deliveries", nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, envelope, _ = do("admin", http.MethodGet, "/webhooks/deliveries?status=bogus", nil)
	assert.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, envelope.Error)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)

	// Deleting the endpoint removes its log.
	status, _, _ = do("admin", http.MethodDelete, fmt.Sprintf("/webhooks/endpoints/%d", endpointID), nil)
	assert.Equal(t, http.StatusNoContent, status)
	var left int64
	require.NoError(t, db.Model(&models.WebhookDelivery{}).Count(&left).Error)
	assert.Zero(t, left)

	t.Run("the twelve event types, deals included (#515)", func(t *testing.T) {
		unsorted := []string{"ticket.deleted", "deal.updated", "lead.created", "customer.updated", "deal.created", "ticket.created",
			"lead.deleted", "customer.created", "deal.deleted", "ticket.updated", "lead.updated", "customer.deleted"}
		sorted := []interface{}{"customer.created", "customer.deleted", "customer.updated", "deal.created", "deal.deleted", "deal.updated",
			"lead.created", "lead.deleted", "lead.updated", "ticket.created", "ticket.deleted", "ticket.updated"}
		status, envelope, raw := do("admin", http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com:" + port + "/all", "event_types": unsorted})
		require.Equal(t, http.StatusCreated, status, raw)
		data := envelope.Data.(map[string]interface{})
		assert.Equal(t, sorted, data["event_types"])
		id := uint(data["id"].(float64))

		status, envelope, raw = do("admin", http.MethodGet, fmt.Sprintf("/webhooks/endpoints/%d", id), nil)
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, sorted, envelope.Data.(map[string]interface{})["event_types"])

		status, _, raw = do("admin", http.MethodGet, "/webhooks/deliveries?event_type=deal.deleted", nil)
		assert.Equal(t, http.StatusOK, status, raw)

		status, envelope, raw = do("admin", http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com:" + port + "/archived", "event_types": []string{"deal.archived"}})
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		details, ok := envelope.Error.Details.(map[string]interface{})
		require.True(t, ok, raw)
		assert.Contains(t, details, "event_types", raw)
	})

	t.Run("the eighteen event types, tasks (#516) and companies (#517) included", func(t *testing.T) {
		// The subscription is every type of the catalogue, sent shuffled.
		all := webhook.AllEventTypes()
		require.Len(t, all, 18)
		unsorted := append([]string{}, all...)
		for i, j := 0, len(unsorted)-1; i < j; i, j = i+2, j-2 {
			unsorted[i], unsorted[j] = unsorted[j], unsorted[i]
		}
		require.NotEqual(t, all, unsorted)
		sorted := []interface{}{"company.created", "company.deleted", "company.updated", "customer.created", "customer.deleted",
			"customer.updated", "deal.created", "deal.deleted", "deal.updated", "lead.created", "lead.deleted", "lead.updated",
			"task.created", "task.deleted", "task.updated", "ticket.created", "ticket.deleted", "ticket.updated"}
		status, envelope, raw := do("admin", http.MethodPost, "/webhooks/endpoints", map[string]interface{}{
			"url": "https://example.com:" + port + "/eighteen", "event_types": unsorted})
		require.Equal(t, http.StatusCreated, status, raw)
		data := envelope.Data.(map[string]interface{})
		assert.Equal(t, sorted, data["event_types"])
		id := uint(data["id"].(float64))

		status, envelope, raw = do("admin", http.MethodGet, fmt.Sprintf("/webhooks/endpoints/%d", id), nil)
		require.Equal(t, http.StatusOK, status, raw)
		assert.Equal(t, sorted, envelope.Data.(map[string]interface{})["event_types"])

		for _, eventType := range []string{"task.deleted", "company.created", "company.updated", "company.deleted"} {
			status, _, raw = do("admin", http.MethodGet, "/webhooks/deliveries?event_type="+eventType, nil)
			assert.Equal(t, http.StatusOK, status, eventType+": "+raw)
		}
	})
}
