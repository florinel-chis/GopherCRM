package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type formDeliveryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type formDeliveryEnvelope struct {
	Success bool               `json:"success"`
	Data    json.RawMessage    `json:"data"`
	Error   *formDeliveryError `json:"error"`
}

func newFormDeliveryRouter(t *testing.T, fake *fakeFormService, role models.UserRole) *gin.Engine {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(11))
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupFormRoutes(router.Group(""), NewFormHandler(fake))
	return router
}

func serveFormDelivery(t *testing.T, router *gin.Engine, method, path string) (*httptest.ResponseRecorder, formDeliveryEnvelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var envelope formDeliveryEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return rec, envelope
}

// AC-12: the handler classifies the service's errors with errors.Is, through
// any wrapping, and never echoes an unclassified cause.
func TestFormDeliveryHandlerErrorMapping(t *testing.T) {
	const cause = "secret-cause-canary relay@example.com"
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not retryable", fmt.Errorf("delivery 5: status sent: %w", apperrors.ErrFormDeliveryNotRetryable), http.StatusConflict, "FORM_DELIVERY_NOT_RETRYABLE"},
		{"service alias", service.ErrFormDeliveryNotRetryable, http.StatusConflict, "FORM_DELIVERY_NOT_RETRYABLE"},
		{"mail delivery", fmt.Errorf("delivery 5 failed: %w", apperrors.ErrMailDelivery), http.StatusBadGateway, utils.ErrCodeMailDeliveryFailed},
		{"not found", fmt.Errorf("form mail delivery 5 not found: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"validation", fmt.Errorf("bad: %w", apperrors.ErrValidation), http.StatusBadRequest, utils.ErrCodeValidation},
		{"anything else", errors.New(cause), http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeFormService{}
			fake.retryDeliveryFn = func(uint) (*models.FormMailDelivery, error) { return nil, c.err }
			fake.listDeliveriesFn = func(service.FormMailDeliveryFilter, int, int) ([]models.FormMailDelivery, int64, error) {
				return nil, 0, c.err
			}
			router := newFormDeliveryRouter(t, fake, models.RoleAdmin)

			rec, envelope := serveFormDelivery(t, router, http.MethodPost, "/forms/deliveries/5/retry")
			assert.Equal(t, c.status, rec.Code)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, c.code, envelope.Error.Code)
			assert.NotContains(t, rec.Body.String(), "secret-cause-canary")
			assert.NotContains(t, rec.Body.String(), "relay@example.com")
			assert.Equal(t, uint(5), fake.retriedDeliveryID)

			rec, envelope = serveFormDelivery(t, router, http.MethodGet, "/forms/deliveries")
			assert.Equal(t, c.status, rec.Code)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, c.code, envelope.Error.Code)
			assert.NotContains(t, rec.Body.String(), "secret-cause-canary")
		})
	}

	t.Run("the list filters are validated and passed on", func(t *testing.T) {
		fake := &fakeFormService{}
		router := newFormDeliveryRouter(t, fake, models.RoleSupport)
		for _, query := range []string{"form_id=abc", "form_id=0", "form_id=-3"} {
			rec, envelope := serveFormDelivery(t, router, http.MethodGet, "/forms/deliveries?"+query)
			assert.Equal(t, http.StatusBadRequest, rec.Code, query)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		}

		rec, envelope := serveFormDelivery(t, router, http.MethodGet, "/forms/deliveries?form_id=7&status=failed&kind=notification&limit=5")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, envelope.Success)
		assert.JSONEq(t, `[]`, string(envelope.Data))
		assert.Equal(t, service.FormMailDeliveryFilter{FormID: 7, Status: "failed", Kind: "notification"}, fake.listedDeliveryFilter)
	})

	t.Run("an invalid delivery id is a bad request", func(t *testing.T) {
		fake := &fakeFormService{}
		router := newFormDeliveryRouter(t, fake, models.RoleAdmin)
		for _, id := range []string{"abc", "0", "-1"} {
			rec, envelope := serveFormDelivery(t, router, http.MethodPost, "/forms/deliveries/"+id+"/retry")
			assert.Equal(t, http.StatusBadRequest, rec.Code, id)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
		}
		assert.Zero(t, fake.retriedDeliveryID, "the service was not called")
	})

	t.Run("a successful retry answers the updated delivery", func(t *testing.T) {
		fake := &fakeFormService{}
		router := newFormDeliveryRouter(t, fake, models.RoleSales)
		rec, envelope := serveFormDelivery(t, router, http.MethodPost, "/forms/deliveries/9/retry")
		assert.Equal(t, http.StatusOK, rec.Code)
		var delivery map[string]interface{}
		require.NoError(t, json.Unmarshal(envelope.Data, &delivery))
		assert.EqualValues(t, 9, delivery["id"])
		assert.Equal(t, models.FormMailStatusSent, delivery["status"])
	})
}

// AC-12: the delivery routes are registered ahead of /forms/:id, support may
// read but not retry, and the customer role reaches neither.
func TestFormDeliveryRoutesRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("static delivery routes win over the id parameter", func(t *testing.T) {
		router := gin.New()
		var matched string
		group := router.Group("", func(c *gin.Context) {
			matched = c.FullPath()
			c.Abort()
		})
		SetupFormRoutes(group, &FormHandler{})
		for _, route := range []struct{ method, path, want string }{
			{http.MethodGet, "/forms/deliveries", "/forms/deliveries"},
			{http.MethodPost, "/forms/deliveries/3/retry", "/forms/deliveries/:id/retry"},
			{http.MethodGet, "/forms/3", "/forms/:id"},
			{http.MethodGet, "/forms/submissions/3", "/forms/submissions/:id"},
		} {
			matched = ""
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(route.method, route.path, nil))
			assert.Equal(t, route.want, matched, "%s %s", route.method, route.path)
		}

		// Registration order (gin's Routes() reports tree order, so the source
		// is read): the delivery routes come before /forms/:id, and the retry
		// carries the admin-and-sales write guard.
		source, err := os.ReadFile("form_routes.go")
		require.NoError(t, err)
		text := string(source)
		list := strings.Index(text, `group.GET("/deliveries", h.ListDeliveries)`)
		retry := strings.Index(text, `group.POST("/deliveries/:id/retry", write, h.RetryDelivery)`)
		detail := strings.Index(text, `group.GET("/:id", h.Get)`)
		require.NotEqual(t, -1, list)
		require.NotEqual(t, -1, retry, "the retry route is behind the write guard")
		require.NotEqual(t, -1, detail)
		assert.Less(t, list, detail)
		assert.Less(t, retry, detail)
		assert.Contains(t, text, "write := middleware.RequireRole(models.RoleAdmin, models.RoleSales)")
	})

	for _, c := range []struct {
		role       models.UserRole
		listStatus int
		retryCode  int
	}{
		{models.RoleAdmin, http.StatusOK, http.StatusOK},
		{models.RoleSales, http.StatusOK, http.StatusOK},
		{models.RoleSupport, http.StatusOK, http.StatusForbidden},
		{models.RoleCustomer, http.StatusForbidden, http.StatusForbidden},
	} {
		t.Run(string(c.role), func(t *testing.T) {
			fake := &fakeFormService{}
			router := newFormDeliveryRouter(t, fake, c.role)

			rec, _ := serveFormDelivery(t, router, http.MethodGet, "/forms/deliveries")
			assert.Equal(t, c.listStatus, rec.Code, "list")

			rec, _ = serveFormDelivery(t, router, http.MethodPost, "/forms/deliveries/4/retry")
			assert.Equal(t, c.retryCode, rec.Code, "retry")
			if c.retryCode == http.StatusForbidden {
				assert.Zero(t, fake.retriedDeliveryID, "the guard answers before the service is reached")
			} else {
				assert.Equal(t, uint(4), fake.retriedDeliveryID)
			}
		})
	}
}
