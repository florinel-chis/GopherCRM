package main

// Issue #213 through the router production uses: setupDependencies builds
// FormService with WithFormTicketIntake(formTicketRepo, txManager), so
// confirming a create_ticket submission opens a ticket and records
// ticket_created. Over a SQLite file opened by the real connector.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestFormTicketIntakeProductionWiring(t *testing.T) {
	// The form service keys its challenges and confirmation tokens with
	// API_KEY_SECRET (else JWT_SECRET), read when the router is built.
	const tokenSecret = "form-ticket-wiring-token-secret-32-chars"
	t.Setenv("API_KEY_SECRET", tokenSecret)
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	boot := bootAutomation(t, false)

	admin := &models.User{Email: "admin@form-ticket-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	customer := &models.Customer{FirstName: "Cora", LastName: "Customer", Email: "cora@form-ticket-wiring.test"}
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
	bearer, err := auth.GenerateJWT(admin)
	require.NoError(t, err)
	call := func(method, path string, body interface{}, authed bool) (int, []byte) {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		if authed {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code, recorder.Body.Bytes()
	}

	status, raw := call(http.MethodPost, "/forms", map[string]interface{}{
		"name":   "Wired complaints",
		"status": "published",
		"fields": []map[string]interface{}{
			{"name": "email", "label": "Email", "type": "email", "required": true},
			{"name": "complaint", "label": "Complaint", "type": "textarea", "required": true},
		},
		"double_opt_in": true,
		"create_ticket": true,
	}, true)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var created utils.APIResponse
	require.NoError(t, json.Unmarshal(raw, &created))
	formData := created.Data.(map[string]interface{})
	publicID := formData["public_id"].(string)
	formID := uint(formData["id"].(float64))

	status, raw = call(http.MethodPost, "/forms/public/"+publicID+"/submissions", map[string]interface{}{
		"values":          map[string]string{"email": customer.Email, "complaint": "Wired complaint"},
		"challenge":       forms.NewChallenge([]byte(tokenSecret), time.Now().Add(-30*time.Second)),
		"idempotency_key": "wired-key-1",
	}, false)
	require.Equal(t, http.StatusOK, status, string(raw))

	var submission models.FormSubmission
	require.NoError(t, models.DB.Where("form_id = ?", formID).Order("id desc").First(&submission).Error)
	require.Equal(t, models.FormSubmissionPending, submission.Status)
	require.Equal(t, "wired-key-1", submission.IdempotencyKey)

	// The development mailer only logs, so the stored token is re-keyed to a
	// raw value this test knows, hashed the way the service hashes it.
	const raw2 = "wired-confirmation-token"
	mac := hmac.New(sha256.New, []byte(tokenSecret))
	mac.Write([]byte(raw2))
	require.NoError(t, models.DB.Model(&models.FormConfirmationToken{}).Where("submission_id = ?", submission.ID).
		Update("token_hash", hex.EncodeToString(mac.Sum(nil))).Error)

	request := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+"/forms/public/confirm",
		strings.NewReader(url.Values{"token": {raw2}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Email confirmed")

	require.NoError(t, models.DB.First(&submission, submission.ID).Error)
	assert.Equal(t, models.FormSubmissionConfirmed, submission.Status)
	assert.Equal(t, models.FormTicketOutcomeCreated, submission.TicketOutcome, "the production router wires ticket intake")
	require.NotNil(t, submission.TicketID)

	var ticket models.Ticket
	require.NoError(t, models.DB.First(&ticket, *submission.TicketID).Error)
	assert.Equal(t, customer.ID, ticket.CustomerID)
	assert.Equal(t, "Form: Wired complaints", ticket.Title)
	var keys int64
	require.NoError(t, models.DB.Model(&models.FormTicketKey{}).Where("form_id = ? AND idempotency_key = ?", formID, "wired-key-1").Count(&keys).Error)
	assert.EqualValues(t, 1, keys)

	var events []models.AutomationEvent
	require.NoError(t, models.DB.Where("entity_type = ? AND entity_id = ? AND event_type = ?",
		models.AutomationEntityTicket, ticket.ID, models.AutomationEventTicketCreated).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.AutomationOriginForm, events[0].Origin)
	assert.Nil(t, events[0].ActorID)
}
