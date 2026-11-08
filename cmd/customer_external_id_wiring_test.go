package main

// Issue #212 through the router production uses: setupRouter (and with it
// setupDependencies) mounts POST and DELETE /customers/{id}/external-ids
// through handler.SetupCustomerExternalIDRoutes behind RequireRole(admin,
// sales), and hands the customer handler a wired external-id service, over a
// SQLite file opened by the real connector with foreign keys on. A handler
// without the service would answer the create and the lookup with 500.

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

func TestCustomerExternalIDProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	var foreignKeys int
	require.NoError(t, models.DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	require.Equal(t, 1, foreignKeys, "the production connector turns foreign keys on")
	require.True(t, models.DB.Migrator().HasTable(&models.CustomerExternalID{}), "MigrateDatabase created the table")

	admin := &models.User{Email: "admin@extid-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	support := &models.User{Email: "support@extid-wiring.test", Password: "x", FirstName: "Sue", LastName: "Support", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, models.DB.Create(support).Error)
	customer := &models.Customer{FirstName: "Cara", LastName: "Client", Email: "cara@extid-wiring.test"}
	require.NoError(t, models.DB.Create(customer).Error)

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
	supportToken, err := auth.GenerateJWT(support)
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
	countMappings := func() int64 {
		var n int64
		require.NoError(t, models.DB.Model(&models.CustomerExternalID{}).Count(&n).Error)
		return n
	}

	body := map[string]interface{}{"source": "wiring-shop", "scope": "eu", "external_id": "W-1"}
	createPath := fmt.Sprintf("/customers/%d/external-ids", customer.ID)

	// Support is refused by the route guard and nothing is written.
	status, envelope := do(http.MethodPost, createPath, supportToken, body)
	require.Equal(t, http.StatusForbidden, status, "%+v", envelope.Error)
	assert.Zero(t, countMappings())

	// Admin creates the mapping.
	status, envelope = do(http.MethodPost, createPath, adminToken, body)
	require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
	assert.EqualValues(t, 1, countMappings())
	var mapping models.CustomerExternalID
	require.NoError(t, models.DB.First(&mapping).Error)
	assert.Equal(t, customer.ID, mapping.CustomerID)

	// The lookup on GET /customers answers through the same wired service.
	query := url.Values{"source": {"wiring-shop"}, "scope": {"eu"}, "external_id": {"W-1"}}
	status, envelope = do(http.MethodGet, "/customers?"+query.Encode(), adminToken, nil)
	require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
	data, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 1, data["total"])
	customers, ok := data["customers"].([]interface{})
	require.True(t, ok)
	require.Len(t, customers, 1)
	assert.EqualValues(t, customer.ID, customers[0].(map[string]interface{})["id"])

	// Support may look up but not delete.
	status, _ = do(http.MethodGet, "/customers?"+query.Encode(), supportToken, nil)
	assert.Equal(t, http.StatusOK, status)
	deletePath := fmt.Sprintf("/customers/%d/external-ids/%d", customer.ID, mapping.ID)
	status, _ = do(http.MethodDelete, deletePath, supportToken, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.EqualValues(t, 1, countMappings())

	// Admin deletes it.
	status, _ = do(http.MethodDelete, deletePath, adminToken, nil)
	assert.Equal(t, http.StatusNoContent, status)
	assert.Zero(t, countMappings())
}
