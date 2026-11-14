package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #213 handler roots: the create_ticket flag on POST /forms and PUT
// /forms/{id} through the real form service, and the idempotency_key check of
// the public submission route.

type formTicketFlagEnv struct {
	db     *gorm.DB
	router *gin.Engine
	role   models.UserRole
	owner  *models.User
}

func newFormTicketFlagEnv(t *testing.T) *formTicketFlagEnv {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", "form-ticket-handler-secret-32-characters")

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}, &models.FormMailDelivery{}))

	env := &formTicketFlagEnv{db: db, role: models.RoleAdmin}
	env.owner = &models.User{Email: "owner@form-ticket.test", Password: "x", FirstName: "Own", LastName: "Er", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(env.owner).Error)

	formService := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db),
		repository.NewUserRepository(db), nil, utils.NewTransactionManager(db), config.FormsConfig{}, "/api/v1")
	env.router = gin.New()
	env.router.Use(func(c *gin.Context) {
		c.Set("user_id", env.owner.ID)
		c.Set("user_role", string(env.role))
		c.Next()
	})
	env.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	SetupFormRoutes(env.router.Group(""), NewFormHandler(formService))
	return env
}

func (e *formTicketFlagEnv) do(t *testing.T, method, path string, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	var envelope map[string]interface{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
	return recorder.Code, envelope
}

func (e *formTicketFlagEnv) formCount(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, e.db.Model(&models.Form{}).Count(&count).Error)
	return count
}

func ticketFormBody(overrides map[string]interface{}) map[string]interface{} {
	body := map[string]interface{}{
		"name":   "Complaints",
		"status": "published",
		"fields": []map[string]interface{}{
			{"name": "email", "label": "Email", "type": "email", "required": true},
			{"name": "complaint", "label": "Complaint", "type": "textarea", "required": true},
		},
		"double_opt_in": true,
	}
	for key, value := range overrides {
		if value == nil {
			delete(body, key)
			continue
		}
		body[key] = value
	}
	return body
}

func TestFormTicketFlagHandler(t *testing.T) {
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			env := newFormTicketFlagEnv(t)
			env.role = role

			// create_ticket true, create_lead absent: stored false, no owner needed.
			code, envelope := env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{"create_ticket": true}))
			require.Equal(t, http.StatusCreated, code, "%v", envelope)
			data := envelope["data"].(map[string]interface{})
			assert.Equal(t, true, data["create_ticket"])
			assert.Equal(t, false, data["create_lead"], "create_lead absent with create_ticket means false")
			formID := uint(data["id"].(float64))
			var stored models.Form
			require.NoError(t, env.db.First(&stored, formID).Error)
			assert.True(t, stored.CreateTicket)
			assert.False(t, stored.CreateLead)
			assert.True(t, stored.DoubleOptIn)
			assert.Zero(t, stored.DefaultOwnerID)

			// create_ticket absent: false, and create_lead keeps its old default.
			code, envelope = env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{"default_owner_id": env.owner.ID}))
			require.Equal(t, http.StatusCreated, code, "%v", envelope)
			data = envelope["data"].(map[string]interface{})
			assert.Equal(t, false, data["create_ticket"])
			assert.Equal(t, true, data["create_lead"])

			// create_ticket false explicitly.
			code, envelope = env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{"create_ticket": false, "create_lead": false}))
			require.Equal(t, http.StatusCreated, code, "%v", envelope)
			assert.Equal(t, false, envelope["data"].(map[string]interface{})["create_ticket"])

			before := env.formCount(t)
			// create_ticket with an explicit create_lead true: 400, nothing written.
			code, envelope = env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{
				"create_ticket": true, "create_lead": true, "default_owner_id": env.owner.ID,
			}))
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, utils.ErrCodeValidation, envelope["error"].(map[string]interface{})["code"])
			assert.Contains(t, envelope["error"].(map[string]interface{})["message"], "invalid form definition")
			// create_ticket without double opt-in: 400, nothing written.
			code, envelope = env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{
				"create_ticket": true, "double_opt_in": false,
			}))
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, utils.ErrCodeValidation, envelope["error"].(map[string]interface{})["code"])
			assert.Equal(t, before, env.formCount(t), "a refused definition writes nothing")

			// PUT replaces the whole definition: an absent create_ticket clears it.
			path := fmt.Sprintf("/forms/%d", formID)
			code, envelope = env.do(t, http.MethodPut, path, ticketFormBody(map[string]interface{}{"create_lead": false}))
			require.Equal(t, http.StatusOK, code, "%v", envelope)
			require.NoError(t, env.db.First(&stored, formID).Error)
			assert.False(t, stored.CreateTicket, "an update without create_ticket stores false")

			// PUT with create_ticket true and create_lead absent stores create_lead false.
			code, envelope = env.do(t, http.MethodPut, path, ticketFormBody(map[string]interface{}{"create_ticket": true}))
			require.Equal(t, http.StatusOK, code, "%v", envelope)
			require.NoError(t, env.db.First(&stored, formID).Error)
			assert.True(t, stored.CreateTicket)
			assert.False(t, stored.CreateLead)

			// The refused updates leave the stored form as it was.
			code, _ = env.do(t, http.MethodPut, path, ticketFormBody(map[string]interface{}{
				"create_ticket": true, "create_lead": true, "default_owner_id": env.owner.ID,
			}))
			assert.Equal(t, http.StatusBadRequest, code)
			code, _ = env.do(t, http.MethodPut, path, ticketFormBody(map[string]interface{}{"create_ticket": true, "double_opt_in": false}))
			assert.Equal(t, http.StatusBadRequest, code)
			require.NoError(t, env.db.First(&stored, formID).Error)
			assert.True(t, stored.CreateTicket)
			assert.False(t, stored.CreateLead)
			assert.True(t, stored.DoubleOptIn)
		})
	}

	t.Run("support may not write", func(t *testing.T) {
		env := newFormTicketFlagEnv(t)
		env.role = models.RoleSupport
		code, _ := env.do(t, http.MethodPost, "/forms", ticketFormBody(map[string]interface{}{"create_ticket": true}))
		assert.Equal(t, http.StatusForbidden, code, "the write guard is unchanged")
		assert.Zero(t, env.formCount(t))
	})
}

func TestFormPublicSubmitIdempotencyKeyValidation(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	gin.SetMode(gin.TestMode)
	require.NoError(t, os.Setenv("DISABLE_RATE_LIMIT", "true"))
	t.Cleanup(func() { _ = os.Unsetenv("DISABLE_RATE_LIMIT") })

	var logs bytes.Buffer
	utils.Logger.SetOutput(&logs)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })

	stub := &formPublicServiceStub{outcome: &service.SubmitOutcome{Action: models.FormSubmitActionMessage, Message: "Thank you."}}
	router := gin.New()
	SetupFormPublicRoutes(router.Group("/api/v1"), NewFormPublicHandler(stub, config.FormsConfig{}, "/api/v1"))

	submit := func(raw string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions", strings.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	body := func(key string) string {
		encoded, err := json.Marshal(key)
		require.NoError(t, err)
		return `{"values":{"email":"a@example.com"},"challenge":"c","idempotency_key":` + string(encoded) + `}`
	}

	invalid := []string{
		"",
		"has space",
		"semi;colon",
		"slash/key",
		"ümlaut",
		strings.Repeat("k", 129),
		"submission-42",
		"submission-",
		"key\nnewline",
		"Distinctive<Script>Key",
	}
	for _, key := range invalid {
		recorder := submit(body(key))
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "key %q", key)
		var envelope struct {
			Success bool
			Error   struct {
				Code    string                 `json:"code"`
				Details map[string]interface{} `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code, "key %q", key)
		assert.Contains(t, envelope.Error.Details, "idempotency_key", "key %q", key)
		if key != "" {
			assert.NotContains(t, recorder.Body.String(), key, "the value is not echoed")
			assert.NotContains(t, logs.String(), key, "the value is not logged")
		}
	}
	assert.Zero(t, stub.submitCalls, "no service call for a refused key")

	// A JSON null is an absent key.
	recorder := submit(`{"values":{"email":"a@example.com"},"challenge":"c","idempotency_key":null}`)
	assert.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, stub.submitCalls)
	assert.Nil(t, stub.lastRequest.IdempotencyKey)

	// Absent and valid keys reach the service unchanged.
	recorder = submit(`{"values":{"email":"a@example.com"},"challenge":"c"}`)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Nil(t, stub.lastRequest.IdempotencyKey)
	for _, key := range []string{"k", "Order-2026.11:A_b-c", strings.Repeat("K", 128), "Submission-1", "xsubmission-1"} {
		recorder = submit(body(key))
		assert.Equal(t, http.StatusOK, recorder.Code, "key %q", key)
		require.NotNil(t, stub.lastRequest.IdempotencyKey)
		assert.Equal(t, key, *stub.lastRequest.IdempotencyKey)
	}
}
