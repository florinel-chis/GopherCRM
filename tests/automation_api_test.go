package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
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

// HTTP roots of the follow-up automation API (issue #164): the real routes
// with their RequireRole guards behind the real JWT middleware, the real
// services and repositories on an in-memory SQLite database with the
// production one-connection pool.

type automationAPIHarness struct {
	t       *testing.T
	db      *gorm.DB
	router  *gin.Engine
	tokens  map[string]string
	users   map[string]*models.User
	repo    repository.AutomationRepository
	engine  *service.AutomationEngine
	service service.AutomationService
}

func newAutomationAPIHarness(t *testing.T) *automationAPIHarness {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:automation_api_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.Deal{}, &models.DealStageChange{},
		&models.Label{}, &models.Task{}, &models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{},
		&models.AutomationRun{}, &models.AutomationRunStep{}, &models.Notification{},
	))

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	automationRepo := repository.NewAutomationRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{Secret: "automation-api-regression-secret-32-bytes", ExpiryHours: 1})
	userService := service.NewUserService(userRepo)
	engine := service.NewAutomationEngine(automationRepo, notificationRepo, repository.NewTaskRepository(db), userRepo, nil, txManager)
	automationService := service.NewAutomationService(automationRepo, userRepo, engine, txManager)
	notificationService := service.NewNotificationService(notificationRepo)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupAutomationRoutes(protected, handler.NewAutomationHandler(automationService))
	handler.SetupNotificationRoutes(protected, handler.NewNotificationHandler(notificationService))

	h := &automationAPIHarness{t: t, db: db, router: router, tokens: map[string]string{}, users: map[string]*models.User{},
		repo: automationRepo, engine: engine, service: automationService}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{Email: fmt.Sprintf("%s@automation-api.test", role), FirstName: "Api", LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, userService.Register(user, "password123"))
		token, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		h.tokens[string(role)] = token
		h.users[string(role)] = user
	}
	other := &models.User{Email: "other-sales@automation-api.test", FirstName: "Other", LastName: "Sales", Role: models.RoleSales, IsActive: true}
	require.NoError(t, userService.Register(other, "password123"))
	token, err := authService.Login(other.Email, "password123")
	require.NoError(t, err)
	h.tokens["other"] = token
	h.users["other"] = other
	return h
}

func (h *automationAPIHarness) request(method, path, role string, body interface{}) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(h.t, err)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token, ok := h.tokens[role]; ok {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *automationAPIHarness) decode(rec *httptest.ResponseRecorder) utils.APIResponse {
	h.t.Helper()
	var response utils.APIResponse
	require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	return response
}

func (h *automationAPIHarness) errorCode(rec *httptest.ResponseRecorder) string {
	h.t.Helper()
	response := h.decode(rec)
	require.NotNil(h.t, response.Error, rec.Body.String())
	return response.Error.Code
}

func (h *automationAPIHarness) data(rec *httptest.ResponseRecorder, into interface{}) {
	h.t.Helper()
	response := h.decode(rec)
	require.True(h.t, response.Success, rec.Body.String())
	raw, err := json.Marshal(response.Data)
	require.NoError(h.t, err)
	require.NoError(h.t, json.Unmarshal(raw, into))
}

func (h *automationAPIHarness) createLead(owner *models.User) *models.Lead {
	h.t.Helper()
	lead := &models.Lead{FirstName: "Api", LastName: "Lead", Email: fmt.Sprintf("lead-%d@automation-api.test", time.Now().UnixNano()), OwnerID: owner.ID, Status: models.LeadStatusNew}
	require.NoError(h.t, h.db.Create(lead).Error)
	return lead
}

func (h *automationAPIHarness) createDeal(owner *models.User) *models.Deal {
	h.t.Helper()
	deal := &models.Deal{Title: "Api deal", Stage: models.DealStageProposal, Currency: "EUR", Probability: 40, OwnerID: owner.ID}
	require.NoError(h.t, h.db.Create(deal).Error)
	return deal
}

// validRuleBody is a lead_status_changed rule with four step types.
func (h *automationAPIHarness) validRuleBody() map[string]interface{} {
	return map[string]interface{}{
		"name":         "Qualified follow-up",
		"description":  "Chase qualified leads",
		"entity_type":  "lead",
		"trigger_type": "lead_status_changed",
		"trigger":      map[string]interface{}{"to": "qualified"},
		"enabled":      true,
		"steps": []map[string]interface{}{
			{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 60}},
			{"step_type": "condition", "config": map[string]interface{}{"field": "status", "operator": "eq", "value": "qualified", "else": "stop"}},
			{"step_type": "create_task", "config": map[string]interface{}{"title": "Call lead {{lead.id}}", "due_in_days": 2, "recipient": "owner"}},
			{"step_type": "notify", "config": map[string]interface{}{"title": "Lead {{lead.id}} is waiting", "body": "Please follow up", "recipient": "user", "user_id": h.users["sales"].ID}},
		},
	}
}

func (h *automationAPIHarness) withSteps(body map[string]interface{}, steps []map[string]interface{}) map[string]interface{} {
	body["steps"] = steps
	return body
}

// AC-1: the admin-only rule CRUD over HTTP.
func TestAutomationRuleAPISQLite(t *testing.T) {
	h := newAutomationAPIHarness(t)

	t.Run("create returns 201 with the steps numbered 1..n", func(t *testing.T) {
		rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", h.validRuleBody())
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var rule models.AutomationRule
		h.data(rec, &rule)
		assert.NotZero(t, rule.ID)
		assert.Equal(t, 1, rule.Version)
		assert.True(t, rule.Enabled)
		assert.Equal(t, "qualified", rule.Trigger.To)
		require.Len(t, rule.Steps, 4)
		for i, step := range rule.Steps {
			assert.Equal(t, i+1, step.Position)
		}
		assert.Equal(t, "wait", rule.Steps[0].StepType)
		assert.EqualValues(t, 60, rule.Steps[0].Config.DurationMinutes)
		assert.Equal(t, "owner", rule.Steps[2].Config.Recipient)
		assert.Equal(t, h.users["sales"].ID, rule.Steps[3].Config.UserID)

		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/rules/%d", rule.ID), "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var fetched models.AutomationRule
		h.data(rec, &fetched)
		assert.Equal(t, rule.ID, fetched.ID)
		assert.Len(t, fetched.Steps, 4)
	})

	t.Run("invalid rules are 400 INVALID_INPUT and write nothing", func(t *testing.T) {
		var rulesBefore, stepsBefore int64
		require.NoError(t, h.db.Model(&models.AutomationRule{}).Count(&rulesBefore).Error)
		require.NoError(t, h.db.Model(&models.AutomationStep{}).Count(&stepsBefore).Error)

		tooMany := make([]map[string]interface{}, 0, 21)
		for i := 0; i < 21; i++ {
			tooMany = append(tooMany, map[string]interface{}{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 1}})
		}
		bodies := map[string]map[string]interface{}{
			"invalid trigger": func() map[string]interface{} {
				body := h.validRuleBody()
				body["trigger_type"] = "deal_stage_changed"
				return body
			}(),
			"invalid trigger value": func() map[string]interface{} {
				body := h.validRuleBody()
				body["trigger"] = map[string]interface{}{"to": "sideways"}
				return body
			}(),
			"invalid step config": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 0}},
			}),
			"unknown step type": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "launch", "config": map[string]interface{}{}},
			}),
			"more than 20 steps": h.withSteps(h.validRuleBody(), tooMany),
			"more than 90 days of waits": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 60 * 24 * 60}},
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 31 * 24 * 60}},
			}),
			"backward else_position": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 1}},
				{"step_type": "condition", "config": map[string]interface{}{"field": "status", "operator": "eq", "value": "new", "else": "jump", "else_position": 1}},
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 1}},
			}),
			"out-of-range else_position": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "condition", "config": map[string]interface{}{"field": "status", "operator": "eq", "value": "new", "else": "jump", "else_position": 5}},
				{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 1}},
			}),
			"unknown placeholder": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "notify", "config": map[string]interface{}{"title": "Hello {{lead.password}}", "recipient": "owner"}},
			}),
			// A task survives the erasure of its lead, so a personal placeholder
			// that an email or notification may use is refused in a task.
			"personal placeholder in a task title": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "create_task", "config": map[string]interface{}{"title": "Call {{lead.first_name}}", "recipient": "owner"}},
			}),
			"personal placeholder in a task description": h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "create_task", "config": map[string]interface{}{"title": "Call", "description": "Dial {{lead.phone}}", "recipient": "owner"}},
			}),
			"no steps": h.withSteps(h.validRuleBody(), []map[string]interface{}{}),
		}
		for name, body := range bodies {
			rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", body)
			assert.Equalf(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
			assert.Equalf(t, apperrors.CodeInvalidInput, h.errorCode(rec), "%s", name)
		}

		var rulesAfter, stepsAfter int64
		require.NoError(t, h.db.Model(&models.AutomationRule{}).Count(&rulesAfter).Error)
		require.NoError(t, h.db.Model(&models.AutomationStep{}).Count(&stepsAfter).Error)
		assert.Equal(t, rulesBefore, rulesAfter, "no rule row was written")
		assert.Equal(t, stepsBefore, stepsAfter, "no step row was written")
	})

	t.Run("an unknown or inactive user is 400 INVALID_REFERENCE", func(t *testing.T) {
		inactive := &models.User{Email: "inactive@automation-api.test", Password: "x", FirstName: "In", LastName: "Active", Role: models.RoleSales, IsActive: true}
		require.NoError(t, h.db.Create(inactive).Error)
		require.NoError(t, h.db.Model(inactive).Update("is_active", false).Error)
		for name, userID := range map[string]uint{"unknown": 999999, "inactive": inactive.ID, "customer": h.users["customer"].ID} {
			body := h.withSteps(h.validRuleBody(), []map[string]interface{}{
				{"step_type": "assign_owner", "config": map[string]interface{}{"user_id": userID}},
			})
			rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", body)
			assert.Equalf(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
			assert.Equalf(t, apperrors.CodeInvalidReference, h.errorCode(rec), "%s", name)
		}
	})

	t.Run("update, enable, disable and delete", func(t *testing.T) {
		rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", h.validRuleBody())
		require.Equal(t, http.StatusCreated, rec.Code)
		var rule models.AutomationRule
		h.data(rec, &rule)

		body := h.withSteps(h.validRuleBody(), []map[string]interface{}{
			{"step_type": "notify", "config": map[string]interface{}{"title": "Changed", "recipient": "owner"}},
		})
		body["name"] = "Renamed"
		delete(body, "enabled")
		rec = h.request(http.MethodPut, fmt.Sprintf("/api/v1/automation/rules/%d", rule.ID), "admin", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var updated models.AutomationRule
		h.data(rec, &updated)
		assert.Equal(t, 2, updated.Version)
		assert.Equal(t, "Renamed", updated.Name)
		assert.True(t, updated.Enabled, "enabled absent keeps the switch")
		require.Len(t, updated.Steps, 1)
		assert.Equal(t, "notify", updated.Steps[0].StepType)

		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/disable", rule.ID), "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		h.data(rec, &updated)
		assert.False(t, updated.Enabled)
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/enable", rule.ID), "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		h.data(rec, &updated)
		assert.True(t, updated.Enabled)

		rec = h.request(http.MethodPut, fmt.Sprintf("/api/v1/automation/rules/%d", rule.ID), "admin", h.withSteps(h.validRuleBody(), []map[string]interface{}{}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, apperrors.CodeInvalidInput, h.errorCode(rec))

		rec = h.request(http.MethodDelete, fmt.Sprintf("/api/v1/automation/rules/%d", rule.ID), "admin", nil)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/rules/%d", rule.ID), "admin", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/enable", rule.ID), "admin", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = h.request(http.MethodPut, "/api/v1/automation/rules/999999", "admin", h.validRuleBody())
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("the list pages through the shared parser and refuses unknown sort columns", func(t *testing.T) {
		// The earlier subtests leave one live rule (the delete above soft-deleted
		// the other); a second one makes the second page non-empty.
		second := h.validRuleBody()
		second["name"] = "Second rule"
		rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", second)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		rec = h.request(http.MethodGet, "/api/v1/automation/rules?limit=0", "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		response := h.decode(rec)
		require.NotNil(t, response.Meta)
		assert.Equal(t, 20, response.Meta.PerPage, "limit=0 gives 20")
		assert.Equal(t, 1, response.Meta.Page)
		assert.GreaterOrEqual(t, response.Meta.Total, int64(2))

		rec = h.request(http.MethodGet, "/api/v1/automation/rules?limit=1&offset=1&sort_by=name&sort_order=asc", "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var rules []models.AutomationRule
		h.data(rec, &rules)
		assert.Len(t, rules, 1)

		rec = h.request(http.MethodGet, "/api/v1/automation/rules?sort_by=trigger_config", "admin", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = h.request(http.MethodGet, "/api/v1/automation/rules?enabled=maybe", "admin", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = h.request(http.MethodGet, "/api/v1/automation/rules?enabled=true&entity_type=lead", "admin", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("sales, support and customer get 403 on every rule route", func(t *testing.T) {
		for _, role := range []string{"sales", "support", "customer"} {
			for _, route := range []struct {
				method string
				path   string
				body   interface{}
			}{
				{http.MethodPost, "/api/v1/automation/rules", h.validRuleBody()},
				{http.MethodGet, "/api/v1/automation/rules", nil},
				{http.MethodGet, "/api/v1/automation/rules/1", nil},
				{http.MethodPut, "/api/v1/automation/rules/1", h.validRuleBody()},
				{http.MethodDelete, "/api/v1/automation/rules/1", nil},
				{http.MethodPost, "/api/v1/automation/rules/1/enable", nil},
				{http.MethodPost, "/api/v1/automation/rules/1/disable", nil},
				{http.MethodPost, "/api/v1/automation/rules/1/dry-run", map[string]interface{}{"entity_type": "lead", "entity_id": 1}},
			} {
				rec := h.request(route.method, route.path, role, route.body)
				assert.Equalf(t, http.StatusForbidden, rec.Code, "%s %s %s", role, route.method, route.path)
			}
		}
		rec := h.request(http.MethodGet, "/api/v1/automation/rules", "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// AC-12: the run history and the dry run over HTTP.
func TestAutomationRunAPISQLite(t *testing.T) {
	h := newAutomationAPIHarness(t)
	sales, other := h.users["sales"], h.users["other"]
	mine, theirs := h.createLead(sales), h.createLead(other)
	deal := h.createDeal(sales)

	rec := h.request(http.MethodPost, "/api/v1/automation/rules", "admin", h.withSteps(h.validRuleBody(), []map[string]interface{}{
		{"step_type": "wait", "config": map[string]interface{}{"duration_minutes": 60}},
		{"step_type": "notify", "config": map[string]interface{}{"title": "Lead {{lead.id}}", "recipient": "owner"}},
	}))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var rule models.AutomationRule
	h.data(rec, &rule)

	startRun := func(lead *models.Lead) models.AutomationRun {
		require.NoError(t, h.repo.RecordEvent(&models.AutomationEvent{EntityType: "lead", EntityID: lead.ID, EventType: "lead_status_changed", FromValue: "new", ToValue: "qualified", Origin: "user"}))
		started, err := h.engine.Dispatch(contextBackground())
		require.NoError(t, err)
		require.Equal(t, 1, started)
		var runs []models.AutomationRun
		require.NoError(t, h.db.Where("entity_id = ?", lead.ID).Order("id DESC").Find(&runs).Error)
		require.NotEmpty(t, runs)
		return runs[0]
	}
	myRun, theirRun := startRun(mine), startRun(theirs)
	require.NoError(t, h.engine.ExecuteDue(contextBackground()))

	t.Run("the list and the detail follow the role scope", func(t *testing.T) {
		rec := h.request(http.MethodGet, "/api/v1/automation/runs", "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var runs []models.AutomationRun
		h.data(rec, &runs)
		assert.Len(t, runs, 2)
		assert.EqualValues(t, 2, h.decode(rec).Meta.Total)

		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/runs?entity_type=lead&entity_id=%d", mine.ID), "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		h.data(rec, &runs)
		require.Len(t, runs, 1)
		assert.Equal(t, myRun.ID, runs[0].ID)
		assert.Equal(t, models.AutomationRunWaiting, runs[0].Status)
		rec = h.request(http.MethodGet, "/api/v1/automation/runs", "sales", nil)
		h.data(rec, &runs)
		assert.Len(t, runs, 1, "the sales user sees only its own lead's run")
		assert.EqualValues(t, 1, h.decode(rec).Meta.Total)

		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/runs/%d", myRun.ID), "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var run models.AutomationRun
		h.data(rec, &run)
		assert.Equal(t, myRun.ID, run.ID)
		assert.Len(t, run.Steps, 1, "the detail carries the step log")
		assert.Equal(t, models.AutomationOutcomeWaiting, run.Steps[0].Outcome)

		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/runs/%d", theirRun.ID), "sales", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, utils.ErrCodeNotFound, h.errorCode(rec))
		rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/runs/%d", theirRun.ID), "admin", nil)
		assert.Equal(t, http.StatusOK, rec.Code)

		for _, role := range []string{"support", "customer"} {
			rec = h.request(http.MethodGet, "/api/v1/automation/runs", role, nil)
			assert.Equalf(t, http.StatusForbidden, rec.Code, role)
			rec = h.request(http.MethodGet, fmt.Sprintf("/api/v1/automation/runs/%d", myRun.ID), role, nil)
			assert.Equalf(t, http.StatusForbidden, rec.Code, role)
		}
		rec = h.request(http.MethodGet, "/api/v1/automation/runs?sort_by=rule_id", "admin", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = h.request(http.MethodGet, "/api/v1/automation/runs?status=paused", "admin", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = h.request(http.MethodGet, "/api/v1/automation/runs?status=waiting&sort_by=resume_at&sort_order=asc", "admin", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("cancel is admin-only and refuses a finished run", func(t *testing.T) {
		rec := h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/runs/%d/cancel", myRun.ID), "sales", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)

		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/runs/%d/cancel", myRun.ID), "admin", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var run models.AutomationRun
		h.data(rec, &run)
		assert.Equal(t, models.AutomationRunCancelled, run.Status)
		assert.Equal(t, models.AutomationCancelManual, run.CancelReason)

		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/runs/%d/cancel", myRun.ID), "admin", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, apperrors.CodeInvalidStatusTransition, h.errorCode(rec))
		rec = h.request(http.MethodPost, "/api/v1/automation/runs/999999/cancel", "admin", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("dry run", func(t *testing.T) {
		rec := h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/dry-run", rule.ID), "admin", map[string]interface{}{"entity_type": "lead", "entity_id": mine.ID})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var report models.AutomationDryRunReport
		h.data(rec, &report)
		assert.Equal(t, rule.ID, report.RuleID)
		require.Len(t, report.Steps, 2)
		assert.Equal(t, models.AutomationOutcomeWaiting, report.Steps[0].Outcome)
		assert.EqualValues(t, sales.ID, report.Steps[1].Details["recipient_id"])

		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/dry-run", rule.ID), "admin", map[string]interface{}{"entity_type": "deal", "entity_id": deal.ID})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, apperrors.CodeInvalidInput, h.errorCode(rec))
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/dry-run", rule.ID), "admin", map[string]interface{}{"entity_type": "lead", "entity_id": 999999})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = h.request(http.MethodPost, "/api/v1/automation/rules/999999/dry-run", "admin", map[string]interface{}{"entity_type": "lead", "entity_id": mine.ID})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/automation/rules/%d/dry-run", rule.ID), "sales", map[string]interface{}{"entity_type": "lead", "entity_id": mine.ID})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var notifications int64
		require.NoError(t, h.db.Model(&models.Notification{}).Count(&notifications).Error)
		assert.Zero(t, notifications, "the dry run notified nobody")
	})
}
