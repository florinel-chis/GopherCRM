package handler

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The max binding on unit_price_cents must be models.QuoteAmountCentsMax, the
// bound the service enforces as well; struct tags cannot reference it.
func TestProductPriceBindingTagMatchesTheModel(t *testing.T) {
	create, ok := reflect.TypeOf(ProductRequest{}).FieldByName("UnitPriceCents")
	require.True(t, ok)
	assert.Equal(t, fmt.Sprintf("required,min=0,max=%d", models.QuoteAmountCentsMax), create.Tag.Get("binding"))
	update, ok := reflect.TypeOf(ProductUpdateRequest{}).FieldByName("UnitPriceCents")
	require.True(t, ok)
	assert.Equal(t, fmt.Sprintf("omitempty,min=0,max=%d", models.QuoteAmountCentsMax), update.Tag.Get("binding"))
}

func newProductTestRouter(svc *mocks.ProductService, role models.UserRole) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupProductRoutes(router.Group(""), NewProductHandler(svc))
	return router
}

// An absent is_active defaults to true, an explicit false survives, and an
// explicit price of 0 passes `required`.
func TestProductHandler_CreateDefaultsActiveAndKeepsExplicitValues(t *testing.T) {
	svc := new(mocks.ProductService)
	var created []*models.Product
	svc.On("Create", mock.Anything).Run(func(args mock.Arguments) {
		created = append(created, args.Get(0).(*models.Product))
	}).Return(nil)
	router := newProductTestRouter(svc, models.RoleAdmin)

	status, envelope := doJSON(t, router, http.MethodPost, "/products", gin.H{"name": "Widget", "unit_price_cents": 1250})
	require.Equal(t, http.StatusCreated, status, "%v", envelope.Error)
	status, _ = doJSON(t, router, http.MethodPost, "/products", gin.H{"name": "Free sample", "unit_price_cents": 0, "is_active": false, "currency": "USD"})
	require.Equal(t, http.StatusCreated, status)
	require.Len(t, created, 2)
	assert.True(t, created[0].IsActive, "absent is_active defaults to true")
	assert.Equal(t, int64(1250), created[0].UnitPriceCents)
	assert.False(t, created[1].IsActive, "an explicit false is kept")
	assert.Equal(t, int64(0), created[1].UnitPriceCents, "an explicit 0 passes required")
	assert.Equal(t, "USD", created[1].Currency)

	for name, body := range map[string]gin.H{
		"missing name":          {"unit_price_cents": 1},
		"missing price":         {"name": "X"},
		"negative price":        {"name": "X", "unit_price_cents": -1},
		"price above the bound": {"name": "X", "unit_price_cents": models.QuoteAmountCentsMax + 1},
		"lowercase currency":    {"name": "X", "unit_price_cents": 1, "currency": "eur"},
		"name too long":         {"name": strings.Repeat("n", models.ProductNameMaxLength+1), "unit_price_cents": 1},
		"sku too long":          {"name": "X", "sku": strings.Repeat("s", models.ProductSKUMaxLength+1), "unit_price_cents": 1},
	} {
		status, envelope := doJSON(t, router, http.MethodPost, "/products", body)
		assert.Equalf(t, http.StatusBadRequest, status, name)
		if assert.NotNil(t, envelope.Error, name) {
			assert.Equalf(t, utils.ErrCodeValidation, envelope.Error.Code, name)
		}
	}
	svc.AssertNumberOfCalls(t, "Create", 2)
}

// Bad query filters are 400 BAD_REQUEST with no data rows; good ones reach the
// service as parsed.
func TestProductHandler_ListFiltersAndBadQueries(t *testing.T) {
	svc := new(mocks.ProductService)
	router := newProductTestRouter(svc, models.RoleSales)

	// The injection payload is percent-encoded: net/url drops a bare `;` pair.
	for _, query := range []string{"sort_by=deleted_at", "sort_by=name%3BDROP+TABLE+products", "is_active=maybe", "currency=eur", "currency=EURO"} {
		status, envelope := doJSON(t, router, http.MethodGet, "/products?"+query, nil)
		assert.Equalf(t, http.StatusBadRequest, status, query)
		assert.False(t, envelope.Success)
		assert.Nil(t, envelope.Data, "no data rows on a refused query: %s", query)
		if assert.NotNil(t, envelope.Error) {
			assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
		}
	}
	assertNoCalls(t, &svc.Mock, "List")

	svc.On("List", 0, 100, mock.MatchedBy(func(filter repository.ProductListFilter) bool {
		return filter.Search == "wid" && filter.IsActive != nil && !*filter.IsActive && filter.Currency == "USD" &&
			filter.SortBy == "unit_price_cents" && filter.SortOrder == "asc"
	})).Return([]models.Product{{Name: "Widget"}}, int64(1), nil).Once()
	status, envelope := doJSON(t, router, http.MethodGet, "/products?search=wid&is_active=false&currency=USD&sort_by=unit_price_cents&sort_order=asc&limit=500", nil)
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	require.NotNil(t, envelope.Meta)
	assert.Equal(t, 100, envelope.Meta.PerPage, "limit is capped at 100 by the shared parser")
	assert.Equal(t, int64(1), envelope.Meta.Total)

	svc.On("List", 0, 20, mock.Anything).Return([]models.Product{}, int64(0), nil).Once()
	status, envelope = doJSON(t, router, http.MethodGet, "/products?limit=0", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 20, envelope.Meta.PerPage, "limit=0 is the default page size, never 0")
	svc.AssertExpectations(t)
}

// The role guards over the real routes: writes are admin only, reads admin
// and sales, nothing for support and customer.
func TestProductHandler_RoleGuards(t *testing.T) {
	product := &models.Product{BaseModel: models.BaseModel{ID: 1}, Name: "Widget", Currency: "EUR", IsActive: true}
	for _, tc := range []struct {
		role   models.UserRole
		method string
		path   string
		body   interface{}
		want   int
	}{
		{models.RoleSales, http.MethodGet, "/products", nil, http.StatusOK},
		{models.RoleSales, http.MethodGet, "/products/1", nil, http.StatusOK},
		{models.RoleSales, http.MethodPost, "/products", gin.H{"name": "X", "unit_price_cents": 1}, http.StatusForbidden},
		{models.RoleSales, http.MethodPut, "/products/1", gin.H{"name": "X"}, http.StatusForbidden},
		{models.RoleSales, http.MethodDelete, "/products/1", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodGet, "/products", nil, http.StatusForbidden},
		{models.RoleSupport, http.MethodGet, "/products/1", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodGet, "/products", nil, http.StatusForbidden},
		{models.RoleCustomer, http.MethodPost, "/products", gin.H{"name": "X", "unit_price_cents": 1}, http.StatusForbidden},
		{models.RoleAdmin, http.MethodPost, "/products", gin.H{"name": "X", "unit_price_cents": 1}, http.StatusCreated},
		{models.RoleAdmin, http.MethodPut, "/products/1", gin.H{"name": "X"}, http.StatusOK},
		{models.RoleAdmin, http.MethodDelete, "/products/1", nil, http.StatusNoContent},
	} {
		svc := new(mocks.ProductService)
		svc.On("List", mock.Anything, mock.Anything, mock.Anything).Return([]models.Product{}, int64(0), nil).Maybe()
		svc.On("GetByID", uint(1)).Return(product, nil).Maybe()
		svc.On("Create", mock.Anything).Return(nil).Maybe()
		svc.On("Update", mock.Anything).Return(nil).Maybe()
		svc.On("Delete", uint(1)).Return(nil).Maybe()
		router := newProductTestRouter(svc, tc.role)
		status, envelope := doJSON(t, router, tc.method, tc.path, tc.body)
		assert.Equalf(t, tc.want, status, "%s %s as %s: %v", tc.method, tc.path, tc.role, envelope.Error)
		if tc.want == http.StatusForbidden {
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
			assertNoCalls(t, &svc.Mock, "Create", "Update", "Delete", "List", "GetByID")
		}
	}
}

// Service outcomes: a missing product is 404 at the path, a domain refusal is
// 400 VALIDATION_ERROR, and the update merges the partial body onto the row.
func TestProductHandler_UpdateMergesAndMapsErrors(t *testing.T) {
	svc := new(mocks.ProductService)
	stored := &models.Product{BaseModel: models.BaseModel{ID: 1}, Name: "Widget", SKU: "W-1", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	svc.On("GetByID", uint(1)).Return(stored, nil)
	svc.On("GetByID", uint(2)).Return(nil, fmt.Errorf("product 2 not found: %w", apperrors.ErrProductNotFound))
	var updated *models.Product
	svc.On("Update", mock.Anything).Run(func(args mock.Arguments) {
		updated = args.Get(0).(*models.Product)
	}).Return(nil).Once()
	svc.On("Create", mock.Anything).Return(fmt.Errorf("product name is required: %w", apperrors.ErrValidation)).Once()
	svc.On("Delete", uint(2)).Return(fmt.Errorf("product 2 not found: %w", apperrors.ErrProductNotFound)).Once()
	router := newProductTestRouter(svc, models.RoleAdmin)

	status, _ := doJSON(t, router, http.MethodPut, "/products/1", gin.H{"unit_price_cents": 2500, "is_active": false})
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, updated)
	assert.Equal(t, "Widget", updated.Name, "absent fields keep their value")
	assert.Equal(t, "W-1", updated.SKU)
	assert.Equal(t, int64(2500), updated.UnitPriceCents)
	assert.False(t, updated.IsActive)

	status, envelope := doJSON(t, router, http.MethodGet, "/products/2", nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	status, envelope = doJSON(t, router, http.MethodDelete, "/products/2", nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	status, envelope = doJSON(t, router, http.MethodPost, "/products", gin.H{"name": "   ", "unit_price_cents": 1})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
	status, envelope = doJSON(t, router, http.MethodGet, "/products/abc", nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
	svc.AssertExpectations(t)
}
