package main

// Issue #239 through the router production uses: setupRouter (and with it
// setupDependencies) mounts POST and DELETE /products/{id}/external-ids behind
// RequireRole(admin), hands the product handler a wired external-id service
// for the lookup mode of GET /products, and builds ProductService with
// WithProductExternalIDs, so DELETE /products/{id} purges the product's
// mappings instead of answering ErrProductExternalIDsUnavailable (500). Over a
// SQLite file opened by the real connector with foreign keys on.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestProductExternalIDProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	var foreignKeys int
	require.NoError(t, models.DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	require.Equal(t, 1, foreignKeys, "the production connector turns foreign keys on")
	require.True(t, models.DB.Migrator().HasTable(&models.ProductExternalID{}), "MigrateDatabase created the table")

	admin := &models.User{Email: "admin@prod-extid-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	sales := &models.User{Email: "sales@prod-extid-wiring.test", Password: "x", FirstName: "Sam", LastName: "Sales", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(sales).Error)
	product := &models.Product{Name: "Wired widget", SKU: "W-1", UnitPriceCents: 100, Currency: "EUR", IsActive: true}
	require.NoError(t, models.DB.Create(product).Error)
	second := &models.Product{Name: "Second widget", UnitPriceCents: 200, Currency: "EUR", IsActive: true}
	require.NoError(t, models.DB.Create(second).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	auth := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT)
	adminToken, err := auth.GenerateJWT(admin)
	require.NoError(t, err)
	salesToken, err := auth.GenerateJWT(sales)
	require.NoError(t, err)

	do := func(method, path, token string, body interface{}) (int, utils.APIResponse) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			payload, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(payload)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, reader)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		if recorder.Body.Len() > 0 {
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), "body: %s", recorder.Body.String())
		}
		return recorder.Code, envelope
	}
	countMappings := func(productID uint) int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.ProductExternalID{}).Where("product_id = ?", productID).Count(&n).Error)
		return n
	}

	body := map[string]interface{}{"source": "wiring-erp", "scope": "plant", "external_id": "MAT-1"}
	createPath := fmt.Sprintf("/products/%d/external-ids", product.ID)

	// Sales is refused by the route guard and nothing is written.
	status, envelope := do(http.MethodPost, createPath, salesToken, body)
	require.Equal(t, http.StatusForbidden, status, "%+v", envelope.Error)
	assert.Zero(t, countMappings(product.ID))

	status, envelope = do(http.MethodPost, createPath, adminToken, body)
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	assert.EqualValues(t, 1, countMappings(product.ID))
	var mapping models.ProductExternalID
	require.NoError(t, models.DB.Where("product_id = ?", product.ID).First(&mapping).Error)

	// A second mapping, removed through the DELETE route.
	status, envelope = do(http.MethodPost, createPath, adminToken, map[string]interface{}{"source": "wiring-erp", "external_id": "MAT-2"})
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	extra := dataMapOfWiring(t, envelope)
	deletePath := fmt.Sprintf("/products/%d/external-ids/%v", product.ID, extra["id"])
	status, _ = do(http.MethodDelete, deletePath, salesToken, nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = do(http.MethodDelete, deletePath, adminToken, nil)
	assert.Equal(t, http.StatusNoContent, status)
	assert.EqualValues(t, 1, countMappings(product.ID))

	// The lookup mode answers through the wired service, for sales too.
	query := url.Values{"source": {"wiring-erp"}, "scope": {"plant"}, "external_id": {"MAT-1"}}
	for _, token := range []string{adminToken, salesToken} {
		status, envelope = do(http.MethodGet, "/products?"+query.Encode(), token, nil)
		require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
		rows, ok := envelope.Data.([]interface{})
		require.True(t, ok, "data is an array")
		require.Len(t, rows, 1)
		assert.EqualValues(t, product.ID, rows[0].(map[string]interface{})["id"])
	}

	// The production ProductService purges the mappings on delete.
	status, envelope = do(http.MethodDelete, fmt.Sprintf("/products/%d", product.ID), adminToken, nil)
	require.Equal(t, http.StatusNoContent, status, "%+v", envelope.Error)
	var raw int64
	require.NoError(t, models.DB.Raw("SELECT COUNT(*) FROM product_external_ids WHERE product_id = ?", product.ID).Scan(&raw).Error)
	assert.Zero(t, raw, "the mappings were hard-deleted with the product")

	status, envelope = do(http.MethodGet, "/products?"+query.Encode(), adminToken, nil)
	require.Equal(t, http.StatusOK, status)
	rows, ok := envelope.Data.([]interface{})
	require.True(t, ok)
	assert.Empty(t, rows, "the deleted product is no longer found")

	status, envelope = do(http.MethodPost, fmt.Sprintf("/products/%d/external-ids", second.ID), adminToken, body)
	require.Equal(t, http.StatusCreated, status, "the freed triple maps to another product: %+v", envelope.Error)
}

func dataMapOfWiring(t *testing.T, envelope utils.APIResponse) map[string]interface{} {
	t.Helper()
	m, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok, "data is an object: %T", envelope.Data)
	return m
}
