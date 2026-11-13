package handler

// AC-11 of #242: the automation handlers accept entity_type ticket in the
// GET /automation/rules and GET /automation/runs filters and in both dry-run
// bodies (binding oneof=lead deal ticket); any other filter value is still
// 400 BAD_REQUEST, now with the message "entity_type must be lead, deal or
// ticket", and no service is called.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAutomationService records the filters and dry-run arguments the
// handler passes on; every other method is unused here.
type recordingAutomationService struct {
	service.AutomationService
	ruleFilters []repository.AutomationRuleListFilter
	runFilters  []repository.AutomationRunListFilter
	dryRuns     []string
	drafts      []string
}

func (s *recordingAutomationService) ListRules(offset, limit int, filter repository.AutomationRuleListFilter) ([]models.AutomationRule, int64, error) {
	s.ruleFilters = append(s.ruleFilters, filter)
	return []models.AutomationRule{}, 0, nil
}

func (s *recordingAutomationService) ListRuns(viewer service.LeadViewer, offset, limit int, filter repository.AutomationRunListFilter) ([]models.AutomationRun, int64, error) {
	s.runFilters = append(s.runFilters, filter)
	return []models.AutomationRun{}, 0, nil
}

func (s *recordingAutomationService) DryRun(ruleID uint, entityType string, entityID uint) (*models.AutomationDryRunReport, error) {
	s.dryRuns = append(s.dryRuns, entityType)
	return &models.AutomationDryRunReport{RuleID: ruleID, EntityType: entityType, EntityID: entityID, Steps: []models.AutomationDryRunStep{}}, nil
}

func (s *recordingAutomationService) DryRunDraft(rule *models.AutomationRule, entityType string, entityID uint) (*models.AutomationDryRunReport, error) {
	s.drafts = append(s.drafts, entityType)
	return &models.AutomationDryRunReport{Draft: true, EntityType: entityType, EntityID: entityID, Steps: []models.AutomationDryRunStep{}}, nil
}

func TestAutomationHandlerTicketEntity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	newRouter := func(svc service.AutomationService, role models.UserRole) *gin.Engine {
		router := gin.New()
		router.Use(middleware.ErrorHandler())
		router.Use(func(c *gin.Context) {
			c.Set("user_id", uint(1))
			c.Set("user_role", string(role))
			c.Next()
		})
		SetupAutomationRoutes(router.Group("/api/v1"), NewAutomationHandler(svc))
		return router
	}
	type envelope struct {
		Success bool `json:"success"`
		Data    struct {
			EntityType string `json:"entity_type"`
			Draft      bool   `json:"draft"`
		} `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	do := func(router *gin.Engine, method, path string, body interface{}) (int, envelope) {
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var out envelope
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	t.Run("list filters accept ticket", func(t *testing.T) {
		svc := &recordingAutomationService{}
		router := newRouter(svc, models.RoleAdmin)
		for _, entity := range []string{"lead", "deal", "ticket"} {
			code, _ := do(router, http.MethodGet, "/api/v1/automation/rules?entity_type="+entity, nil)
			assert.Equal(t, http.StatusOK, code, entity)
			code, _ = do(router, http.MethodGet, "/api/v1/automation/runs?entity_type="+entity, nil)
			assert.Equal(t, http.StatusOK, code, entity)
		}
		require.Len(t, svc.ruleFilters, 3)
		require.Len(t, svc.runFilters, 3)
		assert.Equal(t, models.AutomationEntityTicket, svc.ruleFilters[2].EntityType)
		assert.Equal(t, models.AutomationEntityTicket, svc.runFilters[2].EntityType)

		// A sales caller passes the ticket filter on; the service scope keeps
		// ticket runs out of its list.
		sales := &recordingAutomationService{}
		code, _ := do(newRouter(sales, models.RoleSales), http.MethodGet, "/api/v1/automation/runs?entity_type=ticket", nil)
		assert.Equal(t, http.StatusOK, code)
		require.Len(t, sales.runFilters, 1)
	})

	t.Run("other filter values are refused before the service", func(t *testing.T) {
		svc := &recordingAutomationService{}
		router := newRouter(svc, models.RoleAdmin)
		for _, entity := range []string{"company", "Ticket", "tickets", "customer"} {
			for _, path := range []string{"/api/v1/automation/rules", "/api/v1/automation/runs"} {
				code, body := do(router, http.MethodGet, path+"?entity_type="+entity, nil)
				assert.Equal(t, http.StatusBadRequest, code, path+" "+entity)
				assert.Equal(t, "BAD_REQUEST", body.Error.Code)
				assert.Equal(t, "entity_type must be lead, deal or ticket", body.Error.Message)
			}
		}
		assert.Empty(t, svc.ruleFilters)
		assert.Empty(t, svc.runFilters)
	})

	t.Run("both dry-run bodies bind ticket", func(t *testing.T) {
		svc := &recordingAutomationService{}
		router := newRouter(svc, models.RoleAdmin)
		code, body := do(router, http.MethodPost, "/api/v1/automation/rules/7/dry-run",
			map[string]interface{}{"entity_type": "ticket", "entity_id": 42})
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, models.AutomationEntityTicket, body.Data.EntityType)

		rule := map[string]interface{}{"name": "Draft", "entity_type": "ticket", "trigger_type": "ticket_status_changed"}
		rule["trigger"] = map[string]interface{}{"to": "resolved"}
		step := map[string]interface{}{"step_type": "notify", "config": map[string]interface{}{"title": "{{ticket.number}}"}}
		rule["steps"] = []interface{}{step}
		draft := map[string]interface{}{"rule": rule, "entity_type": "ticket", "entity_id": 42}
		code, body = do(router, http.MethodPost, "/api/v1/automation/rules/dry-run", draft)
		require.Equal(t, http.StatusOK, code)
		assert.True(t, body.Data.Draft)
		assert.Equal(t, []string{models.AutomationEntityTicket}, svc.dryRuns)
		assert.Equal(t, []string{models.AutomationEntityTicket}, svc.drafts)

		code, _ = do(router, http.MethodPost, "/api/v1/automation/rules/7/dry-run",
			map[string]interface{}{"entity_type": "company", "entity_id": 42})
		assert.Equal(t, http.StatusBadRequest, code, "the binding still refuses other values")
		draft["entity_type"] = "company"
		code, _ = do(router, http.MethodPost, "/api/v1/automation/rules/dry-run", draft)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Len(t, svc.dryRuns, 1)
		assert.Len(t, svc.drafts, 1)
	})
}
