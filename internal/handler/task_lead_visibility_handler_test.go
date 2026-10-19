package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// hiddenLeadEmail is the lead data that must never reach a caller who may not
// see the lead.
const hiddenLeadEmail = "someone-elses-lead@example.com"

// taskLinkedToLead is a task assigned to assigneeID and linked to lead 30,
// owned by leadOwnerID, with the lead preloaded as the repository does.
func taskLinkedToLead(assigneeID, leadOwnerID uint) *models.Task {
	leadID := uint(30)
	return &models.Task{
		BaseModel:    models.BaseModel{ID: 5},
		Title:        "Call back",
		Status:       models.TaskStatusPending,
		AssignedToID: assigneeID,
		LeadID:       &leadID,
		Lead:         &models.Lead{BaseModel: models.BaseModel{ID: 30}, Email: hiddenLeadEmail, OwnerID: leadOwnerID},
	}
}

func newTaskLeadRouter(svc *MockTaskService, role models.UserRole, userID uint) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupTaskRoutes(router.Group(""), NewTaskHandler(svc))
	return router
}

func serveJSON(router *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// The assignee of a task linked to another rep's lead (an admin made the link,
// or the lead changed hands) gets the task without the lead: neither its data
// nor its id.
func TestTaskGet_HidesALeadTheCallerMayNotSee(t *testing.T) {
	for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport} {
		svc := new(MockTaskService)
		svc.On("GetByID", uint(5)).Return(taskLinkedToLead(2, 9), nil)

		w := serveJSON(newTaskLeadRouter(svc, role, 2), http.MethodGet, "/tasks/5", nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), hiddenLeadEmail, role)
		assert.NotContains(t, w.Body.String(), `"lead_id"`, role)
		assert.NotContains(t, w.Body.String(), `"lead"`, role)
		assert.Contains(t, w.Body.String(), "Call back", "the rest of the task is still there")
		svc.AssertExpectations(t)
	}
}

func TestTaskGet_ShowsTheLeadToItsOwnerAndToAdmins(t *testing.T) {
	for _, tc := range []struct {
		role   models.UserRole
		userID uint
		task   *models.Task
	}{
		{models.RoleSales, 2, taskLinkedToLead(2, 2)},
		{models.RoleAdmin, 1, taskLinkedToLead(2, 9)},
	} {
		svc := new(MockTaskService)
		svc.On("GetByID", uint(5)).Return(tc.task, nil)

		w := serveJSON(newTaskLeadRouter(svc, tc.role, tc.userID), http.MethodGet, "/tasks/5", nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"lead_id":30`, tc.role)
		assert.Contains(t, w.Body.String(), hiddenLeadEmail, tc.role)
	}
}

// Every listing a non-admin reaches is redacted row by row.
func TestTaskListings_HideLeadsTheCallerMayNotSee(t *testing.T) {
	visible, hidden := *taskLinkedToLead(2, 2), *taskLinkedToLead(2, 9)
	visible.ID, hidden.ID = 6, 7
	visible.Lead.Email = "my-own-lead@example.com"
	visible.LeadID = new(uint)
	*visible.LeadID = 31
	visible.Lead.ID = 31

	for _, path := range []string{"/tasks", "/tasks/my", "/tasks?label_id=4", "/tasks/upcoming"} {
		svc := new(MockTaskService)
		rows := func() []models.Task {
			a, b := visible, hidden
			leadA, leadB := *visible.Lead, *hidden.Lead
			a.Lead, b.Lead = &leadA, &leadB
			idA, idB := *visible.LeadID, *hidden.LeadID
			a.LeadID, b.LeadID = &idA, &idB
			return []models.Task{a, b}
		}
		svc.On("GetByAssignee", uint(2), 0, 20).Return(rows(), int64(2), nil).Maybe()
		svc.On("ListByLabelForAssignee", uint(2), uint(4), 0, 20, "", "asc").Return(rows(), int64(2), nil).Maybe()
		svc.On("GetDueWithinByAssignee", uint(2), mock.Anything, mock.Anything, 100).Return(rows(), nil).Maybe()

		w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
		assert.NotContains(t, w.Body.String(), hiddenLeadEmail, path)
		assert.Contains(t, w.Body.String(), "my-own-lead@example.com", "%s: the caller's own lead stays", path)
		assert.Contains(t, w.Body.String(), `"lead_id":31`, path)
		assert.NotContains(t, w.Body.String(), `"lead_id":30`, path)
	}
}

// The update response follows the same rule: a sales user editing the title of
// its own task does not learn the lead an admin linked.
func TestTaskUpdate_ResponseHidesALeadTheCallerMayNotSee(t *testing.T) {
	svc := new(MockTaskService)
	svc.On("GetByID", uint(5)).Return(taskLinkedToLead(2, 9), nil)
	svc.On("UpdateWithLabels", mock.MatchedBy(func(task *models.Task) bool {
		// The stored link travels to the service untouched; only the response
		// is redacted.
		return task.LeadID != nil && *task.LeadID == 30 && task.Title == "Renamed"
	}), (*[]uint)(nil), uint(2)).Return(nil)

	w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPut, "/tasks/5", gin.H{"title": "Renamed"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), hiddenLeadEmail)
	assert.NotContains(t, w.Body.String(), `"lead_id"`)
	svc.AssertExpectations(t)
}

// A lead the service refuses as hidden is a 404, exactly like a missing lead,
// and the caller's id travels to the service as the actor.
func TestTaskCreate_HiddenLeadIsANotFound(t *testing.T) {
	svc := new(MockTaskService)
	svc.On("CreateWithLabels", mock.MatchedBy(func(task *models.Task) bool {
		return task.LeadID != nil && *task.LeadID == 30
	}), []uint(nil), uint(12)).Return(fmt.Errorf("unknown lead_id 30: %w", apperrors.ErrLeadNotFound))

	w := serveJSON(newTaskLeadRouter(svc, models.RoleSupport, 12), http.MethodPost, "/tasks",
		gin.H{"title": "Probe", "assigned_to_id": 12, "lead_id": 30})
	assert.Equal(t, http.StatusNotFound, w.Code)
	svc.AssertExpectations(t)
}
