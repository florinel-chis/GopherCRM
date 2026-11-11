package main

// Issue #219 through the router production uses: setupDependencies builds
// TicketService with WithTicketCommentMail (the ticket mail delivery
// repository, the shared txManager, the shared appMailer). Without SMTP_HOST
// that mailer is the LogMailer, so a flagged comment is stamped log_only
// rather than refused with ErrTicketCommentMailUnavailable (500). Over a
// SQLite file opened by the real connector.

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

func TestTicketCommentMailProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)
	require.Empty(t, boot.cfg.SMTP.Host, "the fixture runs without SMTP_HOST, so the LogMailer is wired")
	require.True(t, models.DB.Migrator().HasTable(&models.TicketMailDelivery{}), "MigrateDatabase created the table")

	admin := &models.User{Email: "admin@ticket-mail-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	customer := &models.Customer{FirstName: "Cora", LastName: "Customer", Email: "cora@ticket-mail-wiring.test"}
	require.NoError(t, models.DB.Create(customer).Error)
	ticket := &models.Ticket{Title: "Wired ticket", Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &admin.ID}
	require.NoError(t, models.DB.Create(ticket).Error)

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

	payload, err := json.Marshal(map[string]interface{}{"body": "Wired reply", "email_customer": true})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("%s/tickets/%d/comments", boot.cfg.API.Prefix, ticket.ID), bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusCreated, recorder.Code, "body: %s", recorder.Body.String())
	var envelope utils.APIResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	data, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok, "data is an object")
	delivery, ok := data["delivery"].(map[string]interface{})
	require.True(t, ok, "the 201 carries the delivery")
	assert.Equal(t, models.TicketMailStatusLogOnly, delivery["status"])
	assert.Nil(t, delivery["sent_at"])
	assert.Equal(t, "", delivery["error_code"])

	var stored models.TicketMailDelivery
	require.NoError(t, models.DB.Where("ticket_id = ?", ticket.ID).Take(&stored).Error)
	assert.Equal(t, models.TicketMailStatusLogOnly, stored.Status, "the stored row is stamped")
	assert.Equal(t, customer.ID, stored.CustomerID)
}
