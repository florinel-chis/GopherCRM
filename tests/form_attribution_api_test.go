package tests

// Form attribution and the form-submitted event over HTTP (issue #220): the
// real public submission route, the real form and automation routes behind
// the real JWT and role middleware, the real services, engine and
// repositories on SQLite with the production one-connection pool.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/forms"
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

const formAttributionAPISecret = "form-attribution-api-regression-secret-32"

type formAttributionAPIError struct {
	Code string `json:"code"`
}

type formAttributionEnvelope struct {
	Success bool                     `json:"success"`
	Data    json.RawMessage          `json:"data"`
	Error   *formAttributionAPIError `json:"error"`
}

func TestFormAttributionAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formAttributionAPISecret)

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:form_attribution_api_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.Deal{},
		&models.Label{}, &models.Task{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}, &models.FormMailDelivery{},
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{},
		&models.AutomationRun{}, &models.AutomationRunStep{}, &models.Notification{},
	))

	userRepo := repository.NewUserRepository(db)
	automationRepo := repository.NewAutomationRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db), config.JWTConfig{Secret: formAttributionAPISecret, ExpiryHours: 1})
	formsCfg := config.FormsConfig{PublicBaseURL: "https://crm.example.test"}
	formService := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db), userRepo, nil,
		txManager, formsCfg, "/api/v1", service.WithFormAutomationEvents(automationRepo))
	engine := service.NewAutomationEngine(automationRepo, repository.NewNotificationRepository(db), repository.NewTaskRepository(db), userRepo, nil, txManager)
	automationService := service.NewAutomationService(automationRepo, userRepo, engine, txManager)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	api := router.Group("/api/v1")
	handler.SetupFormPublicRoutes(api, handler.NewFormPublicHandler(formService, formsCfg, "/api/v1"))
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupFormRoutes(protected, handler.NewFormHandler(formService))
	handler.SetupAutomationRoutes(protected, handler.NewAutomationHandler(automationService))

	tokens := map[models.UserRole]string{}
	users := map[models.UserRole]*models.User{}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
		user := &models.User{Email: fmt.Sprintf("%s@form-attribution.test", role), FirstName: "Api", LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, service.NewUserService(userRepo).Register(user, "password123"))
		bearer, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		tokens[role], users[role] = bearer, user
	}

	call := func(t *testing.T, role models.UserRole, method, path string, body interface{}) (*httptest.ResponseRecorder, formAttributionEnvelope) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(encoded)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, reader)
		req.Header.Set("Content-Type", "application/json")
		if token, ok := tokens[role]; ok {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope formAttributionEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		return rec, envelope
	}

	form := &models.Form{
		Name:   "Campaign landing",
		Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			{Name: "utm_source", Type: models.FormFieldHidden},
		},
		CreateLead:     true,
		DefaultOwnerID: users[models.RoleSales].ID,
	}
	require.NoError(t, formService.Create(form, users[models.RoleAdmin].ID))
	other := &models.Form{
		Name:           "Other form",
		Status:         models.FormStatusPublished,
		Fields:         []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		CreateLead:     true,
		DefaultOwnerID: users[models.RoleSales].ID,
	}
	require.NoError(t, formService.Create(other, users[models.RoleAdmin].ID))

	var ruleID uint
	t.Run("an admin creates a lead_form_submitted rule narrowed to one form", func(t *testing.T) {
		rec, envelope := call(t, models.RoleAdmin, http.MethodPost, "/automation/rules", map[string]interface{}{
			"name":         "Spring campaign follow-up",
			"entity_type":  "lead",
			"trigger_type": "lead_form_submitted",
			"trigger":      map[string]interface{}{"form_id": form.ID},
			"enabled":      true,
			"steps": []map[string]interface{}{
				{"step_type": "condition", "config": map[string]interface{}{"field": "utm_campaign", "operator": "eq", "value": "spring", "else": "stop"}},
				{"step_type": "create_task", "config": map[string]interface{}{"title": "Spring lead {{lead.id}}", "due_in_days": 1, "recipient": "owner"}},
			},
		})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var rule models.AutomationRule
		require.NoError(t, json.Unmarshal(envelope.Data, &rule))
		assert.Equal(t, models.AutomationEventLeadFormSubmitted, rule.TriggerType)
		assert.Equal(t, form.ID, rule.Trigger.FormID)
		ruleID = rule.ID
	})
	require.NotZero(t, ruleID)

	submit := func(t *testing.T, key, pageURL string, values map[string]string) {
		t.Helper()
		rec, envelope := call(t, "", http.MethodPost, "/forms/public/"+key+"/submissions", map[string]interface{}{
			"values":    values,
			"challenge": forms.NewChallenge([]byte(formAttributionAPISecret), time.Now().Add(-30*time.Second)),
			"page_url":  pageURL,
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, envelope.Success)
	}

	var springID, autumnID uint
	t.Run("two public submissions from one address start one run each", func(t *testing.T) {
		submit(t, form.PublicID, "https://www.shop.example:8443/spring?utm_campaign=Spring&utm_medium=Email",
			map[string]string{"email": "visitor@example.com", "utm_source": "Newsletter"})
		submit(t, form.PublicID, "https://blog.partner.example/post?utm_campaign=Autumn&utm_source=ignored",
			map[string]string{"email": "visitor@example.com", "utm_source": "Partner"})
		// The other form's enquiry does not reach the narrowed rule.
		submit(t, other.PublicID, "https://www.shop.example/?utm_campaign=Spring", map[string]string{"email": "visitor@example.com"})

		var submissions []models.FormSubmission
		require.NoError(t, db.Where("form_id = ?", form.ID).Order("id").Find(&submissions).Error)
		require.Len(t, submissions, 2)
		springID, autumnID = submissions[0].ID, submissions[1].ID
		require.NotNil(t, submissions[0].LeadID)
		require.NotNil(t, submissions[1].LeadID)
		assert.Equal(t, *submissions[0].LeadID, *submissions[1].LeadID, "one lead for the address")

		_, err := engine.Dispatch(context.Background())
		require.NoError(t, err)
		require.NoError(t, engine.ExecuteDue(context.Background()))

		rec, envelope := call(t, models.RoleAdmin, http.MethodGet, fmt.Sprintf("/automation/runs?rule_id=%d", ruleID), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var runs []models.AutomationRun
		require.NoError(t, json.Unmarshal(envelope.Data, &runs))
		require.Len(t, runs, 2, "one run per enquiry of the narrowed form")
		for _, run := range runs {
			assert.Equal(t, *submissions[0].LeadID, run.EntityID)
			assert.Equal(t, models.AutomationRunCompleted, run.Status)
		}
		var tasks int64
		require.NoError(t, db.Model(&models.Task{}).Count(&tasks).Error)
		assert.EqualValues(t, 1, tasks, "only the spring enquiry passed the attribution condition")

		var lead models.Lead
		require.NoError(t, db.First(&lead, *submissions[0].LeadID).Error)
		assert.Equal(t, "Campaign landing", lead.Source, "the lead keeps the form name")
	})

	t.Run("each submission read returns its own attribution", func(t *testing.T) {
		want := map[uint]map[string]string{
			springID: {"utm_source": "newsletter", "utm_medium": "email", "utm_campaign": "spring", "utm_term": "", "utm_content": "", "referrer_host": "www.shop.example"},
			autumnID: {"utm_source": "partner", "utm_medium": "", "utm_campaign": "autumn", "utm_term": "", "utm_content": "", "referrer_host": "blog.partner.example"},
		}
		for id, fields := range want {
			rec, envelope := call(t, models.RoleSales, http.MethodGet, fmt.Sprintf("/forms/submissions/%d", id), nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(envelope.Data, &body))
			for key, value := range fields {
				assert.Equal(t, value, body[key], "submission %d %s", id, key)
			}
		}
	})

	t.Run("a deal rule on lead_form_submitted is 400 INVALID_INPUT naming trigger_type", func(t *testing.T) {
		before := int64(0)
		require.NoError(t, db.Model(&models.AutomationRule{}).Count(&before).Error)
		rec, envelope := call(t, models.RoleAdmin, http.MethodPost, "/automation/rules", map[string]interface{}{
			"name":         "Misplaced",
			"entity_type":  "deal",
			"trigger_type": "lead_form_submitted",
			"enabled":      true,
			"steps":        []map[string]interface{}{{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 5}}},
		})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "INVALID_INPUT", envelope.Error.Code)
		fields := fieldErrors(t, rec)
		require.NotEmpty(t, fields)
		assert.Equal(t, "trigger_type", fields[0]["field"])
		assert.Equal(t, "invalid_value", fields[0]["code"])
		after := int64(0)
		require.NoError(t, db.Model(&models.AutomationRule{}).Count(&after).Error)
		assert.Equal(t, before, after, "nothing was written")
	})
}
