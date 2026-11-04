package main

// Issue #255 through the router production uses: setupRouter wires the user
// repository into the lead service and mounts POST /leads/{id}/assign, over a
// SQLite file opened by the real connector with foreign keys on. A wiring
// mistake (no user repository) would answer 500 here instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestLeadOwnerProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	var foreignKeys int
	require.NoError(t, models.DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	require.Equal(t, 1, foreignKeys, "the production connector turns foreign keys on")

	admin := &models.User{Email: "admin@lead-owner-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	sales := &models.User{Email: "sales@lead-owner-wiring.test", Password: "x", FirstName: "Sam", LastName: "Seller", Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(sales).Error)
	lead := &models.Lead{FirstName: "Lee", LastName: "Prospect", Email: "lee@lead-owner-wiring.test", OwnerID: admin.ID, Status: models.LeadStatusNew}
	require.NoError(t, models.DB.Create(lead).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	// The same secret the router validates against; the middleware reads the
	// user back from the database.
	token, err := service.NewAuthService(repository.NewUserRepository(models.DB),
		repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT).GenerateJWT(admin)
	require.NoError(t, err)

	post := func(router *gin.Engine, path string, body map[string]interface{}) (int, utils.APIResponse) {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+path, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), "body: %s", recorder.Body.String())
		return recorder.Code, envelope
	}

	var leadsBefore, leadsAfter int64
	require.NoError(t, models.DB.Unscoped().Model(&models.Lead{}).Count(&leadsBefore).Error)
	status, envelope := post(router, "/leads", map[string]interface{}{
		"first_name": "No", "last_name": "Owner", "email": "no-owner@lead-owner-wiring.test", "owner_id": 999999,
	})
	require.Equal(t, http.StatusBadRequest, status, "%+v", envelope.Error)
	require.NotNil(t, envelope.Error)
	assert.Equal(t, apperrors.CodeInvalidReference, envelope.Error.Code)
	require.NoError(t, models.DB.Unscoped().Model(&models.Lead{}).Count(&leadsAfter).Error)
	assert.Equal(t, leadsBefore, leadsAfter)

	status, envelope = post(router, fmt.Sprintf("/leads/%d/assign", lead.ID), map[string]interface{}{"user_id": sales.ID})
	require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
	var stored models.Lead
	require.NoError(t, models.DB.First(&stored, lead.ID).Error)
	assert.Equal(t, sales.ID, stored.OwnerID)

	// The real wiring records the event in the same transaction.
	var events []models.AutomationEvent
	require.NoError(t, models.DB.Where("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, lead.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.AutomationEventLeadUpdated, events[0].EventType)
}
