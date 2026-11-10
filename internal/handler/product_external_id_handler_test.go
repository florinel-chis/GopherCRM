package handler

// Product external-id mappings (#239): the error mapping of the two mapping
// routes, the lookup mode of GET /products and the role guards of
// SetupProductRoutes.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func newProductExternalIDTestRouter(svc *mocks.ProductService, ext *mocks.ProductExternalIDService, role models.UserRole) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(role))
		c.Next()
	})
	h := NewProductHandler(svc)
	if ext != nil {
		h.WithExternalIDs(ext)
	}
	SetupProductRoutes(router.Group(""), h)
	return router
}

func errorDetails(t *testing.T, envelope utils.APIResponse) map[string]interface{} {
	t.Helper()
	require.NotNil(t, envelope.Error)
	details, ok := envelope.Error.Details.(map[string]interface{})
	require.True(t, ok, "details is an object: %T", envelope.Error.Details)
	return details
}

func TestProductExternalIDHandlerErrorMapping(t *testing.T) {
	created := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

	t.Run("create", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			path     string
			body     interface{}
			err      error
			wantCode string
			want     int
		}{
			{"success", "/products/7/external-ids", gin.H{"source": "erp", "scope": "plant", "external_id": "M-1"}, nil, "", http.StatusCreated},
			{"field error", "/products/7/external-ids", gin.H{"source": "SecretValue ", "external_id": "M-1"},
				&service.CustomerExternalIDFieldError{Field: "source", Reason: "must not start or end with whitespace"}, utils.ErrCodeValidation, http.StatusBadRequest},
			{"missing product", "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"},
				fmt.Errorf("product 7: %w", apperrors.ErrNotFound), utils.ErrCodeNotFound, http.StatusNotFound},
			{"taken", "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"},
				fmt.Errorf("product 7 external id: %w", apperrors.ErrProductExternalIDTaken), "CONFLICT", http.StatusConflict},
			{"other failure", "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"},
				errors.New("database is gone"), "INTERNAL_ERROR", http.StatusInternalServerError},
			{"unwired", "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"},
				service.ErrProductExternalIDsUnavailable, "INTERNAL_ERROR", http.StatusInternalServerError},
			{"bad id", "/products/abc/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, nil, utils.ErrCodeBadRequest, http.StatusBadRequest},
			{"zero id", "/products/0/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, nil, utils.ErrCodeBadRequest, http.StatusBadRequest},
			{"unbindable body", "/products/7/external-ids", []int{1, 2}, nil, utils.ErrCodeBadRequest, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ext := new(mocks.ProductExternalIDService)
				reachesService := tc.want == http.StatusCreated || tc.err != nil
				if reachesService {
					var mapping *models.ProductExternalID
					if tc.err == nil {
						mapping = &models.ProductExternalID{ID: 3, ProductID: 7, Source: "erp", Scope: "plant", ExternalID: "M-1", CreatedAt: created, UpdatedAt: created}
					}
					ext.On("AddExternalID", mock.Anything, uint(7), mock.Anything, mock.Anything, mock.Anything).Return(mapping, tc.err).Once()
				}
				router := newProductExternalIDTestRouter(new(mocks.ProductService), ext, models.RoleAdmin)
				status, envelope := doJSON(t, router, http.MethodPost, tc.path, tc.body)
				require.Equal(t, tc.want, status, "%v", envelope.Error)
				if tc.want == http.StatusCreated {
					data := dataMapOf(t, envelope)
					assert.EqualValues(t, 3, data["id"])
					assert.EqualValues(t, 7, data["product_id"])
					assert.Equal(t, "erp", data["source"])
					assert.Equal(t, "plant", data["scope"])
					assert.Equal(t, "M-1", data["external_id"])
					assert.NotContains(t, data, "product")
				} else {
					require.NotNil(t, envelope.Error)
					assert.Equal(t, tc.wantCode, envelope.Error.Code)
				}
				if tc.name == "field error" {
					details := errorDetails(t, envelope)
					assert.Equal(t, "source", details["field"])
					assert.Equal(t, "must not start or end with whitespace", details["reason"])
					assert.NotContains(t, envelope.Error.Message, "SecretValue", "the value is never echoed")
				}
				if !reachesService {
					assertNoCalls(t, &ext.Mock, "AddExternalID")
				}
				ext.AssertExpectations(t)
			})
		}
	})

	t.Run("create passes the request context and the values", func(t *testing.T) {
		ext := new(mocks.ProductExternalIDService)
		ext.On("AddExternalID", mock.Anything, uint(7), "erp", "", "M-1").Return(&models.ProductExternalID{ID: 1, ProductID: 7}, nil).Once()
		router := newProductExternalIDTestRouter(new(mocks.ProductService), ext, models.RoleAdmin)
		status, _ := doJSON(t, router, http.MethodPost, "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"})
		require.Equal(t, http.StatusCreated, status)
		ext.AssertExpectations(t)
	})

	t.Run("delete", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			path     string
			err      error
			calls    bool
			wantCode string
			want     int
		}{
			{"success", "/products/7/external-ids/3", nil, true, "", http.StatusNoContent},
			{"another product's mapping", "/products/7/external-ids/3", fmt.Errorf("product 7 mapping 3: %w", apperrors.ErrNotFound), true, utils.ErrCodeNotFound, http.StatusNotFound},
			{"other failure", "/products/7/external-ids/3", errors.New("database is gone"), true, "INTERNAL_ERROR", http.StatusInternalServerError},
			{"bad product id", "/products/x/external-ids/3", nil, false, utils.ErrCodeBadRequest, http.StatusBadRequest},
			{"bad mapping id", "/products/7/external-ids/x", nil, false, utils.ErrCodeBadRequest, http.StatusBadRequest},
			{"zero mapping id", "/products/7/external-ids/0", nil, false, utils.ErrCodeBadRequest, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ext := new(mocks.ProductExternalIDService)
				if tc.calls {
					ext.On("RemoveExternalID", mock.Anything, uint(7), uint(3)).Return(tc.err).Once()
				}
				router := newProductExternalIDTestRouter(new(mocks.ProductService), ext, models.RoleAdmin)
				status, envelope := doJSON(t, router, http.MethodDelete, tc.path, nil)
				require.Equal(t, tc.want, status, "%v", envelope.Error)
				if tc.want != http.StatusNoContent {
					require.NotNil(t, envelope.Error)
					assert.Equal(t, tc.wantCode, envelope.Error.Code)
				}
				if !tc.calls {
					assertNoCalls(t, &ext.Mock, "RemoveExternalID")
				}
				ext.AssertExpectations(t)
			})
		}
	})

	t.Run("not wired", func(t *testing.T) {
		router := newProductExternalIDTestRouter(new(mocks.ProductService), nil, models.RoleAdmin)
		status, _ := doJSON(t, router, http.MethodPost, "/products/7/external-ids", gin.H{"source": "erp", "external_id": "M-1"})
		assert.Equal(t, http.StatusInternalServerError, status)
		status, _ = doJSON(t, router, http.MethodDelete, "/products/7/external-ids/3", nil)
		assert.Equal(t, http.StatusInternalServerError, status)
		status, _ = doJSON(t, router, http.MethodGet, "/products?source=erp&external_id=M-1", nil)
		assert.Equal(t, http.StatusInternalServerError, status)
	})

	t.Run("product delete unwired is 500", func(t *testing.T) {
		svc := new(mocks.ProductService)
		svc.On("Delete", mock.Anything, uint(7)).Return(service.ErrProductExternalIDsUnavailable).Once()
		router := newProductExternalIDTestRouter(svc, nil, models.RoleAdmin)
		status, envelope := doJSON(t, router, http.MethodDelete, "/products/7", nil)
		assert.Equal(t, http.StatusInternalServerError, status)
		assert.Equal(t, "INTERNAL_ERROR", envelope.Error.Code)
		svc.AssertExpectations(t)
	})
}

func TestProductListExternalIDLookup(t *testing.T) {
	product := models.Product{BaseModel: models.BaseModel{ID: 5}, Name: "Widget", Currency: "EUR", IsActive: false}

	t.Run("any parameter selects the lookup, validated first", func(t *testing.T) {
		for _, tc := range []struct {
			query, field string
		}{
			{"source=", "source"},
			{"scope=", "source"},
			{"external_id=", "source"},
			{"source=erp", "external_id"},
			{"source=erp&scope=+plant&external_id=M-1", "scope"},
			{"source=erp&external_id=" + strings.Repeat("e", 256), "external_id"},
			{"source=" + strings.Repeat("s", 65) + "&external_id=M-1", "source"},
			{"source=erp&external_id=M%00X", "external_id"},
			{"source=erp&external_id=%FF", "external_id"},
		} {
			svc := new(mocks.ProductService)
			ext := new(mocks.ProductExternalIDService)
			router := newProductExternalIDTestRouter(svc, ext, models.RoleSales)
			status, envelope := doJSON(t, router, http.MethodGet, "/products?"+tc.query, nil)
			require.Equalf(t, http.StatusBadRequest, status, tc.query)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code, tc.query)
			details := errorDetails(t, envelope)
			assert.Equal(t, tc.field, details["field"], tc.query)
			assert.NotEmpty(t, details["reason"], tc.query)
			assert.Nil(t, envelope.Data)
			assertNoCalls(t, &ext.Mock, "LookupProduct")
			assertNoCalls(t, &svc.Mock, "List")
		}
	})

	t.Run("hit and miss", func(t *testing.T) {
		svc := new(mocks.ProductService)
		ext := new(mocks.ProductExternalIDService)
		ext.On("LookupProduct", mock.Anything, "erp", "plant", "M-1").Return(&product, nil).Once()
		ext.On("LookupProduct", mock.Anything, "erp", "", "M-2").Return(nil, nil).Once()
		router := newProductExternalIDTestRouter(svc, ext, models.RoleAdmin)

		// The ordinary list parameters are ignored; limit only fills per_page.
		status, envelope := doJSON(t, router, http.MethodGet,
			"/products?source=erp&scope=plant&external_id=M-1&search=zzz&is_active=maybe&currency=eur&sort_by=bogus&page=3&offset=40&limit=5", nil)
		require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
		rows, ok := envelope.Data.([]interface{})
		require.True(t, ok, "data is an array: %T", envelope.Data)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 5, rows[0].(map[string]interface{})["id"])
		assert.Equal(t, false, rows[0].(map[string]interface{})["is_active"], "an inactive product is returned")
		require.NotNil(t, envelope.Meta)
		assert.Equal(t, 1, envelope.Meta.Page)
		assert.Equal(t, 5, envelope.Meta.PerPage)
		assert.EqualValues(t, 1, envelope.Meta.Total)
		assert.EqualValues(t, 1, envelope.Meta.TotalPages)

		status, envelope = doJSON(t, router, http.MethodGet, "/products?source=erp&external_id=M-2", nil)
		require.Equal(t, http.StatusOK, status)
		rows, ok = envelope.Data.([]interface{})
		require.True(t, ok, "an empty result is still an array: %T", envelope.Data)
		assert.Empty(t, rows)
		require.NotNil(t, envelope.Meta)
		assert.Equal(t, 1, envelope.Meta.Page)
		assert.Zero(t, envelope.Meta.Total)
		assertNoCalls(t, &svc.Mock, "List")
		ext.AssertExpectations(t)
	})

	t.Run("a lookup failure is 500", func(t *testing.T) {
		ext := new(mocks.ProductExternalIDService)
		ext.On("LookupProduct", mock.Anything, "erp", "", "M-1").Return(nil, errors.New("database is gone")).Once()
		router := newProductExternalIDTestRouter(new(mocks.ProductService), ext, models.RoleAdmin)
		status, envelope := doJSON(t, router, http.MethodGet, "/products?source=erp&external_id=M-1", nil)
		assert.Equal(t, http.StatusInternalServerError, status)
		assert.Equal(t, "INTERNAL_ERROR", envelope.Error.Code)
	})

	t.Run("without the parameters the list runs", func(t *testing.T) {
		svc := new(mocks.ProductService)
		ext := new(mocks.ProductExternalIDService)
		svc.On("List", 0, 20, mock.Anything).Return([]models.Product{product}, int64(1), nil).Once()
		router := newProductExternalIDTestRouter(svc, ext, models.RoleSales)
		status, envelope := doJSON(t, router, http.MethodGet, "/products", nil)
		require.Equal(t, http.StatusOK, status)
		rows, ok := envelope.Data.([]interface{})
		require.True(t, ok)
		assert.Len(t, rows, 1)
		assertNoCalls(t, &ext.Mock, "LookupProduct")
		svc.AssertExpectations(t)
	})
}

func TestProductExternalIDRoutesRoles(t *testing.T) {
	product := &models.Product{BaseModel: models.BaseModel{ID: 1}, Name: "Widget", Currency: "EUR", IsActive: true}
	for _, tc := range []struct {
		role   models.UserRole
		method string
		path   string
		body   interface{}
		want   int
	}{
		{models.RoleAdmin, http.MethodPost, "/products/1/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, http.StatusCreated},
		{models.RoleAdmin, http.MethodDelete, "/products/1/external-ids/2", nil, http.StatusNoContent},
		{models.RoleAdmin, http.MethodGet, "/products?source=erp&external_id=M-1", nil, http.StatusOK},
		{models.RoleSales, http.MethodPost, "/products/1/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, http.StatusForbidden},
		{models.RoleSales, http.MethodDelete, "/products/1/external-ids/2", nil, http.StatusForbidden},
		{models.RoleSales, http.MethodGet, "/products?source=erp&external_id=M-1", nil, http.StatusOK},
		{models.RoleSupport, http.MethodPost, "/products/1/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, http.StatusForbidden},
		{models.RoleSupport, http.MethodDelete, "/products/1/external-ids/2", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodGet, "/products?source=erp&external_id=M-1", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodGet, "/products", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodGet, "/products/1", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodPost, "/products", gin.H{"name": "X", "unit_price_cents": 1}, http.StatusForbidden},
		{models.RoleSupport, http.MethodPut, "/products/1", gin.H{"name": "X"}, http.StatusForbidden},
		{models.RoleSupport, http.MethodDelete, "/products/1", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodPost, "/products/1/external-ids", gin.H{"source": "erp", "external_id": "M-1"}, http.StatusForbidden},
		{models.RoleCustomer, http.MethodDelete, "/products/1/external-ids/2", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodGet, "/products?source=erp&external_id=M-1", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodGet, "/products", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodGet, "/products/1", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodPost, "/products", gin.H{"name": "X", "unit_price_cents": 1}, http.StatusForbidden},
		{models.RoleCustomer, http.MethodPut, "/products/1", gin.H{"name": "X"}, http.StatusForbidden},
		{models.RoleCustomer, http.MethodDelete, "/products/1", nil, http.StatusForbidden},
	} {
		svc := new(mocks.ProductService)
		svc.On("List", mock.Anything, mock.Anything, mock.Anything).Return([]models.Product{}, int64(0), nil).Maybe()
		svc.On("GetByID", uint(1)).Return(product, nil).Maybe()
		svc.On("Create", mock.Anything).Return(nil).Maybe()
		svc.On("Update", mock.Anything).Return(nil).Maybe()
		svc.On("Delete", mock.Anything, uint(1)).Return(nil).Maybe()
		ext := new(mocks.ProductExternalIDService)
		ext.On("AddExternalID", mock.Anything, uint(1), "erp", "", "M-1").Return(&models.ProductExternalID{ID: 2, ProductID: 1}, nil).Maybe()
		ext.On("RemoveExternalID", mock.Anything, uint(1), uint(2)).Return(nil).Maybe()
		ext.On("LookupProduct", mock.Anything, "erp", "", "M-1").Return(product, nil).Maybe()
		router := newProductExternalIDTestRouter(svc, ext, tc.role)
		status, envelope := doJSON(t, router, tc.method, tc.path, tc.body)
		assert.Equalf(t, tc.want, status, "%s %s as %s: %v", tc.method, tc.path, tc.role, envelope.Error)
		if tc.want == http.StatusForbidden {
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
			assertNoCalls(t, &ext.Mock, "AddExternalID", "RemoveExternalID", "LookupProduct")
			assertNoCalls(t, &svc.Mock, "Create", "Update", "Delete", "List", "GetByID")
		}
	}

	// The handler repeats the admin guard for a caller the route guard let
	// through by mistake.
	for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		ext := new(mocks.ProductExternalIDService)
		h := NewProductHandler(new(mocks.ProductService)).WithExternalIDs(ext)
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(middleware.RequestID(), middleware.ErrorHandler(), func(c *gin.Context) {
			c.Set("user_role", string(role))
			c.Next()
		})
		router.POST("/products/:id/external-ids", h.CreateExternalID)
		router.DELETE("/products/:id/external-ids/:mappingId", h.DeleteExternalID)
		status, _ := doJSON(t, router, http.MethodPost, "/products/1/external-ids", gin.H{"source": "erp", "external_id": "M-1"})
		assert.Equal(t, http.StatusForbidden, status, "%s POST", role)
		status, _ = doJSON(t, router, http.MethodDelete, "/products/1/external-ids/2", nil)
		assert.Equal(t, http.StatusForbidden, status, "%s DELETE", role)
		assertNoCalls(t, &ext.Mock, "AddExternalID", "RemoveExternalID")
	}
}
