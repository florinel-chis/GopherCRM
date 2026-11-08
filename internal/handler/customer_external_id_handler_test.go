package handler

// Customer external-id mappings (#212) at the handler: the error mapping of
// POST and DELETE /customers/{id}/external-ids, the lookup mode of GET
// /customers, and the handler's log lines. Each root initialises
// utils.Logger itself: the package has no TestMain or init().

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExternalIDService validates like the real service and then answers
// with the configured results, recording every call.
type fakeExternalIDService struct {
	addErr       error
	removeErr    error
	lookupResult *models.Customer
	lookupErr    error

	addCalls, removeCalls, lookupCalls int

	lastCustomerID, lastMappingID uint

	lastTriple [3]string
}

func (f *fakeExternalIDService) AddExternalID(_ context.Context, customerID uint, source, scope, externalID string) (*models.CustomerExternalID, error) {
	f.addCalls++
	f.lastCustomerID, f.lastTriple = customerID, [3]string{source, scope, externalID}
	if err := service.ValidateCustomerExternalIdentity(source, scope, externalID); err != nil {
		return nil, err
	}
	if f.addErr != nil {
		return nil, f.addErr
	}
	now := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	return &models.CustomerExternalID{ID: 31, CreatedAt: now, UpdatedAt: now, CustomerID: customerID,
		Source: source, Scope: scope, ExternalID: externalID}, nil
}

func (f *fakeExternalIDService) RemoveExternalID(_ context.Context, customerID, mappingID uint) error {
	f.removeCalls++
	f.lastCustomerID, f.lastMappingID = customerID, mappingID
	return f.removeErr
}

func (f *fakeExternalIDService) LookupCustomer(_ context.Context, source, scope, externalID string) (*models.Customer, error) {
	f.lookupCalls++
	f.lastTriple = [3]string{source, scope, externalID}
	if err := service.ValidateCustomerExternalIdentity(source, scope, externalID); err != nil {
		return nil, err
	}
	return f.lookupResult, f.lookupErr
}

// initExternalIDLogger installs a fresh utils.Logger writing to buf (or
// discarding) at level, restored after the test.
func initExternalIDLogger(t *testing.T, level string, buf *bytes.Buffer) {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: level, Format: "json"}))
	if buf != nil {
		utils.Logger.SetOutput(buf)
	}
	t.Cleanup(func() { utils.Logger = previous })
	gin.SetMode(gin.TestMode)
}

// externalIDRouter mounts the routes as production does, with the caller's
// role set by a stand-in for the auth middleware.
func externalIDRouter(h *CustomerHandler, role models.UserRole) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(role))
		c.Next()
	})
	group := router.Group("")
	SetupCustomerRoutes(group, h)
	SetupCustomerExternalIDRoutes(group, h)
	return router
}

type externalIDError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details"`
}

type externalIDResponse struct {
	Success bool             `json:"success"`
	Data    json.RawMessage  `json:"data"`
	Error   *externalIDError `json:"error"`
	Meta    *utils.APIMeta   `json:"meta"`
}

func serveExternalID(t *testing.T, router *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, externalIDResponse) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var parsed externalIDResponse
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed), rec.Body.String())
	}
	return rec, parsed
}

func TestCustomerExternalIDHandlerErrorMapping(t *testing.T) {
	initExternalIDLogger(t, "error", nil)

	t.Run("POST", func(t *testing.T) {
		cases := []struct {
			name      string
			role      models.UserRole
			path      string
			body      string
			addErr    error
			status    int
			code      string
			field     string
			callsSvc  bool
			forbidden string
		}{
			{name: "admin creates", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"shopify","scope":"eu","external_id":"G-1"}`, status: http.StatusCreated, callsSvc: true},
			{name: "sales creates without scope", role: models.RoleSales, path: "/customers/7/external-ids",
				body: `{"source":"shopify","external_id":"G-2"}`, status: http.StatusCreated, callsSvc: true},
			{name: "validation names the field", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":" HiddenSourceValue","external_id":"G-3"}`, status: http.StatusBadRequest,
				code: utils.ErrCodeValidation, field: "source", callsSvc: true, forbidden: "HiddenSourceValue"},
			{name: "missing external_id", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"shopify"}`, status: http.StatusBadRequest, code: utils.ErrCodeValidation, field: "external_id", callsSvc: true},
			{name: "invalid scope", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"shopify","scope":"HiddenScope\n","external_id":"G-4"}`, status: http.StatusBadRequest,
				code: utils.ErrCodeValidation, field: "scope", callsSvc: true, forbidden: "HiddenScope"},
			{name: "body that does not bind", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":5,"external_id":"G-5"}`, status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "malformed body", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":`, status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "invalid id", role: models.RoleAdmin, path: "/customers/abc/external-ids",
				body: `{"source":"s","external_id":"e"}`, status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "zero id", role: models.RoleAdmin, path: "/customers/0/external-ids",
				body: `{"source":"s","external_id":"e"}`, status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "missing customer", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, addErr: fmt.Errorf("customer 7: %w", apperrors.ErrNotFound),
				status: http.StatusNotFound, code: utils.ErrCodeNotFound, callsSvc: true},
			{name: "duplicate", role: models.RoleSales, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, addErr: fmt.Errorf("customer 7 external id: %w", apperrors.ErrCustomerExternalIDTaken),
				status: http.StatusConflict, code: utils.ErrCodeConflict, callsSvc: true},
			{name: "anything else", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, addErr: errors.New("database is gone"),
				status: http.StatusInternalServerError, code: utils.ErrCodeInternal, callsSvc: true},
			{name: "unwired", role: models.RoleAdmin, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, addErr: service.ErrCustomerExternalIDsUnavailable,
				status: http.StatusInternalServerError, code: utils.ErrCodeInternal, callsSvc: true},
			{name: "support refused", role: models.RoleSupport, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, status: http.StatusForbidden, code: utils.ErrCodeForbidden},
			{name: "customer refused", role: models.RoleCustomer, path: "/customers/7/external-ids",
				body: `{"source":"s","external_id":"e"}`, status: http.StatusForbidden, code: utils.ErrCodeForbidden},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeExternalIDService{addErr: tc.addErr}
				router := externalIDRouter(NewCustomerHandler(new(mocks.CustomerService)).WithExternalIDs(fake), tc.role)
				rec, resp := serveExternalID(t, router, http.MethodPost, tc.path, tc.body)
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				assert.Equal(t, tc.callsSvc, fake.addCalls == 1, "service called")
				if tc.status == http.StatusCreated {
					var mapping map[string]interface{}
					require.NoError(t, json.Unmarshal(resp.Data, &mapping))
					assert.EqualValues(t, 31, mapping["id"])
					assert.EqualValues(t, 7, mapping["customer_id"])
					assert.Equal(t, fake.lastTriple[0], mapping["source"])
					assert.Equal(t, fake.lastTriple[1], mapping["scope"])
					assert.Equal(t, fake.lastTriple[2], mapping["external_id"])
					assert.NotContains(t, mapping, "customer")
					assert.Equal(t, uint(7), fake.lastCustomerID)
					return
				}
				require.NotNil(t, resp.Error)
				assert.Equal(t, tc.code, resp.Error.Code)
				if tc.field != "" {
					assert.Equal(t, tc.field, resp.Error.Details["field"])
				}
				if tc.forbidden != "" {
					assert.NotContains(t, rec.Body.String(), tc.forbidden, "the answer never echoes the value")
				}
			})
		}

		t.Run("no service wired", func(t *testing.T) {
			router := externalIDRouter(NewCustomerHandler(new(mocks.CustomerService)), models.RoleAdmin)
			rec, resp := serveExternalID(t, router, http.MethodPost, "/customers/7/external-ids", `{"source":"s","external_id":"e"}`)
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.Equal(t, utils.ErrCodeInternal, resp.Error.Code)
		})
	})

	t.Run("DELETE", func(t *testing.T) {
		cases := []struct {
			name      string
			role      models.UserRole
			path      string
			removeErr error
			status    int
			code      string
			callsSvc  bool
		}{
			{name: "admin deletes", role: models.RoleAdmin, path: "/customers/7/external-ids/31", status: http.StatusNoContent, callsSvc: true},
			{name: "sales deletes", role: models.RoleSales, path: "/customers/7/external-ids/31", status: http.StatusNoContent, callsSvc: true},
			{name: "unknown or another customer's mapping", role: models.RoleAdmin, path: "/customers/7/external-ids/32",
				removeErr: fmt.Errorf("customer 7 mapping 32: %w", apperrors.ErrNotFound), status: http.StatusNotFound,
				code: utils.ErrCodeNotFound, callsSvc: true},
			{name: "invalid id", role: models.RoleAdmin, path: "/customers/x/external-ids/31", status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "invalid mapping id", role: models.RoleAdmin, path: "/customers/7/external-ids/x", status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "zero mapping id", role: models.RoleAdmin, path: "/customers/7/external-ids/0", status: http.StatusBadRequest, code: utils.ErrCodeBadRequest},
			{name: "anything else", role: models.RoleAdmin, path: "/customers/7/external-ids/31", removeErr: errors.New("boom"),
				status: http.StatusInternalServerError, code: utils.ErrCodeInternal, callsSvc: true},
			{name: "support refused", role: models.RoleSupport, path: "/customers/7/external-ids/31", status: http.StatusForbidden, code: utils.ErrCodeForbidden},
			{name: "customer refused", role: models.RoleCustomer, path: "/customers/7/external-ids/31", status: http.StatusForbidden, code: utils.ErrCodeForbidden},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeExternalIDService{removeErr: tc.removeErr}
				router := externalIDRouter(NewCustomerHandler(new(mocks.CustomerService)).WithExternalIDs(fake), tc.role)
				rec, resp := serveExternalID(t, router, http.MethodDelete, tc.path, "")
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				assert.Equal(t, tc.callsSvc, fake.removeCalls == 1, "service called")
				if tc.status == http.StatusNoContent {
					assert.Zero(t, rec.Body.Len(), "204 has no body")
					assert.Equal(t, uint(7), fake.lastCustomerID)
					assert.Equal(t, uint(31), fake.lastMappingID)
					return
				}
				require.NotNil(t, resp.Error)
				assert.Equal(t, tc.code, resp.Error.Code)
			})
		}
	})
}

func TestCustomerListExternalIDLookup(t *testing.T) {
	initExternalIDLogger(t, "error", nil)
	found := &models.Customer{BaseModel: models.BaseModel{ID: 42}, FirstName: "Ana", LastName: "Popescu", Email: "ana@example.com"}

	get := func(t *testing.T, role models.UserRole, fake *fakeExternalIDService, customers *mocks.CustomerService, query url.Values) (*httptest.ResponseRecorder, externalIDResponse) {
		t.Helper()
		h := NewCustomerHandler(customers)
		if fake != nil {
			h = h.WithExternalIDs(fake)
		}
		return serveExternalID(t, externalIDRouter(h, role), http.MethodGet, "/customers?"+query.Encode(), "")
	}

	t.Run("customer role is refused before validation", func(t *testing.T) {
		fake := &fakeExternalIDService{lookupResult: found}
		rec, resp := get(t, models.RoleCustomer, fake, new(mocks.CustomerService), url.Values{"source": {""}})
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, utils.ErrCodeForbidden, resp.Error.Code)
		assert.Zero(t, fake.lookupCalls)
	})

	invalid := []struct {
		name  string
		query url.Values
		field string
	}{
		{"source missing", url.Values{"external_id": {"x"}}, "source"},
		{"source empty", url.Values{"source": {""}, "external_id": {"x"}}, "source"},
		{"only scope", url.Values{"scope": {"eu"}}, "source"},
		{"external_id missing", url.Values{"source": {"s"}}, "external_id"},
		{"external_id empty", url.Values{"source": {"s"}, "external_id": {""}}, "external_id"},
		{"source too long", url.Values{"source": {strings.Repeat("s", 65)}, "external_id": {"x"}}, "source"},
		{"external_id too long", url.Values{"source": {"s"}, "external_id": {strings.Repeat("e", 256)}}, "external_id"},
		{"scope with leading space", url.Values{"source": {"s"}, "scope": {" HiddenScope"}, "external_id": {"x"}}, "scope"},
		{"scope too long", url.Values{"source": {"s"}, "scope": {strings.Repeat("c", 129)}, "external_id": {"x"}}, "scope"},
		{"external_id with control", url.Values{"source": {"s"}, "external_id": {"Hidden\x01Value"}}, "external_id"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport} {
				fake := &fakeExternalIDService{lookupResult: found}
				rec, resp := get(t, role, fake, new(mocks.CustomerService), tc.query)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.NotNil(t, resp.Error)
				assert.Equal(t, utils.ErrCodeValidation, resp.Error.Code)
				assert.Equal(t, tc.field, resp.Error.Details["field"])
				assert.Zero(t, fake.lookupCalls, "no service call")
				assert.NotContains(t, rec.Body.String(), "Hidden", "the answer never echoes the value")
			}
		})
	}

	t.Run("found, other list parameters ignored", func(t *testing.T) {
		for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport} {
			fake := &fakeExternalIDService{lookupResult: found}
			// The plain list methods are not expected: a call would panic the mock.
			customers := new(mocks.CustomerService)
			rec, resp := get(t, role, fake, customers, url.Values{
				"source": {"Shop-A"}, "external_id": {"X1"}, "search": {"ana"}, "sort_by": {"email"},
				"page": {"3"}, "offset": {"40"},
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fake.lookupCalls)
			assert.Equal(t, [3]string{"Shop-A", "", "X1"}, fake.lastTriple, "an absent scope means ''")
			var data struct {
				Customers []models.Customer `json:"customers"`
				Total     int64             `json:"total"`
			}
			require.NoError(t, json.Unmarshal(resp.Data, &data))
			assert.EqualValues(t, 1, data.Total)
			require.Len(t, data.Customers, 1)
			assert.Equal(t, uint(42), data.Customers[0].ID)
			require.NotNil(t, resp.Meta)
			assert.Equal(t, 1, resp.Meta.Page)
			assert.EqualValues(t, 1, resp.Meta.Total)
			customers.AssertExpectations(t)
		}
	})

	t.Run("scope passed through", func(t *testing.T) {
		fake := &fakeExternalIDService{lookupResult: found}
		rec, _ := get(t, models.RoleAdmin, fake, new(mocks.CustomerService), url.Values{"source": {"s"}, "scope": {"Site"}, "external_id": {"x"}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, [3]string{"s", "Site", "x"}, fake.lastTriple)
	})

	t.Run("not found is an empty list", func(t *testing.T) {
		fake := &fakeExternalIDService{}
		rec, resp := get(t, models.RoleSupport, fake, new(mocks.CustomerService), url.Values{"source": {"s"}, "external_id": {"nobody"}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"customers":[],"total":0}`, string(resp.Data))
		require.NotNil(t, resp.Meta)
		assert.Equal(t, 1, resp.Meta.Page)
	})

	t.Run("service failure is a 500", func(t *testing.T) {
		fake := &fakeExternalIDService{lookupErr: errors.New("database is gone")}
		rec, resp := get(t, models.RoleAdmin, fake, new(mocks.CustomerService), url.Values{"source": {"s"}, "external_id": {"x"}})
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, utils.ErrCodeInternal, resp.Error.Code)
	})

	t.Run("unwired is a 500", func(t *testing.T) {
		rec, resp := get(t, models.RoleAdmin, nil, new(mocks.CustomerService), url.Values{"source": {"s"}, "external_id": {"x"}})
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, utils.ErrCodeInternal, resp.Error.Code)
	})

	t.Run("without lookup parameters the list is unchanged", func(t *testing.T) {
		fake := &fakeExternalIDService{}
		customers := new(mocks.CustomerService)
		customers.On("List", 0, 20).Return([]models.Customer{*found}, int64(1), nil).Once()
		rec, _ := get(t, models.RoleAdmin, fake, customers, url.Values{})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Zero(t, fake.lookupCalls)
		customers.AssertExpectations(t)
	})
}

func TestCustomerExternalIDHandlerLogsOmitValues(t *testing.T) {
	var buf bytes.Buffer
	initExternalIDLogger(t, "debug", &buf)

	const (
		source     = "LogCanarySourceA1"
		scope      = "LogCanaryScopeB2"
		externalID = "LogCanaryExternalC3"
	)
	found := &models.Customer{BaseModel: models.BaseModel{ID: 42}, FirstName: "Ana", LastName: "Popescu", Email: "ana@example.com"}
	body := func(s, sc, e string) string {
		raw, err := json.Marshal(map[string]string{"source": s, "scope": sc, "external_id": e})
		require.NoError(t, err)
		return string(raw)
	}

	run := func(fake *fakeExternalIDService, role models.UserRole, method, path, payload string) int {
		router := externalIDRouter(NewCustomerHandler(new(mocks.CustomerService)).WithExternalIDs(fake), role)
		rec, _ := serveExternalID(t, router, method, path, payload)
		return rec.Code
	}
	lookup := url.Values{"source": {source}, "scope": {scope}, "external_id": {externalID}}.Encode()

	assert.Equal(t, http.StatusCreated, run(&fakeExternalIDService{}, models.RoleAdmin, http.MethodPost, "/customers/7/external-ids", body(source, scope, externalID)))
	assert.Equal(t, http.StatusConflict, run(&fakeExternalIDService{addErr: apperrors.ErrCustomerExternalIDTaken}, models.RoleSales, http.MethodPost, "/customers/7/external-ids", body(source, scope, externalID)))
	assert.Equal(t, http.StatusNotFound, run(&fakeExternalIDService{addErr: apperrors.ErrNotFound}, models.RoleAdmin, http.MethodPost, "/customers/7/external-ids", body(source, scope, externalID)))
	assert.Equal(t, http.StatusBadRequest, run(&fakeExternalIDService{}, models.RoleAdmin, http.MethodPost, "/customers/7/external-ids", body(source+" ", scope, externalID)))
	assert.Equal(t, http.StatusBadRequest, run(&fakeExternalIDService{}, models.RoleAdmin, http.MethodPost, "/customers/7/external-ids", body(source, scope, externalID+"\t")))
	assert.Equal(t, http.StatusInternalServerError, run(&fakeExternalIDService{addErr: fmt.Errorf("insert of '%s' failed", externalID)}, models.RoleAdmin, http.MethodPost, "/customers/7/external-ids", body(source, scope, externalID)))
	assert.Equal(t, http.StatusNoContent, run(&fakeExternalIDService{}, models.RoleAdmin, http.MethodDelete, "/customers/7/external-ids/31", ""))
	assert.Equal(t, http.StatusNotFound, run(&fakeExternalIDService{removeErr: apperrors.ErrNotFound}, models.RoleAdmin, http.MethodDelete, "/customers/7/external-ids/31", ""))
	assert.Equal(t, http.StatusOK, run(&fakeExternalIDService{lookupResult: found}, models.RoleSupport, http.MethodGet, "/customers?"+lookup, ""))
	assert.Equal(t, http.StatusOK, run(&fakeExternalIDService{}, models.RoleSupport, http.MethodGet, "/customers?"+lookup, ""))
	assert.Equal(t, http.StatusBadRequest, run(&fakeExternalIDService{}, models.RoleAdmin, http.MethodGet,
		"/customers?"+url.Values{"source": {source}, "scope": {" " + scope}, "external_id": {externalID}}.Encode(), ""))
	assert.Equal(t, http.StatusInternalServerError, run(&fakeExternalIDService{lookupErr: fmt.Errorf("lookup of '%s' failed", externalID)}, models.RoleAdmin, http.MethodGet, "/customers?"+lookup, ""))

	output := buf.String()
	require.NotEmpty(t, output, "the handler paths wrote log lines")
	assert.Contains(t, output, "CustomerHandler.CreateExternalID")
	assert.Contains(t, output, "CustomerHandler.DeleteExternalID")
	assert.Contains(t, output, "CustomerHandler.List")
	for _, needle := range []string{source, scope, externalID, "LogCanary"} {
		assert.NotContains(t, output, needle, "no log line carries a source, scope or external_id value")
	}
}
