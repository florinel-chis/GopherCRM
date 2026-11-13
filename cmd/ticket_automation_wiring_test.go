package main

// Issue #242 through the router production uses: setupDependencies builds
// TicketService with WithTicketAutomationEvents(automationRepo, txManager),
// so a ticket created and updated over HTTP records ticket_created,
// ticket_updated and ticket_status_changed rows. Over a SQLite file opened by
// the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestTicketAutomationProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	admin := &models.User{Email: "admin@ticket-automation-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	customer := &models.Customer{FirstName: "Cora", LastName: "Customer", Email: "cora@ticket-automation-wiring.test"}
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
	token, err := auth.GenerateJWT(admin)
	require.NoError(t, err)
	call := func(method, path string, body interface{}) map[string]interface{} {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Less(t, recorder.Code, 300, "%s %s: %s", method, path, recorder.Body.String())
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		data, ok := envelope.Data.(map[string]interface{})
		require.True(t, ok, "data is an object")
		return data
	}

	created := call(http.MethodPost, "/tickets", map[string]interface{}{
		"title": "Wired automation ticket", "description": "d", "customer_id": customer.ID,
	})
	ticketID := uint(created["id"].(float64))
	require.NotZero(t, ticketID)
	call(http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), map[string]interface{}{"status": "resolved"})

	var events []models.AutomationEvent
	require.NoError(t, models.DB.Where("entity_type = ? AND entity_id = ?", models.AutomationEntityTicket, ticketID).
		Order("id ASC").Find(&events).Error)
	require.Len(t, events, 3, "ticket_created, ticket_updated and ticket_status_changed")
	assert.Equal(t, models.AutomationEventTicketCreated, events[0].EventType)
	assert.Equal(t, string(models.TicketStatusOpen), events[0].ToValue)
	assert.Equal(t, models.AutomationEventTicketUpdated, events[1].EventType)
	assert.Equal(t, models.AutomationEventTicketStatusChanged, events[2].EventType)
	assert.Equal(t, string(models.TicketStatusOpen), events[2].FromValue)
	assert.Equal(t, string(models.TicketStatusResolved), events[2].ToValue)
	for _, event := range events {
		assert.Equal(t, models.AutomationOriginUser, event.Origin)
		assert.Nil(t, event.ActorID)
	}
}
