package tests

// Product external-id mappings (#239) over HTTP on SQLite: the routes of
// SetupProductRoutes behind the real RequestID, Logger, ErrorHandler and JWT
// Auth middleware, the real repositories and services (ProductService built
// with WithProductExternalIDs), and the full schema of models.MigrateDatabase
// with foreign keys on. Every outcome is read back from the database.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type productExternalIDAPIFixture struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[models.UserRole]string
}

func newProductExternalIDAPIFixture(t *testing.T) *productExternalIDAPIFixture {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "product-external-ids.db") +
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
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "product-external-id-api-test-secret-0123456789", ExpiryHours: 1})
	txManager := utils.NewTransactionManager(db)
	externalIDRepo := repository.NewProductExternalIDRepository(db)
	productService := service.NewProductService(repository.NewProductRepository(db), nil,
		service.WithProductExternalIDs(externalIDRepo, txManager))
	productHandler := handler.NewProductHandler(productService).
		WithExternalIDs(service.NewProductExternalIDService(externalIDRepo, txManager))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupProductRoutes(protected, productHandler)

	f := &productExternalIDAPIFixture{db: db, router: router, tokens: map[models.UserRole]string{}}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{Email: fmt.Sprintf("%s@product-external-id-api.test", role), Password: "x", FirstName: "Api",
			LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		token, err := authService.GenerateJWT(user)
		require.NoError(t, err)
		f.tokens[role] = token
	}
	return f
}

func (f *productExternalIDAPIFixture) do(t *testing.T, role models.UserRole, method, path string, body interface{}) (int, utils.APIResponse) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens[role])
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var envelope utils.APIResponse
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	}
	return rec.Code, envelope
}

func (f *productExternalIDAPIFixture) product(t *testing.T, name string, active bool) *models.Product {
	t.Helper()
	p := &models.Product{Name: name, SKU: "API-SKU", UnitPriceCents: 900, Currency: "EUR", IsActive: active}
	require.NoError(t, f.db.Create(p).Error)
	return p
}

func (f *productExternalIDAPIFixture) mappings(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Raw("SELECT COUNT(*) FROM product_external_ids").Scan(&n).Error)
	return n
}

// lookup answers GET /products?source=&scope=&external_id= and returns the
// ids of the products in data (an array) and meta.total.
func (f *productExternalIDAPIFixture) lookup(t *testing.T, role models.UserRole, source, scope, externalID string) (int, []uint, int64) {
	t.Helper()
	query := url.Values{"source": {source}, "scope": {scope}, "external_id": {externalID}}
	status, envelope := f.do(t, role, http.MethodGet, "/products?"+query.Encode(), nil)
	if status != http.StatusOK {
		return status, nil, -1
	}
	list, ok := envelope.Data.([]interface{})
	require.True(t, ok, "data is an array, never null: %T", envelope.Data)
	ids := make([]uint, 0, len(list))
	for _, entry := range list {
		ids = append(ids, uint(entry.(map[string]interface{})["id"].(float64)))
	}
	require.NotNil(t, envelope.Meta)
	assert.Equal(t, 1, envelope.Meta.Page)
	return status, ids, envelope.Meta.Total
}

func TestProductExternalIDAPISQLite(t *testing.T) {
	f := newProductExternalIDAPIFixture(t)
	triple := map[string]interface{}{"source": "erp", "scope": "plant-01", "external_id": "MAT-100042"}

	product := f.product(t, "Mapped widget", true)
	other := f.product(t, "Other widget", false)
	createPath := fmt.Sprintf("/products/%d/external-ids", product.ID)

	// Admin creates the mapping.
	status, envelope := f.do(t, models.RoleAdmin, http.MethodPost, createPath, triple)
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	data := envelope.Data.(map[string]interface{})
	mappingID := uint(data["id"].(float64))
	assert.EqualValues(t, product.ID, data["product_id"])
	assert.Equal(t, "erp", data["source"])
	assert.Equal(t, "plant-01", data["scope"])
	assert.Equal(t, "MAT-100042", data["external_id"])
	assert.NotContains(t, data, "product")
	var stored models.ProductExternalID
	require.NoError(t, f.db.First(&stored, mappingID).Error)
	assert.Equal(t, product.ID, stored.ProductID)

	// A duplicate triple is 409, for the same and for another product.
	before := f.mappings(t)
	status, envelope = f.do(t, models.RoleAdmin, http.MethodPost, createPath, triple)
	require.Equal(t, http.StatusConflict, status)
	assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
	status, _ = f.do(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/products/%d/external-ids", other.ID), triple)
	require.Equal(t, http.StatusConflict, status, "another product cannot take the triple")
	assert.Equal(t, before, f.mappings(t))

	// A triple differing only in case is another identity.
	status, envelope = f.do(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/products/%d/external-ids", other.ID),
		map[string]interface{}{"source": "erp", "scope": "plant-01", "external_id": "mat-100042"})
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	caseMappingID := uint(envelope.Data.(map[string]interface{})["id"].(float64))

	// Admin and sales resolve the triple; support and customer are refused.
	for _, reader := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
		status, ids, total := f.lookup(t, reader, "erp", "plant-01", "MAT-100042")
		require.Equal(t, http.StatusOK, status, string(reader))
		assert.Equal(t, []uint{product.ID}, ids, string(reader))
		assert.EqualValues(t, 1, total)
		_, ids, _ = f.lookup(t, reader, "erp", "plant-01", "mat-100042")
		assert.Equal(t, []uint{other.ID}, ids, "the inactive product is found, by its own case")
	}
	for _, reader := range []models.UserRole{models.RoleSupport, models.RoleCustomer} {
		status, _, _ := f.lookup(t, reader, "erp", "plant-01", "MAT-100042")
		assert.Equal(t, http.StatusForbidden, status, string(reader))
	}
	_, ids, total := f.lookup(t, models.RoleAdmin, "erp", "", "MAT-100042")
	assert.Empty(t, ids, "an absent scope is '', not a wildcard")
	assert.Zero(t, total)

	// Sales cannot write, and nothing is written.
	before = f.mappings(t)
	status, envelope = f.do(t, models.RoleSales, http.MethodPost, createPath,
		map[string]interface{}{"source": "erp", "external_id": "written-by-sales"})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
	status, _ = f.do(t, models.RoleSales, http.MethodDelete, fmt.Sprintf("/products/%d/external-ids/%d", product.ID, mappingID), nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, before, f.mappings(t), "sales wrote nothing")
	require.NoError(t, f.db.First(&models.ProductExternalID{}, mappingID).Error, "the mapping is still there")

	// Another product's mapping at the path is 404 and deletes nothing.
	status, envelope = f.do(t, models.RoleAdmin, http.MethodDelete, fmt.Sprintf("/products/%d/external-ids/%d", other.ID, mappingID), nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	assert.Equal(t, before, f.mappings(t))

	// The admin deletes the right one; the triple is free again.
	status, _ = f.do(t, models.RoleAdmin, http.MethodDelete, fmt.Sprintf("/products/%d/external-ids/%d", other.ID, caseMappingID), nil)
	require.Equal(t, http.StatusNoContent, status)
	assert.Equal(t, before-1, f.mappings(t))
	_, ids, _ = f.lookup(t, models.RoleAdmin, "erp", "plant-01", "mat-100042")
	assert.Empty(t, ids)

	// Validation: field and reason, never the value; a missing product is 404.
	status, envelope = f.do(t, models.RoleAdmin, http.MethodPost, createPath,
		map[string]interface{}{"source": "erp", "external_id": "TrailingCanary "})
	require.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
	details, _ := envelope.Error.Details.(map[string]interface{})
	assert.Equal(t, "external_id", details["field"])
	assert.NotContains(t, envelope.Error.Message, "TrailingCanary")
	status, envelope = f.do(t, models.RoleSales, http.MethodGet, "/products?external_id=x", nil)
	require.Equal(t, http.StatusBadRequest, status)
	details, _ = envelope.Error.Details.(map[string]interface{})
	assert.Equal(t, "source", details["field"])
	status, _ = f.do(t, models.RoleAdmin, http.MethodPost, "/products/99999999/external-ids", triple)
	assert.Equal(t, http.StatusNotFound, status)

	// Deleting the product frees its keys.
	status, envelope = f.do(t, models.RoleAdmin, http.MethodDelete, fmt.Sprintf("/products/%d", product.ID), nil)
	require.Equal(t, http.StatusNoContent, status, "%+v", envelope.Error)
	var raw int64
	require.NoError(t, f.db.Raw("SELECT COUNT(*) FROM product_external_ids WHERE product_id = ?", product.ID).Scan(&raw).Error)
	assert.Zero(t, raw, "the product's mappings were hard-deleted")
	var deleted models.Product
	require.NoError(t, f.db.Unscoped().First(&deleted, product.ID).Error)
	assert.True(t, deleted.DeletedAt.Valid, "the product is soft-deleted")

	status, ids, total = f.lookup(t, models.RoleSales, "erp", "plant-01", "MAT-100042")
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, ids, "the lookup returns an empty array after the product delete")
	assert.Zero(t, total)

	// The same triple maps to a second product.
	status, envelope = f.do(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/products/%d/external-ids", other.ID), triple)
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	_, ids, _ = f.lookup(t, models.RoleAdmin, "erp", "plant-01", "MAT-100042")
	assert.Equal(t, []uint{other.ID}, ids)

	// A mapping on the deleted product is 404 and writes nothing.
	before = f.mappings(t)
	status, envelope = f.do(t, models.RoleAdmin, http.MethodPost, createPath,
		map[string]interface{}{"source": "erp", "external_id": "after-delete"})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	assert.Equal(t, before, f.mappings(t))

	// A second delete of the product is 404.
	status, _ = f.do(t, models.RoleAdmin, http.MethodDelete, fmt.Sprintf("/products/%d", product.ID), nil)
	assert.Equal(t, http.StatusNotFound, status)
}
