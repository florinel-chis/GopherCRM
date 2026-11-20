package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #345: the /webhooks routes on the real Auth and RequireRole middleware with
// a fake AuthService (Bearer and API key) and a recording WebhookService.

type fakeWebhookService struct {
	mu       sync.Mutex
	calls    []string
	created  service.WebhookEndpointInput
	patch    service.WebhookEndpointPatch
	filter   repository.WebhookDeliveryFilter
	sortBy   string
	notFound bool
	// pingFails makes TestEndpoint answer a failed outcome.
	pingFails bool
	// redeliverErr is what RedeliverDelivery returns.
	redeliverErr error
}

func (f *fakeWebhookService) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeWebhookService) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeWebhookService) missing(id uint) error {
	if f.notFound {
		return fmt.Errorf("webhook endpoint %d not found: %w", id, apperrors.ErrNotFound)
	}
	return nil
}

func fakeWebhookEndpoint(id uint) *models.WebhookEndpoint {
	now := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	return &models.WebhookEndpoint{ID: id, CreatedAt: now, UpdatedAt: now, URL: "https://hooks.example.com/x",
		EventTypes: models.WebhookEventTypes{models.WebhookEventLeadCreated}, SecretSealed: "enc:v1:sealed-canary", IsActive: true}
}

func (f *fakeWebhookService) CreateEndpoint(_ context.Context, input service.WebhookEndpointInput) (*models.WebhookEndpointCreated, error) {
	f.record("create")
	f.created = input
	return &models.WebhookEndpointCreated{WebhookEndpoint: *fakeWebhookEndpoint(1), Secret: "whsec_c2VjcmV0"}, nil
}

func (f *fakeWebhookService) GetEndpoint(_ context.Context, id uint) (*models.WebhookEndpoint, error) {
	f.record("get")
	if err := f.missing(id); err != nil {
		return nil, err
	}
	return fakeWebhookEndpoint(id), nil
}

func (f *fakeWebhookService) ListEndpoints(_ context.Context, _, _ int, sortBy, _ string) ([]models.WebhookEndpoint, int64, error) {
	f.record("list")
	f.sortBy = sortBy
	return []models.WebhookEndpoint{*fakeWebhookEndpoint(1)}, 1, nil
}

func (f *fakeWebhookService) UpdateEndpoint(_ context.Context, id uint, patch service.WebhookEndpointPatch) (*models.WebhookEndpoint, error) {
	f.record("update")
	f.patch = patch
	if err := f.missing(id); err != nil {
		return nil, err
	}
	return fakeWebhookEndpoint(id), nil
}

func (f *fakeWebhookService) DeleteEndpoint(_ context.Context, id uint) error {
	f.record("delete")
	return f.missing(id)
}

func (f *fakeWebhookService) ListDeliveries(_ context.Context, filter repository.WebhookDeliveryFilter, _, _ int) ([]models.WebhookDelivery, int64, error) {
	f.record("deliveries")
	f.filter = filter
	return []models.WebhookDelivery{}, 0, nil
}

func (f *fakeWebhookService) TestEndpoint(_ context.Context, id uint) (*models.WebhookPingResult, error) {
	f.record("test")
	if err := f.missing(id); err != nil {
		return nil, err
	}
	if f.pingFails {
		status := http.StatusInternalServerError
		code := models.WebhookErrorHTTPStatus
		return &models.WebhookPingResult{EventID: "evt_0123456789abcdef0123456789abcdef", Outcome: models.WebhookPingFailed, HTTPStatus: &status, ErrorCode: &code}, nil
	}
	status := http.StatusOK
	return &models.WebhookPingResult{EventID: "evt_0123456789abcdef0123456789abcdef", Outcome: models.WebhookPingSucceeded, HTTPStatus: &status}, nil
}

func (f *fakeWebhookService) RedeliverDelivery(_ context.Context, id uint) (*models.WebhookDelivery, error) {
	f.record("redeliver")
	if f.redeliverErr != nil {
		return nil, f.redeliverErr
	}
	now := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	return &models.WebhookDelivery{ID: id, EndpointID: 1, EventID: "evt_0123456789abcdef0123456789abcdef",
		EventType: models.WebhookEventLeadCreated, RecordID: 7, Status: models.WebhookDeliveryPending, NextAttemptAt: &now,
		CreatedAt: now, UpdatedAt: now}, nil
}

// webhookAuth authenticates the token or key "<role>-credential" as a user of
// that role; anything else is refused.
type webhookAuth struct {
	service.AuthService
}

func (webhookAuth) userFor(credential string) (*models.User, error) {
	role, ok := strings.CutSuffix(credential, "-credential")
	if !ok {
		return nil, errors.New("invalid credential")
	}
	return &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.UserRole(role), IsActive: true}, nil
}

func (a webhookAuth) ValidateToken(token string) (*models.User, error) { return a.userFor(token) }

func (a webhookAuth) ValidateAPIKey(key string) (*models.User, error) { return a.userFor(key) }

func newWebhookRouter(t *testing.T, fake *fakeWebhookService) *gin.Engine {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(webhookAuth{}))
	SetupWebhookRoutes(protected, NewWebhookHandler(fake))
	return router
}

type webhookEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func serveWebhook(t *testing.T, router *gin.Engine, method, path, authorization, body string) (*httptest.ResponseRecorder, webhookEnvelope) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var envelope webhookEnvelope
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	}
	return rec, envelope
}

type webhookRoute struct {
	method, path, body string
	adminStatus        int
}

var webhookRoutes = []webhookRoute{
	{http.MethodGet, "/webhooks/endpoints", "", http.StatusOK},
	{http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x","event_types":["lead.created"]}`, http.StatusCreated},
	{http.MethodGet, "/webhooks/endpoints/1", "", http.StatusOK},
	{http.MethodPut, "/webhooks/endpoints/1", `{"is_active":false}`, http.StatusOK},
	{http.MethodDelete, "/webhooks/endpoints/1", "", http.StatusNoContent},
	{http.MethodGet, "/webhooks/deliveries", "", http.StatusOK},
	{http.MethodPost, "/webhooks/endpoints/1/test", "", http.StatusOK},
	{http.MethodPost, "/webhooks/deliveries/1/redeliver", "", http.StatusOK},
}

func TestWebhookRoutesAdminOnly(t *testing.T) {
	for _, route := range webhookRoutes {
		route := route
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			fake := &fakeWebhookService{}
			router := newWebhookRouter(t, fake)

			rec, envelope := serveWebhook(t, router, route.method, route.path, "", route.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "no credentials")
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeUnauthorized, envelope.Error.Code)
			rec, _ = serveWebhook(t, router, route.method, route.path, "Bearer nobody", route.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "an invalid token")

			for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleCustomer} {
				for _, scheme := range []string{"Bearer", "ApiKey"} {
					rec, envelope := serveWebhook(t, router, route.method, route.path, scheme+" "+string(role)+"-credential", route.body)
					assert.Equal(t, http.StatusForbidden, rec.Code, "%s via %s", role, scheme)
					require.NotNil(t, envelope.Error)
					assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
				}
			}
			assert.Zero(t, fake.callCount(), "the guard answers before the handler runs")

			for _, scheme := range []string{"Bearer", "ApiKey"} {
				rec, _ := serveWebhook(t, router, route.method, route.path, scheme+" admin-credential", route.body)
				assert.Equal(t, route.adminStatus, rec.Code, "admin via %s: %s", scheme, rec.Body.String())
			}
			assert.Equal(t, 2, fake.callCount(), "admins reach the service")
		})
	}

	t.Run("the secret appears only in the create answer", func(t *testing.T) {
		fake := &fakeWebhookService{}
		router := newWebhookRouter(t, fake)
		for _, route := range webhookRoutes {
			rec, _ := serveWebhook(t, router, route.method, route.path, "Bearer admin-credential", route.body)
			assert.NotContains(t, rec.Body.String(), "sealed-canary", "%s %s", route.method, route.path)
			assert.NotContains(t, rec.Body.String(), "secret_sealed")
			if route.method == http.MethodPost && route.path == "/webhooks/endpoints" {
				assert.Contains(t, rec.Body.String(), `"secret":"whsec_c2VjcmV0"`)
			} else {
				assert.NotContains(t, rec.Body.String(), "whsec_", "%s %s", route.method, route.path)
			}
		}
	})
}

func TestWebhookHandlerValidation(t *testing.T) {
	const admin = "Bearer admin-credential"
	const canary = "canary-value-7f3a"
	long := "https://hooks.example.com/" + strings.Repeat("a", 2048)

	cases := []struct {
		name, method, path, body string
		field                    string
	}{
		{"non-https URL", http.MethodPost, "/webhooks/endpoints", `{"url":"http://` + canary + `.example.com/x","event_types":["lead.created"]}`, "url"},
		{"URL with a query", http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x?token=` + canary + `","event_types":["lead.created"]}`, "url"},
		{"URL with userinfo", http.MethodPost, "/webhooks/endpoints", `{"url":"https://` + canary + `:pw@hooks.example.com/x","event_types":["lead.created"]}`, "url"},
		{"URL over 2048 characters", http.MethodPost, "/webhooks/endpoints", `{"url":"` + long + `","event_types":["lead.created"]}`, "url"},
		{"private IP literal", http.MethodPost, "/webhooks/endpoints", `{"url":"https://10.0.0.5/` + canary + `","event_types":["lead.created"]}`, "url"},
		{"loopback IP literal", http.MethodPost, "/webhooks/endpoints", `{"url":"https://127.0.0.1/` + canary + `","event_types":["lead.created"]}`, "url"},
		{"metadata IP literal", http.MethodPost, "/webhooks/endpoints", `{"url":"https://169.254.169.254/` + canary + `","event_types":["lead.created"]}`, "url"},
		{"missing URL", http.MethodPost, "/webhooks/endpoints", `{"event_types":["lead.created"]}`, "url"},
		{"empty event types", http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x","event_types":[]}`, "event_types"},
		{"missing event types", http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x"}`, "event_types"},
		{"unknown event type", http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x","event_types":["lead.` + canary + `"]}`, "event_types"},
		{"empty create body", http.MethodPost, "/webhooks/endpoints", "", "body"},
		{"empty PUT body", http.MethodPut, "/webhooks/endpoints/1", "", "body"},
		{"PUT body without fields", http.MethodPut, "/webhooks/endpoints/1", `{}`, "body"},
		{"PUT non-https URL", http.MethodPut, "/webhooks/endpoints/1", `{"url":"http://` + canary + `.example.com/x"}`, "url"},
		{"PUT unknown event type", http.MethodPut, "/webhooks/endpoints/1", `{"event_types":["` + canary + `"]}`, "event_types"},
		{"PUT empty event types", http.MethodPut, "/webhooks/endpoints/1", `{"event_types":[]}`, "event_types"},
		{"bad id on GET", http.MethodGet, "/webhooks/endpoints/" + canary, "", "id"},
		{"zero id on GET", http.MethodGet, "/webhooks/endpoints/0", "", "id"},
		{"negative id on PUT", http.MethodPut, "/webhooks/endpoints/-3", `{"is_active":true}`, "id"},
		{"bad id on DELETE", http.MethodDelete, "/webhooks/endpoints/" + canary, "", "id"},
		{"bad id on test ping", http.MethodPost, "/webhooks/endpoints/abc/test", "", "id"},
		{"zero id on test ping", http.MethodPost, "/webhooks/endpoints/0/test", "", "id"},
		{"bad id on redeliver", http.MethodPost, "/webhooks/deliveries/abc/redeliver", "", "id"},
		{"zero id on redeliver", http.MethodPost, "/webhooks/deliveries/0/redeliver", "", "id"},
		{"subscribing to webhook.ping", http.MethodPost, "/webhooks/endpoints", `{"url":"https://hooks.example.com/x","event_types":["webhook.ping"]}`, "event_types"},
		{"bad sort_by", http.MethodGet, "/webhooks/endpoints?sort_by=url" + canary, "", "sort_by"},
		{"bad status filter", http.MethodGet, "/webhooks/deliveries?status=" + canary, "", "status"},
		{"bad event_type filter", http.MethodGet, "/webhooks/deliveries?event_type=" + canary, "", "event_type"},
		{"bad endpoint_id filter", http.MethodGet, "/webhooks/deliveries?endpoint_id=" + canary, "", "endpoint_id"},
		{"zero endpoint_id filter", http.MethodGet, "/webhooks/deliveries?endpoint_id=0", "", "endpoint_id"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeWebhookService{}
			router := newWebhookRouter(t, fake)
			rec, envelope := serveWebhook(t, router, c.method, c.path, admin, c.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
			assert.Contains(t, envelope.Error.Details, c.field, "error.details is keyed by %s", c.field)
			assert.NotContains(t, rec.Body.String(), canary, "the value is not echoed")
			assert.NotContains(t, rec.Body.String(), strings.Repeat("a", 100))
			assert.Zero(t, fake.callCount(), "the service is not called")
		})
	}

	t.Run("malformed JSON is a bad request", func(t *testing.T) {
		fake := &fakeWebhookService{}
		router := newWebhookRouter(t, fake)
		rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints", admin, `{"url":`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
		assert.Zero(t, fake.callCount())
	})

	t.Run("an unknown id is 404", func(t *testing.T) {
		fake := &fakeWebhookService{notFound: true}
		router := newWebhookRouter(t, fake)
		for _, route := range []struct{ method, body string }{
			{http.MethodGet, ""}, {http.MethodPut, `{"is_active":true}`}, {http.MethodDelete, ""},
		} {
			rec, envelope := serveWebhook(t, router, route.method, "/webhooks/endpoints/999", admin, route.body)
			assert.Equal(t, http.StatusNotFound, rec.Code, route.method)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
		}
		rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints/999/test", admin, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "test ping")
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	})

	t.Run("the test ping answers its outcome with nullable fields present", func(t *testing.T) {
		fake := &fakeWebhookService{}
		router := newWebhookRouter(t, fake)
		rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints/4/test", admin, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope.Data, &data))
		assert.JSONEq(t, `"evt_0123456789abcdef0123456789abcdef"`, string(data["event_id"]))
		assert.JSONEq(t, `"succeeded"`, string(data["outcome"]))
		assert.JSONEq(t, `200`, string(data["http_status"]))
		require.Contains(t, data, "error_code", "error_code is present on success")
		assert.Equal(t, "null", string(data["error_code"]))
		assert.Len(t, data, 4)

		fake = &fakeWebhookService{pingFails: true}
		router = newWebhookRouter(t, fake)
		rec, envelope = serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints/4/test", admin, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		data = nil
		require.NoError(t, json.Unmarshal(envelope.Data, &data))
		assert.JSONEq(t, `"failed"`, string(data["outcome"]))
		assert.JSONEq(t, `500`, string(data["http_status"]))
		assert.JSONEq(t, `"http_status"`, string(data["error_code"]))
	})

	t.Run("an unwired test ping is 500 without the cause", func(t *testing.T) {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
		gin.SetMode(gin.TestMode)
		router := gin.New()
		protected := router.Group("/api/v1")
		protected.Use(middleware.Auth(webhookAuth{}))
		SetupWebhookRoutes(protected, NewWebhookHandler(service.NewWebhookService(nil, nil, nil)))
		rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints/1/test", admin, "")
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeInternal, envelope.Error.Code)
		assert.NotContains(t, rec.Body.String(), "not configured")
	})

	t.Run("valid input reaches the service as sent", func(t *testing.T) {
		fake := &fakeWebhookService{}
		router := newWebhookRouter(t, fake)
		rec, _ := serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints", admin,
			`{"url":" https://hooks.example.com/x ","event_types":["ticket.created","lead.created","lead.created"],"is_active":false}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "https://hooks.example.com/x", fake.created.URL)
		require.NotNil(t, fake.created.IsActive)
		assert.False(t, *fake.created.IsActive, "an explicit false is passed on")

		rec, _ = serveWebhook(t, router, http.MethodPost, "/webhooks/endpoints", admin, `{"url":"https://93.184.216.34/x","event_types":["customer.created"]}`)
		require.Equal(t, http.StatusCreated, rec.Code, "a public IP literal is accepted")
		assert.Nil(t, fake.created.IsActive, "an omitted is_active stays absent; the service defaults it")

		rec, _ = serveWebhook(t, router, http.MethodPut, "/webhooks/endpoints/4", admin, `{"is_active":false}`)
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, fake.patch.IsActive)
		assert.False(t, *fake.patch.IsActive)
		assert.Nil(t, fake.patch.URL)
		assert.Nil(t, fake.patch.EventTypes)

		rec, _ = serveWebhook(t, router, http.MethodGet, "/webhooks/endpoints?sort_by=created_at&sort_order=asc", admin, "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "created_at", fake.sortBy)

		rec, _ = serveWebhook(t, router, http.MethodGet, "/webhooks/deliveries?endpoint_id=4&status=failed&event_type=ticket.created", admin, "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, repository.WebhookDeliveryFilter{EndpointID: 4, Status: "failed", EventType: "ticket.created"}, fake.filter)
		rec, _ = serveWebhook(t, router, http.MethodGet, "/webhooks/deliveries?endpoint_id=&status=&event_type=", admin, "")
		require.Equal(t, http.StatusOK, rec.Code, "an empty value is absent")
		assert.Equal(t, repository.WebhookDeliveryFilter{}, fake.filter)
	})

	t.Run("an unexpected service error is 500 without the cause", func(t *testing.T) {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
		gin.SetMode(gin.TestMode)
		router := gin.New()
		protected := router.Group("/api/v1")
		protected.Use(middleware.Auth(webhookAuth{}))
		SetupWebhookRoutes(protected, NewWebhookHandler(service.NewWebhookService(nil, nil, nil)))
		rec, envelope := serveWebhook(t, router, http.MethodGet, "/webhooks/endpoints", admin, "")
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeInternal, envelope.Error.Code)
		assert.NotContains(t, rec.Body.String(), "not configured")
	})
}

func TestWebhookRedeliverHandler(t *testing.T) {
	const admin = "Bearer admin-credential"

	t.Run("a requeued delivery is 200 with the row", func(t *testing.T) {
		fake := &fakeWebhookService{}
		router := newWebhookRouter(t, fake)
		rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/deliveries/12/redeliver", admin, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope.Data, &data))
		assert.JSONEq(t, `12`, string(data["id"]))
		assert.JSONEq(t, `"pending"`, string(data["status"]))
		assert.JSONEq(t, `"evt_0123456789abcdef0123456789abcdef"`, string(data["event_id"]))
	})

	cases := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"an unknown delivery is 404", fmt.Errorf("webhook delivery 9 not found: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound, "Webhook delivery not found"},
		{"a pending or sending delivery is 409", service.ErrWebhookDeliveryNotRedeliverable, http.StatusConflict, utils.ErrCodeConflict, "Webhook delivery is pending or being sent"},
		{"a deleted or inactive endpoint is 409", service.ErrWebhookEndpointUnavailable, http.StatusConflict, utils.ErrCodeConflict, "Webhook endpoint is deleted or inactive"},
		{"an unexpected error is 500 without the cause", errors.New("driver canary-cause"), http.StatusInternalServerError, utils.ErrCodeInternal, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
			fake := &fakeWebhookService{redeliverErr: c.err}
			router := newWebhookRouter(t, fake)
			rec, envelope := serveWebhook(t, router, http.MethodPost, "/webhooks/deliveries/9/redeliver", admin, "")
			assert.Equal(t, c.status, rec.Code, rec.Body.String())
			require.NotNil(t, envelope.Error)
			assert.Equal(t, c.code, envelope.Error.Code)
			if c.message != "" {
				assert.Equal(t, c.message, envelope.Error.Message)
			}
			assert.NotContains(t, rec.Body.String(), "canary-cause")
			assert.Equal(t, 1, fake.callCount())
		})
	}
}
